package eval

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// fakeBackend is an in-memory search stack: one point per document and a
// bag-of-words "embedding", so the eval pipeline runs without Qdrant or a
// model. Its rankings are crude but deterministic.
type fakeBackend struct {
	mu    sync.Mutex
	colls map[string][]fakePoint
}

type fakePoint struct {
	doc    models.SourceDocument
	vector []float32
}

const fakeDims = 512

func fakeEmbed(text string) []float32 {
	v := make([]float32, fakeDims)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) < 3 {
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%fakeDims]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x * x)
	}
	if norm > 0 {
		for i := range v {
			v[i] /= float32(math.Sqrt(norm))
		}
	}
	return v
}

func (f *fakeBackend) Reset(_ context.Context, coll string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.colls, coll)
	return nil
}

func (f *fakeBackend) Exists(_ context.Context, coll string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.colls[coll]) > 0
}

func (f *fakeBackend) Index(_ context.Context, coll string, docs []models.SourceDocument) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.colls == nil {
		f.colls = map[string][]fakePoint{}
	}
	for _, d := range docs {
		f.colls[coll] = append(f.colls[coll], fakePoint{doc: d, vector: fakeEmbed(d.Text)})
	}
	return nil
}

func (f *fakeBackend) Embed(_ context.Context, text string) ([]float32, error) {
	return fakeEmbed(text), nil
}

func matches(d models.SourceDocument, conds []rag.FieldCondition) bool {
	for _, c := range conds {
		field := strings.TrimPrefix(c.Key, models.FilterPrefix)
		if !slices.ContainsFunc(c.Match.Any, func(v string) bool { return slices.Contains(d.Filters.Fields[field], v) }) {
			return false
		}
	}
	return true
}

func (f *fakeBackend) Search(_ context.Context, coll string, vector []float32, limit int, conds []rag.FieldCondition) ([]models.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.SearchResult
	for _, p := range f.colls[coll] {
		if !matches(p.doc, conds) {
			continue
		}
		var dot float64
		for i := range vector {
			dot += float64(vector[i] * p.vector[i])
		}
		out = append(out, models.SearchResult{SourceDocID: p.doc.ID, Score: dot, Text: p.doc.Text, Properties: p.doc.Properties})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeBackend) Points(_ context.Context, coll string, conds []rag.FieldCondition) ([]PointFields, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PointFields
	for _, p := range f.colls[coll] {
		if matches(p.doc, conds) {
			out = append(out, PointFields{DocID: p.doc.ID, Fields: p.doc.Filters.Fields})
		}
	}
	return out, nil
}

func loadSynthetic(t *testing.T) *Dataset {
	t.Helper()
	ds, err := LoadDataset(BuiltinSynthetic, "")
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func TestSyntheticDatasetIsValid(t *testing.T) {
	ds := loadSynthetic(t)
	if len(ds.Companies) != 3 {
		t.Fatalf("companies = %d, want 3", len(ds.Companies))
	}
	for _, c := range ds.Companies {
		perIntent := map[string]int{}
		for _, d := range c.Documents {
			perIntent[d.Intent]++
		}
		for _, q := range c.Queries {
			if len(c.Scopes[q.Intent]) == 0 {
				t.Errorf("%s: no scope for intent %s (query %s)", c.ID, q.Intent, q.ID)
			}
			// A question must have competition: same-topic documents of
			// other intents are what a merged layout has to keep apart.
			if perIntent[q.Intent] < 2 {
				t.Errorf("%s: intent %s has fewer than 2 documents", c.ID, q.Intent)
			}
		}
	}
}

// Every question's relevant documents must be reachable under every
// layout's targets (right collection, filters matching): a scope that
// excluded the answer would penalize the layout for a labelling mistake.
func TestRelevantDocumentsAreReachableUnderEveryLayout(t *testing.T) {
	ds := loadSynthetic(t)
	for ci := range ds.Companies {
		c := &ds.Companies[ci]
		docs := map[string]Document{}
		for _, d := range c.Documents {
			docs[d.ID] = d
		}
		for _, q := range c.Queries {
			for _, l := range Layouts {
				for _, id := range q.Relevant {
					d := docs[id]
					sd := sourceDocument(l.Index, c.ID, d)
					reachable := slices.ContainsFunc(l.Targets(c, q), func(tg Target) bool {
						return tg.Collection == collectionFor(l.Index, d) && matches(sd, conditions(tg.Filters))
					})
					if !reachable {
						t.Errorf("%s/%s: %s is not reachable under %s", c.ID, q.ID, id, l.Name)
					}
				}
			}
		}
	}
}

// The ideal-agent scopes must not just keep the answer but actually narrow:
// each one should exclude at least one document of another intent that the
// unfiltered merged search would see.
func TestScopesNarrowTheMergedCollections(t *testing.T) {
	ds := loadSynthetic(t)
	for ci := range ds.Companies {
		c := &ds.Companies[ci]
		for intent, scopes := range c.Scopes {
			excluded := 0
			for _, d := range c.Documents {
				if d.Intent == intent {
					continue
				}
				sd := sourceDocument(IndexMerged, c.ID, d)
				inScope := slices.ContainsFunc(scopes, func(s Scope) bool {
					return s.Collection == mergedCollection(d) && matches(sd, conditions(s.Filters))
				})
				if !inScope {
					excluded++
				}
			}
			if excluded == 0 {
				t.Errorf("%s: scope for %s excludes nothing", c.ID, intent)
			}
			// …and keeps every document of its intent.
			for _, d := range c.Documents {
				if d.Intent != intent {
					continue
				}
				sd := sourceDocument(IndexMerged, c.ID, d)
				if !slices.ContainsFunc(scopes, func(s Scope) bool {
					return s.Collection == mergedCollection(d) && matches(sd, conditions(s.Filters))
				}) {
					t.Errorf("%s: scope for %s drops %s", c.ID, intent, d.ID)
				}
			}
		}
	}
}

func TestIsTestPath(t *testing.T) {
	cases := map[string]bool{
		"tests/Storefront.Tests/Identity/PasswordResetServiceTests.cs": true,
		"internal/refunds/service_test.go":                             true,
		"src/sync/offlineQueue.spec.ts":                                true,
		"src/components/Button.test.tsx":                               true,
		"src/test/java/com/acme/OrderServiceTest.java":                 true,
		"app/__tests__/cart.js":                                        true,
		"tests/test_orders.py":                                         true,
		`src\Storefront.Tests\CartTests.cs`:                            true,
		"src/Storefront/Identity/PasswordResetService.cs":              false,
		"internal/refunds/service.go":                                  false,
		"src/sync/offlineQueue.ts":                                     false,
		"docs/test-cases/TC-031-partial-refunds.md":                    false,
		"src/testing/helpers.go":                                       false,
		"src/attestation/verify.ts":                                    false,
	}
	for p, want := range cases {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestScoreRanking(t *testing.T) {
	rank, s := scoreRanking([]string{"a", "b", "r1", "c", "d", "e", "r2"}, []string{"r1", "r2"}, 10)
	if rank != 3 || s.Hit1 != 0 || s.RR != 1.0/3 || s.RecallAt5 != 0.5 || s.RecallAtK != 1 || s.Empty != 0 {
		t.Errorf("rank %d, scores %+v", rank, s)
	}
	rank, s = scoreRanking([]string{"r1"}, []string{"r1"}, 10)
	if rank != 1 || s.Hit1 != 1 || s.RR != 1 {
		t.Errorf("rank %d, scores %+v", rank, s)
	}
	rank, s = scoreRanking(nil, []string{"r1"}, 10)
	if rank != 0 || s.Empty != 1 || s.RR != 0 {
		t.Errorf("rank %d, scores %+v", rank, s)
	}
	// Beyond K doesn't count.
	if _, s = scoreRanking([]string{"a", "b", "r1"}, []string{"r1"}, 2); s.RecallAtK != 0 {
		t.Errorf("scores %+v, want r1 outside K=2", s)
	}
}

func runSynthetic(t *testing.T) ([]QueryResult, *Runner) {
	t.Helper()
	r := &Runner{Backend: &fakeBackend{}, Dataset: loadSynthetic(t), K: 10}
	ctx := context.Background()
	if err := r.Prepare(ctx, []string{IndexCurrent, IndexMerged}); err != nil {
		t.Fatal(err)
	}
	results, err := r.RunRetrieval(ctx, Layouts)
	if err != nil {
		t.Fatal(err)
	}
	return results, r
}

func TestRetrievalRunsEveryQuestionUnderEveryLayout(t *testing.T) {
	results, r := runSynthetic(t)
	questions := 0
	for _, c := range r.Dataset.Companies {
		questions += len(c.Queries)
	}
	if len(results) != questions*len(Layouts) {
		t.Fatalf("results = %d, want %d", len(results), questions*len(Layouts))
	}
	sum := Summarize(results)
	for _, l := range Layouts {
		if a := sum.Overall[l.Name]; a.N != questions || a.RecallAtK == 0 {
			t.Errorf("%s: %+v", l.Name, a)
		}
	}
	md := NewReport("retrieval", r.Dataset, 10, []string{"current", "merged_nofilter", "merged_fields", "merged_category"}, results, nil).Markdown()
	for _, want := range []string{"## Overall", "merged_fields", "## By intent", "## By company", "northwind"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// Filtering only removes competitors, so a filtered merged layout can never
// rank the answer worse than the unfiltered one: a basic sanity check of the
// targets and the merge by score.
func TestFilteredMergedLayoutsNeverRankWorseThanUnfiltered(t *testing.T) {
	results, _ := runSynthetic(t)
	rank := map[string]map[string]int{}
	for _, r := range results {
		if rank[r.Layout] == nil {
			rank[r.Layout] = map[string]int{}
		}
		rank[r.Layout][r.Company+"/"+r.QueryID] = r.FirstRelevantRank
	}
	worse := func(a, b int) bool { return b > 0 && (a == 0 || a > b) } // a worse than b
	for q, unfiltered := range rank["merged_nofilter"] {
		for _, l := range []string{"merged_fields", "merged_category"} {
			if worse(rank[l][q], unfiltered) {
				t.Errorf("%s: %s rank %d is worse than unfiltered %d", q, l, rank[l][q], unfiltered)
			}
		}
	}
}

func TestChangesListsRegressionsFirst(t *testing.T) {
	results := []QueryResult{
		{Layout: "base", Company: "c", QueryID: "q1", FirstRelevantRank: 1},
		{Layout: "base", Company: "c", QueryID: "q2", FirstRelevantRank: 3},
		{Layout: "base", Company: "c", QueryID: "q3", FirstRelevantRank: 2},
		{Layout: "new", Company: "c", QueryID: "q1", FirstRelevantRank: 0, Ranked: []string{"x", "y", "z", "w"}},
		{Layout: "new", Company: "c", QueryID: "q2", FirstRelevantRank: 1, Ranked: []string{"r"}},
		{Layout: "new", Company: "c", QueryID: "q3", FirstRelevantRank: 2, Ranked: []string{"x", "r"}},
	}
	ch := Changes(results, "base")
	if len(ch) != 2 || ch[0].QueryID != "q1" || ch[1].QueryID != "q2" {
		t.Fatalf("changes = %+v", ch)
	}
	if strings.Join(ch[0].Above, ",") != "x,y,z" {
		t.Errorf("above = %v, want the top 3 when the answer was lost", ch[0].Above)
	}
}

func TestAgentToolsSearchAndFacets(t *testing.T) {
	_, r := runSynthetic(t)
	ctx := context.Background()
	tools, err := agentToolsFor("merged_fields")
	if err != nil {
		t.Fatal(err)
	}
	workItems := tools[0]

	out, _, err := r.runTool(ctx, "northwind", IndexMerged, workItems, json.RawMessage(`{"mode":"facets","fields":["work_item_type"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var facets struct {
		Documents int `json:"documents"`
		Facets    map[string][]struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"facets"`
	}
	if err := json.Unmarshal([]byte(out), &facets); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, v := range facets.Facets["work_item_type"] {
		got[v.Value] = v.Count
	}
	if facets.Documents != 15 || got["product requirement"] != 5 || got["test case"] != 5 {
		t.Errorf("facets = %s", out)
	}

	_, ids, err := r.runTool(ctx, "northwind", IndexMerged, workItems,
		json.RawMessage(`{"mode":"semantic_search","query":"password reset link expiry","filters":{"work_item_type":"Product Requirement"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 || ids[0] != "nw-req-reset" {
		t.Errorf("ids = %v, want nw-req-reset first", ids)
	}

	if _, _, err := r.runTool(ctx, "northwind", IndexMerged, workItems, json.RawMessage(`{"mode":"semantic_search","query":"x","filters":{"category":"requirement"}}`)); err == nil {
		t.Error("merged_fields must not accept the category filter")
	}
	current, _ := agentToolsFor("current")
	if _, _, err := r.runTool(ctx, "northwind", IndexCurrent, current[0], json.RawMessage(`{"mode":"facets","fields":["state"]}`)); err == nil {
		t.Error("today's tools have no facets mode")
	}
}

// The agent loop against a fake Messages API: one search, then an answer
// naming documents.
func TestAgentLoopScoresTheNamedDocuments(t *testing.T) {
	_, r := runSynthetic(t)
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
				"content":[{"type":"tool_use","id":"tu_1","name":"search_requirement","input":{"mode":"semantic_search","query":"password reset link valid"}}],
				"stop_reason":"tool_use","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":80}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"text","text":"The link is valid for 30 minutes.\nANSWER: nw-req-reset\nANSWER: nw-doc-reset"}],
			"stop_reason":"end_turn","usage":{"input_tokens":150,"output_tokens":30}}`)
	}))
	defer srv.Close()

	r.Dataset.Filter([]string{"northwind"}, []string{"nw-q1"})
	client := anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	results, err := r.RunAgent(context.Background(), client, []string{"current"}, AgentOptions{Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	res := results[0]
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	a := res.Agent
	if strings.Join(a.Answer, ",") != "nw-req-reset,nw-doc-reset" || a.AnswerRecall != 1 || a.AnswerPrecision != 0.5 || a.ToolCalls != 1 {
		t.Errorf("outcome = %+v", a)
	}
	if res.Scores.Hit1 != 1 || a.SeenRecall != 1 {
		t.Errorf("scores = %+v, seen recall %v", res.Scores, a.SeenRecall)
	}
	if a.Usage.Input != 250 || a.Usage.CacheRead != 80 {
		t.Errorf("usage = %+v", a.Usage)
	}
	// The second request carries the tool result; every request caches the
	// system prompt (and so the tools before it) and sets the effort.
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	system, _ := bodies[0]["system"].([]any)
	if len(system) != 1 || system[0].(map[string]any)["cache_control"] == nil {
		t.Errorf("system = %v, want one cached block", bodies[0]["system"])
	}
	if oc, _ := bodies[0]["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("output_config = %v", bodies[0]["output_config"])
	}
	msgs, _ := bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Errorf("second request has %d messages, want question, tool use and tool result", len(msgs))
	}
}

func TestExportMapsSourceTypes(t *testing.T) {
	cases := []struct {
		src           models.SourceDefinition
		intent, shape string
	}{
		{models.SourceDefinition{Type: models.SourceRequirements, Config: map[string]string{"FetchMode": "workitems"}}, IntentRequirement, ShapeWorkItem},
		{models.SourceDefinition{Type: models.SourceRequirements}, IntentRequirement, ShapeFile},
		{models.SourceDefinition{Type: models.SourceTestCodeRepo}, IntentTestCode, ShapeFile},
		{models.SourceDefinition{Type: models.SourceWorkItemQuery}, IntentWorkItem, ShapeWorkItem},
		{models.SourceDefinition{Type: models.SourceGitCommits}, IntentCommit, ShapeCommit},
	}
	for _, c := range cases {
		if got := intentOf(c.src.Type); got != c.intent {
			t.Errorf("intentOf(%s) = %s", c.src.Type, got)
		}
		if got := shapeOf(c.src); got != c.shape {
			t.Errorf("shapeOf(%s) = %s", c.src.Type, got)
		}
	}
}

func TestDatasetValidateRejectsUnknownRelevantDocument(t *testing.T) {
	ds := &Dataset{Name: "x", Companies: []Company{{ID: "c",
		Documents: []Document{{ID: "d1", Intent: IntentCode, Shape: ShapeFile, Text: "x"}},
		Queries:   []Query{{ID: "q1", Intent: IntentCode, Text: "q", Relevant: []string{"missing"}}},
	}}}
	if err := ds.Validate(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v", err)
	}
}
