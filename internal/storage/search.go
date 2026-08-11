package storage

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
)

const maxToolSearchTextBytes = 8 << 10

// ToolSearchText builds a small persisted search document from stable metadata
// and top-level input argument names. It deliberately avoids indexing the full
// JSON schema so search remains useful without duplicating large schemas in RAM
// or on disk.
func ToolSearchText(tool model.Tool) string {
	parts := []string{tool.ServerID, tool.Name, tool.ExposedName, tool.Title, tool.Description}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if len(tool.InputSchema) > 0 && json.Unmarshal(tool.InputSchema, &schema) == nil {
		keys := make([]string, 0, len(schema.Properties)+len(schema.Required))
		for key := range schema.Properties {
			keys = append(keys, key)
		}
		keys = append(keys, schema.Required...)
		sort.Strings(keys)
		parts = append(parts, keys...)
	}
	text := strings.ToLower(strings.Join(parts, " "))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxToolSearchTextBytes {
		text = text[:maxToolSearchTextBytes]
	}
	return text
}
