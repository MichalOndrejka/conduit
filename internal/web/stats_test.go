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

func TestStatsPageEmptyHistory(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	// Open writes a start event, so remove nothing: a build with no activity.
	rec, err := stats.Open(dir, stats.Build{ID: "b1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	h.server.SetStats(rec)
	body := bodyString(t, h.get("/stats"))
	if !strings.Contains(body, "No syncs in this build") || !strings.Contains(body, "No tool calls in this build") {
		t.Errorf("page for an idle build:\n%s", body)
	}
}

// Two builds' history: the page compares the newest with the one before.
func TestStatsPageComparesBuilds(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()

	old, err := stats.Open(dir, stats.Build{ID: "aaa", Commit: "aaa", Subject: "Per-file GitHub fetch"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	old.RecordSync(&stats.SyncRun{SourceID: "gh", SourceName: "Field app code", SourceType: "code", Provider: "github",
		Status: "completed", DurationMs: 120000, Requests: 3002, RateLimited: 57, Documents: 900, Chunks: 4100})
	old.RecordTool(&stats.ToolCall{Name: "search_source_code", Mode: "semantic_search", Query: "offline queue", Page: 1, Results: 5, Counted: true, LatencyMs: 300})
	old.Close()

	cur, err := stats.Open(dir, stats.Build{ID: "bbb", Commit: "bbb", Subject: "Tarball fetch"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()
	cur.RecordSync(&stats.SyncRun{SourceID: "gh", SourceName: "Field app code", SourceType: "code", Provider: "github",
		Status: "completed", DurationMs: 20000, Requests: 2, Documents: 900, Chunks: 4100})
	cur.RecordTool(&stats.ToolCall{Name: "search_source_code", Mode: "semantic_search", Query: "missing thing <b>", Page: 1, Results: 0, Counted: true,
		LatencyMs: 250, Filters: map[string][]string{"path": {"src/nowhere"}}})
	h.server.SetStats(cur)

	body := bodyString(t, h.get("/stats"))
	for _, want := range []string{
		"Tarball fetch", "Per-file GitHub fetch", // selected and compared builds
		"Field app code", "3002", // the source and its old request count
		"search_source_code", "missing thing &lt;b&gt;", "src/nowhere", // empty search, escaped
		"−100%", // API requests dropped by ~100%
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stats page lacks %q", want)
		}
	}

	// Selecting the old build without a comparison.
	body = bodyString(t, h.get("/stats?build=aaa&compare=none"))
	if !strings.Contains(body, "3002") || !strings.Contains(body, "2 min 00 s") {
		t.Errorf("old build page:\n%s", body)
	}
	if strings.Contains(body, "was ") {
		t.Error("no comparison was asked for, so no 'was' values")
	}
}

func TestCompareTones(t *testing.T) {
	if c := compare(5, 10, true, fmtCount, false, lowerIsBetter); c.Change != "−50%" || c.Tone != "good" || c.Was != "10" {
		t.Errorf("fewer requests = %+v", c)
	}
	if c := compare(0.30, 0.10, true, fmtPct, true, lowerIsBetter); c.Change != "+20 pts" || c.Tone != "bad" {
		t.Errorf("more empty searches = %+v", c)
	}
	if c := compare(800, 900, true, fmtCount, false, higherIsBetter); c.Tone != "bad" {
		t.Errorf("fewer documents = %+v, want flagged", c)
	}
	if c := compare(3, 0, false, fmtCount, false, lowerIsBetter); c.Change != "" || c.Was != "" {
		t.Errorf("no baseline = %+v", c)
	}
	if c := compare(100, 101, true, fmtCount, false, lowerIsBetter); c.Change != "" {
		t.Errorf("1%% noise = %+v, want no change shown", c)
	}
}
