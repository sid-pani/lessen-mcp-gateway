package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/app"
	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/gateway"
	"github.com/sid-pani/lessen-mcp-gateway/internal/httpapi"
	"github.com/sid-pani/lessen-mcp-gateway/internal/logbuf"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-gate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve(args)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "init":
		return initConfig(args[1:])
	case "server":
		return serverCommand(args[1:])
	case "discover":
		return discoverCommand(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("mcp-gate %s (%s, %s)\n", version.Version, version.Commit, version.Date)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q; run mcp-gate help", args[0])
	}
}

func printUsage() {
	fmt.Print(`MCP Gateway

Usage:
  mcp-gate serve [--config PATH] [--stdio]
  mcp-gate init [--config PATH]
  mcp-gate server list [--config PATH]
  mcp-gate server add NAME --type stdio --command CMD [--args JSON]
  mcp-gate server add NAME --type streamable-http --url URL
  mcp-gate server remove NAME [--config PATH]
  mcp-gate discover NAME [--address http://127.0.0.1:4444]
  mcp-gate version
`)
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", configPathDefault(), "configuration file")
	stdioMode := flags.Bool("stdio", false, "serve MCP over stdin/stdout instead of HTTP")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	resolvePaths(&cfg, *configPath)
	if cfg.Memory.SoftLimitMB > 0 {
		debug.SetMemoryLimit(int64(cfg.Memory.SoftLimitMB) << 20)
	}
	ring := logbuf.New(cfg.Memory.LogBufferMB << 20)
	level, err := parseLevel(cfg.Logging.Level)
	if err != nil {
		return err
	}
	logger, closer, err := logbuf.NewLogger(level, cfg.Logging.Format, ring, cfg.Logging.File, int64(cfg.Logging.MaxFileMB)<<20, cfg.Logging.KeepFiles)
	if err != nil {
		return err
	}
	if closer != nil {
		defer closer.Close()
	}
	slog.SetDefault(logger)
	store, err := app.OpenStore(cfg.Storage)
	if err != nil {
		return err
	}
	defer store.Close()
	reg, err := registry.New(context.Background(), store, int64(cfg.Memory.SchemaCacheMB)<<20)
	if err != nil {
		return err
	}
	manager, err := app.NewManager(context.Background(), cfg, reg, logger)
	if err != nil {
		return err
	}
	defer manager.Close()
	gw := gateway.New(manager, logger, version.Version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager.StartBackground(ctx)
	watcher := config.NewWatcher(*configPath, time.Second)
	go watcher.Run(ctx, func(updated config.Config, watchErr error) {
		if watchErr != nil {
			logger.Warn("configuration reload failed", "error", watchErr)
			return
		}
		resolvePaths(&updated, *configPath)
		current := manager.Config()
		if updated.Storage.Type != current.Storage.Type || updated.Storage.Path != current.Storage.Path {
			logger.Warn("storage changes require a restart")
			updated.Storage = current.Storage
		}
		if updated.Gateway.Host != current.Gateway.Host || updated.Gateway.Port != current.Gateway.Port || updated.Gateway.MCPPath != current.Gateway.MCPPath || updated.Gateway.AdminPath != current.Gateway.AdminPath {
			logger.Warn("HTTP address/path changes require a restart")
			updated.Gateway.Host = current.Gateway.Host
			updated.Gateway.Port = current.Gateway.Port
			updated.Gateway.MCPPath = current.Gateway.MCPPath
			updated.Gateway.AdminPath = current.Gateway.AdminPath
		}
		if err := manager.ApplyConfig(context.Background(), updated); err != nil {
			logger.Warn("configuration reload rejected", "error", err)
		} else {
			if updated.Memory.SoftLimitMB > 0 {
				debug.SetMemoryLimit(int64(updated.Memory.SoftLimitMB) << 20)
			}
			ring.SetMaxBytes(updated.Memory.LogBufferMB << 20)
			logger.Info("configuration reloaded")
		}
	})
	if *stdioMode {
		return serveStdio(ctx, gw, cfg, logger)
	}
	return serveHTTP(ctx, manager, gw, ring, logger, *configPath, cfg)
}

func serveHTTP(ctx context.Context, manager *app.Manager, gw *gateway.Gateway, ring *logbuf.Ring, logger *slog.Logger, configPath string, cfg config.Config) error {
	address := cfg.Gateway.Host + ":" + strconv.Itoa(cfg.Gateway.Port)
	if !httpapi.IsLoopbackHost(cfg.Gateway.Host) {
		logger.Warn("gateway is listening on a non-loopback interface without built-in authentication", "address", address)
	}
	api := httpapi.New(manager, gw, configPath, ring, logger, version.Version)
	server := &http.Server{Addr: address, Handler: api.Handler(), ReadHeaderTimeout: cfg.Gateway.ReadHeaderTimeout.Duration, ReadTimeout: cfg.Gateway.RequestTimeout.Duration, WriteTimeout: cfg.Gateway.RequestTimeout.Duration + 5*time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("MCP Gateway listening", "address", "http://"+address, "admin", "http://"+address+cfg.Gateway.AdminPath+"/", "mcp", "http://"+address+cfg.Gateway.MCPPath)
		errCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Gateway.ShutdownTimeout.Duration)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func serveStdio(ctx context.Context, gw *gateway.Gateway, cfg config.Config, logger *slog.Logger) error {
	logger.Info("MCP Gateway serving over stdio")
	scanner := bufio.NewScanner(os.Stdin)
	max := int(cfg.Gateway.MaxRequestBytes)
	if max < 64*1024 {
		max = 64 * 1024
	}
	scanner.Buffer(make([]byte, 64*1024), max)
	writer := bufio.NewWriterSize(os.Stdout, 64*1024)
	defer writer.Flush()
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		payload, notification := gw.Handle(ctx, append([]byte(nil), scanner.Bytes()...))
		if notification {
			continue
		}
		if _, err := writer.Write(payload); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func initConfig(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	path := flags.String("config", configPathDefault(), "configuration file")
	force := flags.Bool("force", false, "overwrite an existing file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*force {
		if _, err := os.Stat(*path); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", *path)
		}
	}
	cfg := config.Default()
	if err := cfg.Save(*path); err != nil {
		return err
	}
	fmt.Println(*path)
	return nil
}

func serverCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("server command requires list, add, or remove")
	}
	switch args[0] {
	case "list":
		return serverList(args[1:])
	case "add":
		return serverAdd(args[1:])
	case "remove":
		return serverRemove(args[1:])
	case "help", "--help", "-h":
		printServerUsage()
		return nil
	default:
		return fmt.Errorf("unknown server command %q; run mcp-gate server help", args[0])
	}
}

func printServerUsage() {
	fmt.Print(`MCP Gateway server configuration

Usage:
  mcp-gate server list [--config PATH]
  mcp-gate server add NAME --type stdio --command CMD [--args JSON]
  mcp-gate server add NAME --type streamable-http --url URL
  mcp-gate server add NAME --type sse --url URL
  mcp-gate server remove NAME [--config PATH]
`)
}

func serverList(args []string) error {
	flags := flag.NewFlagSet("server list", flag.ContinueOnError)
	path := flags.String("config", configPathDefault(), "configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server := cfg.MCPServers[name]
		endpoint := server.URL
		if server.Type == "stdio" {
			endpoint = strings.Join(append([]string{server.Command}, server.Args...), " ")
		}
		fmt.Printf("%-24s %-18s %s\n", name, server.Type, endpoint)
	}
	return nil
}
func serverAdd(args []string) error {
	if len(args) == 0 {
		return errors.New("server add requires NAME")
	}
	name := args[0]
	flags := flag.NewFlagSet("server add", flag.ContinueOnError)
	path := flags.String("config", configPathDefault(), "configuration file")
	kind := flags.String("type", "stdio", "stdio, streamable-http, or sse")
	command := flags.String("command", "", "stdio command")
	argJSON := flags.String("args", "[]", "JSON array of command arguments")
	urlValue := flags.String("url", "", "HTTP/SSE endpoint")
	lifecycle := flags.String("lifecycle", "lazy", "lazy or persistent")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if err := config.ValidateServerName(name); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if _, ok := cfg.MCPServers[name]; ok {
		return fmt.Errorf("server %q already exists", name)
	}
	server := config.ServerConfig{Type: *kind, Command: *command, URL: *urlValue, Lifecycle: config.LifecycleConfig{Mode: *lifecycle, IdleTimeout: config.Duration{Duration: 5 * time.Minute}}}
	if err := json.Unmarshal([]byte(*argJSON), &server.Args); err != nil {
		return fmt.Errorf("decode --args: %w", err)
	}
	cfg.MCPServers[name] = server
	cfg.ApplyDefaults()
	if err := cfg.Save(*path); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}
func serverRemove(args []string) error {
	if len(args) == 0 {
		return errors.New("server remove requires NAME")
	}
	name := args[0]
	flags := flag.NewFlagSet("server remove", flag.ContinueOnError)
	path := flags.String("config", configPathDefault(), "configuration file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if _, ok := cfg.MCPServers[name]; !ok {
		return fmt.Errorf("server %q does not exist", name)
	}
	delete(cfg.MCPServers, name)
	if err := cfg.Save(*path); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

func discoverCommand(args []string) error {
	flags := flag.NewFlagSet("discover", flag.ContinueOnError)
	address := flags.String("address", "http://127.0.0.1:4444", "running gateway address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	name := ""
	if flags.NArg() > 0 {
		name = flags.Arg(0)
	}
	path := "/api/v1/servers"
	if name != "" {
		path += "/" + name + "/discover"
	} else {
		return errors.New("discover currently requires a server name")
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*address, "/")+path, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("gateway returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	fmt.Println(strings.TrimSpace(string(body)))
	return nil
}

func configPathDefault() string {
	if value := os.Getenv("MCP_GATEWAY_CONFIG"); value != "" {
		return value
	}
	return config.DefaultPath
}
func resolvePaths(cfg *config.Config, path string) {
	base := filepath.Dir(path)
	if cfg.Storage.Type == "sqlite" && cfg.Storage.Path != "" && !filepath.IsAbs(cfg.Storage.Path) {
		cfg.Storage.Path = filepath.Clean(filepath.Join(base, cfg.Storage.Path))
	}
	for id, server := range cfg.MCPServers {
		if server.WorkingDir != "" && !filepath.IsAbs(server.WorkingDir) {
			server.WorkingDir = filepath.Clean(filepath.Join(base, server.WorkingDir))
			cfg.MCPServers[id] = server
		}
	}
}
func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unsupported logging level %q", value)
	}
}
