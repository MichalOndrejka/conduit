package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func testSeries(name, color string, values ...float64) series {
	sr := series{Name: name, Color: color, Values: values, Display: make([]string, len(values)), Has: make([]bool, len(values))}
	for i, v := range values {
		sr.Display[i], sr.Has[i] = compactNum(v), true
	}
	return sr
}

func TestNiceMax(t *testing.T) {
	cases := map[float64]float64{0: 1, 0.17: 0.2, 23: 25, 61: 100, 326: 500, 3100: 5000, 7.4: 10}
	for in, want := range cases {
		if got := niceMax(in); got != want {
			t.Errorf("niceMax(%v) = %v, want %v", in, got, want)
		}
	}
}

// Every stacked segment is drawn, each sized from zero (a bug once sized
// segments from the stack's moving top and dropped all but the first).
func TestStackedColumnsDrawEverySegment(t *testing.T) {
	svg := string(trendChart(trendSpec{
		XLabels: []string{"a", "b"}, XTitles: []string{"A", "B"},
		Series:   []series{testSeries("Fetch", "var(--viz-s1)", 6, 2), testSeries("Embed", "var(--viz-s3)", 2, 2)},
		Selected: 1, Baseline: 0, W: 400, H: 200, Columns: true, Stacked: true, Totals: []string{"8 min", "4 min"},
	}))
	for _, color := range []string{"var(--viz-s1)", "var(--viz-s3)"} {
		if n := strings.Count(svg, `fill="`+color+`"`); n < 3 { // 2 columns + legend swatch
			t.Errorf("%s drawn %d times, want both columns and the legend", color, n)
		}
	}
	if !strings.Contains(svg, ">4 min<") {
		t.Error("the selected column's total should be labelled with its unit")
	}
	if !strings.Contains(svg, ">Fetch<") || !strings.Contains(svg, ">Embed<") {
		t.Error("two series need a legend")
	}
}

func TestSingleSeriesHasNoLegend(t *testing.T) {
	svg := string(trendChart(trendSpec{XLabels: []string{"a", "b"}, XTitles: []string{"A", "B"},
		Series: []series{testSeries("Empty", "var(--viz-s1)", 1, 2)}, Selected: 1, Baseline: -1, W: 400, H: 200}))
	if strings.Contains(svg, `class="viz-legend"`) {
		t.Error("a single series is named by the card title, not a legend")
	}
}

// X labels never overlap, and the selected build's label always shows.
func TestXLabelsDontCollide(t *testing.T) {
	var labels []string
	values := make([]float64, 30)
	for i := 0; i < 30; i++ {
		labels = append(labels, "12:"+strconv.Itoa(10+i))
		values[i] = float64(i)
	}
	svg := string(trendChart(trendSpec{XLabels: labels, XTitles: labels, Series: []series{testSeries("v", "var(--viz-s1)", values...)},
		Selected: 28, Baseline: 27, W: 330, H: 170}))
	re := regexp.MustCompile(`class="viz-tick( viz-tick-sel)?" x="([0-9.]+)" y="1[0-9]{2}" text-anchor="middle">([^<]+)<`)
	var xs []float64
	sel := false
	for _, m := range re.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.ParseFloat(m[2], 64)
		xs = append(xs, x)
		if m[1] != "" && m[3] == labels[28] {
			sel = true
		}
	}
	if !sel {
		t.Error("the selected build's label must show")
	}
	for i := range xs {
		for j := i + 1; j < len(xs); j++ {
			if d := xs[i] - xs[j]; d > -40 && d < 40 {
				t.Errorf("labels at %.0f and %.0f overlap", xs[i], xs[j])
			}
		}
	}
}

// Tooltip data is escaped in the attribute; the page builds it with
// textContent, so a hostile label never becomes markup.
func TestTooltipContentIsEscaped(t *testing.T) {
	svg := string(rankedBars([]rankedBar{{Label: `<img src=x onerror=alert(1)>`, Value: 3, Text: "3"}}, 400))
	if strings.Contains(svg, "<img") {
		t.Fatalf("label rendered as markup: %s", svg)
	}
	if !strings.Contains(svg, "&lt;img") {
		t.Error("label should be escaped")
	}
}

func TestDumbbellShowsBothBuildsAndTheChange(t *testing.T) {
	svg := string(dumbbell([]dumbbellRow{{Label: "search_workitem", Base: 33, Cur: 40, HasBase: true, HasCur: true,
		BaseText: "33%", CurText: "40%", Change: cell{Change: "+7 pts", Tone: "bad"}}}, "compared build", "selected build", compactNum, "%", 520))
	for _, want := range []string{"var(--viz-base)", "var(--viz-cur)", ">40%<", "▲ +7 pts", "viz-delta-bad", "compared build", "selected build"} {
		if !strings.Contains(svg, want) {
			t.Errorf("dumbbell lacks %q", want)
		}
	}
}
