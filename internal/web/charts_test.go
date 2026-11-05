package web

import (
	"strings"
	"testing"
)

// Tooltip data is escaped in the attribute; the page builds it with
// textContent, so a hostile label never becomes markup.
func TestTooltipContentIsEscaped(t *testing.T) {
	svg := string(rankedBars([]rankedBar{{Label: `<img src=x onerror=alert(1)>`, Value: 3, Text: "3"}}, "calls", 400))
	if strings.Contains(svg, "<img") {
		t.Fatalf("label rendered as markup: %s", svg)
	}
	if !strings.Contains(svg, "&lt;img") {
		t.Error("label should be escaped")
	}
}

func TestRankedBarsNameTheMeasure(t *testing.T) {
	svg := string(rankedBars([]rankedBar{{Label: "search_workitem", Value: 0.4, Text: "40%"}}, "found nothing", 400))
	for _, want := range []string{">40%<", "found nothing", "search_workitem"} {
		if !strings.Contains(svg, want) {
			t.Errorf("ranked bars lack %q", want)
		}
	}
}
