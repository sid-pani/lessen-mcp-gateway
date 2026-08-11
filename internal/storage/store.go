package storage

import (
	"context"
	"errors"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
)

var ErrNotFound = errors.New("storage: not found")

type Store interface {
	Health(context.Context) error
	Close() error

	UpsertServer(context.Context, model.Server) error
	DeleteServer(context.Context, string) error
	GetServer(context.Context, string) (model.Server, error)
	ListServers(context.Context) ([]model.Server, error)

	ReplaceTools(context.Context, string, []model.Tool) error
	ListToolSummaries(context.Context, string, string, int, int) ([]model.ToolSummary, int, error)
	GetTool(context.Context, string, string) (model.Tool, error)
	ListTools(context.Context, string) ([]model.Tool, error)
	ListToolsPage(context.Context, string, int, int) ([]model.Tool, int, error)

	ReplaceResources(context.Context, string, []model.Resource) error
	ListResources(context.Context, string, int, int) ([]model.Resource, int, error)
	ReplaceResourceTemplates(context.Context, string, []model.ResourceTemplate) error
	ListResourceTemplates(context.Context, string, int, int) ([]model.ResourceTemplate, int, error)
	ReplacePrompts(context.Context, string, []model.Prompt) error
	ListPrompts(context.Context, string, int, int) ([]model.Prompt, int, error)
}
