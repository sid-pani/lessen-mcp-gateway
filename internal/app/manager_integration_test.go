package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/app"
	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	serveMockMCP(os.Stdin, os.Stdout, os.Getenv("GO_MCP_HELPER_MODE"))
	os.Exit(0)
}

func serveMockMCP(r io.Reader, w io.Writer, mode string) {
	modern := mode != "legacy"
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		request, _, err := mcp.ParseEnvelope(scanner.Bytes())
		if err != nil || request == nil || request.ID.IsZero() {
			continue
		}
		if request.Method == "server/discover" {
			if !modern {
				writeRPCError(w, request.ID, -32601, "method not found")
				continue
			}
			if !validModernParams(request.Params) {
				writeRPCError(w, request.ID, -32602, "missing modern request metadata")
				continue
			}
			writeModernResult(w, request.ID, map[string]any{
				"supportedVersions": []string{mcp.ProtocolLatest},
				"capabilities": map[string]any{
					"tools":     map[string]any{},
					"resources": map[string]any{},
					"prompts":   map[string]any{},
				},
				"_meta": map[string]any{
					mcp.MetaServerInfo: map[string]any{"name": "mock-mcp", "version": "2.0.0"},
				},
			})
			continue
		}
		if modern && request.Method != "initialize" && !validModernParams(request.Params) {
			writeRPCError(w, request.ID, -32602, "missing modern request metadata")
			continue
		}

		var result map[string]any
		switch request.Method {
		case "initialize":
			if modern {
				writeRPCError(w, request.ID, -32601, "method not found")
				continue
			}
			var params mcp.InitializeParams
			_ = json.Unmarshal(request.Params, &params)
			result = map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities": map[string]any{
					"tools":     map[string]any{},
					"resources": map[string]any{},
					"prompts":   map[string]any{},
				},
				"serverInfo": map[string]any{"name": "mock-mcp-legacy", "version": "1.0.0"},
			}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "echo", "title": "Echo", "description": "Echo a message", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}}},
				map[string]any{"name": "sum", "description": "Add two numbers", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "number"}, "b": map[string]any{"type": "number"}}}},
			}}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Name == "sum" {
				a, _ := params.Arguments["a"].(float64)
				b, _ := params.Arguments["b"].(float64)
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": a + b}}}
			} else {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": params.Arguments["message"]}}}
			}
		case "resources/list":
			result = map[string]any{"resources": []any{map[string]any{"uri": "mock://readme", "name": "README", "mimeType": "text/plain"}}}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": []any{map[string]any{"uriTemplate": "mock://item/{id}", "name": "Item"}}}
		case "resources/read":
			result = map[string]any{"contents": []any{map[string]any{"uri": "mock://readme", "mimeType": "text/plain", "text": "hello from resource"}}}
		case "prompts/list":
			result = map[string]any{"prompts": []any{map[string]any{"name": "review", "description": "Review a change", "arguments": []any{map[string]any{"name": "target", "required": true}}}}}
		case "prompts/get":
			result = map[string]any{"description": "Review", "messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "Review it"}}}}
		default:
			writeRPCError(w, request.ID, -32601, "method not found")
			continue
		}
		if modern {
			writeModernResult(w, request.ID, result)
		} else {
			payload, _ := mcp.NewResult(request.ID, result)
			_, _ = w.Write(append(payload, '\n'))
		}
	}
}

func validModernParams(params json.RawMessage) bool {
	meta, err := mcp.ParseRequestMeta(params)
	return err == nil && meta.ProtocolVersion == mcp.ProtocolLatest && meta.ClientCapabilities != nil
}

func writeModernResult(w io.Writer, id mcp.ID, result map[string]any) {
	if _, ok := result["resultType"]; !ok {
		result["resultType"] = mcp.ResultComplete
	}
	if _, ok := result["_meta"]; !ok {
		result["_meta"] = map[string]any{mcp.MetaServerInfo: map[string]any{"name": "mock-mcp", "version": "2.0.0"}}
	}
	payload, _ := mcp.NewResult(id, result)
	_, _ = w.Write(append(payload, '\n'))
}

func writeRPCError(w io.Writer, id mcp.ID, code int, message string) {
	payload, _ := mcp.NewError(id, code, message, nil)
	_, _ = w.Write(append(payload, '\n'))
}

func TestManagerDiscoversAndInvokesLazyStdioServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	manager := newHelperManager(t, ctx, "modern")
	defer manager.Close()

	if err := manager.Discover(ctx, "mock"); err != nil {
		t.Fatalf("discover mock server: %v", err)
	}
	server, err := manager.GetServer(ctx, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if server.ToolCount != 2 || server.ResourceCount != 1 || server.ResourceTemplateCount != 1 || server.PromptCount != 1 {
		t.Fatalf("unexpected discovery counts: %+v", server)
	}
	if server.ProtocolVersion != mcp.ProtocolLatest {
		t.Fatalf("protocol = %q, want %q", server.ProtocolVersion, mcp.ProtocolLatest)
	}
	if server.ServerVersion != "2.0.0" {
		t.Fatalf("server version = %q", server.ServerVersion)
	}

	tools, total, err := manager.ListTools(ctx, "mock", "echo", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(tools) != 1 || tools[0].ExposedName != "mock__echo" {
		t.Fatalf("unexpected tools: total=%d tools=%+v", total, tools)
	}

	result, err := manager.CallTool(ctx, "mock__echo", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !json.Valid(result) || !containsJSONText(result, "hello") {
		t.Fatalf("unexpected tool result: %s", result)
	}

	// Let the lazy child process stop, then verify the manager transparently
	// restarts and re-discovers it for the next invocation.
	time.Sleep(180 * time.Millisecond)
	result, err = manager.CallTool(ctx, "mock__echo", map[string]any{"message": "after-idle"})
	if err != nil {
		t.Fatalf("call tool after idle restart: %v", err)
	}
	if !containsJSONText(result, "after-idle") {
		t.Fatalf("unexpected restarted tool result: %s", result)
	}
}

func TestManagerFallsBackToLegacyInitialize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	manager := newHelperManager(t, ctx, "legacy")
	defer manager.Close()
	if err := manager.Discover(ctx, "mock"); err != nil {
		t.Fatalf("discover legacy server: %v", err)
	}
	server, err := manager.GetServer(ctx, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if server.ProtocolVersion != mcp.ProtocolPrevious {
		t.Fatalf("protocol = %q, want %q", server.ProtocolVersion, mcp.ProtocolPrevious)
	}
	if server.ServerVersion != "1.0.0" {
		t.Fatalf("server version = %q", server.ServerVersion)
	}
}

func newHelperManager(t *testing.T, ctx context.Context, mode string) *app.Manager {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	cfg.MCPServers["mock"] = config.ServerConfig{
		Type:    "stdio",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env: map[string]config.Secret{
			"GO_WANT_MCP_HELPER": {Value: "1"},
			"GO_MCP_HELPER_MODE": {Value: mode},
		},
		Lifecycle: config.LifecycleConfig{Mode: "lazy", IdleTimeout: config.Duration{Duration: 75 * time.Millisecond}},
		Timeout:   config.Duration{Duration: 3 * time.Second},
	}
	cfg.ApplyDefaults()
	store := storage.NewMemory()
	reg, err := registry.New(ctx, store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := app.NewManager(ctx, cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func containsJSONText(raw []byte, want string) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	encoded, _ := json.Marshal(value)
	return stringContains(string(encoded), want)
}

func stringContains(value, want string) bool {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
