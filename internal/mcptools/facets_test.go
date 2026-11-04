package mcptools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type facetsPayload struct {
	Documents int `json:"documents"`
	Facets    map[string]struct {
		Level     int    `json:"level"`
		Prefix    string `json:"prefix"`
		More      bool   `json:"more"`
		Documents *int   `json:"documents"`
		Values    []struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"values"`
	} `json:"facets"`
	Note    string `json:"note"`
	Results []any  `json:"results"`
}

func decodeFacets(t *testing.T, text string) facetsPayload {
	t.Helper()
	var p facetsPayload
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("decoding %s: %v", text, err)
	}
	return p
}

func TestFacetsModeCountsDocumentsPerValue(t *testing.T) {
	qd := &fakeQdrant{countResult: 12, facetHits: map[string][]map[string]any{
		"f_work_item_type": {{"value": "bug", "count": 7}, {"value": "product requirement", "count": 5}},
	}}
	s, ctx := setup(t, qd, nil)
	result := callTool(t, s, ctx, "search_workitem", map[string]any{"request": map[string]any{
		"mode": "facets", "fields": []any{"work_item_type"}, "filters": map[string]any{"state": "Active"},
	}})
	if result.IsError {
		t.Fatal(resultText(t, result))
	}
	p := decodeFacets(t, resultText(t, result))
	wit := p.Facets["work_item_type"]
	if p.Documents != 12 || len(wit.Values) != 2 || wit.Values[1].Value != "product requirement" || wit.Values[1].Count != 5 || wit.More {
		t.Errorf("payload = %s", resultText(t, result))
	}
	qd.mu.Lock()
	defer qd.mu.Unlock()
	// Counted over each document's first chunk, within the filters.
	body := fmt.Sprint(qd.facetBodies[0])
	if !strings.Contains(body, "chunk_index") || !strings.Contains(body, "f_state") || qd.facetBodies[0]["limit"] != float64(26) {
		t.Errorf("facet request = %s", body)
	}
	if !strings.Contains(fmt.Sprint(qd.countBodies[0]), "chunk_index") {
		t.Errorf("count request = %v, want documents (first chunks)", qd.countBodies[0])
	}
}

func TestFacetsModeListsTheFoldersInsideAPrefix(t *testing.T) {
	qd := &fakeQdrant{countResult: 3, facetHits: map[string][]map[string]any{
		"f_path__2": {{"value": "docs/requirements", "count": 2}, {"value": "docs/test-cases", "count": 1}},
	}}
	s, ctx := setup(t, qd, nil)
	result := callTool(t, s, ctx, "search_documentation", map[string]any{"request": map[string]any{
		"mode": "facets", "fields": []any{"path"}, "prefix": "Docs/",
	}})
	if result.IsError {
		t.Fatal(resultText(t, result))
	}
	p := decodeFacets(t, resultText(t, result)).Facets["path"]
	if p.Level != 2 || p.Prefix != "docs" || len(p.Values) != 2 {
		t.Errorf("path facets = %+v", p)
	}
	qd.mu.Lock()
	defer qd.mu.Unlock()
	if !strings.Contains(fmt.Sprint(qd.facetBodies[0]["filter"]), "key:f_path match:map[value:docs]") {
		t.Errorf("facet filter = %v, want the prefix folder", qd.facetBodies[0]["filter"])
	}
}

func TestFacetsModeRejectsBadRequests(t *testing.T) {
	s, ctx := setup(t, &fakeQdrant{}, nil)
	cases := map[string]map[string]any{
		"no fields":           {"mode": "facets"},
		"unknown field":       {"mode": "facets", "fields": []any{"code_kind"}}, // not a work item field
		"date field":          {"mode": "facets", "fields": []any{"since"}},
		"prefix on flat":      {"mode": "facets", "fields": []any{"state"}, "prefix": map[string]any{"state": "a"}},
		"bad level":           {"mode": "facets", "fields": []any{"area_path"}, "level": map[string]any{"area_path": 0.5}},
		"ambiguous prefix":    {"mode": "facets", "fields": []any{"area_path", "iteration_path"}, "prefix": "x"},
		"prefix not a string": {"mode": "facets", "fields": []any{"area_path"}, "prefix": map[string]any{"area_path": 3}},
	}
	for name, req := range cases {
		result := callTool(t, s, ctx, "search_workitem", map[string]any{"request": req})
		if !result.IsError {
			t.Errorf("%s: expected an error, got %s", name, resultText(t, result))
		}
	}
}

// A filtered search that finds nothing returns, per filter used, the values
// that exist with the other filters applied.
func TestEmptyFilteredSearchReturnsFacetsForTheFiltersUsed(t *testing.T) {
	qd := &fakeQdrant{countResult: 9, facetHits: map[string][]map[string]any{
		"f_work_item_type": {{"value": "product requirement", "count": 4}},
		"f_area_path__2":   {{"value": `payments\team a`, "count": 9}},
	}}
	s, ctx := setup(t, qd, nil)
	result := callTool(t, s, ctx, "search_workitem", map[string]any{"request": map[string]any{
		"mode": "semantic_search", "query": "reset link", "page": float64(1),
		"filters": map[string]any{"work_item_type": "Requirement", "area_path": `Payments\Team X`},
	}})
	if result.IsError {
		t.Fatal(resultText(t, result))
	}
	p := decodeFacets(t, resultText(t, result))
	if len(p.Results) != 0 || !strings.Contains(p.Note, "Nothing matched these filters") {
		t.Errorf("payload = %s", resultText(t, result))
	}
	wit, area := p.Facets["work_item_type"], p.Facets["area_path"]
	if len(wit.Values) != 1 || wit.Values[0].Value != "product requirement" || wit.Documents == nil || *wit.Documents != 9 {
		t.Errorf("work_item_type hint = %+v", wit)
	}
	// A path value is counted among its siblings.
	if area.Prefix != "payments" || area.Level != 2 || len(area.Values) != 1 {
		t.Errorf("area_path hint = %+v", area)
	}
	qd.mu.Lock()
	defer qd.mu.Unlock()
	for _, b := range qd.facetBodies {
		f := fmt.Sprint(b["filter"])
		if b["key"] == "f_work_item_type" && (strings.Contains(f, "key:f_work_item_type") || !strings.Contains(f, "key:f_area_path")) {
			t.Errorf("work_item_type hint filter = %s, want the other filters only", f)
		}
	}
}

func TestEmptyUnfilteredSearchKeepsItsNote(t *testing.T) {
	qd := &fakeQdrant{}
	s, ctx := setup(t, qd, nil)
	result := callTool(t, s, ctx, "search_code", map[string]any{"request": map[string]any{
		"mode": "semantic_search", "query": "x", "page": float64(1),
	}})
	p := decodeFacets(t, resultText(t, result))
	if p.Facets != nil || !strings.Contains(p.Note, "No data embedded") {
		t.Errorf("payload = %s", resultText(t, result))
	}
	qd.mu.Lock()
	defer qd.mu.Unlock()
	if len(qd.facetBodies) != 0 {
		t.Error("no facets without filters")
	}
}

func TestEverySearchToolMentionsFacets(t *testing.T) {
	for _, spec := range SearchTools() {
		if !strings.Contains(SearchToolDescription(spec), `"mode":"facets"`) {
			t.Errorf("%s description does not explain facets", spec.Name)
		}
		if !strings.Contains(fmt.Sprint(RequestSchema(spec.Fields)), "const:facets") {
			t.Errorf("%s request schema has no facets mode", spec.Name)
		}
	}
}
