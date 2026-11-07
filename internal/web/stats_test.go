package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/stats"
)

func TestStatsPageWithoutRecorder(t *testing.T) {
	h := newHarness(t)
	body := bodyString(t, h.get("/"))
	if !strings.Contains(body, "Stats recording is off") {
		t.Errorf("page without a recorder:\n%s", body)
	}
}

func TestStatsPageNothingRecordedYet(t *testing.T) {
	h := newHarness(t)
	h.server.SetStats(stats.New())
	h.server.SetToolNames([]string{"search_source_code", "sync_source"})
	body := bodyString(t, h.get("/"))
	// The whole dashboard shows, just empty; the tool charts list every
	// registered tool at zero.
	for _, want := range []string{"Tool Calls", "Hits", "Misses", "Latency", "Syncs", "API Requests", ">Sources<", ">Experience<",
		"Calls by tool", "Empty results by tool", "search_source_code", "sync_source", "No syncs yet", "Entries Remembered", "Memories Recalled", "Recall Miss"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty dashboard lacks %q", want)
		}
	}
	for _, gone := range []string{"No stats yet", "No tool calls yet", "No searches yet", "Filter values"} {
		if strings.Contains(body, gone) {
			t.Errorf("empty dashboard still shows %q", gone)
		}
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

	body := bodyString(t, h.get("/"))
	for _, want := range []string{
		"Field app code", "3002", "2 min 00 s", "57 rate-limited", // the sync
		"100% of searches",                                             // the miss
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

// The dashboard is the home page: old /stats links land on it, and the
// sidebar no longer lists it.
func TestDashboardIsHome(t *testing.T) {
	h := newHarness(t)
	h.server.SetStats(stats.New())
	resp := h.get("/stats")
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/" {
		t.Errorf("GET /stats = %d → %q, want 301 → /", resp.StatusCode, resp.Header.Get("Location"))
	}
	body := bodyString(t, h.get("/"))
	if !strings.Contains(body, "<h1 class=\"text-2xl font-semibold text-white\">Dashboard</h1>") || !strings.Contains(body, `href="/sources"`) {
		t.Error("home page is not the dashboard with a Sources link")
	}
	if strings.Contains(body, `href="/stats"`) {
		t.Error("sidebar still links to /stats")
	}
}
