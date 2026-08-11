package transport

import (
	"context"
	"encoding/json"
)

type Client interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Notify(ctx context.Context, method string, params any) error
	Health(ctx context.Context) error
	Close() error
}
