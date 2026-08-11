package mcp

import (
	"encoding/json"
	"testing"
)

func TestIDPreservesRawJSON(t *testing.T) {
	payload, err := NewRequest(NumberID(42), "ping", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"jsonrpc":"2.0","id":42,"method":"ping","params":{}}` {
		t.Fatalf("request = %s", payload)
	}
	request, response, err := ParseEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	if response != nil || request == nil || request.ID.String() != "42" {
		t.Fatalf("unexpected envelope: request=%+v response=%+v", request, response)
	}

	var stringID ID
	if err := json.Unmarshal([]byte(`"abc"`), &stringID); err != nil {
		t.Fatal(err)
	}
	if stringID.String() != `"abc"` {
		t.Fatalf("string id = %q", stringID.String())
	}
}

func TestIDRejectsContainersAndBooleans(t *testing.T) {
	for _, input := range []string{`{}`, `[]`, `true`, `false`} {
		var id ID
		if err := json.Unmarshal([]byte(input), &id); err == nil {
			t.Fatalf("expected %s to be rejected", input)
		}
	}
}

func TestModernRequestMetadataRoundTrip(t *testing.T) {
	params, err := AddRequestMeta(map[string]any{"name": "search"}, ProtocolLatest, ClientInfo{Name: "gateway", Version: "1"}, map[string]any{"extensions": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseRequestMeta(data)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProtocolVersion != ProtocolLatest || meta.ClientInfo == nil || meta.ClientInfo.Name != "gateway" || meta.ClientCapabilities == nil {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if got := RequestName("tools/call", data); got != "search" {
		t.Fatalf("request name = %q", got)
	}
	if !IsModernRequest("tools/call", data) {
		t.Fatal("request was not classified as modern")
	}
}

func TestAddRequestMetaDoesNotMutateInput(t *testing.T) {
	input := map[string]any{"cursor": "10", "_meta": map[string]any{"traceparent": "00-test"}}
	params, err := AddRequestMeta(input, ProtocolLatest, ClientInfo{}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	inputMeta := input["_meta"].(map[string]any)
	if _, exists := inputMeta[MetaProtocolVersion]; exists {
		t.Fatal("input map was mutated")
	}
	meta := params["_meta"].(map[string]any)
	if meta["traceparent"] != "00-test" || meta[MetaProtocolVersion] != ProtocolLatest {
		t.Fatalf("metadata was not merged: %+v", meta)
	}
}

func TestDiscoverIdentityUsesFinalServerInfoMeta(t *testing.T) {
	result := DiscoverResult{Meta: map[string]json.RawMessage{MetaServerInfo: json.RawMessage(`{"name":"server","version":"2"}`)}, ServerInfo: ClientInfo{Name: "legacy", Version: "1"}}
	identity := result.Identity()
	if identity.Name != "server" || identity.Version != "2" {
		t.Fatalf("identity = %+v", identity)
	}
}
