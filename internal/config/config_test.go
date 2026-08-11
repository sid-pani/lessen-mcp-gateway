package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSecretsResolveInlineAndEnvironment(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "from-env")
	cases := []struct {
		name   string
		secret Secret
		want   string
	}{
		{name: "inline", secret: Secret{Value: "literal"}, want: "literal"},
		{name: "environment object", secret: Secret{Env: "MCP_TEST_TOKEN"}, want: "from-env"},
		{name: "environment interpolation", secret: Secret{Value: "Bearer ${MCP_TEST_TOKEN}"}, want: "Bearer from-env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.secret.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "gateway.json")
	cfg := Default()
	cfg.MCPServers["github"] = ServerConfig{
		Type: "streamable-http",
		URL:  "https://example.invalid/mcp",
		Headers: map[string]Secret{
			"Authorization": {Value: "Bearer inline-secret"},
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MCPServers["github"].URL != cfg.MCPServers["github"].URL {
		t.Fatalf("round trip changed config")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || !strings.Contains(string(data), "inline-secret") {
		t.Fatalf("saved config is invalid or missing inline secret")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("permissions = %o, want 600", got)
		}
	}
	matches, err := filepath.Glob(path + ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestServerHashDoesNotContainSecretMaterial(t *testing.T) {
	first := ServerConfig{Type: "streamable-http", URL: "https://example.invalid/mcp", Headers: map[string]Secret{"Authorization": {Value: "one"}}}
	second := first
	second.Headers = map[string]Secret{"Authorization": {Value: "two"}}
	if first.Hash() == second.Hash() {
		t.Fatal("hash should change when inline credentials change so clients are recreated")
	}
	changed := first
	changed.URL = "https://other.invalid/mcp"
	if first.Hash() == changed.Hash() {
		t.Fatal("hash should change for non-secret configuration")
	}
}

func TestServerHashDoesNotMutateSecrets(t *testing.T) {
	server := ServerConfig{
		Type:    "stdio",
		Command: "/bin/example",
		Env:     map[string]Secret{"TOKEN": {Value: "actual-token"}},
	}
	_ = server.Hash()
	if got := server.Env["TOKEN"].Value; got != "actual-token" {
		t.Fatalf("Hash mutated secret to %q", got)
	}
}

func TestDefaultStorageOmitsEmptyDSN(t *testing.T) {
	data, err := json.Marshal(Default().Storage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"dsn"`) {
		t.Fatalf("default storage contains an empty dsn: %s", data)
	}

	configured := StorageConfig{Type: "postgres", DSN: Secret{Env: "DATABASE_URL"}}
	data, err = json.Marshal(configured)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"dsn":{"env":"DATABASE_URL"}`) {
		t.Fatalf("configured storage omitted the dsn: %s", data)
	}
	var roundTrip StorageConfig
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.DSN.Env != "DATABASE_URL" {
		t.Fatalf("round-trip dsn env = %q", roundTrip.DSN.Env)
	}
}

func TestDiscoveryConcurrencyDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	if cfg.Discovery.MaxConcurrent != 4 {
		t.Fatalf("default discovery workers = %d, want 4", cfg.Discovery.MaxConcurrent)
	}
	if cfg.Discovery.MaxHealthConcurrent != 8 {
		t.Fatalf("default health workers = %d, want 8", cfg.Discovery.MaxHealthConcurrent)
	}

	cfg.Discovery.MaxConcurrent = 257
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected excessive discovery concurrency to be rejected")
	}
}
