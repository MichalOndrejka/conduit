package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

func storeFor(t *testing.T, srv *httptest.Server) *VectorStore {
	t.Helper()
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	cfg.Qdrant.APIKey = "qd-key"
	cfg.Embedding.Dimensions = 3
	return NewVectorStore(cfg)
}

func TestSearchSendsFilterAndAPIKey(t *testing.T) {
	var gotPath, gotAPIKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"points": []map[string]any{
					{"id": "abc", "score": 0.91, "payload": map[string]any{
						"text":            "hit text",
						"tag_source_name": "my-source",
						"prop_url":        "https://example.com",
					}},
				},
			},
		})
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	points, err := v.Search(context.Background(), "conduit_workitems", []float32{1, 2, 3}, 5,
		map[string]string{"source_name": "my-source"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/collections/conduit_workitems/points/query" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAPIKey != "qd-key" {
		t.Errorf("api-key = %q", gotAPIKey)
	}
	filter, ok := gotBody["filter"].(map[string]any)
	if !ok {
		t.Fatal("filter missing from request body")
	}
	must := filter["must"].([]any)
	cond := must[0].(map[string]any)
	if cond["key"] != "tag_source_name" {
		t.Errorf("filter key = %v, want tag_source_name", cond["key"])
	}

	res := PointToSearchResult(points[0])
	if res.ID != "abc" || res.Text != "hit text" {
		t.Errorf("converted result = %+v", res)
	}
	if res.Tags["source_name"] != "my-source" || res.Properties["url"] != "https://example.com" {
		t.Errorf("tags/props not split: %+v", res)
	}
}

func TestScrollPagination(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		next := `"offset-2"`
		if call > 1 {
			next = "null"
		}
		_, _ = w.Write([]byte(`{"result": {"points": [{"id": "p` + strconv.Itoa(call) + `", "payload": {"text": "t"}}], "next_page_offset": ` + next + `}}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	_, next, err := v.Scroll(context.Background(), "c", nil, 20, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil {
		t.Fatal("expected next offset on first page")
	}
	_, next, err = v.Scroll(context.Background(), "c", nil, 20, next, false)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Errorf("expected nil next offset on last page, got %s", string(next))
	}
}

func TestCollectionLifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			_, _ = w.Write([]byte(`{"result": {"collections": [{"name": "existing"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"result": true}`))
		}
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	ctx := context.Background()
	if !v.CollectionExists(ctx, "existing") {
		t.Error("existing collection not found")
	}
	if v.CollectionExists(ctx, "missing") {
		t.Error("missing collection reported as existing")
	}
	if err := v.CreateCollection(ctx, "new", 0); err != nil {
		t.Error(err)
	}
	if err := v.HealthCheck(ctx); err != nil {
		t.Error(err)
	}
}

func TestTagFilterNilForEmpty(t *testing.T) {
	if TagFilter(nil) != nil || TagFilter(map[string]string{}) != nil {
		t.Error("empty tags should produce nil filter")
	}
	f := TagFilter(map[string]string{"source_id": "x"})
	if f.Must[0].Key != models.TagKey("source_id") {
		t.Errorf("key = %q", f.Must[0].Key)
	}
}

// TestVectorStoreReflectsLiveConfigChanges guards against regressing to
// baking the connection details in at construction time (the bug this test
// was added for: Settings-page Qdrant changes had no effect without a
// process restart, because VectorStore used to copy host/port/API key into
// plain struct fields instead of reading the shared *config.AppConfig live).
func TestVectorStoreReflectsLiveConfigChanges(t *testing.T) {
	var gotAPIKey string
	oldSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the old Qdrant host after config was updated")
	}))
	defer oldSrv.Close()
	newSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("api-key")
		w.Write([]byte(`{"result":{"collections":[]}}`))
	}))
	defer newSrv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = oldSrv.URL
	cfg.Qdrant.APIKey = "old-key"
	v := NewVectorStore(cfg)

	// Simulate a Settings-page save: mutate the same *config.AppConfig the
	// VectorStore holds, without reconstructing it.
	cfg.Qdrant.URL = newSrv.URL
	cfg.Qdrant.APIKey = "new-key"

	if _, err := v.ListCollections(context.Background()); err != nil {
		t.Fatalf("ListCollections after live config update: %v", err)
	}
	if gotAPIKey != "new-key" {
		t.Errorf("api-key header = %q, want %q (should reflect updated config)", gotAPIKey, "new-key")
	}
}

func TestNewVectorStoreBaseURLTrimsTrailingSlash(t *testing.T) {
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = "https://qdrant.internal:443/"
	v := NewVectorStore(cfg)
	if got := v.baseURL(); got != "https://qdrant.internal:443" {
		t.Errorf("baseURL() = %q, want trailing slash trimmed", got)
	}
}

func TestIDStringHandlesIntegerID(t *testing.T) {
	if got := IDString(json.RawMessage(`42`)); got != "42" {
		t.Errorf("IDString(42) = %q, want %q", got, "42")
	}
}

func TestDoReturnsErrorOnNon2xxStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	err := v.do(context.Background(), http.MethodGet, "/collections", nil, nil)
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to include response body", err)
	}
}

func TestHealthCheckErrorsWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use — connection refused

	v := storeFor(t, srv)
	if err := v.HealthCheck(context.Background()); err == nil {
		t.Error("expected error from unreachable Qdrant")
	}
}

func TestDeleteCollectionSendsDelete(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"result": true}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if err := v.DeleteCollection(context.Background(), "conduit_workitems"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/collections/conduit_workitems" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
}

func TestPointsCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": {"points_count": 7}}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if got := v.PointsCount(context.Background(), "c"); got != 7 {
		t.Errorf("PointsCount = %d, want 7", got)
	}
}

func TestPointsCountReturnsZeroOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if got := v.PointsCount(context.Background(), "missing"); got != 0 {
		t.Errorf("PointsCount = %d, want 0 on error", got)
	}
}

func TestVectorSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": {"config": {"params": {"vectors": {"size": 768}}}}}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	size, err := v.VectorSize(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if size != 768 {
		t.Errorf("VectorSize = %d, want 768", size)
	}
}

func TestVectorSizePropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if _, err := v.VectorSize(context.Background(), "missing"); err == nil {
		t.Error("expected error for missing collection")
	}
}

func TestDeleteByFilterSendsFilterBody(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result": true}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	filter := TagFilter(map[string]string{"source_id": "src-1"})
	if err := v.DeleteByFilter(context.Background(), "c", filter); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/collections/c/points/delete" {
		t.Errorf("path = %q", gotPath)
	}
	if _, ok := gotBody["filter"]; !ok {
		t.Error("filter missing from request body")
	}
}

func TestDeleteByIDsSendsPointsBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result": true}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if err := v.DeleteByIDs(context.Background(), "c", []string{"id-1", "id-2"}); err != nil {
		t.Fatal(err)
	}
	pts, ok := gotBody["points"].([]any)
	if !ok || len(pts) != 2 {
		t.Errorf("points body = %v, want 2 ids", gotBody["points"])
	}
}

func TestCountReturnsExactCount(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result": {"count": 3}}`))
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if got := v.Count(context.Background(), "c", nil); got != 3 {
		t.Errorf("Count = %d, want 3", got)
	}
	if exact, _ := gotBody["exact"].(bool); !exact {
		t.Error("expected exact=true in request body")
	}
}

func TestCountReturnsZeroOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	v := storeFor(t, srv)
	if got := v.Count(context.Background(), "c", TagFilter(map[string]string{"source_id": "x"})); got != 0 {
		t.Errorf("Count = %d, want 0 on error", got)
	}
}
