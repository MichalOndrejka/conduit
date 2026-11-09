// Live sync state for the Sources page, pushed as server-sent events.
//
// Every change the page shows — a source saved (sync started or finished,
// edited, deleted), sync progress, a pause or resume — reaches the broker
// through the stores' OnChange hooks. Each open page holds one /events
// stream; on a change the stream sends a fresh snapshot of every source,
// built from memory only. The one value that lives in Qdrant, each source's
// vector count, is cached and recounted in the background after its source
// changes, so neither the stream nor a page render waits on Qdrant.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// broker fans a "something changed" signal out to every open stream. A
// signal carries no data: each stream reads the current state when it's
// ready, so a burst of changes collapses into one pending signal.
type broker struct {
	mu     sync.Mutex
	subs   map[chan struct{}]struct{}
	done   chan struct{} // closed by close: every stream ends
	closed bool
}

func newBroker() *broker {
	return &broker{subs: map[chan struct{}]struct{}{}, done: make(chan struct{})}
}

func (b *broker) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// notify never blocks: a stream that already has a signal pending keeps it.
func (b *broker) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.done)
	}
}

// countRetryDelay is how long a failed count waits before it's tried again.
const countRetryDelay = 5 * time.Second

// countTimeout bounds one background count request.
const countTimeout = 10 * time.Second

// vectorCounts caches how many points each source has in Qdrant. A count is
// taken once, in the background, and kept until a source of the same
// collection changes.
type vectorCounts struct {
	vectors *rag.VectorStore
	onReady func() // a count finished (or failed): state worth re-sending

	mu       sync.Mutex
	known    map[string]sourceCount
	inflight map[string]chan struct{}
	failedAt map[string]time.Time
	gen      int // bumped by forget; a count started before it is stale
}

// sourceCount is one cached count and the collection it was taken in.
type sourceCount struct {
	n          int
	collection string
}

func newVectorCounts(vectors *rag.VectorStore, onReady func()) *vectorCounts {
	return &vectorCounts{
		vectors: vectors, onReady: onReady,
		known: map[string]sourceCount{}, inflight: map[string]chan struct{}{}, failedAt: map[string]time.Time{},
	}
}

// forget drops the cached count of sourceID and of every source sharing its
// collection, or of every source for "". The whole collection, because a
// sync can change other sources' points there: one that finds the
// collection built for another embedding size recreates it empty.
func (c *vectorCounts) forget(sourceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.failedAt)
	entry, ok := c.known[sourceID]
	if sourceID == "" || !ok {
		// Unknown collection (never counted, or just deleted): forgetting
		// everything is the safe choice, and counts are cheap to retake.
		clear(c.known)
		return
	}
	for id, other := range c.known {
		if other.collection == entry.collection {
			delete(c.known, id)
		}
	}
}

// lookup returns the known count of each source, waiting up to wait for
// counts that are missing. A source missing from the result has no count
// yet; onReady fires once it does.
func (c *vectorCounts) lookup(srcs []models.SourceDefinition, wait time.Duration) map[string]int {
	out := make(map[string]int, len(srcs))
	var pending []chan struct{}
	c.mu.Lock()
	for i := range srcs {
		src := &srcs[i]
		if sources.CollectionFor(src) == "" {
			continue // read live: nothing in Qdrant to count
		}
		if e, ok := c.known[src.ID]; ok {
			out[src.ID] = e.n
			continue
		}
		if ch, ok := c.inflight[src.ID]; ok {
			pending = append(pending, ch)
			continue
		}
		if t, ok := c.failedAt[src.ID]; ok && time.Since(t) < countRetryDelay {
			continue
		}
		pending = append(pending, c.start(src))
	}
	c.mu.Unlock()

	if len(pending) == 0 || wait <= 0 {
		return out
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
waiting:
	for _, ch := range pending {
		select {
		case <-ch:
		case <-timer.C:
			break waiting
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range srcs {
		if e, ok := c.known[srcs[i].ID]; ok {
			out[srcs[i].ID] = e.n
		}
	}
	return out
}

// start counts src in the background; caller holds c.mu.
func (c *vectorCounts) start(src *models.SourceDefinition) chan struct{} {
	id, collection, gen := src.ID, sources.CollectionFor(src), c.gen
	ch := make(chan struct{})
	c.inflight[id] = ch
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), countTimeout)
		defer cancel()
		// Once per collection and process: without the index the count
		// reads every payload in the collection. It only speeds the count
		// up, so a failure is no error, and isn't logged: startup already
		// reports it, and the count's own failure is retried every few seconds.
		_ = c.vectors.EnsureSourceIndex(ctx, collection)
		filter := &rag.Filter{Must: []rag.FieldCondition{{
			Key: models.TagKey("source_id"), Match: rag.Match{Value: id},
		}}}
		n, err := c.vectors.CountExact(ctx, collection, filter)
		c.mu.Lock()
		delete(c.inflight, id)
		switch {
		case err != nil:
			c.failedAt[id] = time.Now()
			// Nothing else may change for a while; retry on our own.
			time.AfterFunc(countRetryDelay, c.onReady)
		case c.gen == gen:
			c.known[id] = sourceCount{n: n, collection: collection}
		}
		// A stale count (the source changed meanwhile) is dropped; onReady
		// makes the next snapshot start a fresh one.
		c.mu.Unlock()
		close(ch)
		c.onReady()
	}()
	return ch
}

// sourceStatus is one source's live state, as /status and /events send it.
type sourceStatus struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	SyncStatus     string               `json:"sync_status"`
	SyncError      *string              `json:"sync_error,omitempty"`
	SyncErrorPhase *string              `json:"sync_error_phase,omitempty"`
	LastSyncedAt   *time.Time           `json:"last_synced_at,omitempty"`
	Paused         bool                 `json:"paused"`
	Progress       *models.SyncProgress `json:"progress,omitempty"`
	VectorCount    *int                 `json:"vector_count"` // null: not counted yet, or live
	Live           bool                 `json:"live"`         // read live by its tool: never synced
}

// statusSnapshot is every source's live state. wait bounds how long it
// waits for vector counts that aren't cached; those not ready by then are
// sent as null and follow in a later event.
func (s *Server) statusSnapshot(wait time.Duration) ([]sourceStatus, error) {
	srcs, err := s.sources.ListAll()
	if err != nil {
		return nil, err
	}
	counts := s.counts.lookup(srcs, wait)
	out := make([]sourceStatus, 0, len(srcs))
	for i := range srcs {
		src := &srcs[i]
		st := sourceStatus{
			ID: src.ID, Name: src.Name,
			SyncStatus: src.SyncStatus, SyncError: src.SyncError, SyncErrorPhase: src.SyncErrorPhase,
			LastSyncedAt: src.LastSyncedAt, Live: models.IsLiveType(src.Type),
		}
		if s.sync != nil {
			st.Paused = s.sync.Control().IsPaused(src.ID)
			if p, ok := s.sync.Progress().Get(src.ID); ok {
				st.Progress = &p
			}
		}
		if n, ok := counts[src.ID]; ok {
			st.VectorCount = &n
		}
		out = append(out, st)
	}
	return out, nil
}

// statusWait is how long /status and a page render wait for vector counts.
const statusWait = 2 * time.Second

// streamWait is how long one event waits for vector counts: short, so a
// slow Qdrant delays only the count, never the progress around it.
const streamWait = 200 * time.Millisecond

// streamMinInterval spaces events of one stream: progress can change many
// times a second, and the page can't show more than this anyway.
const streamMinInterval = 250 * time.Millisecond

// streamHeartbeat keeps idle streams from being dropped by proxies, and
// notices a closed browser tab.
const streamHeartbeat = 20 * time.Second

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	out, err := s.statusSnapshot(statusWait)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, out)
}

// handleEvents streams the status snapshot: once on connect, then after
// every change. The browser's EventSource reconnects by itself.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	changed, unsubscribe := s.events.subscribe()
	defer unsubscribe()

	send := func() error {
		out, err := s.statusSnapshot(streamWait)
		if err != nil {
			log.Printf("warning: status snapshot: %v", err)
			return nil // keep the stream; the next change tries again
		}
		data, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}
	if send() != nil {
		return
	}
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.events.done:
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-changed:
			if send() != nil {
				return
			}
			// Changes during the pause stay pending in changed and go out
			// together in the next event.
			select {
			case <-ctx.Done():
				return
			case <-s.events.done:
				return
			case <-time.After(streamMinInterval):
			}
		}
	}
}

// CloseEvents ends every open /events stream, so a graceful shutdown
// doesn't wait for browser tabs to disconnect.
func (s *Server) CloseEvents() { s.events.close() }

// watchChanges makes every change that the Sources page shows reach the
// open streams, and drops the cached count of a source that changed.
func (s *Server) watchChanges() {
	s.events = newBroker()
	s.counts = newVectorCounts(s.vectors, s.events.notify)
	s.sources.OnChange(func(sourceID string) {
		s.counts.forget(sourceID)
		s.events.notify()
	})
	if s.sync != nil {
		notify := func(string) { s.events.notify() }
		s.sync.Progress().OnChange(notify)
		s.sync.Control().OnChange(notify)
	}
}
