package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// memCounter is an in-memory FacetCounter over documents' payload values.
type memCounter struct {
	docs  []map[string][]string // payload key → values
	asked []int                 // limits FacetValues was called with
}

func (m *memCounter) match(d map[string][]string, must []FieldCondition) bool {
	for _, c := range must {
		want, _ := c.Match.Value.(string)
		found := false
		for _, v := range d[c.Key] {
			found = found || v == want
		}
		if !found {
			return false
		}
	}
	return true
}

func (m *memCounter) FacetValues(_ context.Context, _, key string, must []FieldCondition, limit int) ([]FacetValue, error) {
	m.asked = append(m.asked, limit)
	counts := map[string]int{}
	for _, d := range m.docs {
		if m.match(d, must) {
			for _, v := range d[key] {
				counts[v]++
			}
		}
	}
	var out []FacetValue
	for v, n := range counts {
		out = append(out, FacetValue{v, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memCounter) CountDocuments(_ context.Context, _ string, must []FieldCondition) (int, error) {
	n := 0
	for _, d := range m.docs {
		if m.match(d, must) {
			n++
		}
	}
	return n, nil
}

// fileDoc is a document's payload values for file paths, as the indexer
// writes them (every ancestor, plus one key per level).
func fileDoc(extra map[string][]string, paths ...string) map[string][]string {
	var f models.DocumentFilters
	f.Add(models.FilterPath, paths...)
	d := map[string][]string{models.FilterKey(models.FilterPath): f.Fields[models.FilterPath]}
	for level, vs := range models.FilterLevels(models.FilterPath, f.Fields[models.FilterPath]) {
		d[models.FilterLevelKey(models.FilterPath, level)] = vs
	}
	for k, v := range extra {
		d[models.FilterKey(k)] = v
	}
	return d
}

func TestFacetsCountValuesAndCapThem(t *testing.T) {
	m := &memCounter{}
	for i := 0; i < 30; i++ {
		m.docs = append(m.docs, map[string][]string{models.FilterKey("tags"): {fmt.Sprintf("t%02d", i), "common"}})
	}
	res, err := Facets(context.Background(), m, "c", FacetRequest{Fields: []FacetField{{Name: "tags"}}})
	if err != nil {
		t.Fatal(err)
	}
	tags := res.Facets["tags"]
	if res.Documents != 30 || len(tags.Values) != FacetLimit || !tags.More || tags.Values[0] != (FacetValue{"common", 30}) {
		t.Errorf("facets = %+v", res)
	}
	if _, err := Facets(context.Background(), m, "c", FacetRequest{Fields: []FacetField{{Name: "tags", Prefix: "x"}}}); err == nil {
		t.Error("prefix on a flat field must be rejected")
	}
}

func TestFacetsListOneLevelOfAHierarchicalField(t *testing.T) {
	m := &memCounter{docs: []map[string][]string{
		fileDoc(nil, "docs/requirements/r1.md"),
		fileDoc(nil, "docs/requirements/r2.md"),
		fileDoc(nil, "docs/test-cases/t1.md"),
		fileDoc(nil, "src/main.go"),
	}}
	ctx := context.Background()
	top, err := Facets(ctx, m, "c", FacetRequest{Fields: []FacetField{{Name: "path"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p := top.Facets["path"]; p.Level != 1 || fmt.Sprint(p.Values) != "[{docs 3} {src 1}]" {
		t.Errorf("top level = %+v", p)
	}
	inside, err := Facets(ctx, m, "c", FacetRequest{Fields: []FacetField{{Name: "path", Prefix: "docs"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p := inside.Facets["path"]; p.Level != 2 || p.Prefix != "docs" || fmt.Sprint(p.Values) != "[{docs/requirements 2} {docs/test-cases 1}]" {
		t.Errorf("inside docs = %+v", p)
	}
	files, _ := Facets(ctx, m, "c", FacetRequest{Fields: []FacetField{{Name: "path", Prefix: "docs", Level: 3}}})
	if n := len(files.Facets["path"].Values); n != 3 {
		t.Errorf("level 3 inside docs = %+v", files.Facets["path"])
	}
	if _, err := Facets(ctx, m, "c", FacetRequest{Fields: []FacetField{{Name: "path", Prefix: "docs/requirements", Level: 1}}}); err == nil {
		t.Error("a level above the prefix must be rejected")
	}
}

// A commit touching several folders carries values outside the prefix; they
// are dropped, asking for more values until enough remain.
func TestFacetsPrefixDropsValuesOfOtherFoldersAndAsksForMore(t *testing.T) {
	m := &memCounter{}
	for i := 0; i < 40; i++ {
		// Each commit touches its own lib/ folder (so lib/* outranks) and
		// one file under src/.
		m.docs = append(m.docs, fileDoc(nil, fmt.Sprintf("lib/l%02d/x.go", i%3), fmt.Sprintf("src/s%02d.go", i)))
	}
	res, err := Facets(context.Background(), m, "c", FacetRequest{Fields: []FacetField{{Name: "path", Prefix: "src"}}})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Facets["path"]
	if len(p.Values) != FacetLimit || !p.More {
		t.Errorf("values = %d more=%v", len(p.Values), p.More)
	}
	for _, v := range p.Values {
		if !strings.HasPrefix(v.Value, "src/") {
			t.Errorf("value %q is outside src/", v.Value)
		}
	}
	if len(m.asked) < 2 {
		t.Errorf("asked %v, want a second, larger request", m.asked)
	}
}

// The store counter counts documents, not chunks (first chunk only), and
// leaves disabled sources out.
func TestStoreFacetCounterCountsDocumentsOfEnabledSources(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies[r.URL.Path] = body
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/facet"):
			_, _ = w.Write([]byte(`{"result":{"hits":[{"value":"bug","count":3},{"value":"task","count":1}]}}`))
		case strings.HasSuffix(r.URL.Path, "/points/count"):
			_, _ = w.Write([]byte(`{"result":{"count":4}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := NewStoreFacetCounter(storeFor(t, srv), []string{"disabled-src"})
	ctx := context.Background()
	must := []FieldCondition{{Key: "f_state", Match: Match{Value: "active"}}}
	vals, err := c.FacetValues(ctx, "conduit_workitems", "f_work_item_type", must, 26)
	if err != nil || fmt.Sprint(vals) != "[{bug 3} {task 1}]" {
		t.Fatalf("values = %v, %v", vals, err)
	}
	if n, err := c.CountDocuments(ctx, "conduit_workitems", must); err != nil || n != 4 {
		t.Fatalf("count = %d, %v", n, err)
	}
	facet := bodies["/collections/conduit_workitems/facet"]
	if facet["key"] != "f_work_item_type" || facet["limit"] != float64(26) || facet["exact"] != true {
		t.Errorf("facet body = %v", facet)
	}
	for path, body := range bodies {
		f := fmt.Sprint(body["filter"])
		if !strings.Contains(f, "chunk_index") || !strings.Contains(f, "f_state") || !strings.Contains(f, "disabled-src") {
			t.Errorf("%s filter = %s, want first chunk + the filters − disabled sources", path, f)
		}
	}
}

func TestFacetCreatesAMissingIndexAndRetries(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/facet") && len(calls) == 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":{"error":"Bad request: Index required but not found for \"f_path__2\""}}`))
		case strings.HasSuffix(r.URL.Path, "/facet"):
			_, _ = w.Write([]byte(`{"result":{"hits":[{"value":"docs/a","count":2}]}}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"result":{"payload_schema":{}}}`))
		default:
			_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
		}
	}))
	defer srv.Close()
	hits, err := storeFor(t, srv).Facet(context.Background(), "c", "f_path__2", nil, 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %v, %v", hits, err)
	}
	if want := "POST /collections/c/facet,GET /collections/c,PUT /collections/c/index,POST /collections/c/facet"; strings.Join(calls, ",") != want {
		t.Errorf("calls = %v", calls)
	}
}

func TestEnsurePayloadIndexesCreatesOnlyMissing(t *testing.T) {
	ir := &indexRequests{}
	srv := httptest.NewServer(ir.wrap(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"payload_schema":{"f_state":{"data_type":"keyword"}}}}`))
	}))
	defer srv.Close()
	err := storeFor(t, srv).EnsurePayloadIndexes(context.Background(), "c", map[string]string{
		"f_state": models.IndexKeyword, "f_tags": models.IndexKeyword, "f_date": models.IndexInteger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ir.has("c", "f_state"); ok {
		t.Error("an existing index must not be recreated")
	}
	if s, _ := ir.has("c", "f_tags"); s != "keyword" {
		t.Errorf("f_tags = %q", s)
	}
	if s, _ := ir.has("c", "f_date"); s != "integer" {
		t.Errorf("f_date = %q", s)
	}
}

func TestRemoveFromLegacyCollectionsDropsThemOnceEmpty(t *testing.T) {
	var deletedPoints []string
	var dropped []string
	remaining := map[string]int{"conduit_requirements": 0, "conduit_testcode": 7}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coll := strings.Split(strings.TrimPrefix(r.URL.Path, "/collections/"), "/")[0]
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			_, _ = w.Write([]byte(`{"result":{"collections":[{"name":"conduit_requirements"},{"name":"conduit_testcode"},{"name":"conduit_code"}]}}`))
		case strings.HasSuffix(r.URL.Path, "/points/delete"):
			deletedPoints = append(deletedPoints, coll)
			_, _ = w.Write([]byte(`{"result":true}`))
		case strings.HasSuffix(r.URL.Path, "/points/count"):
			fmt.Fprintf(w, `{"result":{"count":%d}}`, remaining[coll])
		case r.Method == http.MethodDelete:
			dropped = append(dropped, coll)
			_, _ = w.Write([]byte(`{"result":true}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	storeFor(t, srv).RemoveFromLegacyCollections(context.Background(), "src-1")
	if strings.Join(deletedPoints, ",") != "conduit_requirements,conduit_testcode" {
		t.Errorf("deleted points from %v", deletedPoints)
	}
	if strings.Join(dropped, ",") != "conduit_requirements" {
		t.Errorf("dropped %v, want only the emptied one", dropped)
	}
}
