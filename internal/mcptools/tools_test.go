package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// fakeQdrant is a minimal Qdrant stand-in covering the three endpoints the
// search and experience tools exercise: search (points/query), upsert, and
// delete-by-ids (the Remember rollback path).
type fakeQdrant struct {
	mu sync.Mutex

	searchPoints   []map[string]any // returned verbatim from points/query, unless searchErr
	searchErr      bool
	lastSearchBody map[string]any

	upsertErr      bool
	upsertCalls    int
	lastUpsertBody map[string]any

	deleteCalls [][]string
}

func (q *fakeQdrant) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/query"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.lastSearchBody = body
			if q.searchErr {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("search boom"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"points": q.searchPoints},
			})
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/points"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.lastUpsertBody = body
			q.upsertCalls++
			if q.upsertErr {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("upsert boom"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/points/delete"):
			var body struct {
				Points []string `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			q.deleteCalls = append(q.deleteCalls, body.Points)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
		}
	}
}

func fixedEmbedHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}
}

// setup wires a real *server.MCPServer with RegisterTools against a real
// rag.SearchService/memory.Service backed by httptest fakes, matching how
// cmd/conduit/main.go wires these — the only substitution is HTTP endpoints.
func setup(t *testing.T, qd *fakeQdrant, embedHandler http.HandlerFunc) (*server.MCPServer, context.Context) {
	t.Helper()
	if embedHandler == nil {
		embedHandler = fixedEmbedHandler(0, "")
	}
	embedSrv := httptest.NewServer(embedHandler)
	t.Cleanup(embedSrv.Close)

	qdSrv := httptest.NewServer(qd.handler())
	t.Cleanup(qdSrv.Close)

	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = embedSrv.URL
	cfg.Embedding.MaxInputTokens = 8192
	cfg.Qdrant.URL = qdSrv.URL

	vectors := rag.NewVectorStore(cfg)
	embedding := rag.NewEmbeddingService(cfg)
	search := rag.NewSearchService(vectors, embedding, nil)
	mem := memory.NewService(vectors, embedding)

	s := server.NewMCPServer("test", "0.0.0")
	RegisterTools(s, search, mem)
	return s, context.Background()
}

// callTool drives a registered tool through the real JSON-RPC "tools/call"
// path (server.HandleMessage), the same entry point a real MCP client uses.
func callTool(t *testing.T, s *server.MCPServer, ctx context.Context, name string, args map[string]any) mcp.CallToolResult {
	t.Helper()
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	respMsg := s.HandleMessage(ctx, raw)
	resp, ok := respMsg.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected mcp.JSONRPCResponse, got %T: %+v", respMsg, respMsg)
	}
	result, ok := resp.Result.(mcp.CallToolResult)
	if !ok {
		t.Fatalf("expected mcp.CallToolResult, got %T: %+v", resp.Result, resp.Result)
	}
	return result
}

func resultText(t *testing.T, result mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected mcp.TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

func TestRegisterToolsListsAllTools(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, nil)

	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	respMsg := s.HandleMessage(ctx, raw)
	resp, ok := respMsg.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected mcp.JSONRPCResponse, got %T", respMsg)
	}
	result, ok := resp.Result.(mcp.ListToolsResult)
	if !ok {
		t.Fatalf("expected mcp.ListToolsResult, got %T", resp.Result)
	}

	got := map[string]bool{}
	for _, tool := range result.Tools {
		got[tool.Name] = true
	}
	want := []string{
		"search_workitem", "search_requirement", "search_source_code",
		"search_test_code", "search_testcase", "search_documentation",
		"search_commit", "retrieve_experience", "remember",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q not registered", name)
		}
	}
	if len(result.Tools) != len(want) {
		t.Errorf("got %d registered tools, want %d: %v", len(result.Tools), len(want), got)
	}
}

func TestSearchToolReturnsResults(t *testing.T) {
	qd := &fakeQdrant{searchPoints: []map[string]any{
		{"id": "p1", "score": 0.87, "payload": map[string]any{"text": "hit text"}},
	}}
	s, ctx := setup(t, qd, nil)

	result := callTool(t, s, ctx, "search_workitem", map[string]any{"query": "some bug"})
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}

	var payload struct {
		Results []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"results"`
		Page    int  `json:"page"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Results) != 1 || payload.Results[0].ID != "p1" || payload.Results[0].Text != "hit text" {
		t.Errorf("Results = %+v, want one hit with id=p1 text=%q", payload.Results, "hit text")
	}
	if payload.Page != 1 {
		t.Errorf("Page = %d, want 1", payload.Page)
	}
	if payload.HasMore {
		t.Error("HasMore = true, want false (only one point returned by the fake)")
	}
}

func TestSearchToolNoResultsReturnsNote(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, nil)

	result := callTool(t, s, ctx, "search_documentation", map[string]any{"query": "nothing matches"})
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}

	var payload struct {
		Results []any  `json:"results"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Results) != 0 {
		t.Errorf("Results = %v, want empty", payload.Results)
	}
	wantNote := "No data embedded for this query — the source may not be synced yet, or nothing matched."
	if payload.Note != wantNote {
		t.Errorf("Note = %q, want %q", payload.Note, wantNote)
	}
}

func TestSearchToolMissingQueryArgErrors(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, nil)

	result := callTool(t, s, ctx, "search_workitem", map[string]any{})
	if !result.IsError {
		t.Fatal("expected IsError=true for missing query argument")
	}
	if got := resultText(t, result); !strings.Contains(got, `"query"`) {
		t.Errorf("error text = %q, want it to mention the missing query argument", got)
	}
}

func TestSearchToolEmbeddingFailurePropagatesAsToolError(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, fixedEmbedHandler(http.StatusInternalServerError, "embed down"))

	result := callTool(t, s, ctx, "search_commit", map[string]any{"query": "anything"})
	if !result.IsError {
		t.Fatal("expected IsError=true when the embedding backend fails")
	}
	if got := resultText(t, result); !strings.Contains(got, "500") {
		t.Errorf("error text = %q, want it to surface the HTTP 500 from the embedding call", got)
	}
}

func TestSearchToolSourceNameFilterIsPlumbedToQdrant(t *testing.T) {
	qd := &fakeQdrant{}
	s, ctx := setup(t, qd, nil)

	result := callTool(t, s, ctx, "search_testcase", map[string]any{
		"query": "login flow", "source_name": "my-source", "page": float64(3),
	})
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}

	qd.mu.Lock()
	body := qd.lastSearchBody
	qd.mu.Unlock()
	// page 3 with pageSize 1 → offset 2 (skip ranks 1–2), limit 2 (fetch rank 3
	// plus one lookahead to know whether a further page exists).
	if got := body["limit"]; got != float64(2) {
		t.Errorf("limit = %v, want 2", got)
	}
	if got := body["offset"]; got != float64(2) {
		t.Errorf("offset = %v, want 2", got)
	}
	filter, ok := body["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter missing or wrong shape in search body: %+v", body)
	}
	must, ok := filter["must"].([]any)
	if !ok || len(must) != 1 {
		t.Fatalf("filter.must = %+v, want one condition", filter["must"])
	}
	cond, ok := must[0].(map[string]any)
	if !ok || cond["key"] != "tag_source_name" {
		t.Fatalf("filter.must[0] = %+v, want key=tag_source_name", must[0])
	}
	match, ok := cond["match"].(map[string]any)
	if !ok || match["value"] != "my-source" {
		t.Fatalf("filter.must[0].match = %+v, want value=my-source", cond["match"])
	}
}

func TestSearchToolPagination(t *testing.T) {
	type paginationPayload struct {
		Results []any  `json:"results"`
		Page    int    `json:"page"`
		HasMore bool   `json:"has_more"`
		Note    string `json:"note"`
	}
	decode := func(t *testing.T, result mcp.CallToolResult) paginationPayload {
		t.Helper()
		var p paginationPayload
		if err := json.Unmarshal([]byte(resultText(t, result)), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("page 1 with a further match reports has_more", func(t *testing.T) {
		qd := &fakeQdrant{searchPoints: []map[string]any{
			{"id": "p1", "score": 0.9, "payload": map[string]any{"text": "most relevant"}},
			{"id": "p2", "score": 0.5, "payload": map[string]any{"text": "next most relevant"}},
		}}
		s, ctx := setup(t, qd, nil)
		p := decode(t, callTool(t, s, ctx, "search_workitem", map[string]any{"query": "x"}))
		if len(p.Results) != 1 || p.Page != 1 || !p.HasMore {
			t.Errorf("got %+v, want 1 result, page 1, has_more true", p)
		}
	})

	t.Run("last page reports has_more false", func(t *testing.T) {
		qd := &fakeQdrant{searchPoints: []map[string]any{
			{"id": "p2", "score": 0.5, "payload": map[string]any{"text": "next most relevant"}},
		}}
		s, ctx := setup(t, qd, nil)
		p := decode(t, callTool(t, s, ctx, "search_workitem", map[string]any{"query": "x", "page": float64(2)}))
		if len(p.Results) != 1 || p.Page != 2 || p.HasMore {
			t.Errorf("got %+v, want 1 result, page 2, has_more false", p)
		}
	})

	t.Run("paging past the last match returns the exhausted-page note", func(t *testing.T) {
		qd := &fakeQdrant{}
		s, ctx := setup(t, qd, nil)
		p := decode(t, callTool(t, s, ctx, "search_workitem", map[string]any{"query": "x", "page": float64(3)}))
		if len(p.Results) != 0 || p.HasMore {
			t.Errorf("got %+v, want no results and has_more false", p)
		}
		want := "No further matches beyond page 2 — this was the last page."
		if p.Note != want {
			t.Errorf("Note = %q, want %q", p.Note, want)
		}
	})
}

func TestRetrieveExperienceReturnsResults(t *testing.T) {
	qd := &fakeQdrant{searchPoints: []map[string]any{
		{"id": "e1", "score": 0.9, "payload": map[string]any{
			"text": "user asked to avoid X", "prop_guidance": "always do Y instead",
		}},
	}}
	s, ctx := setup(t, qd, nil)

	result := callTool(t, s, ctx, "retrieve_experience", map[string]any{"query": "should I do X?"})
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}

	var payload struct {
		Experience []struct {
			Situation string `json:"situation"`
			Guidance  string `json:"guidance"`
		} `json:"experience"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Experience) != 1 ||
		payload.Experience[0].Situation != "user asked to avoid X" ||
		payload.Experience[0].Guidance != "always do Y instead" {
		t.Errorf("Experience = %+v, want one matching hit", payload.Experience)
	}
}

func TestRetrieveExperienceNoResultsReturnsNote(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, nil)

	result := callTool(t, s, ctx, "retrieve_experience", map[string]any{"query": "brand new situation"})
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	var payload struct {
		Experience []any  `json:"experience"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Note != "No relevant experience found." {
		t.Errorf("Note = %q, want %q", payload.Note, "No relevant experience found.")
	}
}

func TestRememberToolStoresAndReturnsEntryID(t *testing.T) {
	qd := &fakeQdrant{}
	s, ctx := setup(t, qd, nil)

	result := callTool(t, s, ctx, "remember", map[string]any{
		"situation": "user is debugging a flaky test",
		"guidance":  "re-run with -count=1 before assuming it's flaky",
	})
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}

	var payload struct {
		Status  string `json:"status"`
		EntryID string `json:"entry_id"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "stored" {
		t.Errorf("Status = %q, want %q", payload.Status, "stored")
	}
	if _, err := uuid.Parse(payload.EntryID); err != nil {
		t.Errorf("EntryID = %q is not a valid UUID: %v", payload.EntryID, err)
	}

	qd.mu.Lock()
	defer qd.mu.Unlock()
	if qd.upsertCalls != 1 {
		t.Fatalf("upsertCalls = %d, want 1", qd.upsertCalls)
	}
	points, ok := qd.lastUpsertBody["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("upsert body points = %+v, want one point", qd.lastUpsertBody["points"])
	}
	point := points[0].(map[string]any)
	payloadMap := point["payload"].(map[string]any)
	if payloadMap["text"] != "user is debugging a flaky test" {
		t.Errorf("stored text = %v, want the situation text", payloadMap["text"])
	}
	if payloadMap["prop_guidance"] != "re-run with -count=1 before assuming it's flaky" {
		t.Errorf("stored guidance = %v, want the guidance text", payloadMap["prop_guidance"])
	}
}

func TestRememberToolMissingArgsErrors(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, nil)

	cases := []map[string]any{
		{"guidance": "only guidance, no situation"},
		{"situation": "only situation, no guidance"},
		{},
	}
	for _, args := range cases {
		result := callTool(t, s, ctx, "remember", args)
		if !result.IsError {
			t.Errorf("args %+v: expected IsError=true", args)
		}
	}
}

func TestRememberToolRollsBackOnUpsertFailure(t *testing.T) {
	qd := &fakeQdrant{upsertErr: true}
	s, ctx := setup(t, qd, nil)

	result := callTool(t, s, ctx, "remember", map[string]any{
		"situation": "this upsert will fail",
		"guidance":  "should be rolled back",
	})
	if !result.IsError {
		t.Fatal("expected IsError=true when the upsert fails")
	}

	qd.mu.Lock()
	defer qd.mu.Unlock()
	if qd.upsertCalls != 1 {
		t.Fatalf("upsertCalls = %d, want 1", qd.upsertCalls)
	}
	points := qd.lastUpsertBody["points"].([]any)
	attemptedID := points[0].(map[string]any)["id"].(string)

	if len(qd.deleteCalls) != 1 {
		t.Fatalf("deleteCalls = %+v, want exactly one rollback delete", qd.deleteCalls)
	}
	if len(qd.deleteCalls[0]) != 1 || qd.deleteCalls[0][0] != attemptedID {
		t.Errorf("rollback deleted %v, want [%s]", qd.deleteCalls[0], attemptedID)
	}
}
