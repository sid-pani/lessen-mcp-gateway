package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/app"
	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

func TestGatewayAggregatesAndNamespacesCapabilities(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	reg, err := registry.New(ctx, store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	manager, err := app.NewManager(ctx, cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	now := time.Now().UTC()
	if err := reg.UpsertServer(ctx, model.Server{ID: "github", Name: "GitHub", Transport: model.TransportStreamableHTTP, Status: model.HealthHealthy}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReplaceTools(ctx, "github", []model.Tool{{ServerID: "github", Name: "create_issue", ExposedName: "github__create_issue", Description: "Create an issue", InputSchema: json.RawMessage(`{"type":"object"}`), SchemaHash: "hash", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReplaceResources(ctx, "github", []model.Resource{{ServerID: "github", URI: "github://issue/1", Name: "Issue 1", MIMEType: "text/plain", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReplaceResourceTemplates(ctx, "github", []model.ResourceTemplate{{ServerID: "github", URITemplate: "github://issue/{number}", Name: "Issue", MIMEType: "text/plain", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ReplacePrompts(ctx, "github", []model.Prompt{{ServerID: "github", Name: "review", Description: "Review a pull request", Arguments: json.RawMessage(`[]`), UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}

	gw := New(manager, slog.Default(), "test")
	response := callGatewayValue(t, gw, 1, "server/discover", modernParams(t, nil))
	if response.Error != nil {
		t.Fatalf("server/discover error: %+v", response.Error)
	}
	assertJSONContains(t, response.Result, `"supportedVersions"`)
	assertJSONContains(t, response.Result, `"resultType":"complete"`)
	assertJSONContains(t, response.Result, mcp.MetaServerInfo)
	assertJSONContains(t, response.Result, `"ttlMs":30000`)

	response = callGatewayValue(t, gw, 2, "tools/list", modernParams(t, map[string]any{}))
	assertJSONContains(t, response.Result, `github__create_issue`)
	assertJSONContains(t, response.Result, `[github] Create an issue`)
	assertJSONContains(t, response.Result, `"cacheScope":"private"`)

	response = callGatewayValue(t, gw, 3, "resources/list", modernParams(t, map[string]any{}))
	assertJSONContains(t, response.Result, `mcp-gateway://github/`)

	response = callGatewayValue(t, gw, 4, "resources/templates/list", modernParams(t, map[string]any{}))
	assertJSONContains(t, response.Result, `mcp-gateway://github/template?uri=`)
	assertJSONContains(t, response.Result, `{number}`)

	response = callGatewayValue(t, gw, 5, "prompts/list", modernParams(t, map[string]any{}))
	assertJSONContains(t, response.Result, `github__review`)
}

func TestGatewayLegacyInitializeNeverNegotiatesModernRevision(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	reg, _ := registry.New(ctx, store, 1<<20)
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	manager, err := app.NewManager(ctx, cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gw := New(manager, slog.Default(), "test")

	response := callGatewayValue(t, gw, 1, "initialize", map[string]any{"protocolVersion": mcp.ProtocolLatest, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if response.Error != nil {
		t.Fatalf("initialize error: %+v", response.Error)
	}
	assertJSONContains(t, response.Result, `"protocolVersion":"2025-11-25"`)
	if strings.Contains(string(response.Result), `"resultType"`) {
		t.Fatalf("legacy result unexpectedly contains modern resultType: %s", response.Result)
	}
}

func TestGatewayAcceptsNotificationsWithoutResponse(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	reg, _ := registry.New(ctx, store, 1<<20)
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	manager, err := app.NewManager(ctx, cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gw := New(manager, slog.Default(), "test")
	payload, notification := gw.Handle(ctx, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	if !notification || len(payload) != 0 {
		t.Fatalf("notification returned response: notification=%v payload=%s", notification, payload)
	}
}

func TestResourceTemplateRoutingPreservesVariables(t *testing.T) {
	encoded := encodeResourceTemplateURI("github", "github://{owner}/{repo}/issues/{number}")
	if !strings.Contains(encoded, "{owner}") || !strings.Contains(encoded, "{number}") {
		t.Fatalf("template variables were escaped away: %s", encoded)
	}
	expanded := strings.NewReplacer("{owner}", "openai", "{repo}", "sdk", "{number}", "42").Replace(encoded)
	serverID, original, err := decodeResourceURI(expanded)
	if err != nil {
		t.Fatal(err)
	}
	if serverID != "github" || original != "github://openai/sdk/issues/42" {
		t.Fatalf("decoded %q %q", serverID, original)
	}
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *mcp.RPCError   `json:"error"`
}

func modernParams(t *testing.T, params any) map[string]any {
	t.Helper()
	value, err := mcp.AddRequestMeta(params, mcp.ProtocolLatest, mcp.ClientInfo{Name: "test", Version: "1"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func callGatewayValue(t *testing.T, gw *Gateway, id int, method string, params any) rpcResponse {
	t.Helper()
	request, err := mcp.NewRequest(mcp.NumberID(uint64(id)), method, params)
	if err != nil {
		t.Fatal(err)
	}
	payload, notification := gw.Handle(context.Background(), request)
	if notification {
		t.Fatal("request was treated as notification")
	}
	var response rpcResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode response %s: %v", payload, err)
	}
	return response
}

func assertJSONContains(t *testing.T, raw json.RawMessage, value string) {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("invalid JSON: %s", raw)
	}
	if !strings.Contains(string(raw), value) {
		t.Fatalf("JSON %s does not contain %q", raw, value)
	}
}
