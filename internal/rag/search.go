// Search service — Go port of app/rag/search.py.
package rag

import (
	"context"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// SourceLister is the slice of store.SourceConfigStore that SearchService
// needs to keep disabled sources out of results — kept minimal to avoid
// internal/rag depending on the rest of internal/store's surface.
type SourceLister interface {
	ListAll() ([]models.SourceDefinition, error)
}

type SearchService struct {
	store     *VectorStore
	embedding *EmbeddingService
	sources   SourceLister
}

func NewSearchService(store *VectorStore, embedding *EmbeddingService, sources SourceLister) *SearchService {
	return &SearchService{store: store, embedding: embedding, sources: sources}
}

// pageSize is the number of ranked matches Search returns per page — always
// 1, so a single search call can never return more than one full chunk of
// text. Callers page to the next-most-relevant match with a higher page
// number instead of asking for a batch up front.
const pageSize = 1

// Search returns the single most relevant match for page (1-based; values
// below 1 are treated as 1). hasMore reports whether a further page exists.
func (s *SearchService) Search(
	ctx context.Context, collection, query string, page int, tags map[string]string,
) (results []models.SearchResult, hasMore bool, err error) {
	if page < 1 {
		page = 1
	}
	vector, err := s.embedding.Embed(ctx, query)
	if err != nil {
		return nil, false, err
	}
	var excludeSourceIDs []string
	if s.sources != nil {
		if all, err := s.sources.ListAll(); err == nil {
			for _, src := range all {
				if src.Disabled {
					excludeSourceIDs = append(excludeSourceIDs, src.ID)
				}
			}
		}
	}
	offset := (page - 1) * pageSize
	points, err := s.store.Search(ctx, collection, vector, pageSize+1, offset, tags, excludeSourceIDs)
	if err != nil {
		return nil, false, err
	}
	hasMore = len(points) > pageSize
	if hasMore {
		points = points[:pageSize]
	}
	results = make([]models.SearchResult, 0, len(points))
	for _, p := range points {
		results = append(results, PointToSearchResult(p))
	}
	return results, hasMore, nil
}

// GetChunk fetches one specific chunk of a document by exact point ID — a
// deterministic lookup, not a semantic search, so no embedding call is made.
// Pass the source_doc_id and chunk_index from a prior SearchResult (adjusted
// by ±1) to walk to the previous/next chunk of the same document. found is
// false when no chunk exists at that index (e.g. chunkIndex is out of range).
func (s *SearchService) GetChunk(ctx context.Context, collection, sourceDocID string, chunkIndex int) (result models.SearchResult, found bool, err error) {
	if chunkIndex < 0 {
		return models.SearchResult{}, false, nil
	}
	points, err := s.store.Retrieve(ctx, collection, []string{makeChunkID(sourceDocID, chunkIndex)})
	if err != nil {
		return models.SearchResult{}, false, err
	}
	if len(points) == 0 && chunkIndex == 0 {
		// Single-chunk documents are stored under the plain doc ID rather
		// than the "<docID>_chunk_0" form — try that shape too.
		points, err = s.store.Retrieve(ctx, collection, []string{makeID(sourceDocID)})
		if err != nil {
			return models.SearchResult{}, false, err
		}
	}
	if len(points) == 0 {
		return models.SearchResult{}, false, nil
	}
	p := points[0]
	return PointToSearchResult(ScoredPoint{ID: p.ID, Payload: p.Payload}), true, nil
}
