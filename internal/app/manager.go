package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/transport"
	"github.com/sid-pani/lessen-mcp-gateway/internal/version"
)

type backend struct {
	id     string
	cfg    config.ServerConfig
	logger *slog.Logger
	sem    chan struct{}

	mu                    sync.Mutex
	client                transport.Client
	initialized           bool
	initializedGeneration uint64
	modern                bool
	protocolVersion       string
	capabilities          json.RawMessage
	serverName            string
	serverVersion         string
	connectedSince        time.Time

	initMu     sync.Mutex
	discoverMu sync.Mutex
}

type Manager struct {
	registry *registry.Registry
	logger   *slog.Logger
	started  time.Time

	mu        sync.RWMutex
	cfg       config.Config
	backends  map[string]*backend
	globalSem chan struct{}
}

func NewManager(ctx context.Context, cfg config.Config, registry *registry.Registry, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		registry:  registry,
		logger:    logger,
		started:   time.Now().UTC(),
		cfg:       cfg,
		backends:  map[string]*backend{},
		globalSem: make(chan struct{}, cfg.Memory.MaxConcurrent),
	}
	if err := m.ApplyConfig(ctx, cfg); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) StartedAt() time.Time            { return m.started }
func (m *Manager) Ready(ctx context.Context) error { return m.registry.Health(ctx) }

func (m *Manager) Config() config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) ApplyConfig(ctx context.Context, cfg config.Config) error {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	old := m.backends
	globalLimitChanged := m.cfg.Memory.MaxConcurrent != cfg.Memory.MaxConcurrent
	perServerLimitChanged := m.cfg.Memory.MaxConcurrentPerServer != cfg.Memory.MaxConcurrentPerServer
	if globalLimitChanged {
		// Calls already holding the old semaphore continue safely. New calls use the
		// replacement channel, allowing hot reload without closing a channel that may
		// still have active users.
		m.globalSem = make(chan struct{}, cfg.Memory.MaxConcurrent)
	}
	next := make(map[string]*backend, len(cfg.MCPServers))
	removed := make([]*backend, 0)
	for id, serverCfg := range cfg.MCPServers {
		if existing := old[id]; existing != nil {
			existing.mu.Lock()
			sameConfig := existing.cfg.Hash() == serverCfg.Hash()
			if sameConfig && !perServerLimitChanged {
				existing.cfg = serverCfg
				existing.mu.Unlock()
				next[id] = existing
				continue
			}
			existing.mu.Unlock()
			removed = append(removed, existing)
		}
		next[id] = &backend{id: id, cfg: serverCfg, logger: m.logger.With("server", id), sem: make(chan struct{}, cfg.Memory.MaxConcurrentPerServer)}
	}
	for id, existing := range old {
		if _, ok := next[id]; !ok {
			removed = append(removed, existing)
		}
	}
	m.backends = next
	m.cfg = cfg
	m.mu.Unlock()

	for _, b := range removed {
		b.close()
	}

	for id, b := range next {
		b.mu.Lock()
		serverCfg := b.cfg
		b.mu.Unlock()
		enabled := serverCfg.Enabled == nil || *serverCfg.Enabled
		status := model.HealthSleeping
		message := "ready for on-demand activation"
		if !enabled {
			status = model.HealthDisabled
			message = "disabled in configuration"
		}
		if serverCfg.Lifecycle.Mode == "persistent" && enabled {
			status = model.HealthStarting
			message = "waiting for discovery"
		}
		current, err := m.registry.GetServer(ctx, id)
		if err != nil && !registry.IsNotFound(err) {
			return err
		}
		if err != nil {
			current = model.Server{ID: id, Name: displayName(id), Transport: model.TransportType(serverCfg.Type)}
		}
		oldConfigHash := current.ConfigHash
		newConfigHash := serverCfg.Hash()
		current.Name = displayName(id)
		current.Transport = model.TransportType(serverCfg.Type)
		current.ConfigHash = newConfigHash
		current.Endpoint = endpointFor(serverCfg)
		if current.Status == "" || oldConfigHash != newConfigHash || !enabled {
			current.Status = status
			current.StatusMessage = message
		}
		if err := m.registry.UpsertServer(ctx, current); err != nil {
			return err
		}
	}
	for id := range old {
		if _, ok := next[id]; !ok {
			_ = m.registry.DeleteServer(ctx, id)
		}
	}
	m.registry.SetCacheBytes(int64(cfg.Memory.SchemaCacheMB) << 20)
	return nil
}

func (m *Manager) StartBackground(ctx context.Context) {
	cfg := m.Config()
	if cfg.Discovery.OnStartup {
		go func() {
			if err := m.DiscoverAll(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.logger.Warn("startup discovery completed with errors", "error", err)
			}
		}()
	}
	go m.discoveryLoop(ctx)
	go m.healthLoop(ctx)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	list := make([]*backend, 0, len(m.backends))
	for _, b := range m.backends {
		list = append(list, b)
	}
	m.backends = map[string]*backend{}
	m.mu.Unlock()
	for _, b := range list {
		b.close()
	}
	return nil
}

func (m *Manager) ListServers(ctx context.Context) ([]model.Server, error) {
	return m.registry.ListServers(ctx)
}
func (m *Manager) GetServer(ctx context.Context, id string) (model.Server, error) {
	return m.registry.GetServer(ctx, id)
}
func (m *Manager) ListTools(ctx context.Context, serverID, query string, limit, offset int) ([]model.ToolSummary, int, error) {
	return m.registry.ListToolSummaries(ctx, serverID, query, limit, offset)
}
func (m *Manager) GetTool(ctx context.Context, serverID, name string) (model.Tool, error) {
	return m.registry.GetTool(ctx, serverID, name)
}
func (m *Manager) ListResources(ctx context.Context, serverID string, limit, offset int) ([]model.Resource, int, error) {
	return m.registry.ListResources(ctx, serverID, limit, offset)
}
func (m *Manager) ListResourceTemplates(ctx context.Context, serverID string, limit, offset int) ([]model.ResourceTemplate, int, error) {
	return m.registry.ListResourceTemplates(ctx, serverID, limit, offset)
}
func (m *Manager) ListPrompts(ctx context.Context, serverID string, limit, offset int) ([]model.Prompt, int, error) {
	return m.registry.ListPrompts(ctx, serverID, limit, offset)
}
func (m *Manager) CacheStats() (int, int64) { return m.registry.CacheStats() }

func (m *Manager) DiscoverAll(ctx context.Context) error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.backends))
	for id := range m.backends {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	workers := workerCount(m.Config().Discovery.MaxConcurrent, len(ids))
	if workers == 0 {
		return nil
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := []error{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if err := m.Discover(ctx, id); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("%s: %w", id, err))
					mu.Unlock()
				}
			}
		}()
	}

sendLoop:
	for _, id := range ids {
		select {
		case jobs <- id:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (m *Manager) Discover(ctx context.Context, id string) error {
	b, err := m.backend(id)
	if err != nil {
		return err
	}
	b.discoverMu.Lock()
	defer b.discoverMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	enabled := b.cfg.Enabled == nil || *b.cfg.Enabled
	timeout := b.cfg.Timeout.Duration
	b.mu.Unlock()
	if !enabled {
		return errors.New("server is disabled")
	}
	cfg := m.Config()
	if timeout <= 0 {
		timeout = cfg.Discovery.Timeout.Duration
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	initResult, err := m.ensureInitialized(callCtx, b)
	if err != nil {
		m.markError(context.Background(), id, model.HealthUnhealthy, err)
		return err
	}

	var tools []model.Tool
	if supportsCapability(initResult.Capabilities, "tools") {
		tools, err = m.discoverTools(callCtx, b, cfg.Discovery.MaxToolsPerServer)
		if err != nil {
			m.markError(context.Background(), id, model.HealthDegraded, err)
			return err
		}
	}
	var resources []model.Resource
	var templates []model.ResourceTemplate
	if supportsCapability(initResult.Capabilities, "resources") {
		resources, err = m.discoverResources(callCtx, b, cfg.Discovery.MaxResourcesPerServer)
		if err != nil && !isMethodNotFound(err) {
			m.logger.Debug("resource discovery failed", "server", id, "error", err)
		}
		templates, err = m.discoverResourceTemplates(callCtx, b, cfg.Discovery.MaxResourcesPerServer)
		if err != nil && !isMethodNotFound(err) {
			m.logger.Debug("resource template discovery failed", "server", id, "error", err)
		}
	}
	var prompts []model.Prompt
	if supportsCapability(initResult.Capabilities, "prompts") {
		prompts, err = m.discoverPrompts(callCtx, b, cfg.Discovery.MaxPromptsPerServer)
		if err != nil && !isMethodNotFound(err) {
			m.logger.Debug("prompt discovery failed", "server", id, "error", err)
		}
	}

	if err := m.registry.ReplaceTools(callCtx, id, tools); err != nil {
		return err
	}
	if err := m.registry.ReplaceResources(callCtx, id, resources); err != nil {
		return err
	}
	if err := m.registry.ReplaceResourceTemplates(callCtx, id, templates); err != nil {
		return err
	}
	if err := m.registry.ReplacePrompts(callCtx, id, prompts); err != nil {
		return err
	}

	server, err := m.registry.GetServer(callCtx, id)
	if err != nil {
		return err
	}
	server.Status = model.HealthHealthy
	server.StatusMessage = "discovery complete"
	server.ProtocolVersion = initResult.ProtocolVersion
	server.ServerVersion = initResult.ServerInfo.Version
	server.Capabilities = append(json.RawMessage(nil), initResult.Capabilities...)
	server.ToolCount = len(tools)
	server.ResourceCount = len(resources)
	server.ResourceTemplateCount = len(templates)
	server.PromptCount = len(prompts)
	server.LastDiscoveredAt = time.Now().UTC()
	server.LastHealthCheck = server.LastDiscoveredAt
	b.mu.Lock()
	server.ConnectedSince = b.connectedSince
	b.mu.Unlock()
	if err := m.registry.UpsertServer(callCtx, server); err != nil {
		return err
	}
	m.logger.Info("MCP server discovered", "server", id, "tools", len(tools), "resources", len(resources), "templates", len(templates), "prompts", len(prompts), "protocol", initResult.ProtocolVersion)
	return nil
}

func (m *Manager) CallTool(ctx context.Context, exposedName string, arguments map[string]any) (json.RawMessage, error) {
	tool, err := m.registry.ResolveTool(ctx, exposedName)
	if err != nil {
		return nil, err
	}
	b, err := m.backend(tool.ServerID)
	if err != nil {
		return nil, err
	}
	globalSem := m.globalSemaphore()
	if err := acquire(ctx, globalSem); err != nil {
		return nil, err
	}
	defer release(globalSem)
	if err := acquire(ctx, b.sem); err != nil {
		return nil, err
	}
	defer release(b.sem)
	if _, err := m.ensureInitialized(ctx, b); err != nil {
		m.markError(context.Background(), tool.ServerID, model.HealthUnhealthy, err)
		return nil, err
	}
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	params, err := m.paramsForBackend(b, mcp.CallToolParams{Name: tool.Name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	result, err := client.Call(ctx, "tools/call", params)
	if err != nil {
		m.markError(context.Background(), tool.ServerID, model.HealthDegraded, err)
		return nil, err
	}
	return result, nil
}

func (m *Manager) ensureInitialized(ctx context.Context, b *backend) (mcp.InitializeResult, error) {
	b.initMu.Lock()
	defer b.initMu.Unlock()
	b.mu.Lock()
	client := b.client
	cfg := b.cfg
	initialized := b.initialized
	initializedGen := b.initializedGeneration
	b.mu.Unlock()
	if client == nil {
		created, err := m.newClient(b.id, cfg, b.logger)
		if err != nil {
			return mcp.InitializeResult{}, err
		}
		b.mu.Lock()
		if b.client == nil {
			b.client = created
			client = created
		} else {
			client = b.client
			_ = created.Close()
		}
		b.mu.Unlock()
	}
	generation := sessionGeneration(client)
	if initialized && (generation == 0 || generation == initializedGen) {
		b.mu.Lock()
		name := b.serverName
		if name == "" {
			name = b.id
		}
		result := mcp.InitializeResult{ProtocolVersion: b.protocolVersion, Capabilities: append(json.RawMessage(nil), b.capabilities...), ServerInfo: mcp.ClientInfo{Name: name, Version: b.serverVersion}}
		b.mu.Unlock()
		return result, nil
	}

	// MCP 2026-07-28 is stateless and replaces initialize/initialized with an
	// optional server/discover probe plus per-request metadata. Probe first so
	// stdio clients can distinguish modern servers from legacy ones without
	// relying on transport-specific status codes.
	if setter, ok := client.(interface{ SetProtocolVersion(string) }); ok {
		setter.SetProtocolVersion(mcp.ProtocolLatest)
	}
	modernParams, err := mcp.AddRequestMeta(nil, mcp.ProtocolLatest, gatewayClientInfo(), map[string]any{})
	if err != nil {
		return mcp.InitializeResult{}, err
	}
	var lastErr error
	raw, discoverErr := client.Call(ctx, "server/discover", modernParams)
	if discoverErr == nil {
		var discovered mcp.DiscoverResult
		if err := json.Unmarshal(raw, &discovered); err != nil {
			lastErr = fmt.Errorf("decode server/discover result: %w", err)
		} else if containsString(discovered.SupportedVersions, mcp.ProtocolLatest) {
			info := discovered.Identity()
			if info.Name == "" {
				info.Name = b.id
			}
			capabilities := cloneRaw(discovered.Capabilities)
			if len(capabilities) == 0 {
				capabilities = json.RawMessage(`{}`)
			}
			b.mu.Lock()
			b.initialized = true
			b.initializedGeneration = sessionGeneration(client)
			b.modern = true
			b.protocolVersion = mcp.ProtocolLatest
			b.capabilities = capabilities
			b.serverName = info.Name
			b.serverVersion = info.Version
			if b.connectedSince.IsZero() {
				b.connectedSince = time.Now().UTC()
			}
			b.mu.Unlock()
			return mcp.InitializeResult{ProtocolVersion: mcp.ProtocolLatest, Capabilities: capabilities, ServerInfo: info, Instructions: discovered.Instructions}, nil
		} else {
			lastErr = fmt.Errorf("server/discover did not advertise %s", mcp.ProtocolLatest)
		}
	} else {
		lastErr = discoverErr
	}
	if err := ctx.Err(); err != nil {
		return mcp.InitializeResult{}, err
	}

	// Fall back to the stateful 2025-era handshake. Do not send the 2026
	// revision through initialize because that revision removed the handshake.
	for _, protocol := range mcp.LegacyProtocols {
		if setter, ok := client.(interface{ SetProtocolVersion(string) }); ok {
			setter.SetProtocolVersion(protocol)
		}
		raw, err := client.Call(ctx, "initialize", mcp.InitializeParams{ProtocolVersion: protocol, Capabilities: map[string]any{}, ClientInfo: gatewayClientInfo()})
		if err != nil {
			lastErr = err
			continue
		}
		var result mcp.InitializeResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return mcp.InitializeResult{}, fmt.Errorf("decode initialize result: %w", err)
		}
		if result.ProtocolVersion == "" {
			result.ProtocolVersion = protocol
		}
		if !mcp.SupportsLegacyProtocol(result.ProtocolVersion) {
			lastErr = fmt.Errorf("server selected unsupported legacy protocol %q", result.ProtocolVersion)
			continue
		}
		if err := client.Notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
			return mcp.InitializeResult{}, fmt.Errorf("send initialized notification: %w", err)
		}
		name := result.ServerInfo.Name
		if name == "" {
			name = b.id
		}
		b.mu.Lock()
		b.initialized = true
		b.initializedGeneration = sessionGeneration(client)
		b.modern = false
		b.protocolVersion = result.ProtocolVersion
		b.capabilities = append(json.RawMessage(nil), result.Capabilities...)
		b.serverName = name
		b.serverVersion = result.ServerInfo.Version
		if b.connectedSince.IsZero() {
			b.connectedSince = time.Now().UTC()
		}
		b.mu.Unlock()
		result.ServerInfo.Name = name
		return result, nil
	}
	if lastErr == nil {
		lastErr = errors.New("server did not accept modern discovery or a legacy initialize handshake")
	}
	return mcp.InitializeResult{}, fmt.Errorf("negotiate MCP server: %w", lastErr)
}

func (m *Manager) paramsForBackend(b *backend, params any) (any, error) {
	b.mu.Lock()
	modern := b.modern
	protocol := b.protocolVersion
	b.mu.Unlock()
	if !modern {
		return params, nil
	}
	if protocol == "" {
		protocol = mcp.ProtocolLatest
	}
	return mcp.AddRequestMeta(params, protocol, gatewayClientInfo(), map[string]any{})
}

func gatewayClientInfo() mcp.ClientInfo {
	return mcp.ClientInfo{Name: "lessen-mcp-gateway", Version: version.Version}
}

func (m *Manager) discoverTools(ctx context.Context, b *backend, limit int) ([]model.Tool, error) {
	items := []model.Tool{}
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		b.mu.Lock()
		client := b.client
		b.mu.Unlock()
		requestParams, err := m.paramsForBackend(b, params)
		if err != nil {
			return nil, err
		}
		raw, err := client.Call(ctx, "tools/list", requestParams)
		if err != nil {
			return nil, err
		}
		var result mcp.ListToolsResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		for _, def := range result.Tools {
			if len(items) >= limit {
				return nil, fmt.Errorf("server exceeds maxToolsPerServer=%d", limit)
			}
			items = append(items, model.Tool{ServerID: b.id, Name: def.Name, ExposedName: registry.ExposedToolName(b.id, def.Name), Title: def.Title, Description: def.Description, InputSchema: cloneRaw(def.InputSchema), OutputSchema: cloneRaw(def.OutputSchema), Annotations: cloneRaw(def.Annotations), SchemaHash: toolHash(def), UpdatedAt: now})
		}
		cursor = result.NextCursor
		if cursor == "" {
			return items, nil
		}
	}
}
func (m *Manager) discoverResources(ctx context.Context, b *backend, limit int) ([]model.Resource, error) {
	items := []model.Resource{}
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		b.mu.Lock()
		client := b.client
		b.mu.Unlock()
		requestParams, err := m.paramsForBackend(b, params)
		if err != nil {
			return nil, err
		}
		raw, err := client.Call(ctx, "resources/list", requestParams)
		if err != nil {
			return nil, err
		}
		var result mcp.ListResourcesResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		for _, def := range result.Resources {
			if len(items) >= limit {
				return nil, fmt.Errorf("server exceeds maxResourcesPerServer=%d", limit)
			}
			payload, _ := json.Marshal(def)
			items = append(items, model.Resource{ServerID: b.id, URI: def.URI, Name: def.Name, Title: def.Title, Description: def.Description, MIMEType: def.MIMEType, Annotations: cloneRaw(def.Annotations), Payload: payload, UpdatedAt: now})
		}
		cursor = result.NextCursor
		if cursor == "" {
			return items, nil
		}
	}
}
func (m *Manager) discoverResourceTemplates(ctx context.Context, b *backend, limit int) ([]model.ResourceTemplate, error) {
	items := []model.ResourceTemplate{}
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		b.mu.Lock()
		client := b.client
		b.mu.Unlock()
		requestParams, err := m.paramsForBackend(b, params)
		if err != nil {
			return nil, err
		}
		raw, err := client.Call(ctx, "resources/templates/list", requestParams)
		if err != nil {
			return nil, err
		}
		var result mcp.ListResourceTemplatesResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		for _, def := range result.ResourceTemplates {
			if len(items) >= limit {
				return nil, fmt.Errorf("server exceeds maxResourcesPerServer=%d", limit)
			}
			payload, _ := json.Marshal(def)
			items = append(items, model.ResourceTemplate{ServerID: b.id, URITemplate: def.URITemplate, Name: def.Name, Title: def.Title, Description: def.Description, MIMEType: def.MIMEType, Annotations: cloneRaw(def.Annotations), Payload: payload, UpdatedAt: now})
		}
		cursor = result.NextCursor
		if cursor == "" {
			return items, nil
		}
	}
}
func (m *Manager) discoverPrompts(ctx context.Context, b *backend, limit int) ([]model.Prompt, error) {
	items := []model.Prompt{}
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		b.mu.Lock()
		client := b.client
		b.mu.Unlock()
		requestParams, err := m.paramsForBackend(b, params)
		if err != nil {
			return nil, err
		}
		raw, err := client.Call(ctx, "prompts/list", requestParams)
		if err != nil {
			return nil, err
		}
		var result mcp.ListPromptsResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		for _, def := range result.Prompts {
			if len(items) >= limit {
				return nil, fmt.Errorf("server exceeds maxPromptsPerServer=%d", limit)
			}
			payload, _ := json.Marshal(def)
			items = append(items, model.Prompt{ServerID: b.id, Name: def.Name, Title: def.Title, Description: def.Description, Arguments: cloneRaw(def.Arguments), Payload: payload, UpdatedAt: now})
		}
		cursor = result.NextCursor
		if cursor == "" {
			return items, nil
		}
	}
}

func (m *Manager) newClient(id string, cfg config.ServerConfig, logger *slog.Logger) (transport.Client, error) {
	env, err := config.ResolveMap(cfg.Env)
	if err != nil {
		return nil, err
	}
	headers, err := config.ResolveMap(cfg.Headers)
	if err != nil {
		return nil, err
	}
	opts := transport.HTTPOptions{URL: cfg.URL, Headers: headers, Timeout: cfg.Timeout.Duration, IdleTimeout: cfg.Lifecycle.IdleTimeout.Duration, MaxIdleConnections: 16, MaxIdleConnectionsPerHost: 2, MaxConnectionsPerHost: 8, MaxResponseBytes: m.Config().Gateway.MaxResponseBytes, ProtocolVersion: mcp.ProtocolLatest, Logger: logger}
	switch cfg.Type {
	case "stdio":
		return transport.NewStdio(transport.StdioOptions{Command: cfg.Command, Args: append([]string{}, cfg.Args...), Env: env, WorkingDir: cfg.WorkingDir, Persistent: cfg.Lifecycle.Mode == "persistent", IdleTimeout: cfg.Lifecycle.IdleTimeout.Duration, MaxResponseBytes: m.Config().Gateway.MaxResponseBytes, Logger: logger})
	case "streamable-http":
		return transport.NewStreamableHTTP(opts)
	case "sse":
		return transport.NewLegacySSE(opts)
	default:
		return nil, fmt.Errorf("unsupported transport %q", cfg.Type)
	}
}

func (m *Manager) globalSemaphore() chan struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalSem
}

func (m *Manager) backend(id string) (*backend, error) {
	m.mu.RLock()
	b := m.backends[id]
	m.mu.RUnlock()
	if b == nil {
		return nil, fmt.Errorf("server %q is not configured", id)
	}
	return b, nil
}
func (m *Manager) markError(ctx context.Context, id string, status model.HealthStatus, cause error) {
	server, err := m.registry.GetServer(ctx, id)
	if err != nil {
		return
	}
	server.Status = status
	server.StatusMessage = truncate(cause.Error(), 512)
	server.LastHealthCheck = time.Now().UTC()
	_ = m.registry.UpsertServer(ctx, server)
}
func (m *Manager) markHealth(ctx context.Context, id string, status model.HealthStatus, message string) {
	server, err := m.registry.GetServer(ctx, id)
	if err != nil {
		return
	}
	server.Status = status
	server.StatusMessage = message
	server.LastHealthCheck = time.Now().UTC()
	_ = m.registry.UpsertServer(ctx, server)
}

func (m *Manager) healthLoop(ctx context.Context) {
	for {
		cfg := m.Config()
		timer := time.NewTimer(cfg.Discovery.HealthInterval.Duration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		m.runHealthChecks(ctx)
	}
}

func (m *Manager) runHealthChecks(ctx context.Context) {
	m.mu.RLock()
	list := make([]*backend, 0, len(m.backends))
	for _, b := range m.backends {
		list = append(list, b)
	}
	m.mu.RUnlock()
	workers := workerCount(m.Config().Discovery.MaxHealthConcurrent, len(list))
	if workers == 0 {
		return
	}
	jobs := make(chan *backend)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				m.checkHealth(ctx, b)
			}
		}()
	}

sendLoop:
	for _, b := range list {
		select {
		case jobs <- b:
		case <-ctx.Done():
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
}
func (m *Manager) checkHealth(parent context.Context, b *backend) {
	b.mu.Lock()
	cfg := b.cfg
	client := b.client
	b.mu.Unlock()
	if cfg.Enabled != nil && !*cfg.Enabled {
		m.markHealth(context.Background(), b.id, model.HealthDisabled, "disabled in configuration")
		return
	}
	if cfg.Type == "stdio" && cfg.Lifecycle.Mode == "lazy" {
		if client == nil {
			m.markHealth(context.Background(), b.id, model.HealthSleeping, "ready for on-demand activation")
			return
		}
		if runner, ok := client.(interface{ IsRunning() bool }); ok && !runner.IsRunning() {
			b.mu.Lock()
			b.initialized = false
			b.mu.Unlock()
			m.markHealth(context.Background(), b.id, model.HealthSleeping, "stopped after idle timeout")
			return
		}
	}
	ctx, cancel := context.WithTimeout(parent, m.Config().Discovery.HealthTimeout.Duration)
	defer cancel()
	if _, err := m.ensureInitialized(ctx, b); err != nil {
		m.markError(context.Background(), b.id, model.HealthUnhealthy, err)
		return
	}
	b.mu.Lock()
	client = b.client
	modern := b.modern
	b.mu.Unlock()
	var healthErr error
	if modern {
		params, err := m.paramsForBackend(b, nil)
		if err != nil {
			healthErr = err
		} else {
			_, healthErr = client.Call(ctx, "server/discover", params)
		}
	} else {
		healthErr = client.Health(ctx)
	}
	if healthErr != nil {
		m.markError(context.Background(), b.id, model.HealthUnhealthy, healthErr)
		return
	}
	m.markHealth(context.Background(), b.id, model.HealthHealthy, "health check passed")
}
func (m *Manager) discoveryLoop(ctx context.Context) {
	for {
		cfg := m.Config()
		timer := time.NewTimer(cfg.Discovery.Interval.Duration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := m.DiscoverAll(ctx); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("scheduled discovery completed with errors", "error", err)
		}
	}
}

func (b *backend) close() {
	b.mu.Lock()
	client := b.client
	b.client = nil
	b.initialized = false
	b.modern = false
	b.protocolVersion = ""
	b.capabilities = nil
	b.serverName = ""
	b.serverVersion = ""
	b.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}
func sessionGeneration(client transport.Client) uint64 {
	if aware, ok := client.(interface{ SessionGeneration() uint64 }); ok {
		return aware.SessionGeneration()
	}
	return 0
}
func acquire(ctx context.Context, ch chan struct{}) error {
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func release(ch chan struct{}) {
	select {
	case <-ch:
	default:
		{
		}
	}
}
func workerCount(configured, total int) int {
	if total <= 0 {
		return 0
	}
	if configured < 1 {
		configured = 1
	}
	if configured > total {
		return total
	}
	return configured
}
func cloneRaw(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), v...)
}
func toolHash(def mcp.ToolDefinition) string {
	h := sha256.New()
	_, _ = h.Write([]byte(def.Name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(def.Title))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(def.Description))
	_, _ = h.Write(def.InputSchema)
	_, _ = h.Write(def.OutputSchema)
	_, _ = h.Write(def.Annotations)
	return hex.EncodeToString(h.Sum(nil))
}
func supportsCapability(raw json.RawMessage, name string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		// Probe older or non-conforming servers that omitted the capabilities map.
		return true
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		return true
	}
	_, ok := capabilities[name]
	return ok
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isMethodNotFound(err error) bool {
	var rpcErr *mcp.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == -32601
}
func displayName(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i := range parts {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	if len(parts) == 0 {
		return id
	}
	return strings.Join(parts, " ")
}
func endpointFor(cfg config.ServerConfig) string {
	if cfg.Type == "stdio" {
		return cfg.Command
	}
	return cfg.URL
}
func truncate(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return v[:max] + "…"
}

func (m *Manager) ListAllTools(ctx context.Context) ([]model.Tool, error) {
	return m.registry.ListTools(ctx)
}
func (m *Manager) ListToolsPage(ctx context.Context, serverID string, limit, offset int) ([]model.Tool, int, error) {
	return m.registry.ListToolsPage(ctx, serverID, limit, offset)
}

func (m *Manager) CallServer(ctx context.Context, serverID, method string, params any) (json.RawMessage, error) {
	b, err := m.backend(serverID)
	if err != nil {
		return nil, err
	}
	globalSem := m.globalSemaphore()
	if err := acquire(ctx, globalSem); err != nil {
		return nil, err
	}
	defer release(globalSem)
	if err := acquire(ctx, b.sem); err != nil {
		return nil, err
	}
	defer release(b.sem)
	if _, err := m.ensureInitialized(ctx, b); err != nil {
		m.markError(context.Background(), serverID, model.HealthUnhealthy, err)
		return nil, err
	}
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	requestParams, err := m.paramsForBackend(b, params)
	if err != nil {
		return nil, err
	}
	result, err := client.Call(ctx, method, requestParams)
	if err != nil {
		m.markError(context.Background(), serverID, model.HealthDegraded, err)
	}
	return result, err
}
