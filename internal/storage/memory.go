package storage

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
)

type MemoryStore struct {
	mu        sync.RWMutex
	servers   map[string]model.Server
	tools     map[string]map[string]model.Tool
	resources map[string][]model.Resource
	templates map[string][]model.ResourceTemplate
	prompts   map[string][]model.Prompt
	closed    bool
}

func NewMemory() *MemoryStore {
	return &MemoryStore{servers: map[string]model.Server{}, tools: map[string]map[string]model.Tool{}, resources: map[string][]model.Resource{}, templates: map[string][]model.ResourceTemplate{}, prompts: map[string][]model.Prompt{}}
}
func (m *MemoryStore) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return ErrNotFound
	}
	return nil
}
func (m *MemoryStore) Close() error { m.mu.Lock(); m.closed = true; m.mu.Unlock(); return nil }
func (m *MemoryStore) UpsertServer(ctx context.Context, s model.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.servers[s.ID] = s
	m.mu.Unlock()
	return nil
}
func (m *MemoryStore) DeleteServer(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.servers, id)
	delete(m.tools, id)
	delete(m.resources, id)
	delete(m.templates, id)
	delete(m.prompts, id)
	m.mu.Unlock()
	return nil
}
func (m *MemoryStore) GetServer(ctx context.Context, id string) (model.Server, error) {
	if err := ctx.Err(); err != nil {
		return model.Server{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.servers[id]
	if !ok {
		return model.Server{}, ErrNotFound
	}
	return s, nil
}
func (m *MemoryStore) ListServers(ctx context.Context) ([]model.Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Server, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if left == right {
			return out[i].ID < out[j].ID
		}
		return left < right
	})
	return out, nil
}
func (m *MemoryStore) ReplaceTools(ctx context.Context, id string, items []model.Tool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	next := map[string]model.Tool{}
	for _, v := range items {
		next[v.Name] = v
	}
	m.tools[id] = next
	m.mu.Unlock()
	return nil
}
func (m *MemoryStore) ListToolSummaries(ctx context.Context, id, q string, limit, offset int) ([]model.ToolSummary, int, error) {
	items, err := m.ListTools(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	filtered := []model.ToolSummary{}
	for _, v := range items {
		if q == "" || strings.Contains(ToolSearchText(v), q) {
			filtered = append(filtered, model.ToolSummary{ServerID: v.ServerID, Name: v.Name, ExposedName: v.ExposedName, Title: v.Title, Description: v.Description, SchemaHash: v.SchemaHash, UpdatedAt: v.UpdatedAt})
		}
	}
	total := len(filtered)
	if limit <= 0 {
		limit = 200
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}
func (m *MemoryStore) GetTool(ctx context.Context, id, name string) (model.Tool, error) {
	if err := ctx.Err(); err != nil {
		return model.Tool{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.tools[id][name]
	if !ok {
		return model.Tool{}, ErrNotFound
	}
	return v, nil
}
func (m *MemoryStore) ListTools(ctx context.Context, id string) ([]model.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []model.Tool{}
	if id != "" {
		for _, v := range m.tools[id] {
			out = append(out, v)
		}
	} else {
		for _, group := range m.tools {
			for _, v := range group {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExposedName < out[j].ExposedName })
	return out, nil
}
func (m *MemoryStore) ListToolsPage(ctx context.Context, id string, limit, offset int) ([]model.Tool, int, error) {
	items, err := m.ListTools(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	return page(items, limit, offset)
}
func (m *MemoryStore) ReplaceResources(ctx context.Context, id string, v []model.Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.resources[id] = append([]model.Resource{}, v...)
	m.mu.Unlock()
	return nil
}
func (m *MemoryStore) ListResources(ctx context.Context, id string, limit, offset int) ([]model.Resource, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]model.Resource, 0)
	if id != "" {
		items = append(items, m.resources[id]...)
	} else {
		for _, group := range m.resources {
			items = append(items, group...)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].URI == items[j].URI {
			return items[i].ServerID < items[j].ServerID
		}
		return items[i].URI < items[j].URI
	})
	return page(items, limit, offset)
}
func (m *MemoryStore) ReplaceResourceTemplates(ctx context.Context, id string, v []model.ResourceTemplate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.templates[id] = append([]model.ResourceTemplate{}, v...)
	m.mu.Unlock()
	return nil
}
func (m *MemoryStore) ListResourceTemplates(ctx context.Context, id string, limit, offset int) ([]model.ResourceTemplate, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]model.ResourceTemplate, 0)
	if id != "" {
		items = append(items, m.templates[id]...)
	} else {
		for _, group := range m.templates {
			items = append(items, group...)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].URITemplate == items[j].URITemplate {
			return items[i].ServerID < items[j].ServerID
		}
		return items[i].URITemplate < items[j].URITemplate
	})
	return page(items, limit, offset)
}
func (m *MemoryStore) ReplacePrompts(ctx context.Context, id string, v []model.Prompt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.prompts[id] = append([]model.Prompt{}, v...)
	m.mu.Unlock()
	return nil
}
func (m *MemoryStore) ListPrompts(ctx context.Context, id string, limit, offset int) ([]model.Prompt, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]model.Prompt, 0)
	if id != "" {
		items = append(items, m.prompts[id]...)
	} else {
		for _, group := range m.prompts {
			items = append(items, group...)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		left := items[i].ServerID + "__" + items[i].Name
		right := items[j].ServerID + "__" + items[j].Name
		return left < right
	})
	return page(items, limit, offset)
}
func page[T any](items []T, limit, offset int) ([]T, int, error) {
	total := len(items)
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return items[offset:end], total, nil
}
