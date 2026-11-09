package sources

import (
	"container/list"
	"encoding/json"
	"sync"
)

// pipelineCacheBytes bounds the cache of completed runs' details and logs.
const pipelineCacheBytes = 64 << 20

// pipelineCache holds what search_pipeline_run read of completed runs, for
// this process only; a restart starts empty.
var pipelineCache = newLRUCache(pipelineCacheBytes)

// lruCache is a size-bounded least-recently-used cache.
type lruCache struct {
	mu       sync.Mutex
	maxBytes int64
	bytes    int64
	order    *list.List // front: most recently used
	items    map[string]*list.Element
}

type lruEntry struct {
	key  string
	val  any
	size int64
}

func newLRUCache(maxBytes int64) *lruCache {
	return &lruCache{maxBytes: maxBytes, order: list.New(), items: map[string]*list.Element{}}
}

func (c *lruCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

// put stores val, evicting the least recently used entries beyond the
// bound. A value larger than the whole cache isn't kept.
func (c *lruCache) put(key string, val any, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if size > c.maxBytes {
		return
	}
	if el, ok := c.items[key]; ok {
		c.bytes -= el.Value.(*lruEntry).size
		c.order.Remove(el)
		delete(c.items, key)
	}
	c.items[key] = c.order.PushFront(&lruEntry{key: key, val: val, size: size})
	c.bytes += size
	for c.bytes > c.maxBytes {
		el := c.order.Back()
		e := el.Value.(*lruEntry)
		c.order.Remove(el)
		delete(c.items, e.key)
		c.bytes -= e.size
	}
}

// runSize approximates a run's memory by its JSON size.
func runSize(run *PipelineRun) int64 {
	data, err := json.Marshal(run)
	if err != nil {
		return 1 << 20
	}
	return int64(len(data))
}
