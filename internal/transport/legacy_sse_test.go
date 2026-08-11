package transport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
)

func TestLegacySSEEndpointDiscoveryAndCall(t *testing.T) {
	events := make(chan []byte, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, "event: endpoint\ndata: /messages\n\n")
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case payload := <-events:
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		request, _, err := mcp.ParseEnvelope(data)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if request != nil && !request.ID.IsZero() {
			payload, _ := mcp.NewResult(request.ID, map[string]any{"method": request.Method, "ok": true})
			events <- payload
		}
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewLegacySSE(HTTPOptions{URL: server.URL + "/sse", Timeout: 2 * time.Second, MaxResponseBytes: 1 << 20, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
	if !strings.Contains(string(result), `"method":"ping"`) || !strings.Contains(string(result), `"ok":true`) {
		t.Fatalf("unexpected legacy SSE result: %s", result)
	}
}
