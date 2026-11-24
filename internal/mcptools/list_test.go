package mcptools

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

type staticSources []models.SourceDefinition

func (s staticSources) ListAll() ([]models.SourceDefinition, error) { return s, nil }

// setupWithSources is setup with a source list, so "me" and as_of resolve.
func setupWithSources(t *testing.T, qd *fakeQdrant, srcs staticSources) (*server.MCPServer, context.Context) {
	t.Helper()
	embedSrv := httptest.NewServer(fixedEmbedHandler(0, ""))
	t.Cleanup(embedSrv.Close)
	qdSrv := httptest.NewServer(qd.handler())
	t.Cleanup(qdSrv.Close)
	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Qdrant.URL = qdSrv.URL
	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg)
	s := server.NewMCPServer("test", "0.0.0")
	RegisterTools(s, rag.NewSearchService(vectors, embedding, srcs), memory.NewService(vectors, embedding))
	return s, context.Background()
}

func listPoint(id, sourceID string) map[string]any {
	return map[string]any{"id": id, "payload": map[string]any{
		models.PayloadText: "doc " + id, models.PayloadSourceDocID: id, models.PayloadChunkIndex: "0",
		models.PayloadTotalChunks: "1", models.TagKey("source_id"): sourceID,
	}}
}

// "Latest commits by me": one result per document, newest first by the
// stored author date, "me" resolved from the identity the sync recorded, and
// the answer stamped with when its source last synced.
func TestListModeSortsResolvesMeAndStampsAsOf(t *testing.T) {
	synced := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	qd := &fakeQdrant{scrollPoints: []map[string]any{listPoint("c2", "s1"), listPoint("c1", "s1")}}
	s, ctx := setupWithSources(t, qd, staticSources{
		{ID: "s1", LastSyncedAt: &synced, Identity: []string{"Ann Lee", "ann@corp"}},
		{ID: "off", Disabled: true, Identity: []string{"someone else"}},
	})

	res := callTool(t, s, ctx, "search_commit", map[string]any{"request": map[string]any{
		"mode": "list", "filters": map[string]any{"author": "me"},
	}})
	text := resultText(t, res)
	if res.IsError {
		t.Fatalf("list failed: %s", text)
	}
	var out struct {
		Results []models.SearchResult `json:"results"`
		AsOf    map[string]string     `json:"as_of"`
		Order   string                `json:"order"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 2 || out.Results[0].SourceDocID != "c2" || out.Order != "authored descending" {
		t.Errorf("results = %+v, order %q", out.Results, out.Order)
	}
	if out.AsOf["s1"] != "2026-11-05T09:30:00Z" {
		t.Errorf("as_of = %v", out.AsOf)
	}

	body := qd.lastScrollBody
	order, _ := body["order_by"].(map[string]any)
	if order["key"] != models.FilterDateKey || order["direction"] != "desc" {
		t.Errorf("order_by = %v", body["order_by"])
	}
	if body["limit"] != float64(rag.ListPageSize+1) {
		t.Errorf("limit = %v", body["limit"])
	}
	filter, _ := json.Marshal(body["filter"])
	for _, want := range []string{`"key":"chunk_index","match":{"value":"0"}`, `"ann lee"`, `"ann@corp"`} {
		if !strings.Contains(string(filter), want) {
			t.Errorf("filter lacks %s: %s", want, filter)
		}
	}
	if strings.Contains(string(filter), "someone else") {
		t.Error("a disabled source's identity must not count as me")
	}
}

func TestListModeOptionsAndErrors(t *testing.T) {
	qd := &fakeQdrant{}
	s, ctx := setupWithSources(t, qd, staticSources{{ID: "s1"}})

	call := func(tool string, req map[string]any) (string, bool) {
		res := callTool(t, s, ctx, tool, map[string]any{"request": req})
		return resultText(t, res), res.IsError
	}
	if text, isErr := call("search_commit", map[string]any{"mode": "list", "filters": map[string]any{"author": "me"}}); !isErr ||
		!strings.Contains(text, `"me" is unknown`) {
		t.Errorf("me without a recorded identity = %s", text)
	}
	if text, isErr := call("search_commit", map[string]any{"mode": "list", "order_by": "nope"}); !isErr || !strings.Contains(text, "authored") {
		t.Errorf("unknown order_by = %s", text)
	}
	if _, isErr := call("search_commit", map[string]any{"mode": "list", "direction": "asc", "page": float64(2)}); isErr {
		t.Fatal("asc page 2 should work")
	}
	if order, _ := qd.lastScrollBody["order_by"].(map[string]any); order["direction"] != "asc" || qd.lastScrollBody["limit"] != float64(2*rag.ListPageSize+1) {
		t.Errorf("asc page 2 scroll = %v", qd.lastScrollBody)
	}
	// A collection without sort keys lists in storage order.
	text, isErr := call("search_source_code", map[string]any{"mode": "list"})
	if isErr {
		t.Fatalf("unsorted list failed: %s", text)
	}
	if _, ordered := qd.lastScrollBody["order_by"]; ordered {
		t.Error("source code has no sort keys, so no order_by")
	}
	if !strings.Contains(text, "Nothing matched") {
		t.Errorf("empty list note = %s", text)
	}
}

func TestListIndexesCoverEverySortKey(t *testing.T) {
	idx := ListIndexes()
	if o := idx[models.CollectionCommits]; len(o) != 1 || o[0].Key != models.FilterDateKey || o[0].Schema != "integer" {
		t.Errorf("commit indexes = %+v", o)
	}
}
