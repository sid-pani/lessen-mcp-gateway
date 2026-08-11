package app

import (
	"fmt"

	"github.com/sid-pani/lessen-mcp-gateway/internal/config"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage/postgres"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage/sqlite"
)

func OpenStore(cfg config.StorageConfig) (storage.Store, error) {
	switch cfg.Type {
	case "memory":
		return storage.NewMemory(), nil
	case "sqlite", "":
		return sqlite.Open(cfg.Path)
	case "postgres":
		dsn, err := cfg.DSN.Resolve()
		if err != nil {
			return nil, err
		}
		return postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported storage type %q", cfg.Type)
	}
}
