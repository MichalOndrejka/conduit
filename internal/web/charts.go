// Server-rendered SVG charts for the Stats page. Each returns a ready
// <svg> (template.HTML) drawn in viewBox units close to its on-screen size,
// coloured through the CSS custom properties of .viz (input.css) so light
// and dark mode each use their own validated steps.
//
// Conventions (see the dataviz rules): thin marks, 4px rounded data-ends
// square at the baseline, one hue, the value at each bar's tip, and every
// mark carrying a hover/focus tooltip.
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

// rankedBar is one item of a ranked horizontal bar chart.
type rankedBar struct {
	Label string
	Value float64
	Text  string
}

// rankedBars draws horizontal bars in the given order, one hue, value at
// the tip; measure names the value in tooltips (e.g. "calls").
func rankedBars(rows []rankedBar, measure string, w int) template.HTML {
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
	// Room after the longest bar for the longest value text.
	textW := 60
	for _, r := range rows {
		textW = max(textW, 8*len([]rune(r.Text))+12)
	}
	h := rowH*len(rows) + 4
	plotW := float64(w - labelW - textW)
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
			ttAttr(tooltip{Title: r.Label, Rows: []tooltipRow{{Value: r.Text, Label: measure, Key: "var(--viz-s1)"}}}), cy-rowH/2, w, rowH)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// stackSeries is one series of a stacked bar chart.
type stackSeries struct {
	Name, Color string // Color is a CSS colour, e.g. var(--viz-s1)
}

// stackedBar is one row of a stacked bar chart: Values parallel to the
// chart's series, Text at the bar's tip.
type stackedBar struct {
	Label  string
	Values []float64
	Text   string
}

// stackedBars draws horizontal bars split into the series' segments, a 2px
// surface gap between segments, the legend above, the row's text at the
// tip, and a tooltip per row listing every series.
func stackedBars(rows []stackedBar, series []stackSeries, w int) template.HTML {
	if len(rows) == 0 {
		return ""
	}
	const rowH, labelW, thick, legendH, gap = 32, 185, 14, 26, 2
	var maxV float64
	for _, r := range rows {
		var sum float64
		for _, v := range r.Values {
			sum += v
		}
		maxV = math.Max(maxV, sum)
	}
	if maxV == 0 {
		maxV = 1
	}
	textW := 60
	for _, r := range rows {
		textW = max(textW, 7*len([]rune(r.Text))+12)
	}
	h := legendH + rowH*len(rows) + 4
	plotW := float64(w - labelW - textW)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="viz-svg" viewBox="0 0 %d %d" role="img" aria-label="stacked values">`, w, h)
	x := float64(labelW)
	for _, s := range series {
		fmt.Fprintf(&b, `<rect x="%.1f" y="6" width="10" height="10" rx="2" fill="%s"/>`, x, s.Color)
		fmt.Fprintf(&b, `<text class="viz-label" x="%.1f" y="15">%s</text>`, x+15, esc(s.Name))
		x += 15 + 7*float64(len([]rune(s.Name))) + 20
	}
	fmt.Fprintf(&b, `<line class="viz-axis" x1="%d" y1="%d" x2="%d" y2="%d"/>`, labelW, legendH, labelW, h)
	for i, r := range rows {
		cy := float64(legendH + rowH*i + rowH/2)
		label := r.Label
		if len(label) > 24 {
			label = label[:23] + "…"
		}
		fmt.Fprintf(&b, `<text class="viz-label" x="0" y="%.1f">%s</text>`, cy+4, esc(label))
		x0 := float64(labelW)
		y0, y1 := cy-thick/2, cy+thick/2
		last := -1
		for j, v := range r.Values {
			if plotW*v/maxV >= 1 {
				last = j
			}
		}
		tt := tooltip{Title: r.Label}
		for j, v := range r.Values {
			tt.Rows = append(tt.Rows, tooltipRow{Value: fmtCount(v), Label: series[j].Name, Key: series[j].Color})
			bw := plotW * v / maxV
			if bw < 1 {
				continue
			}
			x1 := x0 + bw
			if j == last {
				rr := math.Min(4, bw)
				fmt.Fprintf(&b, `<path fill="%s" d="M%.1f,%.1f L%.1f,%.1f Q%.1f,%.1f %.1f,%.1f L%.1f,%.1f Q%.1f,%.1f %.1f,%.1f L%.1f,%.1f Z"/>`,
					series[j].Color, x0, y0, x1-rr, y0, x1, y0, x1, y0+rr, x1, y1-rr, x1, y1, x1-rr, y1, x0, y1)
			} else {
				fmt.Fprintf(&b, `<rect fill="%s" x="%.1f" y="%.1f" width="%.1f" height="%d"/>`, series[j].Color, x0, y0, math.Max(bw-gap, 0.5), thick)
			}
			x0 = x1
		}
		fmt.Fprintf(&b, `<text class="viz-value" x="%.1f" y="%.1f">%s</text>`, x0+6, cy+4, esc(r.Text))
		fmt.Fprintf(&b, `<g class="viz-col"%s><rect class="viz-hitbox viz-rowhit" x="0" y="%.1f" width="%d" height="%d"/></g>`,
			ttAttr(tt), cy-rowH/2, w, rowH)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
