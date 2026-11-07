package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// SetStats gives the server the recorder whose events the Stats page shows.
func (s *Server) SetStats(r *stats.Recorder) { s.stats = r }

// SetToolNames gives the server the registered MCP tools, so the Stats charts
// list each one before it is first called.
func (s *Server) SetToolNames(names []string) { s.tools = names }

func fmtPct(v float64) string   { return fmt.Sprintf("%.0f%%", v*100) }
func fmtCount(v float64) string { return fmt.Sprintf("%.0f", v) }
func fmtMs(v float64) string    { return fmtDuration(time.Duration(v) * time.Millisecond) }

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
	Label, Value string
	Note         string // a neutral subline, e.g. a share
	Alert        string // a status note, e.g. rate-limit hits
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

type statsEmptySearch struct {
	When, Tool, Mode, Query, Filters string
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Active": "dashboard", "Enabled": s.stats != nil}
	if s.stats == nil {
		s.render(w, s.statsTmpl, "base", data)
		return
	}
	d := stats.Summarize(s.stats.Events())

	// Headline numbers.
	pick := func(ok bool, v string) string {
		if ok {
			return v
		}
		return "–"
	}
	var syncs, requests, rateLimited int
	for _, p := range d.Providers {
		syncs += p.Syncs
		requests += p.Requests
		rateLimited += p.RateLimited
	}
	share := func(n int) string {
		if d.Search.FirstPages == 0 {
			return ""
		}
		return fmtPct(float64(n)/float64(d.Search.FirstPages)) + " of searches"
	}
	tiles := []statsTile{
		{Label: "Tool Calls",
			Value: fmtCount(float64(d.ToolCalls))},
		{Label: "Hits",
			Value: fmtCount(float64(d.Search.Hits)), Note: share(d.Search.Hits)},
		{Label: "Misses",
			Value: fmtCount(float64(d.Search.Empty)), Note: share(d.Search.Empty)},
		{Label: "Latency",
			Value: pick(d.Search.P95Ms > 0, fmtMs(float64(d.Search.P95Ms))), Note: pick(d.Search.P95Ms > 0, "p95")},
		{Label: "Syncs",
			Value: fmtCount(float64(syncs))},
		{Label: "API Requests",
			Value: fmtCount(float64(requests))},
	}
	if d.ToolErrors > 0 {
		tiles[0].Alert = fmt.Sprintf("%d failed", d.ToolErrors)
	}
	if d.Sync.Failed > 0 {
		tiles[4].Alert = fmt.Sprintf("%d sources failing", d.Sync.Failed)
	}
	if rateLimited > 0 {
		tiles[5].Alert = fmt.Sprintf("%d rate-limited", rateLimited)
	}
	data["Tiles"] = tiles

	// Sources tab: every registered tool gets a row, called yet or not, so
	// the charts show from the start.
	byName := map[string]*stats.ToolRow{}
	for _, t := range d.Tools {
		byName[t.Name] = t
	}
	names := slices.Clone(s.tools)
	for _, t := range d.Tools {
		if !slices.Contains(names, t.Name) {
			names = append(names, t.Name)
		}
	}
	var emptyBars, callBars []rankedBar
	for _, name := range names {
		t := byName[name]
		if t == nil {
			t = &stats.ToolRow{Name: name}
		}
		callBars = append(callBars, rankedBar{Label: name, Value: float64(t.Calls), Text: fmtCount(float64(t.Calls))})
		if strings.HasPrefix(name, "search_") {
			emptyBars = append(emptyBars, rankedBar{Label: name, Value: t.Search.EmptyRate(),
				Text: fmtPct(t.Search.EmptyRate())})
		}
	}
	byValue := func(bars []rankedBar) {
		sort.SliceStable(bars, func(i, j int) bool {
			if bars[i].Value != bars[j].Value {
				return bars[i].Value > bars[j].Value
			}
			return bars[i].Label < bars[j].Label
		})
	}
	byValue(callBars)
	byValue(emptyBars)
	data["ChartToolCalls"] = rankedBars(callBars, "calls", 520)
	data["ChartToolEmpty"] = rankedBars(emptyBars, "found nothing", 520)

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
