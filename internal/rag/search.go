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

func (s *SearchService) Search(
	ctx context.Context, collection, query string, topK int, tags map[string]string,
) ([]models.SearchResult, error) {
	vector, err := s.embedding.Embed(ctx, query)
	if err != nil {
		return nil, err
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
	points, err := s.store.Search(ctx, collection, vector, topK, tags, excludeSourceIDs)
	if err != nil {
		return nil, err
	}
	results := make([]models.SearchResult, 0, len(points))
	for _, p := range points {
		results = append(results, PointToSearchResult(p))
	}
	return results, nil
}
