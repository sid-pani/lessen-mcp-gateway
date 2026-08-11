package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	JSONRPCVersion   = "2.0"
	ProtocolLatest   = "2026-07-28"
	ProtocolPrevious = "2025-11-25"
	ProtocolOlder    = "2025-06-18"
	ProtocolLegacy   = "2024-11-05"

	ResultComplete = "complete"

	CacheScopePrivate = "private"
	CacheScopePublic  = "public"

	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

var SupportedProtocols = []string{ProtocolLatest, ProtocolPrevious, ProtocolOlder, ProtocolLegacy}
var LegacyProtocols = []string{ProtocolPrevious, ProtocolOlder, ProtocolLegacy}

type ID json.RawMessage

func (id *ID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		*id = nil
		return nil
	}
	if !json.Valid(trimmed) {
		return fmt.Errorf("invalid JSON-RPC id")
	}
	// JSON-RPC IDs are strings, numbers, or null. Reject containers and booleans
	// so malformed messages cannot enter the pending-request maps.
	if trimmed[0] == '{' || trimmed[0] == '[' || bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		return fmt.Errorf("invalid JSON-RPC id type")
	}
	*id = append((*id)[:0], trimmed...)
	return nil
}

func (id ID) MarshalJSON() ([]byte, error) {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 {
		return []byte("null"), nil
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("invalid JSON-RPC id")
	}
	return append([]byte(nil), trimmed...), nil
}

func NumberID(value uint64) ID {
	data, _ := json.Marshal(value)
	return ID(data)
}

func (id ID) String() string { return string(id) }
func (id ID) IsZero() bool {
	return len(bytes.TrimSpace(id)) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null"))
}

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      ID              `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      ID              `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

func NewRequest(id ID, method string, params any) ([]byte, error) {
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = data
	}
	return json.Marshal(Request{JSONRPC: JSONRPCVersion, ID: id, Method: method, Params: raw})
}

func NewNotification(method string, params any) ([]byte, error) {
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = data
	}
	return json.Marshal(Request{JSONRPC: JSONRPCVersion, Method: method, Params: raw})
}

func NewResult(id ID, result any) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Response{JSONRPC: JSONRPCVersion, ID: id, Result: data})
}

func NewError(id ID, code int, message string, data any) ([]byte, error) {
	var raw json.RawMessage
	if data != nil {
		raw, _ = json.Marshal(data)
	}
	return json.Marshal(Response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: code, Message: message, Data: raw}})
}

func ParseEnvelope(data []byte) (request *Request, response *Response, err error) {
	var base struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, nil, err
	}
	if base.JSONRPC != JSONRPCVersion {
		return nil, nil, fmt.Errorf("unsupported jsonrpc version %q", base.JSONRPC)
	}
	if base.Method != "" {
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, nil, err
		}
		return &req, nil, nil
	}
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, nil, err
	}
	return nil, &resp, nil
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type RequestMeta struct {
	ProtocolVersion    string         `json:"io.modelcontextprotocol/protocolVersion,omitempty"`
	ClientInfo         *ClientInfo    `json:"io.modelcontextprotocol/clientInfo,omitempty"`
	ClientCapabilities map[string]any `json:"io.modelcontextprotocol/clientCapabilities,omitempty"`
}

// AddRequestMeta creates a fresh params object containing the stateless MCP
// 2026 request metadata. The caller's input is never mutated.
func AddRequestMeta(params any, protocolVersion string, clientInfo ClientInfo, capabilities map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if len(data) > 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, fmt.Errorf("MCP params must be a JSON object: %w", err)
			}
		}
	}
	meta := map[string]any{}
	if existing, ok := out["_meta"]; ok && existing != nil {
		data, err := json.Marshal(existing)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("MCP params _meta must be a JSON object: %w", err)
		}
	}
	meta[MetaProtocolVersion] = protocolVersion
	if clientInfo.Name != "" || clientInfo.Version != "" {
		meta[MetaClientInfo] = clientInfo
	}
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	meta[MetaClientCapabilities] = capabilities
	out["_meta"] = meta
	return out, nil
}

func ParseRequestMeta(params json.RawMessage) (RequestMeta, error) {
	if len(bytes.TrimSpace(params)) == 0 {
		return RequestMeta{}, nil
	}
	var envelope struct {
		Meta json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return RequestMeta{}, fmt.Errorf("decode request params: %w", err)
	}
	if len(bytes.TrimSpace(envelope.Meta)) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Meta), []byte("null")) {
		return RequestMeta{}, nil
	}
	var meta RequestMeta
	if err := json.Unmarshal(envelope.Meta, &meta); err != nil {
		return RequestMeta{}, fmt.Errorf("decode request _meta: %w", err)
	}
	return meta, nil
}

func RequestProtocol(params json.RawMessage) string {
	meta, err := ParseRequestMeta(params)
	if err != nil {
		return ""
	}
	return meta.ProtocolVersion
}

func IsModernRequest(method string, params json.RawMessage) bool {
	return method == "server/discover" || RequestProtocol(params) == ProtocolLatest
}

func RequestName(method string, params json.RawMessage) string {
	var input map[string]json.RawMessage
	if json.Unmarshal(params, &input) != nil {
		return ""
	}
	key := ""
	switch method {
	case "tools/call", "prompts/get":
		key = "name"
	case "resources/read":
		key = "uri"
	default:
		return ""
	}
	var value string
	_ = json.Unmarshal(input[key], &value)
	return value
}

func SupportsProtocol(value string) bool {
	for _, supported := range SupportedProtocols {
		if value == supported {
			return true
		}
	}
	return false
}

func SupportsLegacyProtocol(value string) bool {
	for _, supported := range LegacyProtocols {
		if value == supported {
			return true
		}
	}
	return false
}

type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}
type InitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      ClientInfo      `json:"serverInfo"`
	Instructions    string          `json:"instructions,omitempty"`
}

type DiscoverResult struct {
	ResultType        string                     `json:"resultType,omitempty"`
	SupportedVersions []string                   `json:"supportedVersions"`
	Capabilities      json.RawMessage            `json:"capabilities"`
	Instructions      string                     `json:"instructions,omitempty"`
	TTLMS             int64                      `json:"ttlMs,omitempty"`
	CacheScope        string                     `json:"cacheScope,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
	// ServerInfo accepts release-candidate and early SDK responses. The final
	// 2026 revision carries server identity in result _meta instead.
	ServerInfo ClientInfo `json:"serverInfo,omitempty"`
}

func (r DiscoverResult) Identity() ClientInfo {
	if raw := r.Meta[MetaServerInfo]; len(raw) > 0 {
		var info ClientInfo
		if json.Unmarshal(raw, &info) == nil {
			return info
		}
	}
	return r.ServerInfo
}

type ToolDefinition struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}
type ListToolsResult struct {
	ResultType string           `json:"resultType,omitempty"`
	Tools      []ToolDefinition `json:"tools"`
	NextCursor string           `json:"nextCursor,omitempty"`
	TTLMS      int64            `json:"ttlMs,omitempty"`
	CacheScope string           `json:"cacheScope,omitempty"`
}
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type ResourceDefinition struct {
	URI         string          `json:"uri"`
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	MIMEType    string          `json:"mimeType,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}
type ResourceTemplateDefinition struct {
	URITemplate string          `json:"uriTemplate"`
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	MIMEType    string          `json:"mimeType,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}
type PromptDefinition struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}
type ListResourcesResult struct {
	ResultType string               `json:"resultType,omitempty"`
	Resources  []ResourceDefinition `json:"resources"`
	NextCursor string               `json:"nextCursor,omitempty"`
	TTLMS      int64                `json:"ttlMs,omitempty"`
	CacheScope string               `json:"cacheScope,omitempty"`
}
type ListResourceTemplatesResult struct {
	ResultType        string                       `json:"resultType,omitempty"`
	ResourceTemplates []ResourceTemplateDefinition `json:"resourceTemplates"`
	NextCursor        string                       `json:"nextCursor,omitempty"`
	TTLMS             int64                        `json:"ttlMs,omitempty"`
	CacheScope        string                       `json:"cacheScope,omitempty"`
}
type ListPromptsResult struct {
	ResultType string             `json:"resultType,omitempty"`
	Prompts    []PromptDefinition `json:"prompts"`
	NextCursor string             `json:"nextCursor,omitempty"`
	TTLMS      int64              `json:"ttlMs,omitempty"`
	CacheScope string             `json:"cacheScope,omitempty"`
}
