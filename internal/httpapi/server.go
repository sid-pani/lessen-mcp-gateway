package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/app"
	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/gateway"
	"github.com/sid-pani/lessen-mcp-gateway/internal/logbuf"
	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
	webui "github.com/sid-pani/lessen-mcp-gateway/web"
)

type Server struct {
	manager    *app.Manager
	gateway    *gateway.Gateway
	configPath string
	logs       *logbuf.Ring
	logger     *slog.Logger
	version    string
	configMu   sync.Mutex
}

func New(manager *app.Manager, gateway *gateway.Gateway, configPath string, logs *logbuf.Ring, logger *slog.Logger, version string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{manager: manager, gateway: gateway, configPath: configPath, logs: logs, logger: logger, version: version}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	cfg := s.manager.Config()
	mux.HandleFunc(cfg.Gateway.MCPPath, s.handleMCP)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/config", s.handleConfig)
	mux.HandleFunc("/api/v1/servers", s.handleServers)
	mux.HandleFunc("/api/v1/servers/", s.handleServer)
	mux.HandleFunc("/api/v1/tools", s.handleTools)
	mux.HandleFunc("/api/v1/tools/", s.handleTool)
	mux.HandleFunc("/api/v1/resources", s.handleResources)
	mux.HandleFunc("/api/v1/resource-templates", s.handleResourceTemplates)
	mux.HandleFunc("/api/v1/prompts", s.handlePrompts)
	mux.HandleFunc("/api/v1/logs", s.handleLogs)
	mux.Handle(cfg.Gateway.AdminPath+"/", http.StripPrefix(cfg.Gateway.AdminPath+"/", webui.Handler()))
	mux.HandleFunc(cfg.Gateway.AdminPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cfg.Gateway.AdminPath+"/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, cfg.Gateway.AdminPath+"/", http.StatusTemporaryRedirect)
	})
	return s.recover(s.securityHeaders(s.accessLog(mux)))
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !s.validMCPOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, DELETE")
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := s.manager.Config()
	r.Body = http.MaxBytesReader(w, r.Body, cfg.Gateway.MaxRequestBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "request body is invalid or too large")
		return
	}
	protocol, err := validateMCPRequestHeaders(r, data)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cfg.Gateway.RequestTimeout.Duration)
	defer cancel()
	payload, notification := s.gateway.Handle(ctx, data)
	w.Header().Set("MCP-Protocol-Version", responseProtocol(protocol, data, payload))
	if notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "version": s.version})
}
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.manager.Ready(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "persistent storage is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ready"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	servers, err := s.manager.ListServers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	stats := model.GatewayStats{StartedAt: s.manager.StartedAt(), ServerCount: len(servers), Goroutines: runtime.NumGoroutine(), HeapAllocBytes: mem.HeapAlloc, HeapInUseBytes: mem.HeapInuse, SysBytes: mem.Sys}
	for _, server := range servers {
		stats.ToolCount += server.ToolCount
		stats.ResourceCount += server.ResourceCount
		stats.PromptCount += server.PromptCount
		switch server.Status {
		case model.HealthHealthy:
			stats.HealthyServers++
		case model.HealthSleeping:
			stats.SleepingServers++
		case model.HealthUnhealthy, model.HealthDegraded:
			stats.UnhealthyServers++
		}
	}
	stats.SchemaCacheEntries, stats.SchemaCacheBytes = s.manager.CacheStats()
	if s.logs != nil {
		stats.LogBufferEntries, stats.LogBufferBytes = s.logs.Stats()
	}
	writeJSON(w, 200, map[string]any{"version": s.version, "stats": stats})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	cfg := s.manager.Config()
	servers := make(map[string]any, len(cfg.MCPServers))
	for name, server := range cfg.MCPServers {
		servers[name] = config.RedactedServer(name, server)
	}
	writeJSON(w, 200, map[string]any{"gateway": cfg.Gateway, "storage": map[string]any{"type": cfg.Storage.Type, "path": cfg.Storage.Path, "dsn": cfg.Storage.DSN.Redacted()}, "memory": cfg.Memory, "logging": cfg.Logging, "discovery": cfg.Discovery, "mcpServers": servers})
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.manager.ListServers(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"servers": items})
	case http.MethodPost:
		var input struct {
			Name   string              `json:"name"`
			Server config.ServerConfig `json:"server"`
		}
		if err := decodeJSON(w, r, &input, s.manager.Config().Gateway.MaxRequestBytes); err != nil {
			writeProblem(w, 400, err.Error())
			return
		}
		if err := config.ValidateServerName(input.Name); err != nil {
			writeProblem(w, 400, err.Error())
			return
		}
		if err := s.mutateConfig(r.Context(), func(cfg *config.Config) error {
			if _, exists := cfg.MCPServers[input.Name]; exists {
				return fmt.Errorf("server %q already exists", input.Name)
			}
			cfg.MCPServers[input.Name] = input.Server
			return nil
		}); err != nil {
			writeError(w, err)
			return
		}
		server, err := s.manager.GetServer(r.Context(), input.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, server)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeProblem(w, 405, "method not allowed")
	}
}

func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeProblem(w, 404, "server not found")
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "discover" {
		if r.Method != http.MethodPost {
			writeProblem(w, 405, "method not allowed")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.manager.Config().Discovery.Timeout.Duration)
		defer cancel()
		if err := s.manager.Discover(ctx, id); err != nil {
			writeError(w, err)
			return
		}
		server, _ := s.manager.GetServer(r.Context(), id)
		writeJSON(w, 200, server)
		return
	}
	if len(parts) != 1 {
		writeProblem(w, 404, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		server, err := s.manager.GetServer(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, server)
	case http.MethodPut:
		var input config.ServerConfig
		if err := decodeJSON(w, r, &input, s.manager.Config().Gateway.MaxRequestBytes); err != nil {
			writeProblem(w, 400, err.Error())
			return
		}
		if err := s.mutateConfig(r.Context(), func(cfg *config.Config) error {
			if _, exists := cfg.MCPServers[id]; !exists {
				return registryNotFound(id)
			}
			cfg.MCPServers[id] = input
			return nil
		}); err != nil {
			writeError(w, err)
			return
		}
		server, _ := s.manager.GetServer(r.Context(), id)
		writeJSON(w, 200, server)
	case http.MethodDelete:
		if err := s.mutateConfig(r.Context(), func(cfg *config.Config) error {
			if _, exists := cfg.MCPServers[id]; !exists {
				return registryNotFound(id)
			}
			delete(cfg.MCPServers, id)
			return nil
		}); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeProblem(w, 405, "method not allowed")
	}
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	limit, offset := pagination(r, 200)
	items, total, err := s.manager.ListTools(r.Context(), r.URL.Query().Get("server"), r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tools": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/tools/"), "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		writeProblem(w, 400, "expected /api/v1/tools/{server}/{tool}")
		return
	}
	tool, err := s.manager.GetTool(r.Context(), parts[0], parts[1])
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, tool)
}
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	limit, offset := pagination(r, 200)
	items, total, err := s.manager.ListResources(r.Context(), r.URL.Query().Get("server"), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"resources": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) handleResourceTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	limit, offset := pagination(r, 200)
	items, total, err := s.manager.ListResourceTemplates(r.Context(), r.URL.Query().Get("server"), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"resourceTemplates": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	limit, offset := pagination(r, 200)
	items, total, err := s.manager.ListPrompts(r.Context(), r.URL.Query().Get("server"), limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"prompts": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, 405, "method not allowed")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries := []logbuf.Entry{}
	if s.logs != nil {
		entries = s.logs.List(after, limit)
	}
	writeJSON(w, 200, map[string]any{"logs": entries})
}

func (s *Server) mutateConfig(ctx context.Context, mutate func(*config.Config) error) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg := cloneConfig(s.manager.Config())
	if err := mutate(&cfg); err != nil {
		return err
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(s.configPath); err != nil {
		return err
	}
	return s.manager.ApplyConfig(ctx, cfg)
}
func cloneConfig(cfg config.Config) config.Config {
	data, _ := json.Marshal(cfg)
	var out config.Config
	_ = json.Unmarshal(data, &out)
	out.ApplyDefaults()
	return out
}

func validateMCPRequestHeaders(r *http.Request, data []byte) (string, error) {
	protocol := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	if protocol != "" && !mcp.SupportsProtocol(protocol) {
		return "", fmt.Errorf("unsupported MCP-Protocol-Version %q", protocol)
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		if protocol == mcp.ProtocolLatest {
			return "", errors.New("MCP 2026 HTTP requests cannot use JSON-RPC batches because routing headers must identify one method")
		}
		return protocol, nil
	}
	request, response, err := mcp.ParseEnvelope(data)
	if err != nil || response != nil || request == nil {
		// JSON-RPC parse and shape errors are returned by the protocol layer so the
		// response keeps the correct JSON-RPC error structure.
		return protocol, nil
	}
	bodyProtocol := mcp.RequestProtocol(request.Params)
	if protocol == "" && bodyProtocol == mcp.ProtocolLatest {
		return "", errors.New("MCP-Protocol-Version header is required for MCP 2026 HTTP requests")
	}
	if protocol != "" && bodyProtocol != "" && protocol != bodyProtocol {
		return "", errors.New("MCP-Protocol-Version header does not match request _meta")
	}
	methodHeader := strings.TrimSpace(r.Header.Get("Mcp-Method"))
	if protocol == mcp.ProtocolLatest {
		if request.Method == "initialize" {
			return "", errors.New("initialize is not valid for MCP 2026-07-28; use server/discover or send a self-contained request")
		}
		if bodyProtocol != mcp.ProtocolLatest {
			return "", errors.New("MCP 2026 request params must include the protocol version in _meta")
		}
		if methodHeader == "" {
			return "", errors.New("Mcp-Method header is required for MCP 2026 HTTP requests")
		}
	}
	if methodHeader != "" && methodHeader != request.Method {
		return "", errors.New("Mcp-Method header does not match the JSON-RPC method")
	}
	name := mcp.RequestName(request.Method, request.Params)
	nameHeader := r.Header.Get("Mcp-Name")
	if requiresMCPName(request.Method) && protocol == mcp.ProtocolLatest && nameHeader == "" {
		return "", errors.New("Mcp-Name header is required for this MCP 2026 request")
	}
	if nameHeader != "" && nameHeader != name {
		return "", errors.New("Mcp-Name header does not match the JSON-RPC request")
	}
	return protocol, nil
}

func requiresMCPName(method string) bool {
	switch method {
	case "tools/call", "resources/read", "prompts/get":
		return true
	default:
		return false
	}
}

func responseProtocol(requestProtocol string, requestData, responseData []byte) string {
	request, _, _ := mcp.ParseEnvelope(requestData)
	if request != nil && request.Method == "initialize" {
		_, response, _ := mcp.ParseEnvelope(responseData)
		if response != nil && response.Error == nil {
			var result mcp.InitializeResult
			if json.Unmarshal(response.Result, &result) == nil && mcp.SupportsLegacyProtocol(result.ProtocolVersion) {
				return result.ProtocolVersion
			}
		}
	}
	if requestProtocol != "" {
		return requestProtocol
	}
	if request != nil {
		if protocol := mcp.RequestProtocol(request.Params); mcp.SupportsProtocol(protocol) {
			return protocol
		}
		if request.Method == "initialize" {
			var params mcp.InitializeParams
			if json.Unmarshal(request.Params, &params) == nil && mcp.SupportsLegacyProtocol(params.ProtocolVersion) {
				return params.ProtocolVersion
			}
		}
	}
	return mcp.ProtocolPrevious
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, max int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}
func pagination(r *http.Request, defaultLimit int) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeProblem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"status": status, "message": message}})
}
func writeError(w http.ResponseWriter, err error) {
	if registry.IsNotFound(err) {
		writeProblem(w, 404, err.Error())
		return
	}
	writeProblem(w, 500, err.Error())
}
func registryNotFound(id string) error { return fmt.Errorf("%w: server %s", storage.ErrNotFound, id) }

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("HTTP handler panic", "panic", recovered)
				writeProblem(w, 500, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/healthz") {
			s.logger.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
		}
	})
}

func (s *Server) validMCPOrigin(r *http.Request) bool {
	cfg := s.manager.Config()
	requestHost := r.Host
	if host, _, err := net.SplitHostPort(requestHost); err == nil {
		requestHost = host
	}
	requestHost = strings.Trim(requestHost, "[]")
	if IsLoopbackHost(cfg.Gateway.Host) && !IsLoopbackHost(requestHost) {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func IsLoopbackHost(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
