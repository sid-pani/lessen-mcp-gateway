package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/registry"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

func TestApplyConfigUpdatesConcurrencyLimits(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Storage.Type = "memory"
	cfg.Discovery.OnStartup = false
	cfg.Memory.MaxConcurrent = 2
	cfg.Memory.MaxConcurrentPerServer = 1
	cfg.MCPServers["mock"] = config.ServerConfig{
		Type:    "stdio",
		Command: os.Args[0],
	}
	cfg.ApplyDefaults()

	reg, err := registry.New(ctx, storage.NewMemory(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	manager.mu.RLock()
	oldGlobal := manager.globalSem
	oldBackend := manager.backends["mock"]
	manager.mu.RUnlock()
	if got := cap(oldGlobal); got != 2 {
		t.Fatalf("initial global limit = %d, want 2", got)
	}
	if got := cap(oldBackend.sem); got != 1 {
		t.Fatalf("initial per-server limit = %d, want 1", got)
	}

	updated := cfg
	updated.Memory.MaxConcurrent = 7
	updated.Memory.MaxConcurrentPerServer = 3
	if err := manager.ApplyConfig(ctx, updated); err != nil {
		t.Fatal(err)
	}

	manager.mu.RLock()
	newGlobal := manager.globalSem
	newBackend := manager.backends["mock"]
	manager.mu.RUnlock()
	if newGlobal == oldGlobal {
		t.Fatal("global semaphore was not replaced")
	}
	if got := cap(newGlobal); got != 7 {
		t.Fatalf("updated global limit = %d, want 7", got)
	}
	if newBackend == oldBackend {
		t.Fatal("backend was not replaced after per-server limit changed")
	}
	if got := cap(newBackend.sem); got != 3 {
		t.Fatalf("updated per-server limit = %d, want 3", got)
	}
}

func TestWorkerCountIsBounded(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		total      int
		want       int
	}{
		{name: "empty", configured: 4, total: 0, want: 0},
		{name: "uses configured limit", configured: 4, total: 12, want: 4},
		{name: "caps at total", configured: 8, total: 3, want: 3},
		{name: "defensive minimum", configured: 0, total: 5, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerCount(tc.configured, tc.total); got != tc.want {
				t.Fatalf("workerCount(%d, %d) = %d, want %d", tc.configured, tc.total, got, tc.want)
			}
		})
	}
}
