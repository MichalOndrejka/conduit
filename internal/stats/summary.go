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
	// Hits are first-page searches that found something.
	Hits int `json:"hits"`
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
			if tc.Error == "" {
				if tc.Results == 0 {
					s.Empty++
				} else {
					s.Hits++
				}
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

// SyncTotals sum each source's latest sync.
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

// ToolRow is one tool's calls.
type ToolRow struct {
	Name   string      `json:"name"`
	Calls  int         `json:"calls"`
	Errors int         `json:"errors"`
	Search SearchStats `json:"search"`
}

// SourceRow is a source's latest sync.
type SourceRow struct {
	Runs   int      `json:"runs"` // syncs of the source so far
	Latest *SyncRun `json:"latest"`
}

// ProviderRow is the load every sync since the start put on one provider's
// API (all runs, not only each source's latest).
type ProviderRow struct {
	Provider    string `json:"provider"`
	Syncs       int    `json:"syncs"`
	Requests    int    `json:"requests"`
	RateLimited int    `json:"rate_limited"`
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

// ExperienceStats summarize the experience tools.
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
	ToolCalls     int             `json:"tool_calls"`
	ToolErrors    int             `json:"tool_errors"`
	Search        SearchStats     `json:"search"`
	Sync          SyncTotals      `json:"sync"`
	Tools         []*ToolRow      `json:"tools"`
	Sources       []SourceRow     `json:"sources"`
	Providers     []ProviderRow   `json:"providers"`
	EmptySearches []EmptySearch   `json:"empty_searches"`
	Filters       []FilterRow     `json:"filters"`
	Experience    ExperienceStats `json:"experience"`
	// RetrievedSituations are the experience situations returned so far,
	// for spotting entries that are never used.
	RetrievedSituations map[string]bool `json:"-"`
}

// maxEmptySearches caps the empty-search list.
const maxEmptySearches = 100

// Summarize builds the dashboard from events.
func Summarize(events []Event) *Dashboard {
	d := &Dashboard{RetrievedSituations: map[string]bool{}}
	tools := map[string]*ToolRow{}
	latest := map[string]*SyncRun{} // source ID → latest run
	runs := map[string]int{}
	providers := map[string]*ProviderRow{}
	for _, e := range events {
		switch {
		case e.Tool != nil:
			tc := e.Tool
			d.ToolCalls++
			row := tools[tc.Name]
			if row == nil {
				row = &ToolRow{Name: tc.Name}
				tools[tc.Name] = row
			}
			row.Calls++
			if tc.Error != "" {
				d.ToolErrors++
				row.Errors++
			}
			if isSearch(tc) {
				d.Search.add(tc)
				row.Search.add(tc)
			}
			for _, h := range tc.Hits {
				d.RetrievedSituations[h] = true
			}
		case e.Sync != nil:
			runs[e.Sync.SourceID]++
			p := providers[e.Sync.Provider]
			if p == nil {
				p = &ProviderRow{Provider: e.Sync.Provider}
				providers[e.Sync.Provider] = p
			}
			p.Syncs++
			p.Requests += e.Sync.Requests
			p.RateLimited += e.Sync.RateLimited
			latest[e.Sync.SourceID] = e.Sync
		}
	}
	d.Search.finish()
	for _, row := range tools {
		row.Search.finish()
		d.Tools = append(d.Tools, row)
	}
	sort.Slice(d.Tools, func(i, j int) bool {
		if d.Tools[i].Calls != d.Tools[j].Calls {
			return d.Tools[i].Calls > d.Tools[j].Calls
		}
		return d.Tools[i].Name < d.Tools[j].Name
	})
	for id, s := range latest {
		d.Sync.Sources++
		if s.Status == "failed" {
			d.Sync.Failed++
		}
		d.Sync.DurationMs += s.DurationMs
		d.Sync.FetchMs += s.FetchMs
		d.Sync.PreprocessMs += s.PreprocessMs
		d.Sync.IndexMs += s.IndexMs
		d.Sync.Documents += s.Documents
		d.Sync.Chunks += s.Chunks
		d.Sync.Requests += s.Requests
		d.Sync.RateLimited += s.RateLimited
		d.Sources = append(d.Sources, SourceRow{Runs: runs[id], Latest: s})
	}
	sort.Slice(d.Sources, func(i, j int) bool {
		return strings.ToLower(d.Sources[i].Latest.SourceName) < strings.ToLower(d.Sources[j].Latest.SourceName)
	})
	for _, p := range providers {
		d.Providers = append(d.Providers, *p)
	}
	sort.Slice(d.Providers, func(i, j int) bool {
		if d.Providers[i].Requests != d.Providers[j].Requests {
			return d.Providers[i].Requests > d.Providers[j].Requests
		}
		return d.Providers[i].Provider < d.Providers[j].Provider
	})
	d.fill(events)
	return d
}

// fill derives the search and experience tables.
func (d *Dashboard) fill(events []Event) {
	filters := map[[3]string]*FilterRow{}
	hits := map[string]int{}
	for _, e := range events {
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
