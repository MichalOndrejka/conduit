package web

import (
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/stats"
)

func TestStatsPageWithoutRecorder(t *testing.T) {
	h := newHarness(t)
	body := bodyString(t, h.get("/stats"))
	if !strings.Contains(body, "Stats recording is off") {
		t.Errorf("page without a recorder:\n%s", body)
	}
}

func TestStatsPageNothingRecordedYet(t *testing.T) {
	h := newHarness(t)
	h.server.SetStats(stats.New())
	body := bodyString(t, h.get("/stats"))
	// The whole dashboard shows, just empty.
	for _, want := range []string{"Searches", "Latency p95", "Search Quality", "No tool calls yet", "No syncs yet", "Recalls"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty dashboard lacks %q", want)
		}
	}
	if strings.Contains(body, "No stats yet") {
		t.Error("the empty dashboard replaces the placeholder")
	}
}

func TestStatsPageShowsThisProcess(t *testing.T) {
	h := newHarness(t)
	rec := stats.New()
	rec.RecordSync(&stats.SyncRun{SourceID: "gh", SourceName: "Field app code", SourceType: "code", Provider: "github",
		Status: "completed", DurationMs: 120000, FetchMs: 90000, IndexMs: 30000, Requests: 3002, RateLimited: 57, Documents: 900, Chunks: 4100})
	rec.RecordTool(&stats.ToolCall{Name: "search_source_code", Mode: "semantic_search", Query: "missing thing <b>", Page: 1, Results: 0, Counted: true,
		LatencyMs: 250, Filters: map[string][]string{"path": {"src/nowhere"}}})
	h.server.SetStats(rec)

	body := bodyString(t, h.get("/stats"))
	for _, want := range []string{
		"Field app code", "3002", "2 min 00 s", "57 rate-limited", // the sync
		"API requests by provider", "GitHub", "3002 · 57 rate-limited", // provider load
		"search_source_code", "missing thing &lt;b&gt;", "src/nowhere", // empty search, escaped
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stats page lacks %q", want)
		}
	}
	for _, gone := range []string{"Compared with", "Builds", "was "} {
		if strings.Contains(body, gone) {
			t.Errorf("stats page still shows %q", gone)
		}
	}
}
