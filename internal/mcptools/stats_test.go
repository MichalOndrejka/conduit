package mcptools

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// Search calls made through the real MCP server with the stats middleware
// record what was searched, with which filters, and how many results came
// back.
func TestSearchToolCallsAreRecordedInStats(t *testing.T) {
	qd := &fakeQdrant{searchPoints: []map[string]any{
		{"id": "p1", "score": 0.9, "payload": map[string]any{"text": "hit"}},
	}}
	embedSrv := httptest.NewServer(fixedEmbedHandler(0, ""))
	defer embedSrv.Close()
	qdSrv := httptest.NewServer(qd.handler())
	defer qdSrv.Close()
	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Qdrant.URL = qdSrv.URL
	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg)

	rec := stats.New()
	s := server.NewMCPServer("test", "0.0.0", server.WithToolHandlerMiddleware(rec.ToolMiddleware))
	RegisterTools(s, rag.NewSearchService(vectors, embedding, nil), memory.NewService(vectors, embedding))
	ctx := context.Background()

	callTool(t, s, ctx, "search_workitem", map[string]any{"request": map[string]any{
		"mode": "semantic_search", "query": "login bug", "page": float64(1),
		"filters": map[string]any{"state": []any{"Active", "New"}, "project": "Payments"},
	}})
	qd.searchPoints = nil
	callTool(t, s, ctx, "search_workitem", map[string]any{"request": map[string]any{
		"mode": "semantic_search", "query": "nothing here", "page": float64(2),
	}})

	events := rec.Events()
	var calls []*stats.ToolCall
	for _, e := range events {
		if e.Tool != nil {
			calls = append(calls, e.Tool)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d", len(calls))
	}
	first := calls[0]
	if first.Name != "search_workitem" || first.Mode != "semantic_search" || first.Query != "login bug" || first.Page != 1 ||
		first.Results != 1 || !first.Counted || first.Collection == "" || !first.Cold {
		t.Errorf("first call = %+v", first)
	}
	if len(first.Filters["state"]) != 2 || first.Filters["project"][0] != "Payments" {
		t.Errorf("filters = %v", first.Filters)
	}
	if second := calls[1]; second.Results != 0 || second.Page != 2 || second.Cold {
		t.Errorf("second call = %+v", second)
	}
}
