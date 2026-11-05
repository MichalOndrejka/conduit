package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// SetStats gives the server the recorder whose events the Stats page shows.
func (s *Server) SetStats(r *stats.Recorder) { s.stats = r }

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

// statsTile is one headline number.
type statsTile struct {
	Label, Tip, Value string
	Alert             string // a status note, e.g. rate-limit hits
}

type statsSourceRow struct {
	Name, Type, Provider   string
	Runs                   int
	Status, StatusTone     string
	Error                  string
	Duration, Fetch, Index string
	Documents, Chunks      int
	Requests, RateLimited  int
	FetchPct, IndexPct     int // phase split, for the thin bar
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

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Active": "stats", "Enabled": s.stats != nil}
	if s.stats == nil {
		s.render(w, s.statsTmpl, "base", data)
		return
	}
	d := stats.Summarize(s.stats.Events())
	data["Since"] = s.stats.Started().Local().Format("Jan 2 15:04")

	// Headline numbers.
	searched, synced := d.Search.FirstPages > 0, d.Sync.Sources > 0
	pick := func(ok bool, v string) string {
		if ok {
			return v
		}
		return "–"
	}
	tiles := []statsTile{
		{Label: "Searches", Tip: "Questions the agent asked: first-page semantic and pattern searches.",
			Value: fmtCount(float64(d.Search.FirstPages))},
		{Label: "Empty results", Tip: "Share of searches that found nothing: a missing or unsynced source, or a filter value that doesn't exist. Lower is better.",
			Value: pick(searched, fmtPct(d.Search.EmptyRate()))},
		{Label: "Further pages", Tip: "Further pages per search: how often the agent asked for page 2 or later because the answer wasn't on the first page. Lower is better.",
			Value: pick(searched, fmtPerQ(d.Search.NextPageRate()))},
		{Label: "Latency p95", Tip: "95th percentile of semantic search time, embedding included; the cold first search left out.",
			Value: pick(d.Search.P95Ms > 0, fmtMs(float64(d.Search.P95Ms)))},
		{Label: "Sync time", Tip: "Sum of each source's latest sync.",
			Value: pick(synced, fmtMs(float64(d.Sync.DurationMs)))},
		{Label: "API requests", Tip: "HTTP requests to source APIs across each source's latest sync.",
			Value: pick(synced, fmtCount(float64(d.Sync.Requests)))},
	}
	if d.Sync.RateLimited > 0 {
		tiles[5].Alert = fmt.Sprintf("%d rate-limited", d.Sync.RateLimited)
	}
	data["Tiles"] = tiles

	// Search quality tab.
	var emptyBars, callBars []rankedBar
	for _, t := range d.Tools {
		callBars = append(callBars, rankedBar{Label: t.Name, Value: float64(t.Calls), Text: fmtCount(float64(t.Calls))})
		if t.Search.FirstPages > 0 {
			emptyBars = append(emptyBars, rankedBar{Label: t.Name, Value: t.Search.EmptyRate(), Text: fmtPct(t.Search.EmptyRate())})
		}
	}
	sort.SliceStable(emptyBars, func(i, j int) bool { return emptyBars[i].Value > emptyBars[j].Value })
	data["ChartToolEmpty"] = rankedBars(emptyBars, "found nothing", 520)
	data["ChartToolCalls"] = rankedBars(callBars, "calls", 520)

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
	var sources []statsSourceRow
	var timeBars []rankedBar
	for _, src := range d.Sources {
		cur := src.Latest
		row := statsSourceRow{Name: cur.SourceName, Type: cur.SourceType, Provider: cur.Provider, Runs: src.Runs, Status: cur.Status,
			StatusTone: map[string]string{"completed": "good", "failed": "bad", "cancelled": "warn"}[cur.Status],
			Duration:   fmtMs(float64(cur.DurationMs)), Fetch: fmtMs(float64(cur.FetchMs)), Index: fmtMs(float64(cur.IndexMs)),
			Documents: cur.Documents, Chunks: cur.Chunks, Requests: cur.Requests, RateLimited: cur.RateLimited}
		if cur.Error != "" {
			row.Error = cur.ErrorPhase + ": " + rag.Truncate(cur.Error, 300)
		}
		if total := cur.FetchMs + cur.PreprocessMs + cur.IndexMs; total > 0 {
			row.FetchPct = int(float64(cur.FetchMs) / float64(total) * 100)
			row.IndexPct = 100 - row.FetchPct
		}
		sources = append(sources, row)
		timeBars = append(timeBars, rankedBar{Label: cur.SourceName, Value: float64(cur.DurationMs), Text: row.Duration})
	}
	sort.SliceStable(timeBars, func(i, j int) bool { return timeBars[i].Value > timeBars[j].Value })
	data["Sources"] = sources
	data["ChartSourceTime"] = rankedBars(timeBars, "sync time", 1100)

	// API load per provider: every sync since the start, retries included.
	var providerBars []rankedBar
	for _, p := range d.Providers {
		text := fmtCount(float64(p.Requests))
		if p.RateLimited > 0 {
			text += fmt.Sprintf(" · %d rate-limited", p.RateLimited)
		}
		providerBars = append(providerBars, rankedBar{Label: providerLabel(p.Provider), Value: float64(p.Requests), Text: text})
	}
	data["ChartProviders"] = rankedBars(providerBars, "API requests", 1100)

	// Experience tab.
	data["Experience"] = d.Experience
	var recall []rankedBar
	for _, h := range d.Experience.Top {
		recall = append(recall, rankedBar{Label: h.Situation, Value: float64(h.Count), Text: fmtCount(float64(h.Count))})
	}
	data["ChartRecalled"] = rankedBars(recall, "recalls", 520)
	data["NeverRetrieved"] = s.neverRetrieved(r.Context(), d.RetrievedSituations)

	s.render(w, s.statsTmpl, "base", data)
}

// providerLabel is a provider's display name.
func providerLabel(p string) string {
	switch p {
	case "github":
		return "GitHub"
	case "azure-devops":
		return "Azure DevOps"
	case "":
		return "Other"
	}
	return p
}

// neverRetrieved lists experience entries experience_retrieve hasn't
// returned since the process started (at most 20), or nil when the experience
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
