// Server-rendered SVG charts for the Stats page. Each returns a ready
// <svg> (template.HTML) drawn in viewBox units close to its on-screen size,
// coloured through the CSS custom properties of .viz (input.css) so light
// and dark mode each use their own validated steps.
//
// Conventions (see the dataviz rules): thin marks, 2px lines, 4px rounded
// data-ends square at the baseline, columns at most 24px wide with a 2px
// surface gap between stacked segments, hairline solid grid, no dual axes,
// a legend for two or more series, values labelled selectively (the
// selected build), and every mark carrying a hover/focus tooltip whose
// values also appear in the Builds table.
package web

import (
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"
)

// tooltip is what a mark shows on hover and focus.
type tooltip struct {
	Title string       `json:"title"`
	Rows  []tooltipRow `json:"rows"`
}

type tooltipRow struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Key   string `json:"key,omitempty"` // CSS colour of the series key, e.g. var(--viz-s1)
}

// ttAttr renders a data-tt attribute (JSON, read by the page's tooltip
// script with textContent, never as HTML).
func ttAttr(t tooltip) string {
	data, _ := json.Marshal(t)
	return ` tabindex="0" data-tt="` + html.EscapeString(string(data)) + `"`
}

func esc(s string) string { return html.EscapeString(s) }

// niceMax rounds v up to a clean axis maximum (1, 2, 2.5, 5 × 10ⁿ).
func niceMax(v float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*exp {
			return m * exp
		}
	}
	return 10 * exp
}

// compactNum formats an axis tick: 0, 250, 1,500, 12K, 1.5M.
func compactNum(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e6:
		return trimZero(fmt.Sprintf("%.1f", v/1e6)) + "M"
	case a >= 1e4:
		return trimZero(fmt.Sprintf("%.1f", v/1e3)) + "K"
	case a >= 1000:
		return fmt.Sprintf("%d,%03d", int(v)/1000, int(v)%1000)
	case a == math.Trunc(a):
		return fmt.Sprintf("%.0f", v)
	case a >= 1:
		return trimZero(fmt.Sprintf("%.1f", v))
	default:
		return trimZero(fmt.Sprintf("%.2f", v))
	}
}

func trimZero(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// series is one plotted measure across builds. Values are in axis units;
// Display is each point's formatted value for labels and tooltips; Has marks
// builds that have the measure at all (no searches → no empty rate).
type series struct {
	Name    string
	Color   string // CSS colour, e.g. var(--viz-s1)
	Values  []float64
	Display []string
	Has     []bool
}

// trendSpec is a chart across builds.
type trendSpec struct {
	Label      string   // what's plotted, for the accessible name
	XLabels    []string // short label per build
	XTitles    []string // full build label, tooltip title
	Series     []series
	Selected   int // index of the selected build, -1 none
	Baseline   int // index of the compared build, -1 none
	AxisFmt    func(float64) string
	AxisUnit   string // appended to the top tick, e.g. "min"
	W, H       int
	Stacked    bool // columns: stack the series
	Columns    bool // columns instead of a line
	MaxAtLeast float64
	// Totals are the formatted stack totals per build (stacked columns).
	Totals []string
}

const (
	padTop    = 26
	padBottom = 34
	padRight  = 16
)

// trendChart draws a line (one series, with a 10% area wash) or columns
// (stacked when several series) across builds.
func trendChart(s trendSpec) template.HTML {
	n := len(s.XLabels)
	if n == 0 || len(s.Series) == 0 {
		return ""
	}
	if s.AxisFmt == nil {
		s.AxisFmt = compactNum
	}
	// Axis maximum: the largest point, or stacked total.
	var maxV float64
	for i := 0; i < n; i++ {
		var total float64
		for _, sr := range s.Series {
			if sr.Has[i] {
				if s.Stacked {
					total += sr.Values[i]
				} else {
					maxV = math.Max(maxV, sr.Values[i])
				}
			}
		}
		maxV = math.Max(maxV, total)
	}
	maxV = niceMax(math.Max(maxV, s.MaxAtLeast))

	// Y tick labels decide the left padding.
	ticks := []float64{0, maxV / 2, maxV}
	padLeft := 12
	for _, t := range ticks {
		label := s.AxisFmt(t)
		if t == maxV && s.AxisUnit != "" {
			label += " " + s.AxisUnit
		}
		padLeft = max(padLeft, 12+8*len(label))
	}
	legendH := 0
	if len(s.Series) > 1 {
		legendH = 18
	}
	top := padTop + legendH
	plotW := float64(s.W - padLeft - padRight)
	plotH := float64(s.H - top - padBottom)
	step := plotW / float64(n)
	x := func(i int) float64 { return float64(padLeft) + step*(float64(i)+0.5) }
	y := func(v float64) float64 { return float64(top) + plotH - v/maxV*plotH }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="viz-svg" viewBox="0 0 %d %d" role="img" aria-label="%s">`, s.W, s.H, esc(s.Label))

	// Legend (two or more series): swatch + name, in series order.
	if len(s.Series) > 1 {
		lx := padLeft
		for _, sr := range s.Series {
			if s.Columns {
				fmt.Fprintf(&b, `<rect x="%d" y="5" width="10" height="10" rx="2" fill="%s"/>`, lx, sr.Color)
			} else {
				fmt.Fprintf(&b, `<line x1="%d" y1="10" x2="%d" y2="9" stroke="%s" stroke-width="2" stroke-linecap="round"/>`, lx, lx+12, sr.Color)
			}
			fmt.Fprintf(&b, `<text x="%d" y="13" class="viz-legend">%s</text>`, lx+16, esc(sr.Name))
			lx += 16 + 8*len(sr.Name) + 20
		}
	}

	// Grid and y ticks: solid hairlines, recessive.
	for _, t := range ticks {
		ty := y(t)
		cls := "viz-grid"
		if t == 0 {
			cls = "viz-axis"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`, cls, padLeft, ty, s.W-padRight, ty)
		label := s.AxisFmt(t)
		if t == maxV && s.AxisUnit != "" {
			label += " " + s.AxisUnit
		}
		fmt.Fprintf(&b, `<text class="viz-tick" x="%d" y="%.1f" text-anchor="end">%s</text>`, padLeft-6, ty+3.5, esc(label))
	}

	// X labels by priority (selected, compared, first, last, then evenly
	// spaced), skipping any that would collide with one already placed.
	var order []int
	for _, i := range []int{s.Selected, s.Baseline, 0, n - 1} {
		if i >= 0 && i < n {
			order = append(order, i)
		}
	}
	every := max(1, int(math.Ceil(float64(n)/6)))
	for i := 0; i < n; i += every {
		order = append(order, i)
	}
	type span struct{ l, r float64 }
	var placed []span
	labelled := map[int]bool{}
	for _, i := range order {
		if labelled[i] {
			continue
		}
		half := float64(len(s.XLabels[i]))*7.5/2 + 4
		sp := span{x(i) - half, x(i) + half}
		collides := false
		for _, p := range placed {
			if sp.l < p.r && p.l < sp.r {
				collides = true
				break
			}
		}
		if collides {
			continue
		}
		placed = append(placed, sp)
		labelled[i] = true
		cls := "viz-tick"
		if i == s.Selected {
			cls = "viz-tick viz-tick-sel"
		}
		fmt.Fprintf(&b, `<text class="%s" x="%.1f" y="%d" text-anchor="middle">%s</text>`, cls, x(i), s.H-12, esc(s.XLabels[i]))
	}

	if s.Columns {
		drawColumns(&b, s, n, x, y, step)
	} else {
		drawLine(&b, s, n, x, y)
	}

	// Hit columns: one per build, the whole plot height, on top of the marks.
	for i := 0; i < n; i++ {
		t := tooltip{Title: s.XTitles[i]}
		var total float64
		any := false
		for _, sr := range s.Series {
			v := "–"
			if sr.Has[i] {
				v = sr.Display[i]
				total += sr.Values[i]
				any = true
			}
			t.Rows = append(t.Rows, tooltipRow{Value: v, Label: sr.Name, Key: sr.Color})
		}
		if s.Stacked && len(s.Series) > 1 && any {
			v := s.AxisFmt(total) + " " + s.AxisUnit
			if i < len(s.Totals) && s.Totals[i] != "" {
				v = s.Totals[i]
			}
			t.Rows = append(t.Rows, tooltipRow{Value: v, Label: "total"})
		}
		fmt.Fprintf(&b, `<g class="viz-col"%s><line class="viz-xhair" x1="%.1f" y1="%d" x2="%.1f" y2="%.1f"/><rect class="viz-hitbox" x="%.1f" y="%d" width="%.1f" height="%.1f"/></g>`,
			ttAttr(t), x(i), top, x(i), float64(top)+plotH, x(i)-step/2, top, step, plotH)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func drawLine(b *strings.Builder, s trendSpec, n int, x func(int) float64, y func(float64) float64) {
	sr := s.Series[0]
	// Area wash and line, broken where a build has no value.
	var line, area strings.Builder
	segStart := -1
	flush := func(end int) {
		if segStart < 0 {
			return
		}
		if end > segStart {
			fmt.Fprintf(&area, "M%.1f,%.1f ", x(segStart), y(0))
			for i := segStart; i <= end; i++ {
				fmt.Fprintf(&area, "L%.1f,%.1f ", x(i), y(sr.Values[i]))
			}
			fmt.Fprintf(&area, "L%.1f,%.1f Z ", x(end), y(0))
		}
		segStart = -1
	}
	for i := 0; i < n; i++ {
		if !sr.Has[i] {
			flush(i - 1)
			continue
		}
		cmd := "L"
		if segStart < 0 {
			segStart, cmd = i, "M"
		}
		fmt.Fprintf(&line, "%s%.1f,%.1f ", cmd, x(i), y(sr.Values[i]))
	}
	flush(n - 1)
	if area.Len() > 0 {
		fmt.Fprintf(b, `<path d="%s" fill="%s" opacity="0.10"/>`, area.String(), sr.Color)
	}
	if line.Len() > 0 {
		fmt.Fprintf(b, `<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`, line.String(), sr.Color)
	}
	// Small dots where a point stands alone (no line through it).
	for i := 0; i < n; i++ {
		if sr.Has[i] && (i == 0 || !sr.Has[i-1]) && (i == n-1 || !sr.Has[i+1]) {
			fmt.Fprintf(b, `<circle cx="%.1f" cy="%.1f" r="3" fill="%s"/>`, x(i), y(sr.Values[i]), sr.Color)
		}
	}
	// Compared build: hollow ring. Selected: filled dot with a surface ring
	// and its value labelled.
	if i := s.Baseline; i >= 0 && sr.Has[i] {
		fmt.Fprintf(b, `<circle cx="%.1f" cy="%.1f" r="4.5" class="viz-base-dot"/>`, x(i), y(sr.Values[i]))
	}
	if i := s.Selected; i >= 0 && sr.Has[i] {
		fmt.Fprintf(b, `<circle cx="%.1f" cy="%.1f" r="5" fill="%s" class="viz-ring"/>`, x(i), y(sr.Values[i]), sr.Color)
		anchor := "middle"
		lx := x(i)
		if i == n-1 && n > 1 {
			anchor, lx = "end", lx+4
		} else if i == 0 && n > 1 {
			anchor, lx = "start", lx-4
		}
		fmt.Fprintf(b, `<text class="viz-value" x="%.1f" y="%.1f" text-anchor="%s">%s</text>`, lx, y(sr.Values[i])-10, anchor, esc(sr.Display[i]))
	}
}

func drawColumns(b *strings.Builder, s trendSpec, n int, x func(int) float64, y func(float64) float64, step float64) {
	w := math.Min(24, step*0.6)
	for i := 0; i < n; i++ {
		base := y(0)
		var total float64
		var drawn []float64 // heights of drawn segments
		var segs []int
		for k, sr := range s.Series {
			if sr.Has[i] && sr.Values[i] > 0 {
				drawn = append(drawn, sr.Values[i])
				segs = append(segs, k)
				total += sr.Values[i]
			}
		}
		for j, k := range segs {
			sr := s.Series[k]
			h := y(0) - y(drawn[j])
			top := base - h
			if j > 0 {
				h -= 2 // the 2px surface gap between stacked segments
				base -= 2
				top = base - h
			}
			if h < 0.5 {
				base = top
				continue
			}
			cls := ""
			if s.Selected >= 0 && i != s.Selected && i != s.Baseline {
				cls = ` class="viz-dim"`
			}
			if j == len(segs)-1 {
				// Topmost segment: 4px rounded data-end, square at its base.
				r := math.Min(4, h)
				fmt.Fprintf(b, `<path%s fill="%s" d="M%.1f,%.1f L%.1f,%.1f Q%.1f,%.1f %.1f,%.1f L%.1f,%.1f Q%.1f,%.1f %.1f,%.1f L%.1f,%.1f Z"/>`,
					cls, sr.Color,
					x(i)-w/2, base,
					x(i)-w/2, top+r, x(i)-w/2, top, x(i)-w/2+r, top,
					x(i)+w/2-r, top, x(i)+w/2, top, x(i)+w/2, top+r,
					x(i)+w/2, base)
			} else {
				fmt.Fprintf(b, `<rect%s x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, cls, x(i)-w/2, top, w, h, sr.Color)
			}
			base = top
		}
		if i == s.Selected && total > 0 {
			label := s.AxisFmt(total)
			switch {
			case len(s.Series) == 1:
				label = s.Series[0].Display[i]
			case i < len(s.Totals) && s.Totals[i] != "":
				label = s.Totals[i]
			}
			fmt.Fprintf(b, `<text class="viz-value" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, x(i), y(total)-6, esc(label))
		}
	}
}

// sparkline is a stat tile's trend across builds: a de-emphasised line,
// the selected build as an accent dot, the compared build as a ring.
func sparkline(sr series, selected, baseline int) template.HTML {
	const w, h, pad = 120.0, 32.0, 4.0
	n := len(sr.Values)
	if n < 2 {
		return ""
	}
	var maxV float64
	for i, v := range sr.Values {
		if sr.Has[i] {
			maxV = math.Max(maxV, v)
		}
	}
	if maxV == 0 {
		maxV = 1
	}
	x := func(i int) float64 { return pad + (w-2*pad)*float64(i)/float64(n-1) }
	y := func(v float64) float64 { return h - pad - (h-2*pad)*v/maxV }
	var path strings.Builder
	pen := false
	for i := 0; i < n; i++ {
		if !sr.Has[i] {
			pen = false
			continue
		}
		cmd := "L"
		if !pen {
			cmd, pen = "M", true
		}
		fmt.Fprintf(&path, "%s%.1f,%.1f ", cmd, x(i), y(sr.Values[i]))
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="viz-spark" viewBox="0 0 %.0f %.0f" aria-hidden="true">`, w, h)
	if path.Len() > 0 {
		fmt.Fprintf(&b, `<path d="%s" fill="none" class="viz-spark-line" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/>`, path.String())
	}
	if baseline >= 0 && sr.Has[baseline] {
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3" class="viz-base-dot"/>`, x(baseline), y(sr.Values[baseline]))
	}
	if selected >= 0 && sr.Has[selected] {
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3.5" fill="var(--viz-s1)" class="viz-ring"/>`, x(selected), y(sr.Values[selected]))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// dumbbellRow is one item's value in the compared and selected builds.
type dumbbellRow struct {
	Label             string
	Base, Cur         float64
	HasBase, HasCur   bool
	BaseText, CurText string
	Change            cell // the delta, toned by direction
}

// dumbbell compares each item before → after on one shared scale: compared
// build as a light dot, selected build as a dark dot, joined by a line.
func dumbbell(rows []dumbbellRow, baseName, curName string, axisFmt func(float64) string, axisUnit string, w int) template.HTML {
	if len(rows) == 0 {
		return ""
	}
	const rowH, labelW, valueW, top = 34, 175, 120, 30
	var maxV float64
	for _, r := range rows {
		if r.HasBase {
			maxV = math.Max(maxV, r.Base)
		}
		if r.HasCur {
			maxV = math.Max(maxV, r.Cur)
		}
	}
	maxV = niceMax(maxV)
	h := top + rowH*len(rows) + 22
	x0, x1 := float64(labelW), float64(w-valueW)
	x := func(v float64) float64 { return x0 + (x1-x0)*v/maxV }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="viz-svg" viewBox="0 0 %d %d" role="img" aria-label="%s compared with %s">`, w, h, esc(curName), esc(baseName))
	// Legend: compared, then selected.
	fmt.Fprintf(&b, `<circle cx="%d" cy="10" r="4.5" fill="var(--viz-base)"/><text class="viz-legend" x="%d" y="15">%s</text>`, labelW+5, labelW+14, esc(baseName))
	off := labelW + 14 + 8*len(baseName) + 22
	fmt.Fprintf(&b, `<circle cx="%d" cy="10" r="5" fill="var(--viz-cur)"/><text class="viz-legend" x="%d" y="15">%s</text>`, off, off+9, esc(curName))
	// Grid at 0, ½, max.
	for _, t := range []float64{0, maxV / 2, maxV} {
		cls := "viz-grid"
		if t == 0 {
			cls = "viz-axis"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%.1f" y1="%d" x2="%.1f" y2="%d"/>`, cls, x(t), top-4, x(t), h-22)
		label := axisFmt(t)
		if t == maxV && axisUnit != "" {
			label += " " + axisUnit
		}
		fmt.Fprintf(&b, `<text class="viz-tick" x="%.1f" y="%d" text-anchor="middle">%s</text>`, x(t), h-8, esc(label))
	}
	for i, r := range rows {
		cy := float64(top + rowH*i + rowH/2)
		label := r.Label
		if len(label) > 24 {
			label = label[:23] + "…"
		}
		fmt.Fprintf(&b, `<text class="viz-label" x="%d" y="%.1f">%s</text>`, 0, cy+4, esc(label))
		if r.HasBase && r.HasCur {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="var(--viz-base)" stroke-width="2" stroke-linecap="round"/>`, x(r.Base), cy, x(r.Cur), cy)
		}
		if r.HasBase {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4.5" fill="var(--viz-base)" class="viz-ring"/>`, x(r.Base), cy)
		}
		if r.HasCur {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="5" fill="var(--viz-cur)" class="viz-ring"/>`, x(r.Cur), cy)
		}
		cur := "–"
		if r.HasCur {
			cur = r.CurText
		}
		fmt.Fprintf(&b, `<text class="viz-value" x="%.1f" y="%.1f">%s</text>`, x1+12, cy+4, esc(cur))
		if r.Change.Change != "" {
			fmt.Fprintf(&b, `<text class="viz-delta viz-delta-%s" x="%d" y="%.1f" text-anchor="end">%s</text>`, toneOr(r.Change.Tone), w, cy+4, esc(arrow(r.Change)+" "+r.Change.Change))
		}
		t := tooltip{Title: r.Label}
		if r.HasCur {
			t.Rows = append(t.Rows, tooltipRow{Value: r.CurText, Label: curName, Key: "var(--viz-cur)"})
		}
		if r.HasBase {
			t.Rows = append(t.Rows, tooltipRow{Value: r.BaseText, Label: baseName, Key: "var(--viz-base)"})
		}
		fmt.Fprintf(&b, `<g class="viz-col"%s><rect class="viz-hitbox viz-rowhit" x="0" y="%.1f" width="%d" height="%d"/></g>`, ttAttr(t), cy-rowH/2, w, rowH)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// rankedBar is one item of a ranked horizontal bar chart.
type rankedBar struct {
	Label string
	Value float64
	Text  string
}

// rankedBars draws horizontal bars, largest first, one hue, value at the tip.
func rankedBars(rows []rankedBar, w int) template.HTML {
	if len(rows) == 0 {
		return ""
	}
	const rowH, labelW, thick = 32, 185, 14
	var maxV float64
	for _, r := range rows {
		maxV = math.Max(maxV, r.Value)
	}
	if maxV == 0 {
		maxV = 1
	}
	h := rowH*len(rows) + 4
	plotW := float64(w - labelW - 60)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="viz-svg" viewBox="0 0 %d %d" role="img" aria-label="ranked values">`, w, h)
	fmt.Fprintf(&b, `<line class="viz-axis" x1="%d" y1="0" x2="%d" y2="%d"/>`, labelW, labelW, h)
	for i, r := range rows {
		cy := float64(rowH*i + rowH/2)
		label := r.Label
		if len(label) > 24 {
			label = label[:23] + "…"
		}
		fmt.Fprintf(&b, `<text class="viz-label" x="0" y="%.1f">%s</text>`, cy+4, esc(label))
		bw := plotW * r.Value / maxV
		if bw >= 1 {
			rr := math.Min(4, bw)
			x0, x1 := float64(labelW), float64(labelW)+bw
			y0, y1 := cy-thick/2, cy+thick/2
			fmt.Fprintf(&b, `<path fill="var(--viz-s1)" d="M%.1f,%.1f L%.1f,%.1f Q%.1f,%.1f %.1f,%.1f L%.1f,%.1f Q%.1f,%.1f %.1f,%.1f L%.1f,%.1f Z"/>`,
				x0, y0, x1-rr, y0, x1, y0, x1, y0+rr, x1, y1-rr, x1, y1, x1-rr, y1, x0, y1)
		}
		fmt.Fprintf(&b, `<text class="viz-value" x="%.1f" y="%.1f">%s</text>`, float64(labelW)+bw+6, cy+4, esc(r.Text))
		fmt.Fprintf(&b, `<g class="viz-col"%s><rect class="viz-hitbox viz-rowhit" x="0" y="%.1f" width="%d" height="%d"/></g>`,
			ttAttr(tooltip{Title: r.Label, Rows: []tooltipRow{{Value: r.Text, Label: "calls", Key: "var(--viz-s1)"}}}), cy-rowH/2, w, rowH)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func toneOr(t string) string {
	if t == "" {
		return "neutral"
	}
	return t
}

// arrow is the direction glyph of a change, so it never reads by colour
// alone.
func arrow(c cell) string {
	switch {
	case strings.HasPrefix(c.Change, "+"):
		return "▲"
	case strings.HasPrefix(c.Change, "−"):
		return "▼"
	}
	return ""
}
