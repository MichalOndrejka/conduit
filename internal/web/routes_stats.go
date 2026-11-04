package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// SetStats gives the server the stats history to show on the Stats page.
func (s *Server) SetStats(r *stats.Recorder) { s.stats = r }

// cell is one value on the Stats page with its change against the baseline
// build: Change is e.g. "−42%", Was the baseline value, Tone "good" or "bad"
// when the change is an improvement or a regression.
type cell struct {
	Value, Was, Change, Tone string
}

// direction says which way a metric improves.
type direction int

const (
	lowerIsBetter direction = iota
	higherIsBetter
	neutral
)

// compare builds a cell for value against base (have: the baseline has the
// metric). Rates compare in percentage points, everything else relatively.
func compare(value, base float64, have bool, format func(float64) string, isRate bool, dir direction) cell {
	c := cell{Value: format(value)}
	if !have {
		return c
	}
	c.Was = format(base)
	var change float64
	switch {
	case isRate:
		change = (value - base) * 100
		if math.Abs(change) < 0.5 {
			return c
		}
		c.Change = fmt.Sprintf("%+.0f pts", change)
	case base == 0 && value == 0:
		return c
	case base == 0:
		change = 1
		c.Change = "new"
	default:
		change = (value - base) / base
		if math.Abs(change) < 0.02 {
			return c
		}
		c.Change = fmt.Sprintf("%+.0f%%", change*100)
	}
	c.Change = strings.Replace(c.Change, "-", "−", 1)
	switch {
	case dir == neutral:
	case (change < 0) == (dir == lowerIsBetter):
		c.Tone = "good"
	default:
		c.Tone = "bad"
	}
	return c
}

func fmtPct(v float64) string   { return fmt.Sprintf("%.0f%%", v*100) }
func fmtCount(v float64) string { return fmt.Sprintf("%.0f", v) }
func fmtMs(v float64) string    { return fmtDuration(time.Duration(v) * time.Millisecond) }
func fmtPerQ(v float64) string  { return fmt.Sprintf("%.2f", v) }

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%d min %02d s", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
	}
}

// statsTile is one headline number: value, change against the compared
// build, and its trend across builds.
type statsTile struct {
	Label, Tip string
	Cell       cell
	Arrow      string
	Spark      template.HTML
	Alert      string // a status note, e.g. rate-limit hits
}

type statsSourceRow struct {
	Name, Type, Provider    string
	Runs                    int
	Status, StatusTone      string
	Error                   string
	Duration, Fetch, Index  cell
	Documents, Chunks       cell
	Requests, RateLimited   cell
	FetchPct, IndexPct      int // phase split of the selected sync, for the thin bar
	OnlyInBaseline, NewHere bool
}

type statsFilter struct {
	Tool, Field, Value string
	Uses, Empty        int
	EmptyPct           int
	EmptyRate          string
}

type statsEmptySearch struct {
	When, Tool, Mode, Query, Filters string
}

// statsBuildRow is one build in the Builds table: the exact numbers behind
// every trend chart.
type statsBuildRow struct {
	ID, Label, When                      string
	Selected, Baseline                   bool
	Starts, Searches                     int
	EmptyRate, NextPage, ChunkWalks, P95 string
	Sources, Failed                      int
	SyncDuration, Fetch, Index           string
	Requests, RateLimited                int
}

// durationAxis picks the unit for an axis of durations: minutes, seconds or
// milliseconds by the largest value.
func durationAxis(maxMs float64) (div float64, unit string) {
	switch {
	case maxMs >= 120_000:
		return 60_000, "min"
	case maxMs >= 2_000:
		return 1_000, "s"
	}
	return 1, "ms"
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Active": "stats", "Enabled": s.stats != nil}
	if s.stats == nil {
		s.render(w, s.statsTmpl, "base", data)
		return
	}
	events, err := s.stats.Load()
	if err != nil {
		httpError(w, err)
		return
	}
	compareTo := r.URL.Query().Get("compare")
	d := stats.Summarize(events, r.URL.Query().Get("build"), compareTo)
	data["Path"] = s.stats.Path()
	data["Current"] = s.stats.Build()
	data["CompareNone"] = compareTo == "none"
	if d.Selected == nil {
		s.render(w, s.statsTmpl, "base", data)
		return
	}
	sel, base := d.Selected, d.Baseline
	have := base != nil
	var b stats.BuildSummary
	if have {
		b = *base
	}
	data["Selected"] = sel
	data["Baseline"] = base
	data["Builds"] = d.Builds

	// Builds oldest → newest: the x axis of every trend. Builds that did
	// nothing (e.g. one just started) carry no information and stay out of
	// the trends, except the selected one; the Builds table lists them all.
	var builds []*stats.BuildSummary
	for _, bs := range slices.Backward(d.Builds) {
		if bs == sel || bs.ToolCalls > 0 || bs.Sync.Sources > 0 {
			builds = append(builds, bs)
		}
	}
	n := len(builds)
	selIdx, baseIdx := slices.Index(builds, sel), -1
	if base != nil {
		baseIdx = slices.Index(builds, base)
	}
	sameDay := builds[0].FirstSeen.Local().Format("2006-01-02") == builds[n-1].FirstSeen.Local().Format("2006-01-02")
	xLabels, xTitles := make([]string, n), make([]string, n)
	for i, bs := range builds {
		if sameDay {
			xLabels[i] = bs.FirstSeen.Local().Format("15:04")
		} else {
			xLabels[i] = bs.FirstSeen.Local().Format("01-02")
		}
		xTitles[i] = bs.FirstSeen.Local().Format("Jan 2 15:04") + " · " + bs.Build.Label()
	}
	// metric builds a series across builds; has decides whether a build has
	// the measure at all (no searches → no empty rate).
	metric := func(name, color string, value func(*stats.BuildSummary) float64, has func(*stats.BuildSummary) bool, display func(float64) string, div float64) series {
		sr := series{Name: name, Color: color, Values: make([]float64, n), Display: make([]string, n), Has: make([]bool, n)}
		for i, bs := range builds {
			if has(bs) {
				v := value(bs)
				sr.Values[i], sr.Display[i], sr.Has[i] = v/div, display(v), true
			}
		}
		return sr
	}
	searched := func(bs *stats.BuildSummary) bool { return bs.Search.FirstPages > 0 }
	timed := func(bs *stats.BuildSummary) bool { return bs.Search.P95Ms > 0 }
	synced := func(bs *stats.BuildSummary) bool { return bs.Sync.Sources > 0 }
	active := func(bs *stats.BuildSummary) bool { return bs.ToolCalls > 0 }
	trend := func(label string, sr []series, axis func(float64) string, unit string, w int, columns bool) template.HTML {
		return trendChart(trendSpec{Label: label, XLabels: xLabels, XTitles: xTitles, Series: sr, Selected: selIdx, Baseline: baseIdx,
			AxisFmt: axis, AxisUnit: unit, W: w, H: 170, Columns: columns, Stacked: columns && len(sr) > 1})
	}
	pctAxis := func(v float64) string { return fmt.Sprintf("%.0f%%", v) }

	emptyS := metric("Empty results", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return bs.Search.EmptyRate() * 100 }, searched,
		func(v float64) string { return fmt.Sprintf("%.0f%%", v) }, 1)
	nextS := metric("Further pages per search", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return bs.Search.NextPageRate() }, searched, fmtPerQ, 1)
	walkS := metric("Chunk walks per search", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return bs.Search.RetrieveChunkRate() }, searched, fmtPerQ, 1)
	var maxP95 float64
	for _, bs := range builds {
		maxP95 = math.Max(maxP95, float64(bs.Search.P95Ms))
	}
	latDiv, latUnit := durationAxis(maxP95)
	p95S := metric("Search latency p95", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return float64(bs.Search.P95Ms) }, timed, fmtMs, latDiv)
	searchesS := metric("Searches", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return float64(bs.Search.FirstPages) }, active, fmtCount, 1)

	var maxSync float64
	anyPre := false
	for _, bs := range builds {
		maxSync = math.Max(maxSync, float64(bs.Sync.DurationMs))
		anyPre = anyPre || bs.Sync.PreprocessMs > 0
	}
	syncDiv, syncUnit := durationAxis(maxSync)
	syncTotalS := metric("Sync time", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.DurationMs) }, synced, fmtMs, syncDiv)
	phases := []series{
		metric("Fetch", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.FetchMs) }, synced, fmtMs, syncDiv),
	}
	if anyPre {
		phases = append(phases, metric("Preprocess", "var(--viz-s2)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.PreprocessMs) }, synced, fmtMs, syncDiv))
	}
	phases = append(phases, metric("Embed", "var(--viz-s3)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.IndexMs) }, synced, fmtMs, syncDiv))
	requestsS := metric("API requests", "var(--viz-s1)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.Requests) }, synced, fmtCount, 1)
	rateS := metric("Rate-limit hits", "var(--viz-critical)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.RateLimited) }, synced, fmtCount, 1)
	failedS := metric("Failed syncs", "var(--viz-critical)", func(bs *stats.BuildSummary) float64 { return float64(bs.Sync.Failed) }, synced, fmtCount, 1)

	// Headline numbers.
	tile := func(label, tip string, c cell, sr series) statsTile {
		return statsTile{Label: label, Tip: tip, Cell: c, Arrow: arrow(c), Spark: sparkline(sr, selIdx, baseIdx)}
	}
	tiles := []statsTile{
		tile("Searches", "Questions the agent asked: first-page semantic and pattern searches in this build.",
			compare(float64(sel.Search.FirstPages), float64(b.Search.FirstPages), have, fmtCount, false, neutral), searchesS),
		tile("Empty results", "Share of searches that found nothing: a missing or unsynced source, or a filter value that doesn't exist. Lower is better.",
			compare(sel.Search.EmptyRate(), b.Search.EmptyRate(), have && b.Search.FirstPages > 0, fmtPct, true, lowerIsBetter), emptyS),
		tile("Further pages", "Further pages per search: how often the agent asked for page 2 or later because the answer wasn't on the first page. Lower is better.",
			compare(sel.Search.NextPageRate(), b.Search.NextPageRate(), have && b.Search.FirstPages > 0, fmtPerQ, false, lowerIsBetter), nextS),
		tile("Latency p95", "95th percentile of semantic search time, embedding included; each process's cold first search left out.",
			compare(float64(sel.Search.P95Ms), float64(b.Search.P95Ms), have && b.Search.P95Ms > 0, fmtMs, false, lowerIsBetter), p95S),
		tile("Sync time", "Sum of each source's latest sync in this build.",
			compare(float64(sel.Sync.DurationMs), float64(b.Sync.DurationMs), have && b.Sync.Sources > 0, fmtMs, false, lowerIsBetter), syncTotalS),
		tile("API requests", "HTTP requests to source APIs across each source's latest sync.",
			compare(float64(sel.Sync.Requests), float64(b.Sync.Requests), have && b.Sync.Sources > 0, fmtCount, false, lowerIsBetter), requestsS),
	}
	if sel.Search.FirstPages == 0 {
		for i := 1; i <= 3; i++ {
			tiles[i].Cell, tiles[i].Arrow = cell{Value: "–"}, ""
		}
	}
	if sel.Sync.Sources == 0 {
		tiles[4].Cell, tiles[4].Arrow = cell{Value: "–"}, ""
		tiles[5].Cell, tiles[5].Arrow = cell{Value: "–"}, ""
	}
	if sel.Sync.RateLimited > 0 {
		tiles[5].Alert = fmt.Sprintf("%d rate-limited", sel.Sync.RateLimited)
	}
	data["Tiles"] = tiles

	// Search quality tab: small multiples across builds, then per tool.
	data["ChartEmpty"] = trend("Empty results per build", []series{emptyS}, pctAxis, "", 500, false)
	data["ChartNext"] = trend("Further pages per search per build", []series{nextS}, compactNum, "", 500, false)
	data["ChartWalks"] = trend("Chunk walks per search per build", []series{walkS}, compactNum, "", 500, false)
	data["ChartP95"] = trend("Search latency p95 per build", []series{p95S}, compactNum, latUnit, 500, false)

	const baseName, curName = "compared build", "selected build"
	var emptyRows []dumbbellRow
	var callBars []rankedBar
	for _, t := range d.Tools {
		callBars = append(callBars, rankedBar{Label: t.Name, Value: float64(t.Calls), Text: fmtCount(float64(t.Calls))})
		if t.Search.FirstPages == 0 {
			continue
		}
		row := dumbbellRow{Label: t.Name, Cur: t.Search.EmptyRate() * 100, HasCur: true, CurText: fmtPct(t.Search.EmptyRate())}
		if t.Baseline != nil && t.Baseline.Search.FirstPages > 0 {
			row.Base, row.HasBase, row.BaseText = t.Baseline.Search.EmptyRate()*100, true, fmtPct(t.Baseline.Search.EmptyRate())
			row.Change = compare(t.Search.EmptyRate(), t.Baseline.Search.EmptyRate(), true, fmtPct, true, lowerIsBetter)
		}
		emptyRows = append(emptyRows, row)
	}
	sort.SliceStable(emptyRows, func(i, j int) bool { return emptyRows[i].Cur > emptyRows[j].Cur })
	data["ChartToolEmpty"] = dumbbell(emptyRows, baseName, curName, pctAxis, "", 520)
	data["ChartToolCalls"] = rankedBars(callBars, 520)

	var filters []statsFilter
	for _, f := range d.Filters {
		rate := float64(f.Empty) / float64(f.Uses)
		filters = append(filters, statsFilter{Tool: f.Tool, Field: f.Field, Value: f.Value, Uses: f.Uses, Empty: f.Empty,
			EmptyPct: int(rate * 100), EmptyRate: fmtPct(rate)})
	}
	data["Filters"] = filters
	var empties []statsEmptySearch
	for _, e := range d.EmptySearches {
		var parts []string
		keys := make([]string, 0, len(e.Filters))
		for k := range e.Filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+" = "+strings.Join(e.Filters[k], " | "))
		}
		empties = append(empties, statsEmptySearch{When: e.Time.Local().Format("Jan 2 15:04"), Tool: e.Tool, Mode: e.Mode, Query: e.Query, Filters: strings.Join(parts, ", ")})
	}
	data["EmptySearches"] = empties

	// Syncs tab.
	totals := make([]string, n)
	for i, bs := range builds {
		if bs.Sync.Sources > 0 {
			totals[i] = fmtMs(float64(bs.Sync.DurationMs))
		}
	}
	data["ChartSyncTime"] = trendChart(trendSpec{Label: "Sync time per build by phase", XLabels: xLabels, XTitles: xTitles, Series: phases,
		Selected: selIdx, Baseline: baseIdx, AxisFmt: compactNum, AxisUnit: syncUnit, W: 1100, H: 220, Columns: true, Stacked: true, Totals: totals})
	data["ChartRequests"] = trend("API requests per build", []series{requestsS}, compactNum, "", 330, true)
	data["ChartRateLimits"] = trend("Rate-limit hits per build", []series{rateS}, compactNum, "", 330, true)
	data["ChartFailed"] = trend("Failed syncs per build", []series{failedS}, compactNum, "", 330, true)

	var maxSrc float64
	for _, src := range d.Sources {
		if src.Current != nil {
			maxSrc = math.Max(maxSrc, float64(src.Current.DurationMs))
		}
		if src.Baseline != nil {
			maxSrc = math.Max(maxSrc, float64(src.Baseline.DurationMs))
		}
	}
	srcDiv, srcUnit := durationAxis(maxSrc)
	var sources []statsSourceRow
	var sourceRows []dumbbellRow
	for _, src := range d.Sources {
		row := statsSourceRow{Name: src.SourceName, Type: src.SourceType, Provider: src.Provider, Runs: src.Runs}
		cur, prev := src.Current, src.Baseline
		haveS := prev != nil
		var p stats.SyncRun
		if haveS {
			p = *prev
		}
		dr := dumbbellRow{Label: src.SourceName}
		if haveS {
			dr.Base, dr.HasBase, dr.BaseText = float64(p.DurationMs)/srcDiv, true, fmtMs(float64(p.DurationMs))
		}
		if cur != nil {
			dr.Cur, dr.HasCur, dr.CurText = float64(cur.DurationMs)/srcDiv, true, fmtMs(float64(cur.DurationMs))
			if haveS {
				dr.Change = compare(float64(cur.DurationMs), float64(p.DurationMs), true, fmtMs, false, lowerIsBetter)
			}
		}
		sourceRows = append(sourceRows, dr)
		switch {
		case cur == nil:
			row.OnlyInBaseline = true
			row.Status = "not synced"
			cur = &stats.SyncRun{}
		case !haveS && base != nil:
			row.NewHere = true
		}
		if cur.Status != "" {
			row.Status = cur.Status
		}
		row.StatusTone = map[string]string{"completed": "good", "failed": "bad", "cancelled": "warn"}[cur.Status]
		if cur.Error != "" {
			row.Error = cur.ErrorPhase + ": " + rag.Truncate(cur.Error, 300)
		}
		row.Duration = compare(float64(cur.DurationMs), float64(p.DurationMs), haveS, fmtMs, false, lowerIsBetter)
		row.Fetch = compare(float64(cur.FetchMs), float64(p.FetchMs), haveS, fmtMs, false, lowerIsBetter)
		row.Index = compare(float64(cur.IndexMs), float64(p.IndexMs), haveS, fmtMs, false, lowerIsBetter)
		// Fewer documents or chunks isn't better or worse by itself, but a
		// sudden drop usually means a broken token or filter: flag drops.
		row.Documents = compare(float64(cur.Documents), float64(p.Documents), haveS, fmtCount, false, higherIsBetter)
		row.Chunks = compare(float64(cur.Chunks), float64(p.Chunks), haveS, fmtCount, false, higherIsBetter)
		row.Requests = compare(float64(cur.Requests), float64(p.Requests), haveS, fmtCount, false, lowerIsBetter)
		row.RateLimited = compare(float64(cur.RateLimited), float64(p.RateLimited), haveS, fmtCount, false, lowerIsBetter)
		if total := cur.FetchMs + cur.PreprocessMs + cur.IndexMs; total > 0 {
			row.FetchPct = int(float64(cur.FetchMs) / float64(total) * 100)
			row.IndexPct = 100 - row.FetchPct
		}
		sources = append(sources, row)
	}
	sort.SliceStable(sourceRows, func(i, j int) bool { return sourceRows[i].Cur > sourceRows[j].Cur })
	data["Sources"] = sources
	data["ChartSourceTime"] = dumbbell(sourceRows, baseName, curName, compactNum, srcUnit, 1100)

	// Experience tab.
	data["Experience"] = d.Experience
	var recall []rankedBar
	for _, h := range d.Experience.Top {
		recall = append(recall, rankedBar{Label: h.Situation, Value: float64(h.Count), Text: fmtCount(float64(h.Count))})
	}
	data["ChartRecalled"] = rankedBars(recall, 520)
	data["NeverRetrieved"] = s.neverRetrieved(r.Context(), d.RetrievedSituations)

	// Builds tab: the exact numbers behind every chart.
	var rows []statsBuildRow
	for _, bs := range d.Builds {
		row := statsBuildRow{ID: bs.Build.ID, Label: bs.Build.Label(), When: bs.FirstSeen.Local().Format("Jan 2 15:04"),
			Selected: bs == sel, Baseline: bs == base, Starts: bs.Starts, Searches: bs.Search.FirstPages,
			EmptyRate: "–", NextPage: "–", ChunkWalks: "–", P95: "–", Sources: bs.Sync.Sources, Failed: bs.Sync.Failed,
			SyncDuration: "–", Fetch: "–", Index: "–", Requests: bs.Sync.Requests, RateLimited: bs.Sync.RateLimited}
		if bs.Search.FirstPages > 0 {
			row.EmptyRate, row.NextPage, row.ChunkWalks = fmtPct(bs.Search.EmptyRate()), fmtPerQ(bs.Search.NextPageRate()), fmtPerQ(bs.Search.RetrieveChunkRate())
		}
		if bs.Search.P95Ms > 0 {
			row.P95 = fmtMs(float64(bs.Search.P95Ms))
		}
		if bs.Sync.Sources > 0 {
			row.SyncDuration, row.Fetch, row.Index = fmtMs(float64(bs.Sync.DurationMs)), fmtMs(float64(bs.Sync.FetchMs)), fmtMs(float64(bs.Sync.IndexMs))
		}
		rows = append(rows, row)
	}
	data["BuildRows"] = rows

	s.render(w, s.statsTmpl, "base", data)
}

// neverRetrieved lists experience entries no kept build's
// experience_retrieve returned (at most 20), or nil when the experience
// store can't be read.
func (s *Server) neverRetrieved(ctx context.Context, retrieved map[string]bool) []string {
	if s.memory == nil {
		return nil
	}
	var out []string
	var offset json.RawMessage
	for page := 0; page < 20; page++ {
		entries, next, err := s.memory.GetAllPaginated(ctx, 200, offset)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			if !retrieved[rag.Truncate(e.Situation, 200)] {
				out = append(out, e.Situation)
			}
		}
		if len(next) == 0 {
			break
		}
		offset = next
	}
	sort.Strings(out)
	if len(out) > 20 {
		out = append(out[:20], fmt.Sprintf("… and %d more", len(out)-20))
	}
	return out
}
