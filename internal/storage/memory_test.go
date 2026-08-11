package storage

import (
	"context"
	"testing"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
)

func TestMemoryStoreAggregatesCapabilityLists(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	now := time.Now().UTC()
	if err := store.ReplaceResources(ctx, "one", []model.Resource{{ServerID: "one", URI: "one://a", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceResources(ctx, "two", []model.Resource{{ServerID: "two", URI: "two://b", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	items, total, err := store.ListResources(ctx, "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("resources total=%d len=%d, want 2", total, len(items))
	}

	if err := store.ReplacePrompts(ctx, "one", []model.Prompt{{ServerID: "one", Name: "a", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePrompts(ctx, "two", []model.Prompt{{ServerID: "two", Name: "b", UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	prompts, total, err := store.ListPrompts(ctx, "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(prompts) != 2 {
		t.Fatalf("prompts total=%d len=%d, want 2", total, len(prompts))
	}
}
