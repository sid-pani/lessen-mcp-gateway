package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
)

func TestStreamableHTTPModernHeadersJSONSSEAndNoSession(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		request, _, err := mcp.ParseEnvelope(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		if got := r.Header.Get("MCP-Protocol-Version"); got != mcp.ProtocolLatest {
			t.Errorf("protocol header = %q", got)
		}
		if got := r.Header.Get("Mcp-Method"); got != request.Method {
			t.Errorf("method header = %q, want %q", got, request.Method)
		}
		if got := r.Header.Get("Mcp-Session-Id"); got != "" {
			t.Errorf("modern request leaked session header %q", got)
		}
		if request.Method == "tools/call" {
			if got := r.Header.Get("Mcp-Name"); got != "echo" {
				t.Errorf("name header = %q", got)
			}
		} else if got := r.Header.Get("Mcp-Name"); got != "" {
			t.Errorf("unexpected name header = %q", got)
		}
		if call == 1 {
			// A non-conforming modern server may return this header. The client must
			// ignore it and remain stateless.
			w.Header().Set("Mcp-Session-Id", "should-be-ignored")
		}
		if request.Method == "tools/list" {
			payload, _ := mcp.NewResult(request.ID, map[string]any{"resultType": "complete", "tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}})
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
			return
		}
		payload, _ := mcp.NewResult(request.ID, map[string]any{"resultType": "complete", "ok": true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client, err := NewStreamableHTTP(HTTPOptions{URL: server.URL, ProtocolVersion: mcp.ProtocolLatest, MaxResponseBytes: 1 << 20, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := client.Call(ctx, "ping", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"ok":true`) {
		t.Fatalf("unexpected JSON response: %s", result)
	}
	result, err = client.Call(ctx, "tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(result) || !strings.Contains(string(result), `"name":"echo"`) {
		t.Fatalf("unexpected SSE response: %s", result)
	}
	if _, err = client.Call(ctx, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamableHTTPLegacySessionHeaders(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		request, _, err := mcp.ParseEnvelope(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		call := calls.Add(1)
		if got := r.Header.Get("MCP-Protocol-Version"); got != mcp.ProtocolPrevious {
			t.Errorf("protocol header = %q", got)
		}
		if got := r.Header.Get("Mcp-Method"); got != request.Method {
			t.Errorf("method header = %q", got)
		}
		if call == 1 {
			w.Header().Set("Mcp-Session-Id", "session-1")
		} else if got := r.Header.Get("Mcp-Session-Id"); got != "session-1" {
			t.Errorf("session header = %q", got)
		}
		payload, _ := mcp.NewResult(request.ID, map[string]any{"ok": true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	client, err := NewStreamableHTTP(HTTPOptions{URL: server.URL, ProtocolVersion: mcp.ProtocolPrevious, MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Call(context.Background(), "initialize", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "ping", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamableHTTPEnforcesResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", 2048))
	}))
	defer server.Close()
	client, err := NewStreamableHTTP(HTTPOptions{URL: server.URL, MaxResponseBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Call(context.Background(), "ping", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bounded response error, got %v", err)
	}
}
