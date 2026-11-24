// Incremental indexing: a sync embeds only the documents whose content
// changed since the source was last indexed, rewrites the payload of those
// whose metadata alone changed, and deletes the ones the source no longer
// has. What is indexed is read back from Qdrant itself — every chunk carries
// fingerprints of the document it came from — so the state can never drift
// from the index: a dropped collection, a failed write or points written by
// an older version all simply read as "not indexed" and are embedded again.
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// indexFormat versions how documents become points. Bump it whenever that
// changes (chunk payload layout, header format, …) so every source is
// re-embedded once instead of keeping points built the old way.
const indexFormat = "1"

// StoredDoc is what the index holds for one document.
type StoredDoc struct {
	ContentHash string
	MetaHash    string
	IndexFP     string
	FetchKey    string
	// PointIDs are all of the document's points, whatever their state.
	PointIDs []string
	// Chunks is the document's chunk count, when Complete.
	Chunks int
	// Complete reports that every chunk 0..Chunks-1 is present exactly once
	// under its expected point ID, all with the same fingerprints — the
	// document as one sync wrote it.
	Complete bool
}

// Current reports whether the stored document is complete and was embedded
// under index fingerprint fp.
func (s *StoredDoc) Current(fp string) bool {
	return s != nil && s.Complete && s.IndexFP == fp
}

// SourceState is what the index holds for one source.
type SourceState struct {
	Docs map[string]*StoredDoc
	// Seal is the seal every point of the source carries ("" when there are
	// no points, or they don't all carry the same one).
	Seal string
}

// stateFields are the payload fields LoadState reads.
var stateFields = []string{
	models.PayloadSourceDocID, models.PayloadChunkIndex, models.PayloadTotalChunks,
	models.PayloadContentHash, models.PayloadMetaHash, models.PayloadIndexFP,
	models.PayloadFetchKey, models.PayloadSeal,
}

func sourceFilter(sourceID string) *Filter {
	return &Filter{Must: []FieldCondition{{Key: models.TagKey("source_id"), Match: Match{Value: sourceID}}}}
}

// LoadState reads every point of sourceID in collection (payload
// fingerprints only, no vectors). A missing collection is an empty state.
func (d *DocumentIndexer) LoadState(ctx context.Context, collection, sourceID string) (*SourceState, error) {
	type chunkRec struct {
		id                       string
		index, total             int
		content, meta, fp, fetch string
		ok                       bool
	}
	byDoc := map[string][]chunkRec{}
	st := &SourceState{Docs: map[string]*StoredDoc{}}
	seal, sealSeen, sealUniform := "", false, true
	var offset json.RawMessage
	for {
		points, next, err := d.store.ScrollFields(ctx, collection, sourceFilter(sourceID), 1000, offset, stateFields)
		if err != nil {
			return nil, fmt.Errorf("reading the indexed state of the source: %w", err)
		}
		for _, p := range points {
			str := func(k string) string { s, _ := p.Payload[k].(string); return s }
			docID := str(models.PayloadSourceDocID)
			if docID == "" {
				// A point no document owns can't be kept current; give it a
				// pseudo-document so it is deleted as no longer fetched.
				docID = "\x00orphan"
			}
			rec := chunkRec{
				id: IDString(p.ID), content: str(models.PayloadContentHash), meta: str(models.PayloadMetaHash),
				fp: str(models.PayloadIndexFP), fetch: str(models.PayloadFetchKey),
			}
			idx, err1 := strconv.Atoi(str(models.PayloadChunkIndex))
			total, err2 := strconv.Atoi(str(models.PayloadTotalChunks))
			rec.index, rec.total, rec.ok = idx, total, err1 == nil && err2 == nil
			byDoc[docID] = append(byDoc[docID], rec)

			s := str(models.PayloadSeal)
			if !sealSeen {
				seal, sealSeen = s, true
			} else if s != seal {
				sealUniform = false
			}
		}
		if len(next) == 0 {
			break
		}
		offset = next
	}
	if sealUniform {
		st.Seal = seal
	}

	for docID, recs := range byDoc {
		sd := &StoredDoc{}
		for _, r := range recs {
			sd.PointIDs = append(sd.PointIDs, r.id)
		}
		st.Docs[docID] = sd
		first := recs[0]
		// A document whose preprocessing fell back to its original text is
		// never current: every sync tries to summarize it again.
		if docID == "\x00orphan" || !first.ok || first.total <= 0 || first.total != len(recs) || first.content == "" ||
			strings.HasSuffix(first.content, degradedSuffix) {
			continue
		}
		seen := make([]bool, first.total)
		complete := true
		for _, r := range recs {
			if !r.ok || r.total != first.total || r.index < 0 || r.index >= first.total || seen[r.index] ||
				r.content != first.content || r.meta != first.meta || r.fp != first.fp || r.fetch != first.fetch ||
				r.id != pointIDFor(docID, r.index, r.total) {
				complete = false
				break
			}
			seen[r.index] = true
		}
		if complete {
			sd.ContentHash, sd.MetaHash, sd.IndexFP, sd.FetchKey = first.content, first.meta, first.fp, first.fetch
			sd.Chunks, sd.Complete = first.total, true
		}
	}
	return st, nil
}

// Prepare readies collection for an incremental sync and returns the index
// fingerprint documents are embedded under: what, besides a document's own
// text, decides its vectors — the chunking, the embedding model (and the
// digest of its weights where Ollama reports one) and preprocess, the
// preprocessing fingerprint when preprocessing applies ("" otherwise). One
// probe embedding learns the model's real dimension, so a collection sized
// for another model is recreated now, before the state is read, never in
// the middle of the sync.
func (d *DocumentIndexer) Prepare(ctx context.Context, collection, preprocess string) (string, error) {
	vector, err := d.embedding.Embed(ctx, "dimension probe")
	if err != nil {
		return "", err
	}
	if _, err := d.ensureCollection(ctx, collection, len(vector)); err != nil {
		return "", err
	}
	digest, err := d.embedding.ModelDigest(ctx)
	if err != nil {
		return "", err
	}
	ec := d.embedding.cfg.Embedding
	if digest == "" {
		// Without a digest the model name alone can't tell another server's
		// weights apart: the endpoint is part of the fingerprint.
		digest = "url:" + strings.TrimRight(ec.BaseURL, "/")
	}
	return HashOf(indexFormat, strconv.Itoa(d.chunker.maxSize), strconv.Itoa(d.chunker.overlap),
		ec.Model, digest, strconv.Itoa(len(vector)), strconv.Itoa(ec.MaxInputTokens), preprocess), nil
}

// SealValue is the seal stamped on a source's points: seal, the inputs the
// sync was built from, and docs, how many documents it left indexed — so a
// whole document lost since then unseals the source.
func SealValue(seal string, docs int) string {
	return seal + "#" + strconv.Itoa(docs)
}

// HashOf is the hex SHA-256 of parts, each length-prefixed so no two lists
// of parts hash alike.
func HashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// degradedSuffix marks the content hash of a document whose preprocessing
// fell back to its original text.
const degradedSuffix = ":degraded"

// ContentHash is the fingerprint of what embedding doc under index
// fingerprint fp produces: the chunks the indexer cuts it into, headers
// included, so a change to how documents are chunked is caught as well as a
// change to their text. (With preprocessing the embedded chunks are
// summaries of these; fp covers the preprocessing settings.)
func (d *DocumentIndexer) ContentHash(doc *models.SourceDocument, fp string) string {
	parts := []string{fp, doc.ChunkHeader, doc.Text}
	for _, c := range WithChunkHeaders(doc.ChunkHeader, d.chunker.ChunkDocument(doc)) {
		parts = append(parts, strconv.Itoa(c.Index), strconv.Itoa(c.StartOffset), strconv.Itoa(c.EndOffset), c.Text)
	}
	return HashOf(parts...)
}

// MetaHash is the fingerprint of the payload doc's chunks carry besides
// their text.
func MetaHash(doc *models.SourceDocument) string {
	data, err := json.Marshal(metaPayload(doc)) // map keys marshal sorted
	if err != nil {
		return ""
	}
	return HashOf(string(data))
}

// SyncPlan is what an incremental sync changes, decided by Diff.
type SyncPlan struct {
	// Embed are the documents to embed (new, changed, or not completely
	// indexed); MetaOnly those whose stored vectors stay valid but whose
	// payload changed.
	Embed    []models.SourceDocument
	MetaOnly []models.SourceDocument
	// Deleted are the stored documents the source no longer has.
	Deleted []string
	// Added counts the Embed documents not indexed at all before.
	Added int
	// Unchanged counts the documents left as they are; UnchangedChunks
	// their chunks, UnchangedIndexed those of them that have chunks.
	Unchanged        int
	UnchangedChunks  int
	UnchangedIndexed int
	// Duplicates counts documents dropped for repeating an earlier one's ID.
	Duplicates int

	fp    string
	state *SourceState
}

// Diff compares the fetched docs with the indexed state under index
// fingerprint fp. A Keep document must be current in the state: the source
// was told it is (see sources.IndexState) — anything else is a bug, and an
// error rather than a silently stale document.
func (d *DocumentIndexer) Diff(docs []models.SourceDocument, state *SourceState, fp string) (*SyncPlan, error) {
	plan := &SyncPlan{fp: fp, state: state}
	last := make(map[string]int, len(docs))
	for i := range docs {
		last[docs[i].ID] = i
	}
	for i := range docs {
		doc := &docs[i]
		if last[doc.ID] != i {
			plan.Duplicates++
			log.Printf("warning: source returned document %s more than once — indexing its last copy", doc.ID)
			continue
		}
		stored := state.Docs[doc.ID]
		if doc.Keep {
			if !stored.Current(fp) {
				return nil, fmt.Errorf("document %s was not refetched, but its indexed copy is not current", doc.ID)
			}
			plan.Unchanged++
			plan.UnchangedIndexed++
			plan.UnchangedChunks += stored.Chunks
			continue
		}
		switch {
		case stored == nil && doc.Text == "":
			plan.Unchanged++ // no text, no chunks: nothing indexed, nothing to index
		case stored == nil:
			plan.Added++
			plan.Embed = append(plan.Embed, *doc)
		case !stored.Current(fp) || stored.ContentHash != d.ContentHash(doc, fp):
			plan.Embed = append(plan.Embed, *doc)
		case stored.MetaHash != MetaHash(doc) || stored.FetchKey != doc.FetchKey:
			plan.MetaOnly = append(plan.MetaOnly, *doc)
		default:
			plan.Unchanged++
			plan.UnchangedIndexed++
			plan.UnchangedChunks += stored.Chunks
		}
	}
	for id := range state.Docs {
		if _, ok := last[id]; !ok {
			plan.Deleted = append(plan.Deleted, id)
		}
	}
	return plan, nil
}

// ApplyOptions carries the optional knobs of Apply.
type ApplyOptions struct {
	// ProgressCb is called as plan.Embed documents are embedded: (done, total).
	ProgressCb func(done, total int)
	// Checkpoint is called before each embed job; an error aborts the run
	// before any Qdrant write.
	Checkpoint func() error
	// PrecomputedChunks, parallel to plan.Embed, supplies preprocessed
	// chunks (see IndexBatchOptions.PrecomputedChunks).
	PrecomputedChunks [][]models.TextChunk
	// Degraded, parallel to plan.Embed, marks documents whose preprocessing
	// fell back to original text: they are stored so the next sync embeds
	// them again rather than keeping the fallback for good.
	Degraded []bool
	// Seal, when set, is stamped on every point of the source once
	// everything else succeeded (see SourceState.Seal).
	Seal string
}

// ApplyResult counts what Apply wrote.
type ApplyResult struct {
	// Chunks is the source's chunk count after the sync; Embedded how many
	// of them were embedded by it.
	Chunks   int
	Embedded int
}

// ErrIndexChanged reports that the source's points changed under a sync
// (another process dropped or rewrote them); the sync must run again.
var ErrIndexChanged = errors.New("the index changed while the source was syncing")

// Apply carries out plan for sourceID in collection. Every embed happens
// before the first write, so an embed failure or a cancellation leaves the
// index as it was. Writes go document by document — a document's chunks,
// then its stale points — so a failure part-way leaves each document either
// in its old or its new state, or (when it fails within one document) one
// the next sync reads as incomplete and embeds again. Deletions come last,
// and the result is verified against the expected point count.
func (d *DocumentIndexer) Apply(ctx context.Context, collection, sourceID string, plan *SyncPlan, opts ApplyOptions) (ApplyResult, error) {
	var res ApplyResult
	nowMs := time.Now().UnixMilli()
	fp := plan.fp

	contentHashes := make([]string, len(plan.Embed))
	for i := range plan.Embed {
		contentHashes[i] = d.ContentHash(&plan.Embed[i], fp)
		if opts.Degraded != nil && opts.Degraded[i] {
			contentHashes[i] += degradedSuffix
		}
	}
	points, docOf, err := d.embedDocs(ctx, plan.Embed, opts.PrecomputedChunks, opts.ProgressCb, opts.Checkpoint, nowMs,
		func(docIdx int, payload map[string]any) {
			doc := &plan.Embed[docIdx]
			payload[models.PayloadContentHash] = contentHashes[docIdx]
			payload[models.PayloadMetaHash] = MetaHash(doc)
			payload[models.PayloadIndexFP] = fp
			payload[models.PayloadFetchKey] = doc.FetchKey
		})
	if err != nil {
		return res, err
	}
	res.Embedded = len(points)
	if err := ctx.Err(); err != nil {
		return res, err
	}

	// ── Write changed documents, each followed by its stale points ─────────
	const batchSize = 100
	embeddedDocs := map[int]bool{}
	for _, di := range docOf {
		embeddedDocs[di] = true
	}
	start := 0
	for start < len(points) {
		// A batch holds whole documents, up to batchSize points — or one
		// larger document alone.
		end := start
		for end < len(points) {
			docEnd := end
			for docEnd < len(points) && docOf[docEnd] == docOf[end] {
				docEnd++
			}
			if end > start && docEnd-start > batchSize {
				break
			}
			end = docEnd
		}
		for i := start; i < end; i += batchSize {
			if err := d.store.Upsert(ctx, collection, points[i:min(i+batchSize, end)]); err != nil {
				return res, err
			}
		}
		// The batch's documents are written: their points from before that
		// this sync didn't overwrite (fewer chunks now, or the one-chunk ↔
		// several-chunks ID change) go at once.
		written := map[string]bool{}
		for _, p := range points[start:end] {
			written[p.ID] = true
		}
		var stale []string
		for i := start; i < end; {
			doc := docOf[i]
			if stored := plan.state.Docs[plan.Embed[doc].ID]; stored != nil {
				for _, id := range stored.PointIDs {
					if !written[id] {
						stale = append(stale, id)
					}
				}
			}
			for i < end && docOf[i] == doc {
				i++
			}
		}
		if err := d.deleteIDs(ctx, collection, stale); err != nil {
			return res, err
		}
		start = end
	}
	// Documents that now have no chunks at all (empty text) lose every point.
	var emptied []string
	for i := range plan.Embed {
		if stored := plan.state.Docs[plan.Embed[i].ID]; stored != nil && !embeddedDocs[i] {
			emptied = append(emptied, stored.PointIDs...)
		}
	}
	if err := d.deleteIDs(ctx, collection, emptied); err != nil {
		return res, err
	}

	// ── Metadata-only documents: same vectors, new payload ─────────────────
	metaChunks, err := d.rewritePayloads(ctx, collection, plan)
	if err != nil {
		return res, err
	}

	// ── Documents the source no longer has ─────────────────────────────────
	var gone []string
	for _, id := range plan.Deleted {
		gone = append(gone, plan.state.Docs[id].PointIDs...)
	}
	if err := d.deleteIDs(ctx, collection, gone); err != nil {
		return res, err
	}

	// ── Verify, then seal ──────────────────────────────────────────────────
	res.Chunks = plan.UnchangedChunks + metaChunks + len(points)
	n, err := d.store.CountExact(ctx, collection, sourceFilter(sourceID))
	if err != nil {
		return res, err
	}
	if n != res.Chunks {
		return res, fmt.Errorf("%w: expected %d points, found %d — sync it again", ErrIndexChanged, res.Chunks, n)
	}
	if opts.Seal != "" && n > 0 {
		docs := plan.UnchangedIndexed + len(plan.MetaOnly) + len(embeddedDocs)
		seal := SealValue(opts.Seal, docs)
		if err := d.store.SetPayload(ctx, collection, map[string]any{models.PayloadSeal: seal}, sourceFilter(sourceID)); err != nil {
			// Unsealed only means the next sync can't skip its fetch.
			log.Printf("warning: sealing source %s: %v", sourceID, err)
		}
	}
	return res, nil
}

// deleteIDs deletes points by ID in batches.
func (d *DocumentIndexer) deleteIDs(ctx context.Context, collection string, ids []string) error {
	const batch = 500
	for i := 0; i < len(ids); i += batch {
		if err := d.store.DeleteByIDs(ctx, collection, ids[i:min(i+batch, len(ids))]); err != nil {
			return err
		}
	}
	return nil
}

// rewritePayloads gives every chunk of plan.MetaOnly its document's new
// metadata, keeping vector, text and chunk position, and returns how many
// chunks it rewrote.
func (d *DocumentIndexer) rewritePayloads(ctx context.Context, collection string, plan *SyncPlan) (int, error) {
	total := 0
	for i := range plan.MetaOnly {
		doc := &plan.MetaOnly[i]
		stored := plan.state.Docs[doc.ID]
		existing, err := d.store.RetrieveWithVectors(ctx, collection, stored.PointIDs)
		if err != nil {
			return total, err
		}
		if len(existing) != stored.Chunks {
			return total, fmt.Errorf("%w: document %s has %d of its %d points", ErrIndexChanged, doc.ID, len(existing), stored.Chunks)
		}
		meta, metaHash := metaPayload(doc), MetaHash(doc)
		points := make([]Point, 0, len(existing))
		for _, p := range existing {
			var vector []float32
			if err := json.Unmarshal(p.Vector, &vector); err != nil || len(vector) == 0 {
				return total, fmt.Errorf("document %s: reading a stored vector: %v", doc.ID, err)
			}
			payload := make(map[string]any, len(meta)+9)
			for k, v := range meta {
				payload[k] = v
			}
			for _, k := range []string{models.PayloadText, models.PayloadIndexedAtMs, models.PayloadSourceDocID,
				models.PayloadChunkIndex, models.PayloadTotalChunks, models.PayloadContentHash, models.PayloadIndexFP} {
				if v, ok := p.Payload[k]; ok {
					payload[k] = v
				}
			}
			payload[models.PayloadMetaHash] = metaHash
			payload[models.PayloadFetchKey] = doc.FetchKey
			points = append(points, Point{ID: IDString(p.ID), Vector: vector, Payload: payload})
		}
		if err := d.store.Upsert(ctx, collection, points); err != nil {
			return total, err
		}
		total += len(points)
	}
	return total, nil
}

// PurgeSource deletes sourceID's points from every source collection but
// keep ("" for all of them): what a sync leaves behind when its source moved
// to another collection, or was deleted, while it ran — or a cleanup that
// failed back then.
func (d *DocumentIndexer) PurgeSource(ctx context.Context, sourceID, keep string) error {
	for _, c := range models.AllCollections {
		if c == keep || c == models.CollectionExperience {
			continue
		}
		if err := d.store.DeleteByFilter(ctx, c, sourceFilter(sourceID)); err != nil && !errors.Is(err, ErrCollectionNotFound) {
			return fmt.Errorf("removing the source's points from %s: %w", c, err)
		}
	}
	return nil
}
