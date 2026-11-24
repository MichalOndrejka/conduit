package stats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestRecorderKeepsEventsInMemory(t *testing.T) {
	r := New()
	r.RecordTool(&ToolCall{Name: "search_backlog", Mode: "semantic_search", Query: "q", Counted: true})
	r.RecordSync(&SyncRun{SourceID: "s1", Status: "completed", Documents: 3})
	events := r.Events()
	if len(events) != 2 || events[0].Tool.Query != "q" || events[1].Sync.Documents != 3 {
		t.Fatalf("events = %+v", events)
	}
	for _, e := range events {
		if e.Time.IsZero() {
			t.Errorf("event %+v lacks a time", e)
		}
	}
	// Events returns a copy: later records don't change it.
	r.RecordTool(&ToolCall{Name: "t"})
	if len(events) != 2 || len(r.Events()) != 3 {
		t.Error("Events must return a snapshot")
	}
}

func TestToolMiddlewareRecordsCallsAndHandlerDetails(t *testing.T) {
	r := New()
	handler := r.ToolMiddleware(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if tc := ToolCallFrom(ctx); tc != nil {
			tc.Query, tc.Results, tc.Counted = "reset link", 2, true
		}
		if req.Params.Name == "search_broken" {
			return mcp.NewToolResultError("qdrant down"), nil
		}
		return mcp.NewToolResultText("ok"), nil
	})
	call := func(name string, args map[string]any) {
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		if _, err := handler(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	search := map[string]any{"request": map[string]any{"mode": "semantic_search", "query": "x", "page": 1}}
	call("search_backlog", search)
	call("search_backlog", search)
	call("search_broken", map[string]any{"request": map[string]any{"mode": "pattern_search"}})
	call("source_sync", map[string]any{"request": map[string]any{"mode": "status"}})

	events := r.Events()
	var tools []*ToolCall
	for _, e := range events {
		if e.Tool != nil {
			tools = append(tools, e.Tool)
		}
	}
	if len(tools) != 4 {
		t.Fatalf("tool events = %d", len(tools))
	}
	if tools[0].Mode != "semantic_search" || tools[0].Query != "reset link" || tools[0].Results != 2 || !tools[0].Counted {
		t.Errorf("first call = %+v", tools[0])
	}
	if !tools[0].Cold || tools[1].Cold {
		t.Error("only the process's first embedding call is cold")
	}
	if tools[2].Error != "qdrant down" || tools[3].Mode != "status" {
		t.Errorf("calls = %+v, %+v", tools[2], tools[3])
	}
}

func TestNilRecorderIsANoOp(t *testing.T) {
	var r *Recorder
	r.RecordTool(&ToolCall{})
	r.RecordSync(&SyncRun{})
	if r.Events() != nil {
		t.Error("nil recorder should load nothing")
	}
	next := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ToolCallFrom(ctx) != nil {
			t.Error("no call record without a recorder")
		}
		return nil, nil
	}
	r.ToolMiddleware(next)(context.Background(), mcp.CallToolRequest{})
}

func TestCountingTransportCountsRequestsAndRateLimits(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
		case 3:
			w.WriteHeader(http.StatusForbidden) // permission problem, not a rate limit
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: CountingTransport(nil)}
	ctx, counter := WithRequestCounter(context.Background())
	for range 4 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// Outside a counted context nothing is counted.
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if counter.Requests() != 4 || counter.RateLimited() != 2 {
		t.Errorf("requests = %d, rate limited = %d; want 4 and 2", counter.Requests(), counter.RateLimited())
	}
}

func ev(at int, tool *ToolCall, sync *SyncRun) Event {
	return Event{Time: time.Date(2026, 11, 4, 10, at, 0, 0, time.UTC), Type: map[bool]string{true: "tool", false: "sync"}[tool != nil], Tool: tool, Sync: sync}
}

func search(tool, query string, page, results int, ms int64, filters map[string][]string) *ToolCall {
	return &ToolCall{Name: tool, Mode: "semantic_search", Query: query, Page: page, Results: results, Counted: true, LatencyMs: ms, Filters: filters}
}

func TestSummarize(t *testing.T) {
	events := []Event{
		ev(1, &ToolCall{Name: "search_backlog", Mode: "semantic_search", Query: "cold", Page: 1, Results: 2, Counted: true, LatencyMs: 9000, Cold: true}, nil),
		ev(2, search("search_backlog", "reqs", 1, 0, 200, map[string][]string{"work_item_type": {"Requirement"}}), nil),
		ev(3, search("search_backlog", "reqs", 2, 1, 300, nil), nil),
		ev(4, &ToolCall{Name: "search_backlog", Mode: "retrieve_chunk", Counted: true, Results: 1}, nil),
		ev(5, &ToolCall{Name: "experience_retrieve", Query: "x", Counted: true, Results: 1, Hits: []string{"Deploy rules"}}, nil),
		ev(6, &ToolCall{Name: "experience_retrieve", Query: "y", Counted: true, Results: 0}, nil),
		ev(7, nil, &SyncRun{SourceID: "gh", SourceName: "App", Provider: "github", Status: "failed", DurationMs: 1000, Requests: 10}),
		ev(8, nil, &SyncRun{SourceID: "gh", SourceName: "App", Provider: "github", Status: "completed", DurationMs: 5000, Requests: 2, Documents: 900}),
		ev(9, nil, &SyncRun{SourceID: "wi", SourceName: "Work items", Provider: "azure-devops", Status: "completed", Requests: 40, RateLimited: 3}),
		ev(10, nil, &SyncRun{SourceID: "gh2", SourceName: "Docs", Provider: "github", Status: "completed", Requests: 5, RateLimited: 1}),
	}
	d := Summarize(events)
	s := d.Search
	// Two first-page searches (the cold one included), one of them empty;
	// one further page.
	if s.FirstPages != 2 || s.Empty != 1 || s.NextPages != 1 || s.RetrieveChunk != 1 {
		t.Errorf("search stats = %+v", s)
	}
	if s.P95Ms == 9000 || s.P50Ms == 0 {
		t.Errorf("latency %d/%d must leave out the cold call", s.P50Ms, s.P95Ms)
	}
	if d.ToolCalls != 6 || len(d.Tools) != 2 || d.Tools[0].Name != "search_backlog" {
		t.Errorf("tools = %d, %+v", d.ToolCalls, d.Tools)
	}
	// The latest sync of the source counts, not the failed attempt before it.
	if d.Sync.Sources != 3 || d.Sync.Failed != 0 || d.Sync.Requests != 47 {
		t.Errorf("sync totals = %+v", d.Sync)
	}
	if len(d.Sources) != 3 || d.Sources[0].Latest.SourceName != "App" || d.Sources[0].Runs != 2 || d.Sources[0].Latest.Status != "completed" {
		t.Errorf("sources = %+v", d.Sources)
	}
	// Provider load counts every run, the failed attempt included.
	want := []ProviderRow{{Provider: "azure-devops", Syncs: 1, Requests: 40, RateLimited: 3}, {Provider: "github", Syncs: 3, Requests: 17, RateLimited: 1}}
	if len(d.Providers) != 2 || d.Providers[0] != want[0] || d.Providers[1] != want[1] {
		t.Errorf("providers = %+v", d.Providers)
	}
	if len(d.EmptySearches) != 1 || d.EmptySearches[0].Query != "reqs" {
		t.Errorf("empty searches = %+v", d.EmptySearches)
	}
	if len(d.Filters) != 1 || d.Filters[0].Value != "Requirement" || d.Filters[0].Empty != 1 {
		t.Errorf("filters = %+v", d.Filters)
	}
	if d.Experience.Retrieves != 2 || d.Experience.EmptyRetrieves != 1 || len(d.Experience.Top) != 1 || !d.RetrievedSituations["Deploy rules"] {
		t.Errorf("experience = %+v", d.Experience)
	}
	if e := Summarize(nil); e.ToolCalls != 0 || len(e.Sources) != 0 {
		t.Error("no events, empty dashboard")
	}
}

func TestIsRateLimitedRetryAfter(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Retry-After": {"60"}}}
	if !IsRateLimited(resp) {
		t.Error("403 with Retry-After is GitHub's secondary rate limit")
	}
	if IsRateLimited(&http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}}) {
		t.Error("404 is not a rate limit")
	}
}

func TestToolMiddlewareCountsAPIRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	client := &http.Client{Transport: CountingTransport(nil)}
	r := New()
	handler := r.ToolMiddleware(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ToolCallFrom(ctx).SourceID = "prs"
		for range 3 {
			hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			resp, err := client.Do(hreq)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		}
		return mcp.NewToolResultText("ok"), nil
	})
	req := mcp.CallToolRequest{}
	req.Params.Name = "search_pull_request"
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if tc := r.Events()[0].Tool; tc.Requests != 3 || tc.SourceID != "prs" {
		t.Errorf("tool call = %+v, want 3 requests to prs", tc)
	}
}

func TestSummarizeAPIRequestsByToolAndSource(t *testing.T) {
	events := []Event{
		ev(1, nil, &SyncRun{SourceID: "code", SourceName: "Code", Provider: "github", Status: "completed", Requests: 30, RateLimited: 2}),
		ev(2, &ToolCall{Name: "search_pull_request", SourceID: "code", SourceName: "Code", Provider: "github", Requests: 4}, nil),
		ev(3, &ToolCall{Name: "search_pull_request", SourceID: "prs", SourceName: "PRs", Provider: "azure-devops", Requests: 50, RateLimited: 1}, nil),
		ev(4, &ToolCall{Name: "search_pipeline_run", SourceID: "ci", SourceName: "CI", Provider: "azure-devops", Requests: 6}, nil),
		ev(5, &ToolCall{Name: "search_backlog", Mode: "semantic_search", Counted: true}, nil),
		ev(6, nil, &SyncRun{SourceID: "idle", SourceName: "Idle", Provider: "github", Status: "completed"}),
	}
	d := Summarize(events)
	reqs := map[string]int{}
	for _, t := range d.Tools {
		reqs[t.Name] = t.Requests
	}
	if reqs["search_pull_request"] != 54 || reqs["search_pipeline_run"] != 6 || reqs["search_backlog"] != 0 {
		t.Errorf("requests by tool = %v", reqs)
	}
	want := []SourceRequestRow{
		{SourceID: "prs", SourceName: "PRs", Provider: "azure-devops", ToolRequests: 50, RateLimited: 1},
		{SourceID: "code", SourceName: "Code", Provider: "github", SyncRequests: 30, ToolRequests: 4, RateLimited: 2},
		{SourceID: "ci", SourceName: "CI", Provider: "azure-devops", ToolRequests: 6},
	}
	if !slices.Equal(d.SourceRequests, want) {
		t.Errorf("source requests = %+v", d.SourceRequests)
	}
	// Provider load includes the live calls.
	if d.Providers[0].Provider != "azure-devops" || d.Providers[0].Requests != 56 || d.Providers[1].Requests != 34 {
		t.Errorf("providers = %+v", d.Providers)
	}
}

func TestOpenPersistsEventsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	r, err := Open(path, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r.RecordTool(&ToolCall{Name: "search_backlog", Query: "q", Requests: 2, SourceID: "s"})
	r.RecordSync(&SyncRun{SourceID: "s1", Status: "completed", Documents: 3})
	r.Close()

	r2, err := Open(path, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	events := r2.Events()
	if len(events) != 2 || events[0].Tool.Query != "q" || events[0].Tool.Requests != 2 || events[1].Sync.Documents != 3 {
		t.Fatalf("reloaded events = %+v", events)
	}
	if !r2.Started().Equal(events[0].Time) {
		t.Errorf("started = %v, want the oldest event %v", r2.Started(), events[0].Time)
	}
	// New events append after the reloaded ones.
	r2.RecordTool(&ToolCall{Name: "t"})
	r2.Close()
	r3, _ := Open(path, 7*24*time.Hour)
	defer r3.Close()
	if n := len(r3.Events()); n != 3 {
		t.Errorf("events after a second restart = %d, want 3", n)
	}
}

func TestOpenDropsExpiredAndUnreadableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	old, _ := json.Marshal(Event{Time: time.Now().Add(-8 * 24 * time.Hour), Type: "tool", Tool: &ToolCall{Name: "old"}})
	fresh, _ := json.Marshal(Event{Time: time.Now().Add(-time.Hour), Type: "tool", Tool: &ToolCall{Name: "fresh"}})
	os.WriteFile(path, []byte(string(old)+"\nnot json\n"+string(fresh)+"\n"), 0o644)

	r, err := Open(path, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if ev := r.Events(); len(ev) != 1 || ev[0].Tool.Name != "fresh" {
		t.Fatalf("events = %+v, want only the fresh one", ev)
	}
	// The file itself is rewritten without them.
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"old"`) || strings.Contains(string(data), "not json") {
		t.Errorf("stats file still holds dropped lines:\n%s", data)
	}
}

func TestResetClearsMemoryAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	r, _ := Open(path, 7*24*time.Hour)
	r.RecordTool(&ToolCall{Name: "t"})
	if err := r.Reset(); err != nil {
		t.Fatal(err)
	}
	r.RecordTool(&ToolCall{Name: "after"})
	r.Close()
	r2, _ := Open(path, 7*24*time.Hour)
	defer r2.Close()
	if ev := r2.Events(); len(ev) != 1 || ev[0].Tool.Name != "after" {
		t.Errorf("events after reset = %+v", ev)
	}
}
