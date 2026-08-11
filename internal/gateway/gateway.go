package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/sid-pani/lessen-mcp-gateway/internal/app"
	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
)

const (
	listCacheTTLMS     = int64(30_000)
	resourceCacheTTLMS = int64(0)
)

type Gateway struct {
	manager *app.Manager
	logger  *slog.Logger
	name    string
	version string
}

func New(manager *app.Manager, logger *slog.Logger, version string) *Gateway {
	if logger == nil {
		logger = slog.Default()
	}
	return &Gateway{manager: manager, logger: logger, name: "lessen-mcp-gateway", version: version}
}

func (g *Gateway) Handle(ctx context.Context, data []byte) ([]byte, bool) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		payload, _ := mcp.NewError(nil, -32700, "empty JSON-RPC request", nil)
		return payload, false
	}
	if strings.HasPrefix(trimmed, "[") {
		var batch []json.RawMessage
		if err := json.Unmarshal(data, &batch); err != nil {
			payload, _ := mcp.NewError(nil, -32700, "invalid JSON", nil)
			return payload, false
		}
		if len(batch) == 0 {
			payload, _ := mcp.NewError(nil, -32600, "empty JSON-RPC batch", nil)
			return payload, false
		}
		responses := make([]json.RawMessage, 0, len(batch))
		for _, item := range batch {
			response, notification := g.handleOne(ctx, item)
			if !notification && len(response) > 0 {
				responses = append(responses, response)
			}
		}
		if len(responses) == 0 {
			return nil, true
		}
		payload, _ := json.Marshal(responses)
		return payload, false
	}
	return g.handleOne(ctx, data)
}

func (g *Gateway) handleOne(ctx context.Context, data []byte) ([]byte, bool) {
	request, response, err := mcp.ParseEnvelope(data)
	if err != nil {
		payload, _ := mcp.NewError(nil, -32700, "invalid JSON-RPC request", map[string]any{"detail": err.Error()})
		return payload, false
	}
	if response != nil {
		payload, _ := mcp.NewError(response.ID, -32600, "gateway accepts requests, not responses", nil)
		return payload, false
	}
	if request == nil {
		payload, _ := mcp.NewError(nil, -32600, "invalid request", nil)
		return payload, false
	}
	notification := request.ID.IsZero()
	result, callErr := g.dispatch(ctx, request.Method, request.Params)
	if notification {
		return nil, true
	}
	if callErr != nil {
		code := -32603
		message := callErr.Error()
		var rpcErr *mcp.RPCError
		if errors.As(callErr, &rpcErr) {
			code = rpcErr.Code
			message = rpcErr.Message
		} else if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			code = -32001
		} else if registry.IsNotFound(callErr) {
			code = -32602
		}
		payload, _ := mcp.NewError(request.ID, code, message, nil)
		return payload, false
	}
	if mcp.IsModernRequest(request.Method, request.Params) {
		result, err = g.decorateModernResult(request.Method, result)
		if err != nil {
			payload, _ := mcp.NewError(request.ID, -32603, "gateway produced an invalid MCP result", nil)
			return payload, false
		}
	}
	payload, _ := mcp.NewResult(request.ID, json.RawMessage(result))
	return payload, false
}

func (g *Gateway) dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "server/discover":
		return g.serverDiscover()
	case "initialize":
		return g.initialize(params)
	case "ping":
		return json.RawMessage(`{}`), nil
	case "tools/list":
		return g.listTools(ctx, params)
	case "tools/call":
		return g.callTool(ctx, params)
	case "resources/list":
		return g.listResources(ctx, params)
	case "resources/templates/list":
		return g.listResourceTemplates(ctx, params)
	case "resources/read":
		return g.readResource(ctx, params)
	case "prompts/list":
		return g.listPrompts(ctx, params)
	case "prompts/get":
		return g.getPrompt(ctx, params)
	case "notifications/initialized", "notifications/cancelled":
		return json.RawMessage(`{}`), nil
	default:
		return nil, &mcp.RPCError{Code: -32601, Message: "method not found: " + method}
	}
}

func (g *Gateway) serverDiscover() (json.RawMessage, error) {
	result := map[string]any{
		"supportedVersions": append([]string(nil), mcp.SupportedProtocols...),
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
		"instructions": "Tools and prompts are namespaced as <server>__<name>; resource URIs are routed through the gateway.",
	}
	return json.Marshal(result)
}

func (g *Gateway) initialize(params json.RawMessage) (json.RawMessage, error) {
	var request mcp.InitializeParams
	if len(params) > 0 {
		_ = json.Unmarshal(params, &request)
	}
	protocol := request.ProtocolVersion
	if !mcp.SupportsLegacyProtocol(protocol) {
		protocol = mcp.ProtocolPrevious
	}
	result := map[string]any{
		"protocolVersion": protocol,
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
		"serverInfo":   map[string]any{"name": g.name, "version": g.version},
		"instructions": "Tools and prompts are namespaced as <server>__<name>; resource URIs are routed through the gateway.",
	}
	return json.Marshal(result)
}

func (g *Gateway) listTools(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	offset := cursorOffset(params)
	const pageSize = 200
	tools, total, err := g.manager.ListToolsPage(ctx, "", pageSize, offset)
	if err != nil {
		return nil, err
	}
	defs := make([]mcp.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		description := tool.Description
		if description != "" {
			description = "[" + tool.ServerID + "] " + description
		}
		defs = append(defs, mcp.ToolDefinition{Name: tool.ExposedName, Title: tool.Title, Description: description, InputSchema: defaultObjectSchema(tool.InputSchema), OutputSchema: tool.OutputSchema, Annotations: tool.Annotations})
	}
	result := map[string]any{"tools": defs}
	if offset+len(tools) < total {
		result["nextCursor"] = strconv.Itoa(offset + len(tools))
	}
	return json.Marshal(result)
}

func (g *Gateway) callTool(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input mcp.CallToolParams
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "invalid tools/call parameters"}
	}
	if input.Name == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "tool name is required"}
	}
	return g.manager.CallTool(ctx, input.Name, input.Arguments)
}

func (g *Gateway) listResources(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	offset := cursorOffset(params)
	items, total, err := g.manager.ListResources(ctx, "", 200, offset)
	if err != nil {
		return nil, err
	}
	resources := make([]map[string]any, 0, len(items))
	for _, item := range items {
		resource := map[string]any{
			"uri":         encodeResourceURI(item.ServerID, item.URI),
			"name":        item.Name,
			"title":       item.Title,
			"description": prefixDescription(item.ServerID, item.Description),
			"mimeType":    item.MIMEType,
		}
		if annotations := decodeOptionalJSON(item.Annotations); annotations != nil {
			resource["annotations"] = annotations
		}
		resources = append(resources, resource)
	}
	result := map[string]any{"resources": resources}
	if offset+len(items) < total {
		result["nextCursor"] = strconv.Itoa(offset + len(items))
	}
	return json.Marshal(result)
}

func (g *Gateway) listResourceTemplates(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	offset := cursorOffset(params)
	items, total, err := g.manager.ListResourceTemplates(ctx, "", 200, offset)
	if err != nil {
		return nil, err
	}
	templates := make([]map[string]any, 0, len(items))
	for _, item := range items {
		template := map[string]any{
			"uriTemplate": encodeResourceTemplateURI(item.ServerID, item.URITemplate),
			"name":        item.Name,
			"title":       item.Title,
			"description": prefixDescription(item.ServerID, item.Description),
			"mimeType":    item.MIMEType,
		}
		if annotations := decodeOptionalJSON(item.Annotations); annotations != nil {
			template["annotations"] = annotations
		}
		templates = append(templates, template)
	}
	result := map[string]any{"resourceTemplates": templates}
	if offset+len(items) < total {
		result["nextCursor"] = strconv.Itoa(offset + len(items))
	}
	return json.Marshal(result)
}

func (g *Gateway) readResource(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &input); err != nil || input.URI == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "resource URI is required"}
	}
	serverID, original, err := decodeResourceURI(input.URI)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: err.Error()}
	}
	result, err := g.manager.CallServer(ctx, serverID, "resources/read", map[string]any{"uri": original})
	if err != nil {
		return nil, err
	}
	return rewriteResourceResultURIs(result, serverID)
}

func (g *Gateway) listPrompts(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	offset := cursorOffset(params)
	items, total, err := g.manager.ListPrompts(ctx, "", 200, offset)
	if err != nil {
		return nil, err
	}
	prompts := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var args any
		if len(item.Arguments) > 0 {
			_ = json.Unmarshal(item.Arguments, &args)
		}
		prompts = append(prompts, map[string]any{"name": registry.ExposedToolName(item.ServerID, item.Name), "title": item.Title, "description": prefixDescription(item.ServerID, item.Description), "arguments": args})
	}
	result := map[string]any{"prompts": prompts}
	if offset+len(items) < total {
		result["nextCursor"] = strconv.Itoa(offset + len(items))
	}
	return json.Marshal(result)
}

func (g *Gateway) getPrompt(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: "invalid prompts/get parameters"}
	}
	serverID, name, err := registry.ParseExposedToolName(input.Name)
	if err != nil {
		return nil, &mcp.RPCError{Code: -32602, Message: err.Error()}
	}
	return g.manager.CallServer(ctx, serverID, "prompts/get", map[string]any{"name": name, "arguments": input.Arguments})
}

func (g *Gateway) decorateModernResult(method string, raw json.RawMessage) (json.RawMessage, error) {
	result := map[string]any{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
	}
	if _, ok := result["resultType"]; !ok {
		result["resultType"] = mcp.ResultComplete
	}
	meta := map[string]any{}
	if existing, ok := result["_meta"]; ok && existing != nil {
		data, err := json.Marshal(existing)
		if err == nil {
			_ = json.Unmarshal(data, &meta)
		}
	}
	meta[mcp.MetaServerInfo] = mcp.ClientInfo{Name: g.name, Version: g.version}
	result["_meta"] = meta

	if isCacheableMethod(method) {
		if _, ok := result["ttlMs"]; !ok {
			if method == "resources/read" {
				result["ttlMs"] = resourceCacheTTLMS
			} else {
				result["ttlMs"] = listCacheTTLMS
			}
		}
		if _, ok := result["cacheScope"]; !ok {
			result["cacheScope"] = mcp.CacheScopePrivate
		}
	}
	return json.Marshal(result)
}

func isCacheableMethod(method string) bool {
	switch method {
	case "server/discover", "tools/list", "resources/list", "resources/templates/list", "resources/read", "prompts/list":
		return true
	default:
		return false
	}
}

func cursorOffset(params json.RawMessage) int {
	if len(params) == 0 {
		return 0
	}
	var value struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(params, &value)
	offset, _ := strconv.Atoi(value.Cursor)
	if offset < 0 {
		return 0
	}
	return offset
}

func defaultObjectSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}

func prefixDescription(serverID, description string) string {
	if description == "" {
		return "[" + serverID + "]"
	}
	return "[" + serverID + "] " + description
}

func encodeResourceURI(serverID, original string) string {
	return "mcp-gateway://" + url.PathEscape(serverID) + "/" + base64.RawURLEncoding.EncodeToString([]byte(original))
}

func encodeResourceTemplateURI(serverID, original string) string {
	encoded := url.QueryEscape(original)
	// URI template variables must remain visible to the client so it can expand
	// them before issuing resources/read.
	encoded = strings.ReplaceAll(encoded, "%7B", "{")
	encoded = strings.ReplaceAll(encoded, "%7D", "}")
	return "mcp-gateway://" + url.PathEscape(serverID) + "/template?uri=" + encoded
}

func decodeResourceURI(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", "", err
	}
	if parsed.Scheme != "mcp-gateway" || parsed.Host == "" {
		return "", "", fmt.Errorf("resource URI is not managed by this gateway")
	}
	if strings.Trim(parsed.Path, "/") == "template" {
		original := parsed.Query().Get("uri")
		if original == "" {
			return "", "", fmt.Errorf("invalid gateway resource template URI")
		}
		return parsed.Host, original, nil
	}
	encoded := strings.TrimPrefix(parsed.EscapedPath(), "/")
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", fmt.Errorf("invalid gateway resource URI")
	}
	return parsed.Host, string(data), nil
}

func rewriteResourceResultURIs(raw json.RawMessage, serverID string) (json.RawMessage, error) {
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	contents, ok := result["contents"].([]any)
	if !ok {
		return raw, nil
	}
	for _, content := range contents {
		item, ok := content.(map[string]any)
		if !ok {
			continue
		}
		if uri, ok := item["uri"].(string); ok && uri != "" {
			item["uri"] = encodeResourceURI(serverID, uri)
		}
	}
	return json.Marshal(result)
}

func decodeOptionalJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}
