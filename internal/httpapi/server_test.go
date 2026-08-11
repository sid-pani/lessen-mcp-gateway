package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sid-pani/lessen-mcp-gateway/internal/app"
	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/gateway"
	"github.com/sid-pani/lessen-mcp-gateway/internal/logbuf"
	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

func TestOnePortHandlerServesMCPAPIAndAdminUI(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	cfg.MCPServers["github"] = config.ServerConfig{
		Type: "streamable-http",
		URL:  "https://example.invalid/mcp",
		Headers: map[string]config.Secret{
			"Authorization": {Value: "Bearer top-secret"},
		},
	}
	cfg.ApplyDefaults()
	configPath := filepath.Join(t.TempDir(), "gateway.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}

	store := storage.NewMemory()
	reg, err := registry.New(ctx, store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := app.NewManager(ctx, cfg, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gw := gateway.New(manager, logger, "test")
	api := New(manager, gw, configPath, logbuf.New(64<<10), logger, "test")
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	response := request(t, http.MethodGet, server.URL+"/healthz", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(readBody(t, response), `"status":"ok"`) {
		t.Fatal("health endpoint failed")
	}

	response = request(t, http.MethodGet, server.URL+"/admin/", nil)
	body := readBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "MCP Gateway") {
		t.Fatalf("admin UI response = %d %q", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Security-Policy"); got == "" {
		t.Fatal("admin UI response is missing security headers")
	}

	response = request(t, http.MethodGet, server.URL+"/admin/app.js", nil)
	body = readBody(t, response)
	if !strings.Contains(body, "mcp-gateway-theme") || !strings.Contains(body, "prefers-color-scheme") {
		t.Fatal("admin UI bundle is missing dark/light theme support")
	}

	discover := modernRequest(t, 1, "server/discover", nil)
	crossOrigin := newRequest(t, http.MethodPost, server.URL+"/mcp", discover)
	setModernHeaders(crossOrigin, "server/discover", "")
	crossOrigin.Header.Set("Origin", "https://attacker.invalid")
	crossResponse, err := http.DefaultClient.Do(crossOrigin)
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, crossResponse)
	if crossResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin MCP request status = %d, want 403", crossResponse.StatusCode)
	}

	modern := newRequest(t, http.MethodPost, server.URL+"/mcp", discover)
	setModernHeaders(modern, "server/discover", "")
	response, err = http.DefaultClient.Do(modern)
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"supportedVersions"`) || !strings.Contains(body, `"resultType":"complete"`) {
		t.Fatalf("MCP discover response = %d %s", response.StatusCode, body)
	}
	if got := response.Header.Get("MCP-Protocol-Version"); got != mcp.ProtocolLatest {
		t.Fatalf("MCP response protocol = %q", got)
	}
	if !strings.Contains(body, mcp.MetaServerInfo) {
		t.Fatalf("MCP modern response is missing server identity: %s", body)
	}

	legacyInitialize, err := mcp.NewRequest(mcp.NumberID(2), "initialize", mcp.InitializeParams{ProtocolVersion: mcp.ProtocolPrevious, Capabilities: map[string]any{}, ClientInfo: mcp.ClientInfo{Name: "test", Version: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, http.MethodPost, server.URL+"/mcp", legacyInitialize)
	body = readBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("MCP legacy initialize response = %d %s", response.StatusCode, body)
	}

	mismatched := newRequest(t, http.MethodPost, server.URL+"/mcp", discover)
	setModernHeaders(mismatched, "tools/list", "")
	response, err = http.DefaultClient.Do(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, response)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Mcp-Method") {
		t.Fatalf("header mismatch response = %d %s", response.StatusCode, body)
	}

	toolCall := modernRequest(t, 3, "tools/call", map[string]any{"name": "github__search", "arguments": map[string]any{}})
	missingName := newRequest(t, http.MethodPost, server.URL+"/mcp", toolCall)
	setModernHeaders(missingName, "tools/call", "")
	response, err = http.DefaultClient.Do(missingName)
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, response)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Mcp-Name") {
		t.Fatalf("missing name response = %d %s", response.StatusCode, body)
	}

	response = request(t, http.MethodGet, server.URL+"/api/v1/config", nil)
	body = readBody(t, response)
	if strings.Contains(body, "top-secret") || !strings.Contains(body, `"source":"inline"`) {
		t.Fatalf("configuration endpoint leaked or omitted secret metadata: %s", body)
	}
}

func TestServerCreatePersistsAndHotAppliesConfig(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	configPath := filepath.Join(t.TempDir(), "gateway.json")
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemory()
	reg, _ := registry.New(ctx, store, 1<<20)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := app.NewManager(ctx, cfg, reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gw := gateway.New(manager, logger, "test")
	server := httptest.NewServer(New(manager, gw, configPath, nil, logger, "test").Handler())
	defer server.Close()

	payload := []byte(`{"name":"filesystem","server":{"type":"stdio","command":"/bin/cat","args":[],"lifecycle":{"mode":"lazy","idleTimeout":"5m"}}}`)
	response := request(t, http.MethodPost, server.URL+"/api/v1/servers", payload)
	body := readBody(t, response)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create server = %d %s", response.StatusCode, body)
	}
	if _, err := manager.GetServer(ctx, "filesystem"); err != nil {
		t.Fatalf("server not hot-applied: %v", err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MCPServers["filesystem"].Command != "/bin/cat" {
		t.Fatalf("server not persisted: %+v", loaded.MCPServers["filesystem"])
	}
}

func modernRequest(t *testing.T, id uint64, method string, params any) []byte {
	t.Helper()
	withMeta, err := mcp.AddRequestMeta(params, mcp.ProtocolLatest, mcp.ClientInfo{Name: "test", Version: "1"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := mcp.NewRequest(mcp.NumberID(id), method, withMeta)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setModernHeaders(req *http.Request, method, name string) {
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolLatest)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
}

func request(t *testing.T, method, url string, body []byte) *http.Response {
	t.Helper()
	response, err := http.DefaultClient.Do(newRequest(t, method, url, body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func newRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, data) == nil {
		return compact.String()
	}
	return string(data)
}
