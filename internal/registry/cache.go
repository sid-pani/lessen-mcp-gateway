package registry

import (
	"container/list"
	"sync"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
)

type cacheEntry struct {
	key  string
	tool model.Tool
	size int64
}

type ToolCache struct {
	mu       sync.Mutex
	maxBytes int64
	bytes    int64
	items    map[string]*list.Element
	order    *list.List
}

func NewToolCache(maxBytes int64) *ToolCache {
	if maxBytes < 1<<20 {
		maxBytes = 1 << 20
	}
	return &ToolCache{maxBytes: maxBytes, items: map[string]*list.Element{}, order: list.New()}
}

func (c *ToolCache) Get(key string) (model.Tool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return model.Tool{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*cacheEntry).tool, true
}

func (c *ToolCache) Put(key string, tool model.Tool) {
	size := estimateToolBytes(tool)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.items[key]; existing != nil {
		entry := existing.Value.(*cacheEntry)
		c.bytes -= entry.size
		entry.tool = tool
		entry.size = size
		c.bytes += size
		c.order.MoveToFront(existing)
	} else {
		entry := &cacheEntry{key: key, tool: tool, size: size}
		c.items[key] = c.order.PushFront(entry)
		c.bytes += size
	}
	for c.bytes > c.maxBytes && c.order.Len() > 0 {
		last := c.order.Back()
		entry := last.Value.(*cacheEntry)
		delete(c.items, entry.key)
		c.bytes -= entry.size
		c.order.Remove(last)
	}
}

func (c *ToolCache) DeletePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, element := range c.items {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			entry := element.Value.(*cacheEntry)
			c.bytes -= entry.size
			delete(c.items, key)
			c.order.Remove(element)
		}
	}
}

func (c *ToolCache) Clear() {
	c.mu.Lock()
	c.items = map[string]*list.Element{}
	c.order.Init()
	c.bytes = 0
	c.mu.Unlock()
}
func (c *ToolCache) Stats() (entries int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len(), c.bytes
}
func (c *ToolCache) SetMaxBytes(max int64) {
	if max < 1<<20 {
		max = 1 << 20
	}
	c.mu.Lock()
	c.maxBytes = max
	for c.bytes > c.maxBytes && c.order.Len() > 0 {
		last := c.order.Back()
		entry := last.Value.(*cacheEntry)
		delete(c.items, entry.key)
		c.bytes -= entry.size
		c.order.Remove(last)
	}
	c.mu.Unlock()
}
func estimateToolBytes(t model.Tool) int64 {
	return int64(len(t.ServerID) + len(t.Name) + len(t.ExposedName) + len(t.Title) + len(t.Description) + len(t.InputSchema) + len(t.OutputSchema) + len(t.Annotations) + len(t.SchemaHash) + 256)
}
