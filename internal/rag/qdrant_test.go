package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

func storeFor(t *testing.T, srv *httptest.Server) *VectorStore {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	cfg := &config.AppConfig{}
	cfg.Qdrant.Host = u.Hostname()
	cfg.Qdrant.Port = port
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
		map[string]string{"source_name": "my-source"})
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
