package model

import (
	"encoding/json"
	"time"
)

type TransportType string

const (
	TransportStdio          TransportType = "stdio"
	TransportStreamableHTTP TransportType = "streamable-http"
	TransportLegacySSE      TransportType = "sse"
)

type HealthStatus string

const (
	HealthStarting  HealthStatus = "starting"
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthSleeping  HealthStatus = "sleeping"
	HealthDisabled  HealthStatus = "disabled"
)

type Server struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Transport             TransportType   `json:"transport"`
	Status                HealthStatus    `json:"status"`
	StatusMessage         string          `json:"statusMessage,omitempty"`
	Endpoint              string          `json:"endpoint,omitempty"`
	ProtocolVersion       string          `json:"protocolVersion,omitempty"`
	ServerVersion         string          `json:"serverVersion,omitempty"`
	Capabilities          json.RawMessage `json:"capabilities,omitempty"`
	ToolCount             int             `json:"toolCount"`
	ResourceCount         int             `json:"resourceCount"`
	ResourceTemplateCount int             `json:"resourceTemplateCount"`
	PromptCount           int             `json:"promptCount"`
	LastHealthCheck       time.Time       `json:"lastHealthCheck,omitempty"`
	LastDiscoveredAt      time.Time       `json:"lastDiscoveredAt,omitempty"`
	ConnectedSince        time.Time       `json:"connectedSince,omitempty"`
	ConfigHash            string          `json:"configHash,omitempty"`
}

type Tool struct {
	ServerID     string          `json:"serverId"`
	Name         string          `json:"name"`
	ExposedName  string          `json:"exposedName"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	SchemaHash   string          `json:"schemaHash"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

type ToolSummary struct {
	ServerID    string    `json:"serverId"`
	Name        string    `json:"name"`
	ExposedName string    `json:"exposedName"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	SchemaHash  string    `json:"schemaHash"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Resource struct {
	ServerID    string          `json:"serverId"`
	URI         string          `json:"uri"`
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	MIMEType    string          `json:"mimeType,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type ResourceTemplate struct {
	ServerID    string          `json:"serverId"`
	URITemplate string          `json:"uriTemplate"`
	Name        string          `json:"name,omitempty"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	MIMEType    string          `json:"mimeType,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type Prompt struct {
	ServerID    string          `json:"serverId"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type GatewayStats struct {
	StartedAt          time.Time `json:"startedAt"`
	ServerCount        int       `json:"serverCount"`
	HealthyServers     int       `json:"healthyServers"`
	UnhealthyServers   int       `json:"unhealthyServers"`
	SleepingServers    int       `json:"sleepingServers"`
	ToolCount          int       `json:"toolCount"`
	ResourceCount      int       `json:"resourceCount"`
	PromptCount        int       `json:"promptCount"`
	Goroutines         int       `json:"goroutines"`
	HeapAllocBytes     uint64    `json:"heapAllocBytes"`
	HeapInUseBytes     uint64    `json:"heapInUseBytes"`
	SysBytes           uint64    `json:"sysBytes"`
	SchemaCacheBytes   int64     `json:"schemaCacheBytes"`
	SchemaCacheEntries int       `json:"schemaCacheEntries"`
	LogBufferBytes     int64     `json:"logBufferBytes"`
	LogBufferEntries   int       `json:"logBufferEntries"`
}
