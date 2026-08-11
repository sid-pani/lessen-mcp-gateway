package app

import (
	"encoding/json"
	"testing"
)

func TestSupportsCapability(t *testing.T) {
	raw := json.RawMessage(`{"resources":{"subscribe":true},"prompts":{}}`)
	if supportsCapability(raw, "tools") {
		t.Fatal("tools should not be reported when absent")
	}
	if !supportsCapability(raw, "resources") || !supportsCapability(raw, "prompts") {
		t.Fatal("advertised capabilities should be detected")
	}
	if !supportsCapability(nil, "tools") {
		t.Fatal("missing capability document should use compatibility probing")
	}
}
