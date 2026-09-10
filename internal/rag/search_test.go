package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

type fakeSourceLister []models.SourceDefinition

func (f fakeSourceLister) ListAll() ([]models.SourceDefinition, error) { return f, nil }

func TestSearchExcludesDisabledSources(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	defer embedSrv.Close()

	var gotBody map[string]any
	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"points": []map[string]any{}},
		})
	}))
	defer qdrantSrv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = qdrantSrv.URL
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192

	vectors := NewVectorStore(cfg)
	embedding := NewEmbeddingService(cfg)
	sources := fakeSourceLister{
		{ID: "enabled-1", Disabled: false},
		{ID: "disabled-1", Disabled: true},
		{ID: "disabled-2", Disabled: true},
	}
	svc := NewSearchService(vectors, embedding, sources)

	if _, _, err := svc.Search(context.Background(), "conduit_workitems", "query", 1, nil); err != nil {
		t.Fatal(err)
	}

	filter, ok := gotBody["filter"].(map[string]any)
	if !ok {
		t.Fatal("filter missing from request body")
	}
	mustNot, ok := filter["must_not"].([]any)
	if !ok || len(mustNot) != 2 {
		t.Fatalf("must_not = %v, want 2 conditions", filter["must_not"])
	}
	excluded := map[string]bool{}
	for _, c := range mustNot {
		cond := c.(map[string]any)
		if cond["key"] != models.TagKey("source_id") {
			t.Errorf("must_not key = %v, want %v", cond["key"], models.TagKey("source_id"))
		}
		match := cond["match"].(map[string]any)
		excluded[match["value"].(string)] = true
	}
	if !excluded["disabled-1"] || !excluded["disabled-2"] {
		t.Errorf("excluded IDs = %v, want disabled-1 and disabled-2", excluded)
	}
	if excluded["enabled-1"] {
		t.Error("enabled-1 should not be excluded")
	}
}

func TestGetChunkFetchesByComputedID(t *testing.T) {
	wantID := makeChunkID("doc1", 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.IDs) != 1 || body.IDs[0] != wantID {
			t.Errorf("requested ids = %v, want [%s]", body.IDs, wantID)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{
				{"id": wantID, "payload": map[string]any{
					"text": "chunk text", "source_doc_id": "doc1", "chunk_index": "2", "total_chunks": "5",
				}},
			},
		})
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	svc := NewSearchService(NewVectorStore(cfg), nil, nil)

	result, found, err := svc.GetChunk(context.Background(), "conduit_workitems", "doc1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if result.Text != "chunk text" || result.ChunkIndex != 2 || result.TotalChunks != 5 {
		t.Errorf("got %+v, want chunk 2 of 5 with text %q", result, "chunk text")
	}
}

func TestGetChunkFallsBackToSingleChunkPointID(t *testing.T) {
	singleID := makeID("doc1")
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var body struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.IDs) == 1 && body.IDs[0] == singleID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": []map[string]any{
					{"id": singleID, "payload": map[string]any{"text": "single chunk doc", "source_doc_id": "doc1"}},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{}})
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	svc := NewSearchService(NewVectorStore(cfg), nil, nil)

	result, found, err := svc.GetChunk(context.Background(), "conduit_workitems", "doc1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected found=true via the single-chunk fallback ID")
	}
	if result.Text != "single chunk doc" {
		t.Errorf("Text = %q, want %q", result.Text, "single chunk doc")
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (chunk-form lookup, then single-chunk fallback)", callCount)
	}
}

func TestGetChunkNotFoundReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{}})
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	svc := NewSearchService(NewVectorStore(cfg), nil, nil)

	_, found, err := svc.GetChunk(context.Background(), "conduit_workitems", "doc1", 9)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false when no point matches either ID form")
	}
}

func TestGetChunkPropagatesRetrieveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	svc := NewSearchService(NewVectorStore(cfg), nil, nil)

	_, found, err := svc.GetChunk(context.Background(), "conduit_workitems", "doc1", 2)
	if err == nil {
		t.Fatal("expected the Retrieve error to propagate")
	}
	if found {
		t.Error("expected found=false on error")
	}
}

func TestGetChunkPropagatesFallbackRetrieveError(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First lookup (chunk-form ID) misses, forcing the chunkIndex==0 fallback.
			_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	svc := NewSearchService(NewVectorStore(cfg), nil, nil)

	_, found, err := svc.GetChunk(context.Background(), "conduit_workitems", "doc1", 0)
	if err == nil {
		t.Fatal("expected the fallback Retrieve error to propagate")
	}
	if found {
		t.Error("expected found=false on error")
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (chunk-form lookup, then failing fallback)", callCount)
	}
}

func TestGetChunkNegativeIndexSkipsRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{}})
	}))
	defer srv.Close()

	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	svc := NewSearchService(NewVectorStore(cfg), nil, nil)

	_, found, err := svc.GetChunk(context.Background(), "conduit_workitems", "doc1", -1)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected found=false for a negative chunk index")
	}
	if called {
		t.Error("expected no HTTP call for a negative chunk index")
	}
}
