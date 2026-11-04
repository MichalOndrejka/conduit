package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report is a run's full output, written as JSON next to the Markdown
// rendering.
type Report struct {
	Kind      string            `json:"kind"` // retrieval | agent
	CreatedAt time.Time         `json:"created_at"`
	Dataset   string            `json:"dataset"`
	K         int               `json:"k"`
	Settings  map[string]string `json:"settings,omitempty"`
	Layouts   []LayoutInfo      `json:"layouts"`
	Summary   Summary           `json:"summary"`
	Changes   []Change          `json:"changes"`
	Results   []QueryResult     `json:"results"`
}

// LayoutInfo describes a compared layout in the report.
type LayoutInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// NewReport assembles a report; the first layout is the baseline.
func NewReport(kind string, ds *Dataset, k int, layouts []string, results []QueryResult, settings map[string]string) *Report {
	rep := &Report{Kind: kind, CreatedAt: time.Now().UTC(), Dataset: ds.Summary(), K: k, Settings: settings,
		Summary: Summarize(results), Results: results}
	for _, name := range layouts {
		if l, ok := LayoutByName(name); ok {
			rep.Layouts = append(rep.Layouts, LayoutInfo{l.Name, l.Description})
		}
	}
	if len(layouts) > 0 {
		rep.Changes = Changes(results, layouts[0])
	}
	return rep
}

// Write stores the report as <dir>/<kind>-<timestamp>.json and .md and
// returns the Markdown path.
func (rep *Report) Write(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(dir, fmt.Sprintf("%s-%s", rep.Kind, rep.CreatedAt.Format("20060102-150405")))
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(base+".json", data, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(base+".md", []byte(rep.Markdown()), 0o644); err != nil {
		return "", err
	}
	return base + ".md", nil
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

// delta renders v against the baseline value, e.g. "71% (−8)".
func delta(v, base float64, isBaseline bool) string {
	if isBaseline {
		return pct(v)
	}
	d := (v - base) * 100
	switch {
	case d > 0.5:
		return fmt.Sprintf("%s (+%.0f)", pct(v), d)
	case d < -0.5:
		return fmt.Sprintf("%s (−%.0f)", pct(v), -d)
	}
	return pct(v)
}

// Markdown renders the report for reading.
func (rep *Report) Markdown() string {
	var b strings.Builder
	s := rep.Summary
	base := ""
	if len(s.Layouts) > 0 {
		base = s.Layouts[0]
	}
	fmt.Fprintf(&b, "# Conduit %s eval\n\n", rep.Kind)
	fmt.Fprintf(&b, "- **Dataset:** %s\n- **Run:** %s\n- **Scored:** top %d documents per question\n",
		rep.Dataset, rep.CreatedAt.Format(time.RFC3339), rep.K)
	keys := make([]string, 0, len(rep.Settings))
	for k := range rep.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "- **%s:** %s\n", k, rep.Settings[k])
	}
	b.WriteString("\n## Layouts\n\n")
	for _, l := range rep.Layouts {
		marker := ""
		if l.Name == base {
			marker = " _(baseline)_"
		}
		fmt.Fprintf(&b, "- **%s**%s: %s\n", l.Name, marker, l.Description)
	}

	b.WriteString("\n## Overall\n\nDifferences against the baseline in percentage points.\n\n")
	if rep.Kind == "agent" {
		b.WriteString("| Layout | Answer recall | Answer precision | Hit@1 | MRR | Seen by searches | Tool calls | Errors |\n|---|---|---|---|---|---|---|---|\n")
	} else {
		b.WriteString("| Layout | Hit@1 | Recall@5 | Recall@K | MRR | Empty |\n|---|---|---|---|---|---|\n")
	}
	bo := s.Overall[base]
	for _, l := range s.Layouts {
		a := s.Overall[l]
		isBase := l == base
		if rep.Kind == "agent" {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %.1f | %d |\n", l,
				delta(a.AnswerRecall, bo.AnswerRecall, isBase), delta(a.AnswerPrecision, bo.AnswerPrecision, isBase),
				delta(a.Hit1, bo.Hit1, isBase), delta(a.MRR, bo.MRR, isBase), pct(seenRecall(rep.Results, l)),
				a.ToolCalls, a.Errors)
		} else {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", l,
				delta(a.Hit1, bo.Hit1, isBase), delta(a.RecallAt5, bo.RecallAt5, isBase),
				delta(a.RecallAtK, bo.RecallAtK, isBase), delta(a.MRR, bo.MRR, isBase), pct(a.Empty))
		}
	}

	metric, metricName := func(a Aggregate) float64 { return a.MRR }, "MRR"
	if rep.Kind == "agent" {
		metric, metricName = func(a Aggregate) float64 { return a.AnswerRecall }, "answer recall"
	}
	writeGroup := func(title string, groups map[string]map[string]Aggregate) {
		fmt.Fprintf(&b, "\n## %s (%s)\n\n| | n |", title, metricName)
		for _, l := range s.Layouts {
			fmt.Fprintf(&b, " %s |", l)
		}
		b.WriteString("\n|---|---|" + strings.Repeat("---|", len(s.Layouts)) + "\n")
		names := make([]string, 0, len(groups))
		for g := range groups {
			names = append(names, g)
		}
		sort.Strings(names)
		for _, g := range names {
			fmt.Fprintf(&b, "| %s | %d |", g, groups[g][base].N)
			for _, l := range s.Layouts {
				fmt.Fprintf(&b, " %s |", delta(metric(groups[g][l]), metric(groups[g][base]), l == base))
			}
			b.WriteString("\n")
		}
	}
	writeGroup("By intent", s.ByIntent)
	writeGroup("By company", s.ByCompany)

	if len(rep.Changes) > 0 {
		b.WriteString("\n## Questions that changed against the baseline\n\nRank of the first relevant document (– = not found); worst regressions first.\n\n")
		b.WriteString("| Layout | Question | Baseline | Now | Ranked above it |\n|---|---|---|---|---|\n")
		for _, c := range rep.Changes {
			fmt.Fprintf(&b, "| %s | %s/%s: %s | %s | %s | %s |\n", c.Layout, c.Company, c.QueryID,
				strings.ReplaceAll(c.Query, "|", "\\|"), rankText(c.BaselineRank), rankText(c.Rank), strings.Join(c.Above, ", "))
		}
	} else {
		b.WriteString("\nNo question's first relevant rank differs from the baseline.\n")
	}
	return b.String()
}

func rankText(r int) string {
	if r == 0 {
		return "–"
	}
	return fmt.Sprint(r)
}

// seenRecall is the mean share of relevant documents an agent's searches
// returned under layout.
func seenRecall(results []QueryResult, layout string) float64 {
	var sum float64
	var n int
	for _, r := range results {
		if r.Layout == layout && r.Agent != nil {
			sum += r.Agent.SeenRecall
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
