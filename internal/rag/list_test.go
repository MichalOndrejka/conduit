package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

// listQdrant fakes the index and scroll endpoints: the first ordered scroll
// fails the way Qdrant does when the order key has no index.
type listQdrant struct {
	mu          sync.Mutex
	indexCalls  []string
	scrolls     int
	failFirstBy bool
}

func (q *listQdrant) handler(w http.ResponseWriter, r *http.Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/index"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		q.indexCalls = append(q.indexCalls, strings.Split(r.URL.Path, "/")[2]+"."+body["field_name"].(string)+":"+body["field_schema"].(string))
		_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
	case strings.HasSuffix(r.URL.Path, "/points/scroll"):
		q.scrolls++
		if q.failFirstBy && q.scrolls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":{"error":"Bad request: No range index for order_by key: s_finished. Please create one to use order_by."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": []any{
			map[string]any{"id": "p1", "payload": map[string]any{models.PayloadSourceDocID: "run-1", models.PayloadChunkIndex: "0"}},
		}}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
	}
}

func listStore(t *testing.T, q *listQdrant) *VectorStore {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(q.handler))
	t.Cleanup(srv.Close)
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	return NewVectorStore(cfg)
}

func TestEnsureListIndexesCreatesEachSortIndex(t *testing.T) {
	q := &listQdrant{}
	store := listStore(t, q)
	err := store.EnsureListIndexes(context.Background(), map[string][]OrderBy{
		"conduit_startup_test": {{Key: "s_finished"}, {Key: "f_date", Schema: "integer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(q.indexCalls, ",")
	if !strings.Contains(got, "conduit_startup_test.s_finished:float") || !strings.Contains(got, "conduit_startup_test.f_date:integer") {
		t.Errorf("index calls = %s", got)
	}
}

// A collection recreated after its index was made (e.g. a new embedding
// model) lost the index: the listing makes it again and retries once.
func TestListRecreatesALostIndex(t *testing.T) {
	q := &listQdrant{failFirstBy: true}
	store := listStore(t, q)
	search := NewSearchService(store, nil, nil)
	coll := "conduit_recreated_test"
	fieldIndexes.Store(coll+"\x00s_finished", true) // made before the collection was recreated

	results, _, err := search.List(context.Background(), coll, nil, OrderBy{Key: "s_finished", Desc: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || q.scrolls != 2 || len(q.indexCalls) != 1 {
		t.Errorf("results %d, scrolls %d, index calls %v", len(results), q.scrolls, q.indexCalls)
	}
}

func TestEnsureSourceIndexIsKeywordOnSourceIDOncePerCollection(t *testing.T) {
	q := &listQdrant{}
	store := listStore(t, q)
	coll := "conduit_source_index_test"
	ctx := context.Background()
	for range 2 {
		if err := store.EnsureSourceIndex(ctx, coll); err != nil {
			t.Fatal(err)
		}
	}
	want := coll + "." + models.TagKey("source_id") + ":keyword"
	if len(q.indexCalls) != 1 || q.indexCalls[0] != want {
		t.Errorf("index calls = %v, want one %s", q.indexCalls, want)
	}
}

// A deleted or recreated collection has lost its indexes: the next ensure
// creates them again instead of trusting what this process remembers.
func TestDeleteAndCreateCollectionForgetItsIndexes(t *testing.T) {
	q := &listQdrant{}
	store := listStore(t, q)
	ctx := context.Background()
	coll := "conduit_forget_index_test"
	if err := store.EnsureSourceIndex(ctx, coll); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCollection(ctx, coll); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSourceIndex(ctx, coll); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCollection(ctx, coll, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSourceIndex(ctx, coll); err != nil {
		t.Fatal(err)
	}
	if len(q.indexCalls) != 3 {
		t.Errorf("index calls = %v, want 3 (initial, after delete, after create)", q.indexCalls)
	}
}
