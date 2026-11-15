package sources

import (
	"container/list"
	"encoding/json"
	"sync"
	"time"
)

// How long short-lived answers are kept (see shortCache). Each is the most
// a change on the provider can go unseen.
const (
	// pipelineListTTL: the pipelines a source covers (a pipeline added to a
	// folder is listed within this time).
	pipelineListTTL = 5 * time.Minute
	// runStateTTL: a completed run's state, checked before its cached details
	// and logs are served (a rerun is seen within this time).
	runStateTTL = 60 * time.Second
	// liveRunTTL: what was read of a run still in progress (an agent polling
	// a running build asks the provider at most this often).
	liveRunTTL = 30 * time.Second
	// prStateTTL: a pull request's iterations (Azure DevOps) or head commit
	// (GitHub), which decide which cached file lists apply (a push is seen
	// within this time).
	prStateTTL = 60 * time.Second
)

// shortCache keeps answers that change for a short time, for this process
// only.
var shortCache = &ttlCache{items: map[string]ttlEntry{}}

// ttlSweepAt is the entry count at which put first drops expired entries.
const ttlSweepAt = 1024

// ttlCache keeps each value until its own expiry.
type ttlCache struct {
	mu    sync.Mutex
	items map[string]ttlEntry
}

type ttlEntry struct {
	val     any
	expires time.Time
}

func (c *ttlCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		delete(c.items, key)
		return nil, false
	}
	return e.val, true
}

func (c *ttlCache) put(key string, val any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.items) >= ttlSweepAt {
		for k, e := range c.items {
			if now.After(e.expires) {
				delete(c.items, k)
			}
		}
	}
	c.items[key] = ttlEntry{val: val, expires: now.Add(ttl)}
}

// prCacheBytes bounds the cache of pull request data that never changes.
const prCacheBytes = 64 << 20

// prCache holds what search_pull_request read that can't change: file
// versions by blob id or commit, a pull request's changed files at an
// iteration or head commit, and commit listings at a head commit.
var prCache = newLRUCache(prCacheBytes)

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
func runSize(run *PipelineRun) int64 { return jsonSize(run) }

// jsonSize approximates a value's memory by its JSON size.
func jsonSize(v any) int64 {
	data, err := json.Marshal(v)
	if err != nil {
		return 1 << 20
	}
	return int64(len(data))
}
