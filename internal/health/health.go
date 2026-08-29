// Package health is the Go port of app/rag/bootstrap.py's probe state
// machine: background goroutines retry Qdrant and the embedding model with
// exponential backoff and expose pending → ready/error states to /health.
//
// Unlike the Python bootstrap (one-shot at startup), probes here run for the
// lifetime of the process: a service that dies after startup flips to
// "error", and one that recovers flips back to "ready" — so /health never
// reports stale state.
package health

import (
	"context"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

const (
	maxAttempts = 10 // startup grace: stay "pending" for this many failures

	// Re-check cadence once ready. Embedding re-checks are real embed calls,
	// so they run far less often than the local Qdrant ping.
	qdrantRecheck    = 60 * time.Second
	embeddingRecheck = 15 * time.Minute
)

type ProbeState struct {
	Status  string `json:"status"` // pending | ready | error
	Message string `json:"message,omitempty"`
	// Static info for the UI
	Model string `json:"model,omitempty"`
}

type Monitor struct {
	cfg       *config.AppConfig
	mu        sync.RWMutex
	qdrant    ProbeState
	embedding ProbeState
}

// Start launches the probes in the background and returns immediately.
func Start(cfg *config.AppConfig, vectors *rag.VectorStore, embedding *rag.EmbeddingService) *Monitor {
	m := &Monitor{
		cfg:       cfg,
		qdrant:    ProbeState{Status: "pending"},
		embedding: ProbeState{Status: "pending"},
	}

	go m.probe(func(ctx context.Context) error {
		return vectors.HealthCheck(ctx)
	}, m.setQdrant, qdrantRecheck)

	go m.probe(func(ctx context.Context) error {
		_, err := embedding.Embed(ctx, "health check")
		return err
	}, m.setEmbedding, embeddingRecheck)

	return m
}

// probe runs forever. While failing it retries with exponential backoff
// (1s, 2s, 4s … capped at 30s); the state stays "pending" through the first
// maxAttempts startup failures, then reflects errors immediately. Once
// healthy, it re-checks every recheck interval and flips to "error" on the
// first failure after having been ready.
func (m *Monitor) probe(check func(context.Context) error, set func(ProbeState), recheck time.Duration) {
	backoff := time.Second
	fails := 0
	everReady := false
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := check(ctx)
		cancel()
		if err == nil {
			set(ProbeState{Status: "ready"})
			everReady = true
			fails = 0
			backoff = time.Second
			time.Sleep(recheck)
			continue
		}
		fails++
		if everReady || fails >= maxAttempts {
			set(ProbeState{Status: "error", Message: err.Error()})
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (m *Monitor) setQdrant(s ProbeState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.qdrant = s
}

func (m *Monitor) setEmbedding(s ProbeState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embedding = s
}

func (m *Monitor) Qdrant() ProbeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.qdrant
}

func (m *Monitor) Embedding() ProbeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.embedding
	s.Model = m.cfg.Embedding.Model
	return s
}

// IsReady reports whether Qdrant is usable (gates destructive operations,
// like container.health.is_ready in the Python app).
func (m *Monitor) IsReady() bool {
	return m.Qdrant().Status == "ready"
}
