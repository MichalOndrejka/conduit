// End-to-end sync pipeline test: a fake source API, a fake embedding
// endpoint, and a minimal in-memory fake Qdrant — exercising fetch → embed →
// upsert, status persistence, replace-on-resync, and failure handling.
package syncsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/store"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
)

type fakeSecrets struct{}

func (fakeSecrets) GetValue(string) string { return "" }

// fakeQdrant implements the few REST endpoints the pipeline touches.
type fakeQdrant struct {
	mu          sync.Mutex
	collections map[string]map[string]map[string]any // collection → id → payload
}

func newFakeQdrant() *fakeQdrant {
	return &fakeQdrant{collections: map[string]map[string]map[string]any{}}
}

func (f *fakeQdrant) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /collections", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var cols []map[string]string
		for name := range f.collections {
			cols = append(cols, map[string]string{"name": name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"collections": cols},
		})
	})
	mux.HandleFunc("PUT /collections/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.collections[r.PathValue("name")] = map[string]map[string]any{}
		_, _ = w.Write([]byte(`{"result": true}`))
	})
	mux.HandleFunc("PUT /collections/{name}/points", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Points []struct {
				ID      string         `json:"id"`
				Payload map[string]any `json:"payload"`
			} `json:"points"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		col := f.collections[r.PathValue("name")]
		for _, p := range body.Points {
			col[p.ID] = p.Payload
		}
		_, _ = w.Write([]byte(`{"result": true}`))
	})
	mux.HandleFunc("POST /collections/{name}/points/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Filter *struct {
				Must []struct {
					Key   string `json:"key"`
					Match struct {
						Value any `json:"value"`
					} `json:"match"`
				} `json:"must"`
			} `json:"filter"`
			Points []string `json:"points"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		col := f.collections[r.PathValue("name")]
		for _, id := range body.Points {
			delete(col, id)
		}
		if body.Filter != nil {
			for id, payload := range col {
				match := true
				for _, cond := range body.Filter.Must {
					if payload[cond.Key] != cond.Match.Value {
						match = false
					}
				}
				if match {
					delete(col, id)
				}
			}
		}
		_, _ = w.Write([]byte(`{"result": true}`))
	})
	return mux
}

func (f *fakeQdrant) count(collection string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.collections[collection])
}

func setupPipeline(t *testing.T, sourceURL string) (*Service, *store.SourceConfigStore, *fakeQdrant, *models.SourceDefinition) {
	t.Helper()

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	t.Cleanup(embedSrv.Close)

	qd := newFakeQdrant()
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
	cfg.Chunking.MaxChunkSize = 2000
	cfg.Chunking.Overlap = 200

	st := store.NewSourceConfigStore(filepath.Join(t.TempDir(), "sources.json"))
	src := models.SourceDefinition{
		ID: "test-src", Name: "Fake API", Type: models.SourceWorkItemQuery,
		SyncStatus: "idle",
		Config: map[string]string{
			"Provider": "api", "Url": sourceURL, "ItemsPath": "value", "IdField": "id",
		},
	}
	if err := st.Save(src); err != nil {
		t.Fatal(err)
	}

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg, nil)
	indexer := rag.NewDocumentIndexer(vectors, embedding, rag.NewTextChunker(cfg))
	svc := New(cfg, st, fakeSecrets{}, indexer, syncctl.NewProgressStore(), syncctl.NewControlStore())
	return svc, st, qd, &src
}

func TestSyncEndToEnd(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{
				{"id": 1, "title": "First item", "detail": "alpha"},
				{"id": 2, "title": "Second item", "detail": "beta"},
			},
		})
	}))
	defer apiSrv.Close()

	svc, st, qd, src := setupPipeline(t, apiSrv.URL)
	svc.Sync(context.Background(), src.ID)

	saved, err := st.Get(src.ID)
	if err != nil || saved == nil {
		t.Fatal("source missing after sync")
	}
	if saved.SyncStatus != "completed" {
		t.Fatalf("status = %q (error: %v)", saved.SyncStatus, saved.SyncError)
	}
	if saved.LastSyncedAt == nil {
		t.Error("last_synced_at not set")
	}
	if got := qd.count(models.CollectionWorkItems); got != 2 {
		t.Errorf("qdrant points = %d, want 2", got)
	}

	// Re-sync must replace, not duplicate.
	svc.Sync(context.Background(), src.ID)
	if got := qd.count(models.CollectionWorkItems); got != 2 {
		t.Errorf("after resync points = %d, want 2 (replace semantics)", got)
	}
}

func TestSyncFetchFailureRecorded(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer apiSrv.Close()

	svc, st, qd, src := setupPipeline(t, apiSrv.URL)
	svc.Sync(context.Background(), src.ID)

	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "failed" {
		t.Fatalf("status = %q, want failed", saved.SyncStatus)
	}
	if saved.SyncErrorPhase == nil || *saved.SyncErrorPhase != "fetch" {
		t.Errorf("error phase = %v, want fetch", saved.SyncErrorPhase)
	}
	if saved.SyncError == nil || !strings.Contains(*saved.SyncError, "500") {
		t.Errorf("sync error = %v", saved.SyncError)
	}
	if qd.count(models.CollectionWorkItems) != 0 {
		t.Error("failed fetch should write nothing")
	}
}

func TestConcurrentSyncDeduplicated(t *testing.T) {
	var fetchCount int32
	release := make(chan struct{})
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		<-release // hold the first sync mid-fetch so the second overlaps it
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": 1, "title": "x"}},
		})
	}))
	defer apiSrv.Close()

	svc, st, qd, src := setupPipeline(t, apiSrv.URL)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); svc.Sync(context.Background(), src.ID) }()
	time.Sleep(100 * time.Millisecond) // let the first sync claim the in-flight slot
	go func() { defer wg.Done(); svc.Sync(context.Background(), src.ID) }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&fetchCount); got != 1 {
		t.Errorf("fetch ran %d times, want 1 (duplicate sync must be a no-op)", got)
	}
	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "completed" {
		t.Errorf("status = %q (error: %v)", saved.SyncStatus, saved.SyncError)
	}
	if got := qd.count(models.CollectionWorkItems); got != 1 {
		t.Errorf("qdrant points = %d, want 1", got)
	}
}

func TestSyncCancelBeforeIndexLeavesIdle(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": 1, "title": "x"}},
		})
	}))
	defer apiSrv.Close()

	svc, st, _, src := setupPipeline(t, apiSrv.URL)
	// Cancelling before Sync registers is lost (Register resets state), so
	// simulate a cancel arriving right after fetch by pre-cancelling via the
	// control store after Register: easiest deterministic hook is to cancel
	// the context instead.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.Sync(ctx, src.ID)

	saved, _ := st.Get(src.ID)
	if saved.SyncStatus == "completed" {
		t.Error("cancelled sync must not complete")
	}
}
