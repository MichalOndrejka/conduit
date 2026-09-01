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
