package embedstate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/store"
)

// fakeQdrant tracks collection names and their vector size.
type fakeQdrant struct {
	mu    sync.Mutex
	sizes map[string]int
	down  bool
}

// size returns a collection's vector size and whether it exists, under the
// lock the handlers write with.
func (f *fakeQdrant) size(name string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.sizes[name]
	return n, ok
}

func (f *fakeQdrant) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /collections", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var cols []map[string]string
		for name := range f.sizes {
			cols = append(cols, map[string]string{"name": name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"collections": cols}})
	})
	mux.HandleFunc("PUT /collections/{name}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Vectors struct {
				Size int `json:"size"`
			} `json:"vectors"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.sizes[r.PathValue("name")] = body.Vectors.Size
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"result":true}`))
	})
	mux.HandleFunc("DELETE /collections/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		delete(f.sizes, r.PathValue("name"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"result":true}`))
	})
	return mux
}

func setup(t *testing.T) (dataDir string, qd *fakeQdrant, vectors *rag.VectorStore, st *store.SourceConfigStore) {
	t.Helper()
	qd = &fakeQdrant{sizes: map[string]int{models.CollectionWorkItems: 768, models.CollectionExperience: 768}}
	srv := httptest.NewServer(qd.handler())
	t.Cleanup(srv.Close)
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	dataDir = t.TempDir()
	st = store.NewSourceConfigStore(filepath.Join(dataDir, "sources.json"))
	if err := st.Save(models.SourceDefinition{ID: "s1", Name: "S", Type: models.SourceWorkItemQuery, SyncStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	return dataDir, qd, rag.NewVectorStore(cfg), st
}

func ec(model string, dims int) config.EmbeddingConfig {
	return config.EmbeddingConfig{Model: model, Dimensions: dims, BaseURL: "http://ollama:11434/v1"}
}

func status(t *testing.T, st *store.SourceConfigStore) string {
	t.Helper()
	src, err := st.Get("s1")
	if err != nil || src == nil {
		t.Fatalf("Get: %v", err)
	}
	return src.SyncStatus
}

func TestReconcileFirstRunRecordsWithoutDropping(t *testing.T) {
	dataDir, qd, vectors, st := setup(t)
	changed, _, err := Reconcile(context.Background(), dataDir, ec("a", 768), vectors, st)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want no change", changed, err)
	}
	if _, ok := qd.size(models.CollectionWorkItems); !ok {
		t.Error("first run must not drop collections")
	}
	if got, ok, _ := Load(dataDir); !ok || got != (State{Model: "a", Dimensions: 768}) {
		t.Errorf("recorded state = %+v (ok=%v)", got, ok)
	}
}

func TestReconcileUnchangedAndURLOnlyChangeKeepData(t *testing.T) {
	dataDir, qd, vectors, st := setup(t)
	if err := Save(dataDir, ec("a", 768)); err != nil {
		t.Fatal(err)
	}
	moved := ec("a", 768)
	moved.BaseURL = "http://elsewhere:11434/v1"
	changed, _, err := Reconcile(context.Background(), dataDir, moved, vectors, st)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want no change", changed, err)
	}
	if _, ok := qd.size(models.CollectionWorkItems); !ok || status(t, st) != "completed" {
		t.Error("a base-URL-only change must keep vectors and sync status")
	}
}

func TestReconcileModelChangeDropsAndFlags(t *testing.T) {
	dataDir, qd, vectors, st := setup(t)
	if err := Save(dataDir, ec("a", 768)); err != nil {
		t.Fatal(err)
	}
	changed, prev, err := Reconcile(context.Background(), dataDir, ec("b", 1024), vectors, st)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want change", changed, err)
	}
	if prev.Model != "a" {
		t.Errorf("prev = %+v", prev)
	}
	if _, ok := qd.size(models.CollectionWorkItems); ok {
		t.Error("work items collection should be dropped")
	}
	if n, _ := qd.size(models.CollectionExperience); n != 1024 {
		t.Errorf("experience recreated with size %d, want 1024", n)
	}
	if s := status(t, st); s != "needs-reindex" {
		t.Errorf("status = %q, want needs-reindex", s)
	}
	if got, _, _ := Load(dataDir); got.Model != "b" {
		t.Errorf("new state not recorded: %+v", got)
	}
}

func TestReconcileDropFailureKeepsOldStateForRetry(t *testing.T) {
	dataDir, qd, vectors, st := setup(t)
	if err := Save(dataDir, ec("a", 768)); err != nil {
		t.Fatal(err)
	}
	qd.mu.Lock()
	qd.down = true
	qd.mu.Unlock()
	changed, _, err := Reconcile(context.Background(), dataDir, ec("b", 768), vectors, st)
	if !changed || err == nil {
		t.Fatalf("changed=%v err=%v, want change with error", changed, err)
	}
	if got, _, _ := Load(dataDir); got.Model != "a" {
		t.Errorf("state = %+v, want old state kept so the next start retries", got)
	}
	if s := status(t, st); s != "needs-reindex" {
		t.Errorf("status = %q, want needs-reindex even when the drop failed", s)
	}
}
