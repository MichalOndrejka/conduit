package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag/ragtest"
)

const incColl = "test_coll"

type incHarness struct {
	t       *testing.T
	cfg     *config.AppConfig
	qd      *ragtest.Qdrant
	embeds  atomic.Int32 // embedding calls, the probe excluded
	indexer *DocumentIndexer
}

func newIncHarness(t *testing.T) *incHarness {
	t.Helper()
	h := &incHarness{t: t, qd: ragtest.New()}
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "m:latest", "digest": "d1"}}})
			return
		}
		var body struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Input != "dimension probe" {
			h.embeds.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float32{float32(len(body.Input)), 1, 2}}}})
	}))
	t.Cleanup(embedSrv.Close)
	qdSrv := httptest.NewServer(h.qd.Handler())
	t.Cleanup(qdSrv.Close)

	h.cfg = &config.AppConfig{}
	h.cfg.Qdrant.URL = qdSrv.URL
	h.cfg.Embedding.BaseURL = embedSrv.URL + "/v1"
	h.cfg.Embedding.Model = "m"
	h.cfg.Embedding.MaxInputTokens = 8192
	h.cfg.Embedding.Concurrency = 4
	h.cfg.Chunking.MaxChunkSize = 100
	h.cfg.Chunking.Overlap = 10
	h.rebuild()
	return h
}

// rebuild recreates the indexer from cfg (as a restart does).
func (h *incHarness) rebuild() {
	h.indexer = NewDocumentIndexer(NewVectorStore(h.cfg), NewEmbeddingService(h.cfg), NewTextChunker(h.cfg))
}

// sync runs one incremental pass and returns the plan and result.
func (h *incHarness) sync(docs []models.SourceDocument, opts ApplyOptions) (*SyncPlan, ApplyResult, error) {
	h.t.Helper()
	ctx := context.Background()
	fp, err := h.indexer.Prepare(ctx, incColl, "")
	if err != nil {
		h.t.Fatal(err)
	}
	state, err := h.indexer.LoadState(ctx, incColl, "src")
	if err != nil {
		h.t.Fatal(err)
	}
	plan, err := h.indexer.Diff(docs, state, fp)
	if err != nil {
		return nil, ApplyResult{}, err
	}
	res, err := h.indexer.Apply(ctx, incColl, "src", plan, opts)
	return plan, res, err
}

func (h *incHarness) mustSync(docs []models.SourceDocument) (*SyncPlan, ApplyResult) {
	h.t.Helper()
	plan, res, err := h.sync(docs, ApplyOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	return plan, res
}

func incDoc(id, text string) models.SourceDocument {
	return models.SourceDocument{
		ID: id, Text: text,
		Tags:       map[string]string{"source_id": "src", "source_name": "S"},
		Properties: map[string]string{"title": id},
	}
}

func counts(p *SyncPlan) [5]int {
	return [5]int{p.Added, len(p.Embed) - p.Added, len(p.MetaOnly), p.Unchanged, len(p.Deleted)}
}

func TestIncrementalSecondSyncEmbedsNothing(t *testing.T) {
	h := newIncHarness(t)
	docs := []models.SourceDocument{incDoc("a", "alpha"), incDoc("b", strings.Repeat("beta. ", 50))}
	plan, res := h.mustSync(docs)
	if counts(plan) != [5]int{2, 0, 0, 0, 0} {
		t.Fatalf("first sync = %v", counts(plan))
	}
	first := h.embeds.Load()
	if first == 0 || res.Chunks != h.qd.Count(incColl, "", nil) || res.Embedded != res.Chunks {
		t.Fatalf("embeds %d, result %+v, points %d", first, res, h.qd.Count(incColl, "", nil))
	}

	plan, res = h.mustSync(docs)
	if counts(plan) != [5]int{0, 0, 0, 2, 0} || h.embeds.Load() != first || res.Embedded != 0 {
		t.Errorf("second sync = %v, embeds %d → %d, result %+v", counts(plan), first, h.embeds.Load(), res)
	}
}

func TestIncrementalCreateUpdateDelete(t *testing.T) {
	h := newIncHarness(t)
	h.mustSync([]models.SourceDocument{incDoc("a", "alpha"), incDoc("b", "beta"), incDoc("c", "gamma")})
	before := h.embeds.Load()

	plan, _ := h.mustSync([]models.SourceDocument{incDoc("a", "alpha"), incDoc("b", "beta changed"), incDoc("d", "delta")})
	if counts(plan) != [5]int{1, 1, 0, 1, 1} {
		t.Fatalf("sync = %v", counts(plan))
	}
	if got := h.embeds.Load() - before; got != 2 {
		t.Errorf("embedded %d chunks, want 2", got)
	}
	payloads := h.qd.Payloads(incColl)
	texts := map[string]string{}
	for _, p := range payloads {
		texts[p[models.PayloadSourceDocID].(string)] = p[models.PayloadText].(string)
	}
	want := map[string]string{"a": "alpha", "b": "beta changed", "d": "delta"}
	if len(texts) != 3 || texts["a"] != want["a"] || texts["b"] != want["b"] || texts["d"] != want["d"] {
		t.Errorf("indexed = %v, want %v", texts, want)
	}
}

func TestIncrementalChunkCountChanges(t *testing.T) {
	h := newIncHarness(t)
	long := strings.Repeat("A sentence here. ", 30) // several chunks
	_, res := h.mustSync([]models.SourceDocument{incDoc("a", long)})
	if res.Chunks < 3 {
		t.Fatalf("chunks = %d, want several", res.Chunks)
	}
	// Several chunks → one: the single-chunk point ID differs from every
	// chunk ID, so all old points must go.
	_, res = h.mustSync([]models.SourceDocument{incDoc("a", "short")})
	if res.Chunks != 1 || h.qd.Count(incColl, "", nil) != 1 {
		t.Fatalf("after shrink: result %+v, points %d", res, h.qd.Count(incColl, "", nil))
	}
	if _, ok := h.qd.Payloads(incColl)[makeID("a")]; !ok {
		t.Error("single chunk not stored under the document's point ID")
	}
	// And back to several.
	_, res = h.mustSync([]models.SourceDocument{incDoc("a", long)})
	if h.qd.Count(incColl, "", nil) != res.Chunks {
		t.Errorf("after growth: points %d, want %d", h.qd.Count(incColl, "", nil), res.Chunks)
	}
	if _, ok := h.qd.Payloads(incColl)[makeID("a")]; ok {
		t.Error("stale single-chunk point kept")
	}
}

func TestIncrementalMetadataOnlyKeepsVectors(t *testing.T) {
	h := newIncHarness(t)
	d := incDoc("a", "alpha")
	d.Filters.Add(models.FilterVersion, "main")
	h.mustSync([]models.SourceDocument{d})
	before := h.embeds.Load()

	d2 := incDoc("a", "alpha")
	d2.Filters.Add(models.FilterVersion, "main")
	d2.Filters.Add(models.FilterVersion, "v1.0")
	d2.Properties["version"] = "main, v1.0"
	plan, _ := h.mustSync([]models.SourceDocument{d2})
	if counts(plan) != [5]int{0, 0, 1, 0, 0} || h.embeds.Load() != before {
		t.Fatalf("sync = %v, embeds %d → %d", counts(plan), before, h.embeds.Load())
	}
	var p *ragtest.Point
	h.qd.Mutate(func(cols map[string]map[string]*ragtest.Point) { p = cols[incColl][makeID("a")] })
	if p.Payload[models.PropKey("version")] != "main, v1.0" || len(p.Vector) != 3 || p.Payload[models.PayloadText] != "alpha" {
		t.Errorf("point = %+v", p)
	}
	if plan, _ := h.mustSync([]models.SourceDocument{d2}); counts(plan) != [5]int{0, 0, 0, 1, 0} {
		t.Errorf("third sync = %v, want unchanged", counts(plan))
	}
}

func TestIncrementalReembedsLegacyAndIncompleteDocs(t *testing.T) {
	h := newIncHarness(t)
	long := strings.Repeat("A sentence here. ", 30)
	docs := []models.SourceDocument{incDoc("a", "alpha"), incDoc("b", long)}
	h.mustSync(docs)
	h.qd.Mutate(func(cols map[string]map[string]*ragtest.Point) {
		delete(cols[incColl][makeID("a")].Payload, models.PayloadContentHash) // written by an older version
		delete(cols[incColl], makeChunkID("b", 1))                            // lost a chunk
	})
	plan, res := h.mustSync(docs)
	if counts(plan) != [5]int{0, 2, 0, 0, 0} {
		t.Fatalf("sync = %v, want both re-embedded", counts(plan))
	}
	if h.qd.Count(incColl, "", nil) != res.Chunks {
		t.Errorf("points %d, want %d", h.qd.Count(incColl, "", nil), res.Chunks)
	}
}

func TestIncrementalSettingsChangeReembedsEverything(t *testing.T) {
	h := newIncHarness(t)
	docs := []models.SourceDocument{incDoc("a", "alpha"), incDoc("b", "beta")}
	h.mustSync(docs)

	h.cfg.Chunking.MaxChunkSize = 90
	h.rebuild()
	if plan, _ := h.mustSync(docs); counts(plan) != [5]int{0, 2, 0, 0, 0} {
		t.Errorf("after a chunking change = %v, want both re-embedded", counts(plan))
	}
	h.cfg.Embedding.Model = "m2" // a model not listed: no digest
	if plan, _ := h.mustSync(docs); counts(plan) != [5]int{0, 2, 0, 0, 0} {
		t.Errorf("after a model change = %v, want both re-embedded", counts(plan))
	}
}

func TestIncrementalWriteFailureHeals(t *testing.T) {
	h := newIncHarness(t)
	var docs []models.SourceDocument
	for _, id := range []string{"a", "b", "c"} {
		docs = append(docs, incDoc(id, strings.Repeat(id+" text. ", 60)))
	}
	h.mustSync(docs)
	changed := make([]models.SourceDocument, len(docs))
	for i, d := range docs {
		changed[i] = incDoc(d.ID, strings.Repeat(d.ID+" new text. ", 700)) // one upsert batch each
	}
	h.qd.FailUpsertAfter = h.qd.Requests["PUT /collections/{name}/points"] + 1
	if _, _, err := h.sync(changed, ApplyOptions{}); err == nil {
		t.Fatal("want the injected write failure")
	}
	h.qd.FailUpsertAfter = 0
	plan, res := h.mustSync(changed)
	if plan.Unchanged+len(plan.Embed) != 3 || len(plan.Embed) == 0 {
		t.Errorf("heal sync = %v", counts(plan))
	}
	if n := h.qd.Count(incColl, "", nil); n != res.Chunks {
		t.Errorf("points %d, want %d", n, res.Chunks)
	}
	for _, p := range h.qd.Payloads(incColl) {
		if !strings.Contains(p[models.PayloadText].(string), "new text") {
			t.Errorf("stale chunk left: %q", p[models.PayloadText])
		}
	}
	if plan, _ := h.mustSync(changed); counts(plan) != [5]int{0, 0, 0, 3, 0} {
		t.Errorf("after healing = %v, want all unchanged", counts(plan))
	}
}

func TestIncrementalDegradedPreprocessingRetries(t *testing.T) {
	h := newIncHarness(t)
	docs := []models.SourceDocument{incDoc("a", "alpha"), incDoc("b", "beta")}
	if _, _, err := h.sync(docs, ApplyOptions{Degraded: []bool{true, false}}); err != nil {
		t.Fatal(err)
	}
	if plan, _ := h.mustSync(docs); counts(plan) != [5]int{0, 1, 0, 1, 0} || plan.Embed[0].ID != "a" {
		t.Errorf("sync = %v, want the degraded document re-embedded", counts(plan))
	}
}

func TestIncrementalKeepAndSeal(t *testing.T) {
	h := newIncHarness(t)
	if _, _, err := h.sync([]models.SourceDocument{{ID: "a", Keep: true}}, ApplyOptions{}); err == nil {
		t.Error("a Keep document that isn't indexed must fail the sync")
	}
	docs := []models.SourceDocument{incDoc("a", "alpha"), incDoc("b", "beta")}
	if _, _, err := h.sync(docs, ApplyOptions{Seal: "s1"}); err != nil {
		t.Fatal(err)
	}
	state, err := h.indexer.LoadState(context.Background(), incColl, "src")
	if err != nil || state.Seal != SealValue("s1", 2) {
		t.Fatalf("seal = %q, %v", state.Seal, err)
	}
	plan, _, err := h.sync([]models.SourceDocument{{ID: "a", Keep: true}, {ID: "b", Keep: true}}, ApplyOptions{Seal: "s1"})
	if err != nil || counts(plan) != [5]int{0, 0, 0, 2, 0} {
		t.Fatalf("keep sync = %v, %v", counts(plan), err)
	}
	// A changed document is written unsealed; the seal returns only when
	// the sync completes.
	if _, _, err := h.sync([]models.SourceDocument{incDoc("a", "alpha 2"), {ID: "b", Keep: true}}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if state, _ := h.indexer.LoadState(context.Background(), incColl, "src"); state.Seal != "" {
		t.Errorf("seal = %q after an unsealed sync, want none", state.Seal)
	}
}

func TestIncrementalDetectsIndexChangedUnderSync(t *testing.T) {
	h := newIncHarness(t)
	docs := []models.SourceDocument{incDoc("a", "alpha"), incDoc("b", "beta")}
	h.mustSync(docs)
	ctx := context.Background()
	fp, _ := h.indexer.Prepare(ctx, incColl, "")
	state, _ := h.indexer.LoadState(ctx, incColl, "src")
	plan, _ := h.indexer.Diff(docs, state, fp)
	h.qd.Mutate(func(cols map[string]map[string]*ragtest.Point) { delete(cols[incColl], makeID("a")) })
	if _, err := h.indexer.Apply(ctx, incColl, "src", plan, ApplyOptions{}); !errors.Is(err, ErrIndexChanged) {
		t.Errorf("err = %v, want ErrIndexChanged", err)
	}
	if plan, _ := h.mustSync(docs); counts(plan) != [5]int{1, 0, 0, 1, 0} {
		t.Errorf("next sync = %v, want the lost document re-added", counts(plan))
	}
}

func TestIncrementalOtherSourcesUntouched(t *testing.T) {
	h := newIncHarness(t)
	h.mustSync([]models.SourceDocument{incDoc("a", "alpha")})
	other := incDoc("x", "other source")
	other.Tags["source_id"] = "other"
	if err := h.indexer.IndexBatch(context.Background(), incColl, []models.SourceDocument{other}, IndexBatchOptions{}); err != nil {
		t.Fatal(err)
	}
	h.mustSync(nil) // the source now has nothing
	if n := h.qd.Count(incColl, models.TagKey("source_id"), "other"); n != 1 {
		t.Errorf("other source's points = %d, want 1", n)
	}
	if n := h.qd.Count(incColl, models.TagKey("source_id"), "src"); n != 0 {
		t.Errorf("source's points = %d, want 0", n)
	}
}

func TestIncrementalDuplicateIDsKeepLast(t *testing.T) {
	h := newIncHarness(t)
	plan, _ := h.mustSync([]models.SourceDocument{incDoc("a", "first"), incDoc("a", "second")})
	if plan.Duplicates != 1 || len(plan.Embed) != 1 || plan.Embed[0].Text != "second" {
		t.Errorf("plan = %+v", plan)
	}
}

// The layout of a point (its payload keys and how they're derived) is part
// of what the index holds; points built another way are only rebuilt when
// indexFormat changes. (Chunking and chunk headers need no pin: the content
// hash covers the chunks themselves.) This pins the layout so changing it fails here first:
// bump indexFormat, then update the pin.
func TestPointLayoutPinnedToIndexFormat(t *testing.T) {
	doc := incDoc("a", "alpha")
	doc.Filters.Add(models.FilterVersion, "main")
	doc.Sort = map[string]float64{"date": 1}
	doc.ChunkHeader = "hdr"
	payload := chunkPayload(&doc, models.TextChunk{Text: "t", Index: 0}, 1, 0)
	var keys []string
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	got := indexFormat + " " + strings.Join(keys, ",")
	const want = "1 chunk_index,f_version,indexed_at_ms,prop_title,s_date,source_doc_id,tag_source_id,tag_source_name,text,total_chunks"
	if got != want {
		t.Errorf("point layout = %q\nwant %q\n(bump indexFormat when the layout changes, then update this pin)", got, want)
	}
}
