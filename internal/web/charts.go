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
