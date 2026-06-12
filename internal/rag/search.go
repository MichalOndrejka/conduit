// Search service — Go port of app/rag/search.py.
package rag

import (
	"context"

	"github.com/MichalOndrejka/conduit/internal/models"
)

type SearchService struct {
	store     *VectorStore
	embedding *EmbeddingService
}

func NewSearchService(store *VectorStore, embedding *EmbeddingService) *SearchService {
	return &SearchService{store: store, embedding: embedding}
}

func (s *SearchService) Search(
	ctx context.Context, collection, query string, topK int, tags map[string]string,
) ([]models.SearchResult, error) {
	vector, err := s.embedding.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	points, err := s.store.Search(ctx, collection, vector, topK, tags)
	if err != nil {
		return nil, err
	}
	results := make([]models.SearchResult, 0, len(points))
	for _, p := range points {
		results = append(results, PointToSearchResult(p))
	}
	return results, nil
}
