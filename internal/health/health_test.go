package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// waitFor polls cond until it returns true or the timeout elapses, failing
// the test on timeout. Needed because probe() is an infinite loop driven by
// real timers — state changes are observed asynchronously, not returned.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for: %s", timeout, what)
}

// ── Direct state accessors ───────────────────────────────────────────────────

func TestInitialStateIsPending(t *testing.T) {
	m := &Monitor{cfg: &config.AppConfig{}, qdrant: ProbeState{Status: "pending"}, embedding: ProbeState{Status: "pending"}}
	if got := m.Qdrant().Status; got != "pending" {
		t.Errorf("Qdrant().Status = %q, want pending", got)
	}
	if m.IsReady() {
		t.Error("IsReady() = true before any successful probe")
	}
}

func TestEmbeddingReflectsLiveConfig(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Embedding.Model = "model-a"
	m := &Monitor{cfg: cfg}
	m.setEmbedding(ProbeState{Status: "ready"})

	got := m.Embedding()
	if got.Model != "model-a" {
		t.Errorf("Embedding() = %+v, want Model from cfg", got)
	}

	// Embedding() reads cfg live on every call, not a snapshot cached at
	// setEmbedding time — a Settings-page model change should show up
	// immediately on the next /health poll.
	cfg.Embedding.Model = "model-b"
	if got := m.Embedding().Model; got != "model-b" {
		t.Errorf("Embedding().Model = %q after cfg change, want live value %q", got, "model-b")
	}
}

func TestIsReadyDependsOnlyOnQdrant(t *testing.T) {
	m := &Monitor{cfg: &config.AppConfig{}}
	m.setQdrant(ProbeState{Status: "ready"})
	m.setEmbedding(ProbeState{Status: "error", Message: "embedding is down"})
	if !m.IsReady() {
		t.Error("IsReady() = false, want true — it should track Qdrant only, not embedding")
	}
}

// ── probe() state machine ────────────────────────────────────────────────────
//
// probe() runs forever, so these tests launch it in a goroutine (leaked for
// the remaining lifetime of the test binary, like any long-running server
// loop — harmless since the process exits once the package's tests finish)
// and poll the resulting state instead of waiting for probe() to return.
//
// probe()'s retry backoff (1s, doubling, capped at 30s) and its
// maxAttempts=10 startup grace period are hardcoded, not parameters, so the
// exact "flips to error only after the 10th startup failure" boundary would
// need on the order of two minutes of real sleeping to observe end-to-end and
// isn't covered here. What's covered below are the fast, deterministic edges
// of the same logic: an immediate success, an early failure that must NOT
// flip status yet, and the everReady-short-circuit that flips to error on the
// very first failure after having been ready (no waiting for maxAttempts).

func TestProbeSetsReadyOnFirstSuccess(t *testing.T) {
	m := &Monitor{cfg: &config.AppConfig{}, qdrant: ProbeState{Status: "pending"}}
	var calls int32
	check := func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	go m.probe(check, m.setQdrant, time.Hour) // long recheck: only need the first iteration

	waitFor(t, func() bool { return m.Qdrant().Status == "ready" }, time.Second, "status to become ready")
	if atomic.LoadInt32(&calls) == 0 {
		t.Error("check() was never called")
	}
}

func TestProbeStaysPendingThroughAnEarlyFailure(t *testing.T) {
	m := &Monitor{cfg: &config.AppConfig{}, qdrant: ProbeState{Status: "pending"}}
	check := func(ctx context.Context) error { return errors.New("not reachable yet") }
	go m.probe(check, m.setQdrant, time.Hour)

	// The first failure's backoff sleep is a full second, so sampling well
	// before that deterministically catches state as of exactly one failed
	// attempt (fails=1 < maxAttempts=10 → probe must not call set() yet).
	time.Sleep(300 * time.Millisecond)
	if got := m.Qdrant().Status; got != "pending" {
		t.Errorf("Qdrant().Status = %q after one early failure, want still pending (fails < maxAttempts)", got)
	}
}

func TestProbeFlipsToErrorImmediatelyAfterHavingBeenReady(t *testing.T) {
	var failing int32
	check := func(ctx context.Context) error {
		if atomic.LoadInt32(&failing) == 1 {
			return errors.New("backend went away")
		}
		return nil
	}
	m := &Monitor{cfg: &config.AppConfig{}, qdrant: ProbeState{Status: "pending"}}
	go m.probe(check, m.setQdrant, 20*time.Millisecond) // short recheck: reach "ready" quickly

	waitFor(t, func() bool { return m.Qdrant().Status == "ready" }, time.Second, "status to become ready")

	atomic.StoreInt32(&failing, 1)
	// Once everReady is true, probe() flips to "error" on the very next
	// failed check — no maxAttempts wait needed, unlike the startup case.
	waitFor(t, func() bool { return m.Qdrant().Status == "error" }, time.Second, "status to flip to error after having been ready")
	if msg := m.Qdrant().Message; msg == "" {
		t.Error("expected an error Message once status flips to error")
	}

	// And recovers on the next successful check (after the ~1s post-failure
	// backoff probe() sleeps before retrying).
	atomic.StoreInt32(&failing, 0)
	waitFor(t, func() bool { return m.Qdrant().Status == "ready" }, 3*time.Second, "status to recover to ready")
}

// ── Start() integration ──────────────────────────────────────────────────────

func fixedEmbedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}
}

func emptyQdrantHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"collections": []any{}}})
	}
}

// TestStartReachesReadyAgainstWorkingBackends is the integration-level
// counterpart to the direct probe() tests above: it exercises Start()'s
// actual wiring (vectors.HealthCheck and embedding.Embed) against real HTTP
// fakes, confirming both goroutines are launched and reach "ready".
func TestStartReachesReadyAgainstWorkingBackends(t *testing.T) {
	embedSrv := httptest.NewServer(fixedEmbedHandler())
	defer embedSrv.Close()

	qdSrv := httptest.NewServer(emptyQdrantHandler())
	defer qdSrv.Close()

	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Qdrant.URL = qdSrv.URL

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg)

	m := Start(cfg, vectors, embedding)
	waitFor(t, m.IsReady, 2*time.Second, "Qdrant probe to become ready")
	waitFor(t, func() bool { return m.Embedding().Status == "ready" }, 2*time.Second, "embedding probe to become ready")
}

// TestStartStaysPendingAgainstUnreachableQdrant confirms Start() doesn't
// falsely report readiness when Qdrant is unreachable from the very first
// attempt (a startup-ordering bug — e.g. calling the wrong probe's setter —
// would show up as IsReady() flipping true here).
func TestStartStaysPendingAgainstUnreachableQdrant(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = "http://127.0.0.1:1" // reserved port, connection refused
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Qdrant.URL = "http://127.0.0.1:1"

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg)

	m := Start(cfg, vectors, embedding)
	time.Sleep(300 * time.Millisecond) // comfortably under the 1s startup backoff
	if m.IsReady() {
		t.Error("IsReady() = true against an unreachable Qdrant")
	}
	if got := m.Qdrant().Status; got != "pending" {
		t.Errorf("Qdrant().Status = %q, want still pending this early", got)
	}
}
