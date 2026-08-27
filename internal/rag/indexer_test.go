package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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

// TestIndexWrapsIndexBatch asserts the single-document Index convenience
// method delegates to IndexBatch and writes exactly one point.
func TestIndexWrapsIndexBatch(t *testing.T) {
	embedHandler := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}
	indexer, qd := testIndexer(t, embedHandler, 2)

	doc := models.SourceDocument{ID: "solo-doc", Text: "hello world"}
	if err := indexer.Index(context.Background(), "conduit_test", doc); err != nil {
		t.Fatal(err)
	}

	qd.mu.Lock()
	defer qd.mu.Unlock()
	if len(qd.upsertedIDs) != 1 {
		t.Errorf("upserted %d points, want 1", len(qd.upsertedIDs))
	}
}

// recreatingQdrant is a fake Qdrant server that reports an existing
// collection sized for a different embedding model, so IndexBatch must
// delete and recreate it before writing.
type recreatingQdrant struct {
	mu               sync.Mutex
	deletedColl      string
	createdSizes     []int
	upsertedIDs      []string
	existingSize     int
	collectionExists bool
}

func (q *recreatingQdrant) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			if q.collectionExists {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]any{"collections": []map[string]any{{"name": "conduit_test"}}},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]any{"collections": []map[string]any{}},
				})
			}
		case r.Method == http.MethodGet:
			// GET /collections/<name> — vector size lookup
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"config": map[string]any{"params": map[string]any{
					"vectors": map[string]any{"size": q.existingSize},
				}}},
			})
		case r.Method == http.MethodDelete:
			q.deletedColl = strings.TrimPrefix(r.URL.Path, "/collections/")
			q.collectionExists = false
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case r.Method == http.MethodPut && r.URL.Query().Get("wait") == "":
			var body struct {
				Vectors struct {
					Size int `json:"size"`
				} `json:"vectors"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.createdSizes = append(q.createdSizes, body.Vectors.Size)
			q.collectionExists = true
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case r.Method == http.MethodPut:
			var body struct {
				Points []Point `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, p := range body.Points {
				q.upsertedIDs = append(q.upsertedIDs, p.ID)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}
}

// TestIndexBatchRecreatesCollectionOnDimensionMismatch asserts a collection
// sized for a different embedding model is deleted and recreated rather than
// written to with mismatched vectors.
func TestIndexBatchRecreatesCollectionOnDimensionMismatch(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}}, // size 3
		})
	}))
	defer embedSrv.Close()

	qd := &recreatingQdrant{collectionExists: true, existingSize: 1536}
	qdSrv := httptest.NewServer(qd.handler())
	defer qdSrv.Close()
	qdURL, _ := url.Parse(qdSrv.URL)
	qdPort, _ := strconv.Atoi(qdURL.Port())

	cfg := &config.AppConfig{}
	cfg.Qdrant.Host = qdURL.Hostname()
	cfg.Qdrant.Port = qdPort
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Chunking.MaxChunkSize = 2000
	cfg.Chunking.Overlap = 200

	vectors := NewVectorStore(cfg)
	embedding := NewEmbeddingService(cfg, nil)
	indexer := NewDocumentIndexer(vectors, embedding, NewTextChunker(cfg))

	docs := []models.SourceDocument{{ID: "doc-1", Text: "hello world"}}
	if err := indexer.IndexBatch(context.Background(), "conduit_test", docs, IndexBatchOptions{}); err != nil {
		t.Fatal(err)
	}

	qd.mu.Lock()
	defer qd.mu.Unlock()
	if qd.deletedColl != "conduit_test" {
		t.Errorf("deletedColl = %q, want conduit_test", qd.deletedColl)
	}
	if len(qd.createdSizes) != 1 || qd.createdSizes[0] != 3 {
		t.Errorf("createdSizes = %v, want [3]", qd.createdSizes)
	}
	if len(qd.upsertedIDs) != 1 {
		t.Errorf("upsertedIDs = %v, want 1 point", qd.upsertedIDs)
	}
}

// TestIndexBatchReplaceSourceIDDeletesOldVectorsBeforeUpsert asserts
// ReplaceSourceID triggers a filtered delete after embeds succeed but before
// the new points are written.
func TestIndexBatchReplaceSourceIDDeletesOldVectorsBeforeUpsert(t *testing.T) {
	embedHandler := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}

	var mu sync.Mutex
	var deleteFilterBody map[string]any
	var deleteBeforeUpsert bool
	var sawDelete, sawUpsert bool

	qd := &minimalQdrant{}
	baseHandler := qd.handler()
	wrapped := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/delete") {
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&deleteFilterBody)
			sawDelete = true
			if !sawUpsert {
				deleteBeforeUpsert = true
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
			return
		}
		if r.Method == http.MethodPut && r.URL.Query().Get("wait") == "true" {
			mu.Lock()
			sawUpsert = true
			mu.Unlock()
		}
		baseHandler(w, r)
	}

	embedSrv := httptest.NewServer(http.HandlerFunc(embedHandler))
	defer embedSrv.Close()
	qdSrv := httptest.NewServer(http.HandlerFunc(wrapped))
	defer qdSrv.Close()
	qdURL, _ := url.Parse(qdSrv.URL)
	qdPort, _ := strconv.Atoi(qdURL.Port())

	cfg := &config.AppConfig{}
	cfg.Qdrant.Host = qdURL.Hostname()
	cfg.Qdrant.Port = qdPort
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Chunking.MaxChunkSize = 2000
	cfg.Chunking.Overlap = 200

	vectors := NewVectorStore(cfg)
	embedding := NewEmbeddingService(cfg, nil)
	indexer := NewDocumentIndexer(vectors, embedding, NewTextChunker(cfg))

	docs := []models.SourceDocument{{ID: "doc-1", Text: "hello world"}}
	opts := IndexBatchOptions{ReplaceSourceID: "src-42"}
	if err := indexer.IndexBatch(context.Background(), "conduit_test", docs, opts); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawDelete {
		t.Fatal("expected a delete-by-filter call for ReplaceSourceID")
	}
	if !deleteBeforeUpsert {
		t.Error("delete should happen before upsert")
	}
	filter, ok := deleteFilterBody["filter"].(map[string]any)
	if !ok {
		t.Fatal("filter missing from delete request body")
	}
	must := filter["must"].([]any)
	cond := must[0].(map[string]any)
	match := cond["match"].(map[string]any)
	if match["value"] != "src-42" {
		t.Errorf("delete filter value = %v, want src-42", match["value"])
	}
}

// TestIndexBatchRollsBackOnUpsertFailure asserts a failing later batch
// triggers a delete of the points written by earlier successful batches.
func TestIndexBatchRollsBackOnUpsertFailure(t *testing.T) {
	embedHandler := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}

	var mu sync.Mutex
	upsertCalls := 0
	var rollbackIDs []string

	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"collections": []map[string]any{}},
			})
		case r.Method == http.MethodPut && r.URL.Query().Get("wait") == "":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case r.Method == http.MethodPut:
			mu.Lock()
			upsertCalls++
			n := upsertCalls
			mu.Unlock()
			if n == 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("upsert failed"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/delete"):
			var body struct {
				Points []string `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			rollbackIDs = body.Points
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}

	embedSrv := httptest.NewServer(http.HandlerFunc(embedHandler))
	defer embedSrv.Close()
	qdSrv := httptest.NewServer(http.HandlerFunc(handler))
	defer qdSrv.Close()
	qdURL, _ := url.Parse(qdSrv.URL)
	qdPort, _ := strconv.Atoi(qdURL.Port())

	cfg := &config.AppConfig{}
	cfg.Qdrant.Host = qdURL.Hostname()
	cfg.Qdrant.Port = qdPort
	cfg.Embedding.Provider = "openai-compatible"
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Chunking.MaxChunkSize = 2000
	cfg.Chunking.Overlap = 200

	vectors := NewVectorStore(cfg)
	embedding := NewEmbeddingService(cfg, nil)
	indexer := NewDocumentIndexer(vectors, embedding, NewTextChunker(cfg))

	// 150 docs of one chunk each → two upsert batches of 100 given batchSize=100.
	docs := make([]models.SourceDocument, 150)
	for i := range docs {
		docs[i] = models.SourceDocument{ID: fmt.Sprintf("doc-%d", i), Text: "hello world"}
	}

	err := indexer.IndexBatch(context.Background(), "conduit_test", docs, IndexBatchOptions{})
	if err == nil {
		t.Fatal("expected error from failing second upsert batch")
	}

	mu.Lock()
	defer mu.Unlock()
	if upsertCalls != 2 {
		t.Errorf("upsertCalls = %d, want 2", upsertCalls)
	}
	if len(rollbackIDs) != 100 {
		t.Errorf("rollback deleted %d ids, want 100 (the first successful batch)", len(rollbackIDs))
	}
}

// TestIndexBatchCheckpointAbortsBeforeDispatch asserts a failing Checkpoint
// hook stops the run without embedding or writing anything.
func TestIndexBatchCheckpointAbortsBeforeDispatch(t *testing.T) {
	var embedCalls int64
	embedHandler := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&embedCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}
	indexer, qd := testIndexer(t, embedHandler, 2)

	docs := []models.SourceDocument{{ID: "doc-1", Text: "hello world"}}
	wantErr := fmt.Errorf("sync cancelled")
	opts := IndexBatchOptions{Checkpoint: func() error { return wantErr }}

	err := indexer.IndexBatch(context.Background(), "conduit_test", docs, opts)
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if atomic.LoadInt64(&embedCalls) != 0 {
		t.Errorf("embedCalls = %d, want 0 — checkpoint should abort before dispatch", embedCalls)
	}
	qd.mu.Lock()
	defer qd.mu.Unlock()
	if qd.upsertCalls != 0 {
		t.Errorf("upsertCalls = %d, want 0", qd.upsertCalls)
	}
}
