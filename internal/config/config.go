package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultPath = "mcp-gateway-config.json"

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return err
		}
		d.Duration = parsed
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(data, &nanos); err != nil {
		return fmt.Errorf("duration must be a Go duration string or nanoseconds: %w", err)
	}
	d.Duration = time.Duration(nanos)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

type Secret struct {
	Value string `json:"value,omitempty"`
	Env   string `json:"env,omitempty"`
}

func (s *Secret) UnmarshalJSON(data []byte) error {
	var direct string
	if err := json.Unmarshal(data, &direct); err == nil {
		s.Value = direct
		s.Env = ""
		return nil
	}
	type alias Secret
	var obj alias
	if err := json.Unmarshal(data, &obj); err != nil {
		return errors.New("secret must be a string or an object with value/env")
	}
	if obj.Value != "" && obj.Env != "" {
		return errors.New("secret cannot define both value and env")
	}
	*s = Secret(obj)
	return nil
}

func (s Secret) Resolve() (string, error) {
	if s.Env != "" {
		value, ok := os.LookupEnv(s.Env)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set", s.Env)
		}
		return value, nil
	}
	if s.Value == "" {
		return "", nil
	}
	return ExpandEnvStrict(s.Value)
}

func (s Secret) Redacted() map[string]any {
	if s.Env != "" {
		return map[string]any{"configured": true, "source": "env", "env": s.Env}
	}
	return map[string]any{"configured": s.Value != "", "source": "inline"}
}

type Config struct {
	Gateway    GatewayConfig           `json:"gateway"`
	Storage    StorageConfig           `json:"storage"`
	Memory     MemoryConfig            `json:"memory"`
	Logging    LoggingConfig           `json:"logging"`
	Discovery  DiscoveryConfig         `json:"discovery"`
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

type GatewayConfig struct {
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	AdminPath         string   `json:"adminPath"`
	MCPPath           string   `json:"mcpPath"`
	ShutdownTimeout   Duration `json:"shutdownTimeout"`
	ReadHeaderTimeout Duration `json:"readHeaderTimeout"`
	RequestTimeout    Duration `json:"requestTimeout"`
	MaxRequestBytes   int64    `json:"maxRequestBytes"`
	MaxResponseBytes  int64    `json:"maxResponseBytes"`
}

type StorageConfig struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	DSN  Secret `json:"dsn,omitempty"`
}

// MarshalJSON keeps the zero-configuration SQLite file clean while preserving
// support for inline and environment-backed PostgreSQL connection strings.
// encoding/json does not consider a zero-value struct empty for `omitempty`,
// so the pointer wrapper is intentional.
func (s StorageConfig) MarshalJSON() ([]byte, error) {
	type storageJSON struct {
		Type string  `json:"type"`
		Path string  `json:"path,omitempty"`
		DSN  *Secret `json:"dsn,omitempty"`
	}
	var dsn *Secret
	if s.DSN.Value != "" || s.DSN.Env != "" {
		copy := s.DSN
		dsn = &copy
	}
	return json.Marshal(storageJSON{Type: s.Type, Path: s.Path, DSN: dsn})
}

type MemoryConfig struct {
	SoftLimitMB            int `json:"softLimitMB"`
	SchemaCacheMB          int `json:"schemaCacheMB"`
	LogBufferMB            int `json:"logBufferMB"`
	MaxConcurrent          int `json:"maxConcurrent"`
	MaxConcurrentPerServer int `json:"maxConcurrentPerServer"`
}

type LoggingConfig struct {
	Level     string `json:"level"`
	Format    string `json:"format"`
	File      string `json:"file,omitempty"`
	MaxFileMB int    `json:"maxFileMB"`
	KeepFiles int    `json:"keepFiles"`
}

type DiscoveryConfig struct {
	OnStartup             bool     `json:"onStartup"`
	Interval              Duration `json:"interval"`
	Timeout               Duration `json:"timeout"`
	HealthInterval        Duration `json:"healthInterval"`
	HealthTimeout         Duration `json:"healthTimeout"`
	MaxConcurrent         int      `json:"maxConcurrent"`
	MaxHealthConcurrent   int      `json:"maxHealthConcurrent"`
	MaxToolsPerServer     int      `json:"maxToolsPerServer"`
	MaxResourcesPerServer int      `json:"maxResourcesPerServer"`
	MaxPromptsPerServer   int      `json:"maxPromptsPerServer"`
}

type ServerConfig struct {
	Type       string            `json:"type"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]Secret `json:"env,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]Secret `json:"headers,omitempty"`
	Lifecycle  LifecycleConfig   `json:"lifecycle,omitempty"`
	Timeout    Duration          `json:"timeout,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
}

type LifecycleConfig struct {
	Mode        string   `json:"mode,omitempty"`
	IdleTimeout Duration `json:"idleTimeout,omitempty"`
	Restart     string   `json:"restart,omitempty"`
	MaxRestarts int      `json:"maxRestarts,omitempty"`
}

func Default() Config {
	return Config{
		Gateway: GatewayConfig{
			Host: "127.0.0.1", Port: 4444, AdminPath: "/admin", MCPPath: "/mcp",
			ShutdownTimeout:   Duration{10 * time.Second},
			ReadHeaderTimeout: Duration{5 * time.Second},
			RequestTimeout:    Duration{60 * time.Second},
			MaxRequestBytes:   8 << 20, MaxResponseBytes: 32 << 20,
		},
		Storage: StorageConfig{Type: "sqlite", Path: "./data/gateway.db"},
		Memory:  MemoryConfig{SoftLimitMB: 128, SchemaCacheMB: 16, LogBufferMB: 2, MaxConcurrent: 128, MaxConcurrentPerServer: 16},
		Logging: LoggingConfig{Level: "info", Format: "json", MaxFileMB: 16, KeepFiles: 3},
		Discovery: DiscoveryConfig{
			OnStartup:           true,
			Interval:            Duration{15 * time.Minute},
			Timeout:             Duration{20 * time.Second},
			HealthInterval:      Duration{30 * time.Second},
			HealthTimeout:       Duration{5 * time.Second},
			MaxConcurrent:       4,
			MaxHealthConcurrent: 8,
			MaxToolsPerServer:   10000, MaxResourcesPerServer: 10000, MaxPromptsPerServer: 2000,
		},
		MCPServers: map[string]ServerConfig{},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) ApplyDefaults() {
	def := Default()
	if c.Gateway.Host == "" {
		c.Gateway.Host = def.Gateway.Host
	}
	if c.Gateway.Port == 0 {
		c.Gateway.Port = def.Gateway.Port
	}
	if c.Gateway.AdminPath == "" {
		c.Gateway.AdminPath = def.Gateway.AdminPath
	}
	if c.Gateway.MCPPath == "" {
		c.Gateway.MCPPath = def.Gateway.MCPPath
	}
	if c.Gateway.ShutdownTimeout.Duration == 0 {
		c.Gateway.ShutdownTimeout = def.Gateway.ShutdownTimeout
	}
	if c.Gateway.ReadHeaderTimeout.Duration == 0 {
		c.Gateway.ReadHeaderTimeout = def.Gateway.ReadHeaderTimeout
	}
	if c.Gateway.RequestTimeout.Duration == 0 {
		c.Gateway.RequestTimeout = def.Gateway.RequestTimeout
	}
	if c.Gateway.MaxRequestBytes == 0 {
		c.Gateway.MaxRequestBytes = def.Gateway.MaxRequestBytes
	}
	if c.Gateway.MaxResponseBytes == 0 {
		c.Gateway.MaxResponseBytes = def.Gateway.MaxResponseBytes
	}
	if c.Storage.Type == "" {
		c.Storage.Type = def.Storage.Type
	}
	if c.Storage.Path == "" && c.Storage.Type == "sqlite" {
		c.Storage.Path = def.Storage.Path
	}
	if c.Memory.SoftLimitMB == 0 {
		c.Memory.SoftLimitMB = def.Memory.SoftLimitMB
	}
	if c.Memory.SchemaCacheMB == 0 {
		c.Memory.SchemaCacheMB = def.Memory.SchemaCacheMB
	}
	if c.Memory.LogBufferMB == 0 {
		c.Memory.LogBufferMB = def.Memory.LogBufferMB
	}
	if c.Memory.MaxConcurrent == 0 {
		c.Memory.MaxConcurrent = def.Memory.MaxConcurrent
	}
	if c.Memory.MaxConcurrentPerServer == 0 {
		c.Memory.MaxConcurrentPerServer = def.Memory.MaxConcurrentPerServer
	}
	if c.Logging.Level == "" {
		c.Logging.Level = def.Logging.Level
	}
	if c.Logging.Format == "" {
		c.Logging.Format = def.Logging.Format
	}
	if c.Logging.MaxFileMB == 0 {
		c.Logging.MaxFileMB = def.Logging.MaxFileMB
	}
	if c.Logging.KeepFiles == 0 {
		c.Logging.KeepFiles = def.Logging.KeepFiles
	}
	if c.Discovery.Interval.Duration == 0 {
		c.Discovery.Interval = def.Discovery.Interval
	}
	if c.Discovery.Timeout.Duration == 0 {
		c.Discovery.Timeout = def.Discovery.Timeout
	}
	if c.Discovery.HealthInterval.Duration == 0 {
		c.Discovery.HealthInterval = def.Discovery.HealthInterval
	}
	if c.Discovery.HealthTimeout.Duration == 0 {
		c.Discovery.HealthTimeout = def.Discovery.HealthTimeout
	}
	if c.Discovery.MaxConcurrent == 0 {
		c.Discovery.MaxConcurrent = def.Discovery.MaxConcurrent
	}
	if c.Discovery.MaxHealthConcurrent == 0 {
		c.Discovery.MaxHealthConcurrent = def.Discovery.MaxHealthConcurrent
	}
	if c.Discovery.MaxToolsPerServer == 0 {
		c.Discovery.MaxToolsPerServer = def.Discovery.MaxToolsPerServer
	}
	if c.Discovery.MaxResourcesPerServer == 0 {
		c.Discovery.MaxResourcesPerServer = def.Discovery.MaxResourcesPerServer
	}
	if c.Discovery.MaxPromptsPerServer == 0 {
		c.Discovery.MaxPromptsPerServer = def.Discovery.MaxPromptsPerServer
	}
	if c.MCPServers == nil {
		c.MCPServers = map[string]ServerConfig{}
	}
	for name, server := range c.MCPServers {
		if server.Enabled == nil {
			v := true
			server.Enabled = &v
		}
		if server.Lifecycle.Mode == "" {
			server.Lifecycle.Mode = "lazy"
		}
		if server.Lifecycle.IdleTimeout.Duration == 0 {
			server.Lifecycle.IdleTimeout = Duration{5 * time.Minute}
		}
		if server.Lifecycle.Restart == "" {
			server.Lifecycle.Restart = "on-failure"
		}
		if server.Lifecycle.MaxRestarts == 0 {
			server.Lifecycle.MaxRestarts = 5
		}
		if server.Timeout.Duration == 0 {
			server.Timeout = c.Gateway.RequestTimeout
		}
		c.MCPServers[name] = server
	}
}

func (c Config) Validate() error {
	if c.Gateway.Port < 1 || c.Gateway.Port > 65535 {
		return fmt.Errorf("gateway.port must be between 1 and 65535")
	}
	if !strings.HasPrefix(c.Gateway.AdminPath, "/") || !strings.HasPrefix(c.Gateway.MCPPath, "/") {
		return errors.New("gateway paths must begin with /")
	}
	if c.Gateway.AdminPath == c.Gateway.MCPPath {
		return errors.New("gateway.adminPath and gateway.mcpPath must differ")
	}
	if c.Memory.SoftLimitMB < 32 {
		return errors.New("memory.softLimitMB must be at least 32")
	}
	if c.Memory.SchemaCacheMB < 1 || c.Memory.LogBufferMB < 1 {
		return errors.New("memory cache budgets must be positive")
	}
	if c.Memory.MaxConcurrent < 1 || c.Memory.MaxConcurrentPerServer < 1 {
		return errors.New("memory concurrency limits must be positive")
	}
	if c.Gateway.MaxRequestBytes < 1024 || c.Gateway.MaxResponseBytes < 1024 {
		return errors.New("gateway request/response byte limits are too small")
	}
	if c.Discovery.MaxToolsPerServer < 1 || c.Discovery.MaxResourcesPerServer < 1 || c.Discovery.MaxPromptsPerServer < 1 {
		return errors.New("discovery limits must be positive")
	}
	if c.Discovery.MaxConcurrent < 1 || c.Discovery.MaxHealthConcurrent < 1 {
		return errors.New("discovery concurrency limits must be positive")
	}
	if c.Discovery.MaxConcurrent > 256 || c.Discovery.MaxHealthConcurrent > 256 {
		return errors.New("discovery concurrency limits cannot exceed 256")
	}
	switch c.Storage.Type {
	case "sqlite", "memory", "postgres":
	default:
		return fmt.Errorf("unsupported storage.type %q", c.Storage.Type)
	}
	for name, server := range c.MCPServers {
		if err := ValidateServerName(name); err != nil {
			return err
		}
		switch server.Type {
		case "stdio":
			if server.Command == "" {
				return fmt.Errorf("mcpServers.%s.command is required", name)
			}
		case "streamable-http", "sse":
			if server.URL == "" {
				return fmt.Errorf("mcpServers.%s.url is required", name)
			}
		default:
			return fmt.Errorf("mcpServers.%s.type %q is unsupported", name, server.Type)
		}
		if server.Lifecycle.Mode != "lazy" && server.Lifecycle.Mode != "persistent" {
			return fmt.Errorf("mcpServers.%s.lifecycle.mode must be lazy or persistent", name)
		}
	}
	return nil
}

func ValidateServerName(name string) error {
	if name == "" {
		return errors.New("server name cannot be empty")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("server name %q must contain only lowercase letters, numbers, - or _", name)
		}
	}
	return nil
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return err
	}
	tmp := path + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	file, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err == nil {
		_ = file.Sync()
		_ = file.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (s ServerConfig) Hash() string {
	copy := s
	if s.Env != nil {
		copy.Env = make(map[string]Secret, len(s.Env))
		for key, secret := range s.Env {
			copy.Env[key] = Secret{Env: secret.Env, Value: marker(secret.Value)}
		}
	}
	if s.Headers != nil {
		copy.Headers = make(map[string]Secret, len(s.Headers))
		for key, secret := range s.Headers {
			copy.Headers[key] = Secret{Env: secret.Env, Value: marker(secret.Value)}
		}
	}
	data, _ := json.Marshal(copy)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func marker(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ExpandEnvStrict(value string) (string, error) {
	missing := []string{}
	result := envPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := envPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		resolved, ok := os.LookupEnv(parts[1])
		if !ok {
			missing = append(missing, parts[1])
			return match
		}
		return resolved
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("environment variables are not set: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

func ResolveMap(values map[string]Secret) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for key, secret := range values {
		value, err := secret.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", key, err)
		}
		out[key] = value
	}
	return out, nil
}

func RedactedServer(name string, s ServerConfig) map[string]any {
	out := map[string]any{
		"name": name, "type": s.Type, "command": s.Command, "args": s.Args, "workingDir": s.WorkingDir,
		"url": s.URL, "lifecycle": s.Lifecycle, "tags": s.Tags,
	}
	if s.Enabled != nil {
		out["enabled"] = *s.Enabled
	}
	if len(s.Env) > 0 {
		m := map[string]any{}
		for key, secret := range s.Env {
			m[key] = secret.Redacted()
		}
		out["env"] = m
	}
	if len(s.Headers) > 0 {
		m := map[string]any{}
		for key, secret := range s.Headers {
			m[key] = secret.Redacted()
		}
		out["headers"] = m
	}
	return out
}
