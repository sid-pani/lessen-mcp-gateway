//go:build !postgres

package postgres

import (
	"errors"

	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

func Open(string) (storage.Store, error) {
	return nil, errors.New("PostgreSQL storage is optional: rebuild with -tags postgres and install libpq development headers")
}
