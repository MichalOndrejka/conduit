package sources

import (
	"container/list"
	"encoding/json"
	"sync"
	"time"
)

// pipelineListTTL is how long the pipelines a source covers are kept: a
// pipeline added to a folder is listed within this time.
const pipelineListTTL = 5 * time.Minute

// pipelineLists keeps each source's pipelines (see PipelineProvider.Pipelines).
var pipelineLists = &ttlCache{ttl: pipelineListTTL, items: map[string]ttlEntry{}}

// ttlCache keeps pipeline lists for a fixed time. Its keys are few — one per
// pipeline-run source — so expired entries are dropped when read.
type ttlCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]ttlEntry
}

type ttlEntry struct {
	val []PipelineInfo
	at  time.Time
}

func (c *ttlCache) get(key string) ([]PipelineInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.items, key)
		return nil, false
	}
	return e.val, true
}

func (c *ttlCache) put(key string, val []PipelineInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = ttlEntry{val: val, at: time.Now()}
}

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
