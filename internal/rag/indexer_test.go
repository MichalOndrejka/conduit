package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

// minimalQdrant is a bare-bones fake Qdrant server: it always reports the
// collection as missing (so IndexBatch creates it), then accepts upserts and
// deletes without validation. Enough for exercising IndexBatch's embed path.
type minimalQdrant struct {
	mu          sync.Mutex
	upsertCalls int
	upsertedIDs []string
}

func (q *minimalQdrant) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"collections": []map[string]any{}},
			})
		case r.Method == http.MethodPut && r.URL.Path != "" && r.URL.Query().Get("wait") == "":
			// PUT /collections/<name> — create collection
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case r.Method == http.MethodPut:
			// PUT /collections/<name>/points?wait=true — upsert
			var body struct {
				Points []Point `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.mu.Lock()
			q.upsertCalls++
			for _, p := range body.Points {
				q.upsertedIDs = append(q.upsertedIDs, p.ID)
			}
			q.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		default:
			// delete-by-filter/ids etc.
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}
}

func testIndexer(t *testing.T, embedHandler http.HandlerFunc, concurrency int) (*DocumentIndexer, *minimalQdrant) {
	t.Helper()

	embedSrv := httptest.NewServer(embedHandler)
	t.Cleanup(embedSrv.Close)

	qd := &minimalQdrant{}
	qdSrv := httptest.NewServer(qd.handler())
	t.Cleanup(qdSrv.Close)
	qdURL, _ := url.Parse(qdSrv.URL)
	qdPort, _ := strconv.Atoi(qdURL.Port())

	cfg := &config.AppConfig{}
	cfg.Qdrant.Host = qdURL.Hostname()
	cfg.Qdrant.Port = qdPort
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Embedding.Concurrency = concurrency
	cfg.Chunking.MaxChunkSize = 2000
	cfg.Chunking.Overlap = 200

	vectors := NewVectorStore(cfg)
	embedding := NewEmbeddingService(cfg, nil)
	indexer := NewDocumentIndexer(vectors, embedding, NewTextChunker(cfg))
	return indexer, qd
}

// TestIndexBatchEmbedsConcurrently asserts chunks are embedded in parallel,
// not one at a time — the whole point of the worker pool in IndexBatch.
func TestIndexBatchEmbedsConcurrently(t *testing.T) {
	var inFlight, peak int64

	embedHandler := func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&inFlight, 1)
		defer atomic.AddInt64(&inFlight, -1)
		for {
			p := atomic.LoadInt64(&peak)
			if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}

	const concurrency = 4
	indexer, qd := testIndexer(t, embedHandler, concurrency)

	docs := make([]models.SourceDocument, 8)
	for i := range docs {
		docs[i] = models.SourceDocument{ID: fmt.Sprintf("doc-%d", i), Text: "hello world"}
	}

	start := time.Now()
	if err := indexer.IndexBatch(context.Background(), "conduit_test", docs, IndexBatchOptions{}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if got := atomic.LoadInt64(&peak); got < 2 {
		t.Errorf("peak concurrent embed calls = %d, want > 1 (sequential embedding not fixed)", got)
	}
	if got := atomic.LoadInt64(&peak); got > concurrency {
		t.Errorf("peak concurrent embed calls = %d, want <= configured concurrency %d", got, concurrency)
	}
	// 8 docs * 20ms sequentially would take >= 160ms; concurrent should be well under that.
	if elapsed >= 160*time.Millisecond {
		t.Errorf("IndexBatch took %v, expected concurrency to make this much faster than sequential", elapsed)
	}

	qd.mu.Lock()
	defer qd.mu.Unlock()
	if len(qd.upsertedIDs) != len(docs) {
		t.Errorf("upserted %d points, want %d", len(qd.upsertedIDs), len(docs))
	}
}

// TestIndexBatchAbortsOnEmbedError asserts a single failing embed call still
// aborts the whole batch before any Qdrant write, matching the pre-concurrency
// behavior.
func TestIndexBatchAbortsOnEmbedError(t *testing.T) {
	var calls int64
	embedHandler := func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		if n == 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}

	indexer, qd := testIndexer(t, embedHandler, 4)

	docs := make([]models.SourceDocument, 10)
	for i := range docs {
		docs[i] = models.SourceDocument{ID: fmt.Sprintf("doc-%d", i), Text: "hello world"}
	}

	err := indexer.IndexBatch(context.Background(), "conduit_test", docs, IndexBatchOptions{})
	if err == nil {
		t.Fatal("expected error from failing embed call, got nil")
	}

	qd.mu.Lock()
	defer qd.mu.Unlock()
	if qd.upsertCalls != 0 {
		t.Errorf("upsertCalls = %d, want 0 — no writes should happen after an embed failure", qd.upsertCalls)
	}
}
