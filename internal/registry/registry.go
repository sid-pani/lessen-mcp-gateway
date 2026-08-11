package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

type toolRef struct {
	ServerID string
	Name     string
}

type Registry struct {
	store     storage.Store
	cache     *ToolCache
	mu        sync.RWMutex
	toolIndex map[string]toolRef
}

func New(ctx context.Context, store storage.Store, cacheBytes int64) (*Registry, error) {
	r := &Registry{store: store, cache: NewToolCache(cacheBytes), toolIndex: map[string]toolRef{}}
	if err := r.RebuildIndex(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) RebuildIndex(ctx context.Context) error {
	items, _, err := r.store.ListToolSummaries(ctx, "", "", 1000, 0)
	if err != nil {
		return err
	}
	index := map[string]toolRef{}
	offset := len(items)
	for {
		for _, item := range items {
			index[item.ExposedName] = toolRef{ServerID: item.ServerID, Name: item.Name}
		}
		if len(items) < 1000 {
			break
		}
		items, _, err = r.store.ListToolSummaries(ctx, "", "", 1000, offset)
		if err != nil {
			return err
		}
		offset += len(items)
	}
	r.mu.Lock()
	r.toolIndex = index
	r.mu.Unlock()
	return nil
}

func (r *Registry) Health(ctx context.Context) error { return r.store.Health(ctx) }

func (r *Registry) UpsertServer(ctx context.Context, server model.Server) error {
	return r.store.UpsertServer(ctx, server)
}
func (r *Registry) DeleteServer(ctx context.Context, id string) error {
	if err := r.store.DeleteServer(ctx, id); err != nil {
		return err
	}
	r.cache.DeletePrefix(id + "/")
	r.mu.Lock()
	for key, ref := range r.toolIndex {
		if ref.ServerID == id {
			delete(r.toolIndex, key)
		}
	}
	r.mu.Unlock()
	return nil
}
func (r *Registry) GetServer(ctx context.Context, id string) (model.Server, error) {
	return r.store.GetServer(ctx, id)
}
func (r *Registry) ListServers(ctx context.Context) ([]model.Server, error) {
	items, err := r.store.ListServers(ctx)
	if err == nil {
		sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	}
	return items, err
}

func (r *Registry) ReplaceTools(ctx context.Context, serverID string, tools []model.Tool) error {
	if err := r.store.ReplaceTools(ctx, serverID, tools); err != nil {
		return err
	}
	r.cache.DeletePrefix(serverID + "/")
	r.mu.Lock()
	for key, ref := range r.toolIndex {
		if ref.ServerID == serverID {
			delete(r.toolIndex, key)
		}
	}
	for _, tool := range tools {
		r.toolIndex[tool.ExposedName] = toolRef{ServerID: tool.ServerID, Name: tool.Name}
	}
	r.mu.Unlock()
	return nil
}
func (r *Registry) ListToolSummaries(ctx context.Context, serverID, query string, limit, offset int) ([]model.ToolSummary, int, error) {
	return r.store.ListToolSummaries(ctx, serverID, query, limit, offset)
}
func (r *Registry) GetTool(ctx context.Context, serverID, name string) (model.Tool, error) {
	key := serverID + "/" + name
	if tool, ok := r.cache.Get(key); ok {
		return tool, nil
	}
	tool, err := r.store.GetTool(ctx, serverID, name)
	if err != nil {
		return model.Tool{}, err
	}
	r.cache.Put(key, tool)
	return tool, nil
}
func (r *Registry) ResolveTool(ctx context.Context, exposedName string) (model.Tool, error) {
	r.mu.RLock()
	ref, ok := r.toolIndex[exposedName]
	r.mu.RUnlock()
	if !ok {
		return model.Tool{}, storage.ErrNotFound
	}
	return r.GetTool(ctx, ref.ServerID, ref.Name)
}
func (r *Registry) ListTools(ctx context.Context) ([]model.Tool, error) {
	return r.store.ListTools(ctx, "")
}
func (r *Registry) ListToolsPage(ctx context.Context, serverID string, limit, offset int) ([]model.Tool, int, error) {
	return r.store.ListToolsPage(ctx, serverID, limit, offset)
}
func (r *Registry) ReplaceResources(ctx context.Context, id string, items []model.Resource) error {
	return r.store.ReplaceResources(ctx, id, items)
}
func (r *Registry) ListResources(ctx context.Context, id string, limit, offset int) ([]model.Resource, int, error) {
	return r.store.ListResources(ctx, id, limit, offset)
}
func (r *Registry) ReplaceResourceTemplates(ctx context.Context, id string, items []model.ResourceTemplate) error {
	return r.store.ReplaceResourceTemplates(ctx, id, items)
}
func (r *Registry) ListResourceTemplates(ctx context.Context, id string, limit, offset int) ([]model.ResourceTemplate, int, error) {
	return r.store.ListResourceTemplates(ctx, id, limit, offset)
}
func (r *Registry) ReplacePrompts(ctx context.Context, id string, items []model.Prompt) error {
	return r.store.ReplacePrompts(ctx, id, items)
}
func (r *Registry) ListPrompts(ctx context.Context, id string, limit, offset int) ([]model.Prompt, int, error) {
	return r.store.ListPrompts(ctx, id, limit, offset)
}
func (r *Registry) CacheStats() (int, int64)  { return r.cache.Stats() }
func (r *Registry) SetCacheBytes(value int64) { r.cache.SetMaxBytes(value) }

func ExposedToolName(serverID, toolName string) string { return serverID + "__" + toolName }
func ParseExposedToolName(value string) (string, string, error) {
	server, name, ok := strings.Cut(value, "__")
	if !ok || server == "" || name == "" {
		return "", "", fmt.Errorf("invalid namespaced tool name %q", value)
	}
	return server, name, nil
}
func IsNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }
