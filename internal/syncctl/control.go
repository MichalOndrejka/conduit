// Package syncctl ports app/store/sync_control.py and sync_progress.py: the
// in-memory pause/cancel control and progress reporting used by the sync
// engine. asyncio.Event-per-source becomes a channel that is closed to signal
// "not paused"; checkpoint() blocks on it and returns ErrSyncCancelled if a
// cancel was requested.
package syncctl

import (
	"context"
	"errors"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// ErrSyncCancelled mirrors the Python SyncCancelled exception.
var ErrSyncCancelled = errors.New("sync cancelled")

type controlState struct {
	cancelled bool
	paused    bool
	resumeCh  chan struct{} // closed when resumed; nil when not paused
}

type ControlStore struct {
	mu     sync.Mutex
	states map[string]*controlState
}

func NewControlStore() *ControlStore {
	return &ControlStore{states: map[string]*controlState{}}
}

func (c *ControlStore) get(sourceID string) *controlState {
	st, ok := c.states[sourceID]
	if !ok {
		st = &controlState{}
		c.states[sourceID] = st
	}
	return st
}

// Register resets control state for a new sync run.
func (c *ControlStore) Register(sourceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states[sourceID] = &controlState{}
}

// RequestCancel marks the sync cancelled and unblocks a paused checkpoint.
func (c *ControlStore) RequestCancel(sourceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.get(sourceID)
	st.cancelled = true
	if st.paused {
		st.paused = false
		close(st.resumeCh)
		st.resumeCh = nil
	}
}

func (c *ControlStore) Pause(sourceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.get(sourceID)
	if !st.paused {
		st.paused = true
		st.resumeCh = make(chan struct{})
	}
}

func (c *ControlStore) Resume(sourceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.get(sourceID)
	if st.paused {
		st.paused = false
		close(st.resumeCh)
		st.resumeCh = nil
	}
}

func (c *ControlStore) IsPaused(sourceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.states[sourceID]
	return ok && st.paused
}

// Checkpoint waits while paused. Returns ErrSyncCancelled if cancelled, or
// the context error if ctx ends first.
func (c *ControlStore) Checkpoint(ctx context.Context, sourceID string) error {
	c.mu.Lock()
	st := c.get(sourceID)
	resumeCh := st.resumeCh
	c.mu.Unlock()

	if resumeCh != nil {
		select {
		case <-resumeCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	c.mu.Lock()
	cancelled := c.get(sourceID).cancelled
	c.mu.Unlock()
	if cancelled {
		return ErrSyncCancelled
	}
	return nil
}

func (c *ControlStore) Clear(sourceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, sourceID)
}

// ── Progress store (port of app/store/sync_progress.py) ─────────────────────

type ProgressStore struct {
	mu       sync.RWMutex
	progress map[string]models.SyncProgress
}

func NewProgressStore() *ProgressStore {
	return &ProgressStore{progress: map[string]models.SyncProgress{}}
}

func (p *ProgressStore) Set(sourceID string, prog models.SyncProgress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.progress[sourceID] = prog
}

func (p *ProgressStore) Get(sourceID string) (models.SyncProgress, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	prog, ok := p.progress[sourceID]
	return prog, ok
}

func (p *ProgressStore) Clear(sourceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.progress, sourceID)
}
