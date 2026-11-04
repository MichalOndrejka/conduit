package stats

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func openTemp(t *testing.T, build string, keep int) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := Open(dir, Build{ID: build}, keep)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, dir
}

func TestRecorderAppendsEventsAndSkipsBrokenLines(t *testing.T) {
	r, dir := openTemp(t, "b1", 10)
	r.RecordTool(&ToolCall{Name: "search_workitem", Mode: "semantic_search", Query: "q", Counted: true})
	r.RecordSync(&SyncRun{SourceID: "s1", Status: "completed", Documents: 3})
	// A line cut short by a crash mid-write.
	f, _ := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"t":"2026-01-01T00:00:00Z","b":"b1","type":"to`)
	f.Close()

	events, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "start" || events[1].Tool.Query != "q" || events[2].Sync.Documents != 3 {
		t.Fatalf("events = %+v", events)
	}
	for _, e := range events {
		if e.Build != "b1" || e.Time.IsZero() {
			t.Errorf("event %+v lacks build or time", e)
		}
	}
}

// A reopened file keeps earlier builds: history survives restarts.
func TestHistorySurvivesRestartsAndKeepsNewestBuildsWhole(t *testing.T) {
	dir := t.TempDir()
	for i, id := range []string{"b1", "b2", "b1", "b3", "b4"} {
		r, err := Open(dir, Build{ID: id}, 3)
		if err != nil {
			t.Fatal(err)
		}
		r.RecordTool(&ToolCall{Name: "t", Results: i})
		r.Close()
	}
	events, err := ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	builds := map[string]int{}
	for _, e := range events {
		builds[e.Build]++
	}
	// b1 was first seen before b2, so it is the oldest and dropped whole.
	if builds["b1"] != 0 || builds["b2"] != 2 || builds["b3"] != 2 || builds["b4"] != 2 {
		t.Errorf("events per build = %v, want b2, b3, b4 kept complete", builds)
	}
}

func TestToolMiddlewareRecordsCallsAndHandlerDetails(t *testing.T) {
	r, _ := openTemp(t, "b1", 10)
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
	call("search_workitem", search)
	call("search_workitem", search)
	call("search_broken", map[string]any{"request": map[string]any{"mode": "pattern_search"}})
	call("sync_source", map[string]any{"request": map[string]any{"mode": "status"}})

	events, _ := r.Load()
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
	if events, err := r.Load(); err != nil || events != nil {
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

func TestDetectBuildFromGitCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("main.go", "package main\n")
	git("add", ".")
	git("commit", "-q", "-m", "Initial version")
	t.Chdir(dir)

	clean := DetectBuild("0.1.0")
	if clean.Dirty || clean.ID != clean.Commit || clean.Subject != "Initial version" || len(clean.Commit) != 10 {
		t.Fatalf("clean build = %+v", clean)
	}
	// Untracked tool folders don't make a new build.
	write(".venv/lib/site.py", "x = 1\n")
	if b := DetectBuild("0.1.0"); b.ID != clean.ID {
		t.Errorf("untracked .venv changed the build: %s", b.ID)
	}
	// A code change does, and the same change gives the same ID again.
	write("main.go", "package main\n\nfunc main() {}\n")
	dirty := DetectBuild("0.1.0")
	if !dirty.Dirty || !strings.HasPrefix(dirty.ID, clean.Commit+"+") {
		t.Fatalf("dirty build = %+v", dirty)
	}
	if again := DetectBuild("0.1.0"); again.ID != dirty.ID {
		t.Errorf("same code, different IDs: %s vs %s", dirty.ID, again.ID)
	}
	write("internal/new.go", "package internal\n")
	if b := DetectBuild("0.1.0"); b.ID == dirty.ID {
		t.Error("a new untracked source file must change the build")
	}
}

func ev(build string, at int, tool *ToolCall, sync *SyncRun) Event {
	return Event{Time: time.Date(2026, 11, 4, 10, at, 0, 0, time.UTC), Build: build, Type: map[bool]string{true: "tool", false: "sync"}[tool != nil], Tool: tool, Sync: sync}
}

func search(tool, query string, page, results int, ms int64, filters map[string][]string) *ToolCall {
	return &ToolCall{Name: tool, Mode: "semantic_search", Query: query, Page: page, Results: results, Counted: true, LatencyMs: ms, Filters: filters}
}

func TestSummarizeComparesBuilds(t *testing.T) {
	events := []Event{
		{Time: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), Build: "old", Type: "start", Start: &Build{ID: "old", Subject: "Before"}},
		ev("old", 1, search("search_workitem", "a", 1, 0, 100, nil), nil),
		ev("old", 2, search("search_workitem", "b", 1, 3, 100, nil), nil),
		ev("old", 3, nil, &SyncRun{SourceID: "gh", SourceName: "App", Status: "completed", DurationMs: 60000, Requests: 3000, RateLimited: 40, Documents: 900}),
		{Time: time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC), Build: "new", Type: "start", Start: &Build{ID: "new", Subject: "Tarball"}},
		ev("new", 1, &ToolCall{Name: "search_workitem", Mode: "semantic_search", Query: "cold", Page: 1, Results: 2, Counted: true, LatencyMs: 9000, Cold: true}, nil),
		ev("new", 2, search("search_workitem", "reqs", 1, 0, 200, map[string][]string{"work_item_type": {"Requirement"}}), nil),
		ev("new", 3, search("search_workitem", "reqs", 2, 1, 300, nil), nil),
		ev("new", 4, &ToolCall{Name: "search_workitem", Mode: "retrieve_chunk", Counted: true, Results: 1}, nil),
		ev("new", 5, &ToolCall{Name: "experience_retrieve", Query: "x", Counted: true, Results: 1, Hits: []string{"Deploy rules"}}, nil),
		ev("new", 6, &ToolCall{Name: "experience_retrieve", Query: "y", Counted: true, Results: 0}, nil),
		ev("new", 7, nil, &SyncRun{SourceID: "gh", SourceName: "App", Status: "failed", DurationMs: 1000}),
		ev("new", 8, nil, &SyncRun{SourceID: "gh", SourceName: "App", Status: "completed", DurationMs: 5000, Requests: 2, Documents: 900}),
	}
	d := Summarize(events, "", "")
	if len(d.Builds) != 2 || d.Selected.Build.ID != "new" || d.Baseline == nil || d.Baseline.Build.ID != "old" {
		t.Fatalf("selected/baseline = %v/%v", d.Selected, d.Baseline)
	}
	s := d.Selected.Search
	// Two first-page searches (the cold one included), one of them empty;
	// one further page.
	if s.FirstPages != 2 || s.Empty != 1 || s.NextPages != 1 || s.RetrieveChunk != 1 {
		t.Errorf("search stats = %+v", s)
	}
	if s.P95Ms == 9000 || s.P50Ms == 0 {
		t.Errorf("latency %d/%d must leave out the cold call", s.P50Ms, s.P95Ms)
	}
	// The latest sync of the source counts, not the failed attempt before it.
	if d.Selected.Sync.Sources != 1 || d.Selected.Sync.Failed != 0 || d.Selected.Sync.Requests != 2 {
		t.Errorf("sync totals = %+v", d.Selected.Sync)
	}
	if len(d.Sources) != 1 || d.Sources[0].Runs != 2 || d.Sources[0].Baseline.Requests != 3000 {
		t.Errorf("sources = %+v", d.Sources)
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
	if d.Selected.Build.Subject != "Tarball" {
		t.Errorf("build info from the start event = %+v", d.Selected.Build)
	}

	// A just-started build with no activity isn't selected by default.
	idle := append(events, Event{Time: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC), Build: "fresh", Type: "start", Start: &Build{ID: "fresh"}})
	if d := Summarize(idle, "", ""); d.Selected.Build.ID != "new" || len(d.Builds) != 3 {
		t.Errorf("default selection = %s, want the newest build with activity", d.Selected.Build.ID)
	}

	// Explicit choices.
	if d := Summarize(events, "old", "none"); d.Selected.Build.ID != "old" || d.Baseline != nil {
		t.Errorf("explicit build without comparison: %v %v", d.Selected.Build.ID, d.Baseline)
	}
	if d := Summarize(nil, "", ""); d.Selected != nil || len(d.Builds) != 0 {
		t.Error("no events, no dashboard")
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
