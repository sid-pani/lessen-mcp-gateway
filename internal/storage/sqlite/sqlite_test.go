package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
)

func TestStorePersistsRegistryWithoutKeepingSchemaObjectsInMemory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := model.Server{ID: "github", Name: "GitHub", Transport: model.TransportStreamableHTTP, Status: model.HealthHealthy, ToolCount: 1, LastDiscoveredAt: now}
	if err := store.UpsertServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	tool := model.Tool{ServerID: "github", Name: "create_issue", ExposedName: "github__create_issue", Description: "Create an issue", InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"assignee_login":{"type":"string"}}}`), SchemaHash: "hash", UpdatedAt: now}
	if err := store.ReplaceTools(ctx, "github", []model.Tool{tool}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gotServer, err := store.GetServer(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if gotServer.Name != "GitHub" || gotServer.ToolCount != 1 {
		t.Fatalf("unexpected server: %+v", gotServer)
	}
	gotTool, err := store.GetTool(ctx, "github", "create_issue")
	if err != nil {
		t.Fatal(err)
	}
	if gotTool.ExposedName != tool.ExposedName || string(gotTool.InputSchema) != string(tool.InputSchema) {
		t.Fatalf("unexpected tool: %+v", gotTool)
	}
	for _, query := range []string{"issue", "assignee_login"} {
		matches, total, err := store.ListToolSummaries(ctx, "", query, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(matches) != 1 || matches[0].ExposedName != tool.ExposedName {
			t.Fatalf("unexpected search result for %q: total=%d matches=%+v", query, total, matches)
		}
	}
}
