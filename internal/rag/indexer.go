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
	// Checkpoint is called before each document; returning an error (e.g.
	// syncctl.ErrSyncCancelled) aborts the run before any Qdrant write.
	Checkpoint func() error
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
	// untouched.
	var points []Point

	for i, doc := range docs {
		if opts.Checkpoint != nil {
			if err := opts.Checkpoint(); err != nil {
				return err
			}
		}
		chunks := d.chunker.Chunk(doc.Text)
		totalChunks := len(chunks)

		for _, chunk := range chunks {
			vector, err := d.embedding.Embed(ctx, chunk.Text)
			if err != nil {
				return err
			}
			pointID := makeChunkID(doc.ID, chunk.Index)
			if totalChunks == 1 {
				pointID = makeID(doc.ID)
			}

			payload := map[string]any{
				models.PayloadText:        chunk.Text,
				models.PayloadIndexedAtMs: nowMs,
				models.PayloadSourceDocID: doc.ID,
				models.PayloadChunkIndex:  strconv.Itoa(chunk.Index),
				models.PayloadTotalChunks: strconv.Itoa(totalChunks),
			}
			for k, v := range doc.Tags {
				payload[models.TagKey(k)] = v
			}
			for k, v := range doc.Properties {
				payload[models.PropKey(k)] = v
			}

			points = append(points, Point{ID: pointID, Vector: vector, Payload: payload})
		}

		if opts.ProgressCb != nil {
			opts.ProgressCb(i+1, len(docs))
		}
	}

	if len(points) == 0 {
		return nil
	}

	// Create the collection now that we know the actual vector size, or
	// recreate it if an existing collection was sized for a different
	// embedding model — its vectors are in the wrong space and unusable
	// regardless of source, so every source touching it must be re-synced.
	size := len(points[0].Vector)
	if err := func() error {
		d.collMu.Lock()
		defer d.collMu.Unlock()
		if !d.store.CollectionExists(ctx, collection) {
			return d.store.CreateCollection(ctx, collection, size)
		}
		existing, err := d.store.VectorSize(ctx, collection)
		if err != nil {
			return err
		}
		if existing == size {
			return nil
		}
		log.Printf("collection %s vector size %d != %d — recreating for new embedding model", collection, existing, size)
		if err := d.store.DeleteCollection(ctx, collection); err != nil {
			return err
		}
		return d.store.CreateCollection(ctx, collection, size)
	}(); err != nil {
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
	return nil
}
