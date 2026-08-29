// End-to-end sync pipeline test: a fake source API, a fake embedding
// endpoint, and a minimal in-memory fake Qdrant — exercising fetch → embed →
// upsert, status persistence, replace-on-resync, and failure handling.
package syncsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	sizes       map[string]int                       // collection → configured vector size
}

func newFakeQdrant() *fakeQdrant {
	return &fakeQdrant{
		collections: map[string]map[string]map[string]any{},
		sizes:       map[string]int{},
	}
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
		var body struct {
			Vectors struct {
				Size int `json:"size"`
			} `json:"vectors"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		name := r.PathValue("name")
		f.collections[name] = map[string]map[string]any{}
		f.sizes[name] = body.Vectors.Size
		_, _ = w.Write([]byte(`{"result": true}`))
	})
	mux.HandleFunc("GET /collections/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		name := r.PathValue("name")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"points_count": len(f.collections[name]),
				"config": map[string]any{
					"params": map[string]any{
						"vectors": map[string]any{"size": f.sizes[name]},
					},
				},
			},
		})
	})
	mux.HandleFunc("DELETE /collections/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		name := r.PathValue("name")
		delete(f.collections, name)
		delete(f.sizes, name)
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
	return setupPipelineWithEmbed(t, sourceURL, defaultEmbedHandler)
}

// defaultEmbedHandler is a well-behaved fake embedding endpoint, shared by
// tests that don't care about the embedding response itself.
func defaultEmbedHandler(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
	})
}

// setupPipelineWithEmbed is setupPipeline with a caller-supplied embedding
// endpoint, so failure-path tests can make it return errors.
func setupPipelineWithEmbed(t *testing.T, sourceURL string, embedHandler http.HandlerFunc) (*Service, *store.SourceConfigStore, *fakeQdrant, *models.SourceDefinition) {
	t.Helper()

	embedSrv := httptest.NewServer(http.HandlerFunc(embedHandler))
	t.Cleanup(embedSrv.Close)

	qd := newFakeQdrant()
	qdSrv := httptest.NewServer(qd.handler())
	t.Cleanup(qdSrv.Close)

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = qdSrv.URL
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
	embedding := rag.NewEmbeddingService(cfg)
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

func TestSyncUnknownSourceIsNoop(t *testing.T) {
	svc, _, _, _ := setupPipeline(t, "http://unused.invalid")
	// Must return quietly (log only) rather than panic on a missing source.
	svc.Sync(context.Background(), "does-not-exist")
}

func TestSyncDisabledSourceSkipsFetch(t *testing.T) {
	var fetchCount int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
	}))
	defer apiSrv.Close()

	svc, st, _, src := setupPipeline(t, apiSrv.URL)
	src.Disabled = true
	if err := st.Save(*src); err != nil {
		t.Fatal(err)
	}

	svc.Sync(context.Background(), src.ID)

	if got := atomic.LoadInt32(&fetchCount); got != 0 {
		t.Errorf("fetch called %d times, want 0 for a disabled source", got)
	}
	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "idle" {
		t.Errorf("status = %q, want idle (unchanged)", saved.SyncStatus)
	}
}

func TestSyncUnknownProviderFailsAtFetchPhase(t *testing.T) {
	svc, st, _, src := setupPipeline(t, "http://unused.invalid")
	src.Config["Provider"] = "sharepoint"
	if err := st.Save(*src); err != nil {
		t.Fatal(err)
	}

	svc.Sync(context.Background(), src.ID)

	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "failed" {
		t.Fatalf("status = %q, want failed", saved.SyncStatus)
	}
	if saved.SyncErrorPhase == nil || *saved.SyncErrorPhase != "fetch" {
		t.Errorf("error phase = %v, want fetch", saved.SyncErrorPhase)
	}
	if saved.SyncError == nil || !strings.Contains(*saved.SyncError, "unknown provider") {
		t.Errorf("sync error = %v", saved.SyncError)
	}
}

func TestSyncEmbedFailureRecordedAsEmbedPhase(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": 1, "title": "First item"}},
		})
	}))
	defer apiSrv.Close()

	failingEmbed := func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "embedding unavailable", http.StatusServiceUnavailable)
	}

	svc, st, qd, src := setupPipelineWithEmbed(t, apiSrv.URL, failingEmbed)
	svc.Sync(context.Background(), src.ID)

	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "failed" {
		t.Fatalf("status = %q, want failed", saved.SyncStatus)
	}
	if saved.SyncErrorPhase == nil || *saved.SyncErrorPhase != "embed" {
		t.Errorf("error phase = %v, want embed", saved.SyncErrorPhase)
	}
	if qd.count(models.CollectionWorkItems) != 0 {
		t.Error("failed embed should write nothing")
	}
}

func TestSyncCancelledBetweenFetchAndIndexLeavesIdle(t *testing.T) {
	const sourceID = "test-src"
	var svc *Service
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Cancel via the control store (not the context) once the fetch is
		// underway — Register has already run by this point, so the cancel
		// flag will be observed at the checkpoint just before indexing.
		svc.Control().RequestCancel(sourceID)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": 1, "title": "x"}},
		})
	}))
	defer apiSrv.Close()

	svc, st, qd, src := setupPipeline(t, apiSrv.URL)
	if src.ID != sourceID {
		t.Fatalf("setupPipeline source id changed to %q — update this test", src.ID)
	}

	svc.Sync(context.Background(), src.ID)

	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "idle" {
		t.Errorf("status = %q, want idle (cancelled)", saved.SyncStatus)
	}
	if saved.SyncError != nil {
		t.Errorf("sync error = %v, want nil after cancellation", saved.SyncError)
	}
	if got := qd.count(models.CollectionWorkItems); got != 0 {
		t.Errorf("cancelled sync should not write points, got %d", got)
	}
}

func TestSyncPreprocessingRunsBeforeIndexing(t *testing.T) {
	longDetail := strings.Repeat("word ", 60) // > minPreprocessLength so it's actually summarized
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{
				{"id": 1, "title": "First item", "detail": longDetail},
			},
		})
	}))
	defer apiSrv.Close()

	var preprocessCalls int32
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&preprocessCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "a concise summary"}}},
		})
	}))
	defer chatSrv.Close()

	svc, st, qd, src := setupPipeline(t, apiSrv.URL)
	svc.cfg.Preprocessing.Enabled = true
	svc.cfg.Preprocessing.BaseURL = chatSrv.URL

	svc.Sync(context.Background(), src.ID)

	saved, _ := st.Get(src.ID)
	if saved.SyncStatus != "completed" {
		t.Fatalf("status = %q (error: %v)", saved.SyncStatus, saved.SyncError)
	}
	if atomic.LoadInt32(&preprocessCalls) == 0 {
		t.Error("expected preprocessing to call the chat endpoint for the long document")
	}
	if got := qd.count(models.CollectionWorkItems); got != 1 {
		t.Errorf("qdrant points = %d, want 1", got)
	}
}

func TestSyncSelectedRunsAllSourcesAndSkipsUnknownIDs(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": 1, "title": "x"}},
		})
	}))
	defer apiSrv.Close()

	svc, st, qd, src1 := setupPipeline(t, apiSrv.URL)

	src2 := *src1
	src2.ID = "test-src-2"
	src2.SyncStatus = "idle"
	if err := st.Save(src2); err != nil {
		t.Fatal(err)
	}

	svc.SyncSelected(context.Background(), []string{src1.ID, src2.ID, "does-not-exist"})

	for _, id := range []string{src1.ID, src2.ID} {
		saved, _ := st.Get(id)
		if saved.SyncStatus != "completed" {
			t.Errorf("source %s status = %q, want completed", id, saved.SyncStatus)
		}
	}
	if got := qd.count(models.CollectionWorkItems); got != 2 {
		t.Errorf("qdrant points = %d, want 2", got)
	}
}

func TestProgressAndControlAccessors(t *testing.T) {
	svc, _, _, src := setupPipeline(t, "http://unused.invalid")

	if svc.Progress() == nil {
		t.Fatal("Progress() returned nil")
	}
	if svc.Control() == nil {
		t.Fatal("Control() returned nil")
	}

	svc.Progress().Set(src.ID, models.SyncProgress{Phase: "testing"})
	got, ok := svc.Progress().Get(src.ID)
	if !ok || got.Phase != "testing" {
		t.Errorf("progress = %+v, ok=%v, want Phase=testing", got, ok)
	}

	svc.Control().Register(src.ID)
	if err := svc.Control().Checkpoint(context.Background(), src.ID); err != nil {
		t.Errorf("checkpoint on a freshly registered source should pass: %v", err)
	}
}
