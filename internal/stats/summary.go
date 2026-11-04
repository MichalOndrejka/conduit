package stats

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// SearchStats summarize the search tools' calls.
type SearchStats struct {
	// FirstPages are searches (semantic or pattern) for page 1: one per
	// question asked.
	FirstPages int `json:"first_pages"`
	// Empty are first-page searches that found nothing.
	Empty int `json:"empty"`
	// NextPages are searches for page 2 or later: the answer wasn't on the
	// first page.
	NextPages int `json:"next_pages"`
	// RetrieveChunk are retrieve_chunk calls: a result was cut off by
	// chunking.
	RetrieveChunk int `json:"retrieve_chunk"`
	// P50Ms / P95Ms are semantic search latencies, the cold first call of
	// each process left out.
	P50Ms int64 `json:"p50_ms"`
	P95Ms int64 `json:"p95_ms"`

	latencies []int64
}

// EmptyRate is Empty / FirstPages.
func (s SearchStats) EmptyRate() float64 { return ratio(s.Empty, s.FirstPages) }

// NextPageRate is NextPages / FirstPages: further pages per question.
func (s SearchStats) NextPageRate() float64 { return ratio(s.NextPages, s.FirstPages) }

// RetrieveChunkRate is RetrieveChunk / FirstPages.
func (s SearchStats) RetrieveChunkRate() float64 { return ratio(s.RetrieveChunk, s.FirstPages) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (s *SearchStats) add(tc *ToolCall) {
	switch tc.Mode {
	case "semantic_search", "pattern_search":
		if tc.Page > 1 {
			s.NextPages++
		} else {
			s.FirstPages++
			if tc.Error == "" && tc.Results == 0 {
				s.Empty++
			}
		}
		if tc.Mode == "semantic_search" && !tc.Cold && tc.Error == "" {
			s.latencies = append(s.latencies, tc.LatencyMs)
		}
	case "retrieve_chunk":
		s.RetrieveChunk++
	}
}

func (s *SearchStats) finish() {
	if len(s.latencies) == 0 {
		return
	}
	slices.Sort(s.latencies)
	pick := func(p float64) int64 { return s.latencies[min(len(s.latencies)-1, int(p*float64(len(s.latencies))))] }
	s.P50Ms, s.P95Ms = pick(0.50), pick(0.95)
}

// isSearch reports whether tc is a knowledge search tool call.
func isSearch(tc *ToolCall) bool {
	return tc.Counted && strings.HasPrefix(tc.Name, "search_") &&
		(tc.Mode == "semantic_search" || tc.Mode == "pattern_search" || tc.Mode == "retrieve_chunk")
}

// SyncTotals sum each source's latest sync of a build.
type SyncTotals struct {
	Sources    int   `json:"sources"`
	Failed     int   `json:"failed"`
	DurationMs int64 `json:"duration_ms"`
	// FetchMs, PreprocessMs and IndexMs split DurationMs by phase.
	FetchMs      int64 `json:"fetch_ms"`
	PreprocessMs int64 `json:"preprocess_ms"`
	IndexMs      int64 `json:"index_ms"`
	Documents    int   `json:"documents"`
	Chunks       int   `json:"chunks"`
	Requests     int   `json:"requests"`
	RateLimited  int   `json:"rate_limited"`
}

// BuildSummary is one build's history.
type BuildSummary struct {
	Build      Build       `json:"build"`
	FirstSeen  time.Time   `json:"first_seen"`
	LastSeen   time.Time   `json:"last_seen"`
	Starts     int         `json:"starts"`
	ToolCalls  int         `json:"tool_calls"`
	ToolErrors int         `json:"tool_errors"`
	Search     SearchStats `json:"search"`
	Sync       SyncTotals  `json:"sync"`

	latestSync map[string]*SyncRun // source ID → latest run
	syncRuns   map[string]int
	tools      map[string]*ToolRow
	events     []Event
}

// ToolRow is one tool's calls in a build.
type ToolRow struct {
	Name   string      `json:"name"`
	Calls  int         `json:"calls"`
	Errors int         `json:"errors"`
	Search SearchStats `json:"search"`
	// Baseline is the same tool in the baseline build (nil if unused there).
	Baseline *ToolRow `json:"baseline,omitempty"`
}

// SourceRow compares a source's latest sync in the selected and baseline
// builds.
type SourceRow struct {
	SourceID   string   `json:"source_id"`
	SourceName string   `json:"source_name"`
	SourceType string   `json:"source_type"`
	Provider   string   `json:"provider,omitempty"`
	Runs       int      `json:"runs"` // syncs of the source in the selected build
	Current    *SyncRun `json:"current,omitempty"`
	Baseline   *SyncRun `json:"baseline,omitempty"`
}

// EmptySearch is a first-page search that found nothing.
type EmptySearch struct {
	Time    time.Time           `json:"time"`
	Tool    string              `json:"tool"`
	Mode    string              `json:"mode"`
	Query   string              `json:"query"`
	Filters map[string][]string `json:"filters,omitempty"`
}

// FilterRow is one filter value's use in first-page searches.
type FilterRow struct {
	Tool  string `json:"tool"`
	Field string `json:"field"`
	Value string `json:"value"`
	Uses  int    `json:"uses"`
	Empty int    `json:"empty"`
}

// ExperienceStats summarize the experience tools in a build.
type ExperienceStats struct {
	Retrieves      int        `json:"retrieves"`
	EmptyRetrieves int        `json:"empty_retrieves"`
	Remembers      int        `json:"remembers"`
	Top            []HitCount `json:"top"`
}

// HitCount is how often an experience entry was returned.
type HitCount struct {
	Situation string `json:"situation"`
	Count     int    `json:"count"`
}

// Dashboard is everything the Stats page shows.
type Dashboard struct {
	// Builds are every kept build, newest first.
	Builds        []*BuildSummary `json:"builds"`
	Selected      *BuildSummary   `json:"selected,omitempty"`
	Baseline      *BuildSummary   `json:"baseline,omitempty"`
	Tools         []*ToolRow      `json:"tools"`
	Sources       []SourceRow     `json:"sources"`
	EmptySearches []EmptySearch   `json:"empty_searches"`
	Filters       []FilterRow     `json:"filters"`
	Experience    ExperienceStats `json:"experience"`
	// RetrievedSituations are the experience situations returned in any kept
	// build, for spotting entries that are never used.
	RetrievedSituations map[string]bool `json:"-"`
}

// maxEmptySearches caps the empty-search list.
const maxEmptySearches = 100

// Summarize builds the dashboard for the selected build (default: newest)
// against baseline (default: the next older build with activity).
func Summarize(events []Event, selected, baseline string) *Dashboard {
	byID := map[string]*BuildSummary{}
	var order []*BuildSummary
	d := &Dashboard{RetrievedSituations: map[string]bool{}}
	for _, e := range events {
		b := byID[e.Build]
		if b == nil {
			b = &BuildSummary{Build: Build{ID: e.Build}, FirstSeen: e.Time,
				latestSync: map[string]*SyncRun{}, syncRuns: map[string]int{}, tools: map[string]*ToolRow{}}
			byID[e.Build] = b
			order = append(order, b)
		}
		if e.Time.After(b.LastSeen) {
			b.LastSeen = e.Time
		}
		b.events = append(b.events, e)
		switch {
		case e.Start != nil:
			b.Starts++
			b.Build = *e.Start
		case e.Tool != nil:
			tc := e.Tool
			b.ToolCalls++
			row := b.tools[tc.Name]
			if row == nil {
				row = &ToolRow{Name: tc.Name}
				b.tools[tc.Name] = row
			}
			row.Calls++
			if tc.Error != "" {
				b.ToolErrors++
				row.Errors++
			}
			if isSearch(tc) {
				b.Search.add(tc)
				row.Search.add(tc)
			}
			for _, h := range tc.Hits {
				d.RetrievedSituations[h] = true
			}
		case e.Sync != nil:
			b.syncRuns[e.Sync.SourceID]++
			b.latestSync[e.Sync.SourceID] = e.Sync
		}
	}
	for _, b := range order {
		b.Search.finish()
		for _, row := range b.tools {
			row.Search.finish()
		}
		for _, s := range b.latestSync {
			b.Sync.Sources++
			if s.Status == "failed" {
				b.Sync.Failed++
			}
			b.Sync.DurationMs += s.DurationMs
			b.Sync.FetchMs += s.FetchMs
			b.Sync.PreprocessMs += s.PreprocessMs
			b.Sync.IndexMs += s.IndexMs
			b.Sync.Documents += s.Documents
			b.Sync.Chunks += s.Chunks
			b.Sync.Requests += s.Requests
			b.Sync.RateLimited += s.RateLimited
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].FirstSeen.After(order[j].FirstSeen) })
	d.Builds = order
	if len(order) == 0 {
		return d
	}

	d.Selected = byID[selected]
	if d.Selected == nil {
		// The newest build that did something: a freshly started new build
		// has no activity yet and would show an empty page.
		d.Selected = order[0]
		for _, b := range order {
			if b.ToolCalls > 0 || len(b.latestSync) > 0 {
				d.Selected = b
				break
			}
		}
	}
	d.Baseline = byID[baseline]
	if d.Baseline == nil && baseline == "" {
		// The next older build that did something comparable.
		i := slices.Index(order, d.Selected)
		for _, b := range order[i+1:] {
			if b.ToolCalls > 0 || b.Sync.Sources > 0 {
				d.Baseline = b
				break
			}
		}
	}
	if d.Baseline == d.Selected {
		d.Baseline = nil
	}
	d.fill()
	return d
}

// fill derives the selected build's tables.
func (d *Dashboard) fill() {
	sel, base := d.Selected, d.Baseline
	for _, row := range sel.tools {
		if base != nil {
			row.Baseline = base.tools[row.Name]
		}
		d.Tools = append(d.Tools, row)
	}
	sort.Slice(d.Tools, func(i, j int) bool {
		if d.Tools[i].Calls != d.Tools[j].Calls {
			return d.Tools[i].Calls > d.Tools[j].Calls
		}
		return d.Tools[i].Name < d.Tools[j].Name
	})

	ids := map[string]bool{}
	for id := range sel.latestSync {
		ids[id] = true
	}
	if base != nil {
		for id := range base.latestSync {
			ids[id] = true
		}
	}
	for id := range ids {
		row := SourceRow{SourceID: id, Runs: sel.syncRuns[id], Current: sel.latestSync[id]}
		if base != nil {
			row.Baseline = base.latestSync[id]
		}
		ref := row.Current
		if ref == nil {
			ref = row.Baseline
		}
		row.SourceName, row.SourceType, row.Provider = ref.SourceName, ref.SourceType, ref.Provider
		d.Sources = append(d.Sources, row)
	}
	sort.Slice(d.Sources, func(i, j int) bool {
		return strings.ToLower(d.Sources[i].SourceName) < strings.ToLower(d.Sources[j].SourceName)
	})

	filters := map[[3]string]*FilterRow{}
	hits := map[string]int{}
	for _, e := range sel.events {
		tc := e.Tool
		if tc == nil {
			continue
		}
		switch tc.Name {
		case "experience_retrieve":
			d.Experience.Retrieves++
			if tc.Counted && tc.Results == 0 && tc.Error == "" {
				d.Experience.EmptyRetrieves++
			}
			for _, h := range tc.Hits {
				hits[h]++
			}
		case "experience_remember":
			if tc.Error == "" {
				d.Experience.Remembers++
			}
		}
		if !isSearch(tc) || tc.Mode == "retrieve_chunk" || tc.Page > 1 {
			continue
		}
		empty := tc.Error == "" && tc.Results == 0
		if empty {
			d.EmptySearches = append(d.EmptySearches, EmptySearch{Time: e.Time, Tool: tc.Name, Mode: tc.Mode, Query: tc.Query, Filters: tc.Filters})
		}
		for field, values := range tc.Filters {
			for _, v := range values {
				k := [3]string{tc.Name, field, v}
				row := filters[k]
				if row == nil {
					row = &FilterRow{Tool: tc.Name, Field: field, Value: v}
					filters[k] = row
				}
				row.Uses++
				if empty {
					row.Empty++
				}
			}
		}
	}
	slices.Reverse(d.EmptySearches) // newest first
	if len(d.EmptySearches) > maxEmptySearches {
		d.EmptySearches = d.EmptySearches[:maxEmptySearches]
	}
	for _, row := range filters {
		d.Filters = append(d.Filters, *row)
	}
	sort.Slice(d.Filters, func(i, j int) bool {
		a, b := d.Filters[i], d.Filters[j]
		if ra, rb := ratio(a.Empty, a.Uses), ratio(b.Empty, b.Uses); ra != rb {
			return ra > rb
		}
		if a.Uses != b.Uses {
			return a.Uses > b.Uses
		}
		return a.Tool+a.Field+a.Value < b.Tool+b.Field+b.Value
	})
	for s, n := range hits {
		d.Experience.Top = append(d.Experience.Top, HitCount{Situation: s, Count: n})
	}
	sort.Slice(d.Experience.Top, func(i, j int) bool {
		if d.Experience.Top[i].Count != d.Experience.Top[j].Count {
			return d.Experience.Top[i].Count > d.Experience.Top[j].Count
		}
		return d.Experience.Top[i].Situation < d.Experience.Top[j].Situation
	})
	if len(d.Experience.Top) > 15 {
		d.Experience.Top = d.Experience.Top[:15]
	}
}
