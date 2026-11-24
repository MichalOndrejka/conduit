// Document indexer — Go port of app/rag/indexer.py, including the 3-phase
// embed → replace → write-with-rollback protocol and deterministic UUIDv5
// point IDs (same namespace as Python, so re-indexing the same doc from
// either runtime produces the same point ID).
package rag

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// idNamespace matches _NAMESPACE in app/rag/indexer.py (uuid.NAMESPACE_DNS).
var idNamespace = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

func makeID(docID string) string {
	return uuid.NewSHA1(idNamespace, []byte(docID)).String()
}

func makeChunkID(docID string, chunkIndex int) string {
	return uuid.NewSHA1(idNamespace, []byte(docID+"_chunk_"+strconv.Itoa(chunkIndex))).String()
}

// pointIDFor is the ID of chunk index of a document of total chunks: a
// single-chunk document's point is keyed by the document alone.
func pointIDFor(docID string, index, total int) string {
	if total == 1 {
		return makeID(docID)
	}
	return makeChunkID(docID, index)
}

// metaPayload is the part of a chunk's payload that comes from its document
// rather than from the chunk: tags, properties, filters and sort keys.
func metaPayload(doc *models.SourceDocument) map[string]any {
	payload := map[string]any{}
	for k, v := range doc.Tags {
		payload[models.TagKey(k)] = v
	}
	for k, v := range doc.Properties {
		payload[models.PropKey(k)] = v
	}
	for k, v := range doc.Filters.Fields {
		payload[models.FilterKey(k)] = v
	}
	if d := doc.Filters.Date; !d.IsZero() {
		payload[models.FilterDateKey] = d.Unix()
	}
	for k, v := range doc.Sort {
		payload[models.SortKey(k)] = v
	}
	return payload
}

// chunkPayload is the full payload of one chunk of doc.
func chunkPayload(doc *models.SourceDocument, chunk models.TextChunk, total int, nowMs int64) map[string]any {
	payload := metaPayload(doc)
	payload[models.PayloadText] = chunk.Text
	payload[models.PayloadIndexedAtMs] = nowMs
	payload[models.PayloadSourceDocID] = doc.ID
	payload[models.PayloadChunkIndex] = strconv.Itoa(chunk.Index)
	payload[models.PayloadTotalChunks] = strconv.Itoa(total)
	return payload
}

type DocumentIndexer struct {
	store     *VectorStore
	embedding *EmbeddingService
	chunker   *TextChunker

	// collMu serializes the "ensure collection matches this dimension" check
	// below. Multiple sources of the same type share a collection and can
	// sync concurrently (SyncSelected) — without this, two syncs can both
	// see a dimension mismatch and race on delete+recreate, one losing its
	// CreateCollection to a 409 "already exists".
	collMu sync.Mutex
}

func NewDocumentIndexer(store *VectorStore, embedding *EmbeddingService, chunker *TextChunker) *DocumentIndexer {
	return &DocumentIndexer{store: store, embedding: embedding, chunker: chunker}
}

// IndexBatchOptions carries the optional knobs of index_batch.
type IndexBatchOptions struct {
	// ProgressCb is called after each document is embedded: (done, total).
	ProgressCb func(done, total int)
	// ReplaceSourceID deletes existing vectors tagged with this source_id
	// after all embeds succeed and before any writes.
	ReplaceSourceID string
	// Checkpoint is called before dispatching each embed job; returning an
	// error (e.g. syncctl.ErrSyncCancelled) aborts the run before any Qdrant
	// write.
	Checkpoint func() error
	// PrecomputedChunks, when non-nil, supplies already-chunked text per
	// document (parallel to docs) — e.g. from DocumentPreprocessor, which
	// chunks a document before summarizing so a large document is never sent
	// to an LLM as one oversized call. A nil entry for a given document falls
	// back to chunking that document's text normally.
	PrecomputedChunks [][]models.TextChunk
	// Chunks, when non-nil, receives the number of chunks written once the
	// batch is stored (for sync statistics).
	Chunks *int
}

func (d *DocumentIndexer) Index(ctx context.Context, collection string, doc models.SourceDocument) error {
	return d.IndexBatch(ctx, collection, []models.SourceDocument{doc}, IndexBatchOptions{})
}

func (d *DocumentIndexer) IndexBatch(
	ctx context.Context, collection string, docs []models.SourceDocument, opts IndexBatchOptions,
) error {
	nowMs := time.Now().UnixMilli()

	// ── Phase 1: embed everything ───────────────────────────────────────────
	// Collection creation is deferred until after the first embed so we can
	// use the model's *actual* output dimension rather than the configured
	// one. No Qdrant writes happen here — an embed failure leaves everything
	// untouched. Chunks are embedded concurrently (bounded by the embedding
	// service's configured concurrency) since each call is one blocking HTTP
	// round-trip to Ollama; sequential embedding was the dominant cost of a
	// sync run.
	points, _, err := d.embedDocs(ctx, docs, opts.PrecomputedChunks, opts.ProgressCb, opts.Checkpoint, nowMs, nil)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		return nil
	}

	// Create the collection now that we know the actual vector size, or
	// recreate it if an existing collection was sized for a different
	// embedding model — its vectors are in the wrong space and unusable
	// regardless of source, so every source touching it must be re-synced.
	if _, err := d.ensureCollection(ctx, collection, len(points[0].Vector)); err != nil {
		return err
	}

	// ── Between phases: replace old vectors for this source ────────────────
	// Deleting here (after all embeds succeed, before any writes) means:
	//   - embed failure → old vectors intact, nothing lost
	//   - write failure → rollback removes the partial new batch
	if opts.ReplaceSourceID != "" {
		filter := &Filter{Must: []FieldCondition{{
			Key: models.TagKey("source_id"), Match: Match{Value: opts.ReplaceSourceID},
		}}}
		if err := d.store.DeleteByFilter(ctx, collection, filter); err != nil {
			return err
		}
	}

	// ── Phase 2: write to Qdrant with rollback on partial failure ──────────
	var writtenIDs []string
	const batchSize = 100
	for i := 0; i < len(points); i += batchSize {
		end := min(i+batchSize, len(points))
		batch := points[i:end]
		if err := d.store.Upsert(ctx, collection, batch); err != nil {
			if len(writtenIDs) > 0 {
				log.Printf("warning: upsert failed after writing %d points — rolling back", len(writtenIDs))
				if rbErr := d.store.DeleteByIDs(ctx, collection, writtenIDs); rbErr != nil {
					log.Printf("error: rollback delete also failed — collection %s may have orphaned points: %v", collection, rbErr)
				}
			}
			return err
		}
		for _, p := range batch {
			writtenIDs = append(writtenIDs, p.ID)
		}
	}
	if opts.Chunks != nil {
		*opts.Chunks = len(points)
	}
	return nil
}

// ensureCollection creates collection for vectors of size, or recreates it
// if an existing one was sized for a different embedding model — its
// vectors are in the wrong space and unusable regardless of source, so
// every source touching it must be re-synced. recreated reports that the
// collection was emptied (or is new).
func (d *DocumentIndexer) ensureCollection(ctx context.Context, collection string, size int) (recreated bool, err error) {
	d.collMu.Lock()
	defer d.collMu.Unlock()
	if !d.store.CollectionExists(ctx, collection) {
		return true, d.store.CreateCollection(ctx, collection, size)
	}
	existing, err := d.store.VectorSize(ctx, collection)
	if err != nil {
		return false, err
	}
	if existing == size {
		return false, nil
	}
	log.Printf("collection %s vector size %d != %d — recreating for new embedding model", collection, existing, size)
	if err := d.store.DeleteCollection(ctx, collection); err != nil {
		return false, err
	}
	return true, d.store.CreateCollection(ctx, collection, size)
}

// embedDocs chunks docs (or takes precomputed[i] for document i when set),
// adds chunk headers and embeds every chunk concurrently (bounded by the
// embedding service's configured concurrency, since each call is one
// blocking HTTP round-trip). It returns the points in document order and,
// parallel to them, the index of the document each belongs to. decorate,
// when set, adds to each chunk's payload. No Qdrant writes happen here.
func (d *DocumentIndexer) embedDocs(
	ctx context.Context, docs []models.SourceDocument, precomputed [][]models.TextChunk,
	progressCb func(done, total int), checkpoint func() error, nowMs int64,
	decorate func(docIdx int, payload map[string]any),
) (points []Point, docOf []int, err error) {
	type embedJob struct {
		docIdx      int
		doc         *models.SourceDocument
		chunk       models.TextChunk
		totalChunks int
	}

	var jobs []embedJob
	remaining := make([]int, len(docs)) // chunks left to embed, per document — drives progress
	for i := range docs {
		var chunks []models.TextChunk
		if precomputed != nil && precomputed[i] != nil {
			chunks = precomputed[i]
		} else {
			chunks = d.chunker.ChunkDocument(&docs[i])
		}
		chunks = WithChunkHeaders(docs[i].ChunkHeader, chunks)
		remaining[i] = len(chunks)
		for _, chunk := range chunks {
			jobs = append(jobs, embedJob{docIdx: i, doc: &docs[i], chunk: chunk, totalChunks: len(chunks)})
		}
	}

	points = make([]Point, len(jobs))

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, d.embedding.concurrency())
		mu       sync.Mutex
		firstErr error
		doneDocs int
	)

	// Documents with no chunks (empty text) never get a job below, so they're
	// "done" immediately.
	for _, n := range remaining {
		if n == 0 {
			doneDocs++
			if progressCb != nil {
				progressCb(doneDocs, len(docs))
			}
		}
	}

dispatch:
	for jobIdx, job := range jobs {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				cancel()
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				break dispatch
			}
		}
		select {
		case <-workCtx.Done():
			break dispatch
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(jobIdx int, job embedJob) {
			defer wg.Done()
			defer func() { <-sem }()

			vector, err := d.embedding.Embed(workCtx, job.chunk.Text)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
				return
			}

			pointID := pointIDFor(job.doc.ID, job.chunk.Index, job.totalChunks)
			payload := chunkPayload(job.doc, job.chunk, job.totalChunks, nowMs)
			if decorate != nil {
				decorate(job.docIdx, payload)
			}
			points[jobIdx] = Point{ID: pointID, Vector: vector, Payload: payload}

			mu.Lock()
			remaining[job.docIdx]--
			if remaining[job.docIdx] == 0 {
				doneDocs++
				if progressCb != nil {
					progressCb(doneDocs, len(docs))
				}
			}
			mu.Unlock()
		}(jobIdx, job)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, firstErr
	}
	docOf = make([]int, len(jobs))
	for i, job := range jobs {
		docOf[i] = job.docIdx
	}
	return points, docOf, nil

}
