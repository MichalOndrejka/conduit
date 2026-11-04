package eval

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// Backend is the search stack under evaluation: the real one indexes with
// Conduit's own chunker, embedding model and Qdrant, so the eval measures
// exactly what production would do.
type Backend interface {
	// Reset deletes a collection, if it exists.
	Reset(ctx context.Context, collection string) error
	// Exists reports whether a collection exists and holds points.
	Exists(ctx context.Context, collection string) bool
	// Index embeds and stores docs in collection.
	Index(ctx context.Context, collection string, docs []models.SourceDocument) error
	// Embed embeds a query.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Search returns up to limit chunks nearest to vector matching filters.
	Search(ctx context.Context, collection string, vector []float32, limit int, filters []rag.FieldCondition) ([]models.SearchResult, error)
	// FacetValues and CountDocuments count documents for the facets mode
	// (rag.Facets).
	rag.FacetCounter
}

// QdrantBackend is the production stack.
type QdrantBackend struct {
	store     *rag.VectorStore
	embedding *rag.EmbeddingService
	indexer   *rag.DocumentIndexer
}

func NewQdrantBackend(cfg *config.AppConfig) *QdrantBackend {
	store := rag.NewVectorStore(cfg)
	emb := rag.NewEmbeddingService(cfg)
	return &QdrantBackend{store: store, embedding: emb, indexer: rag.NewDocumentIndexer(store, emb, rag.NewTextChunker(cfg))}
}

// Check fails when Qdrant or the embedding model is unreachable.
func (b *QdrantBackend) Check(ctx context.Context) error {
	if err := b.store.HealthCheck(ctx); err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	if _, err := b.embedding.Embed(ctx, "ping"); err != nil {
		return fmt.Errorf("embedding model: %w", err)
	}
	return nil
}

func (b *QdrantBackend) Reset(ctx context.Context, collection string) error {
	if !b.store.CollectionExists(ctx, collection) {
		return nil
	}
	return b.store.DeleteCollection(ctx, collection)
}

func (b *QdrantBackend) Exists(ctx context.Context, collection string) bool {
	return b.store.CollectionExists(ctx, collection) && b.store.PointsCount(ctx, collection) > 0
}

func (b *QdrantBackend) Index(ctx context.Context, collection string, docs []models.SourceDocument) error {
	return b.indexer.IndexBatch(ctx, collection, docs, rag.IndexBatchOptions{})
}

func (b *QdrantBackend) Embed(ctx context.Context, text string) ([]float32, error) {
	return b.embedding.Embed(ctx, text)
}

func (b *QdrantBackend) Search(ctx context.Context, collection string, vector []float32, limit int, filters []rag.FieldCondition) ([]models.SearchResult, error) {
	points, err := b.store.Search(ctx, collection, vector, limit, 0, filters, nil)
	if err != nil {
		return nil, err
	}
	out := make([]models.SearchResult, len(points))
	for i, p := range points {
		out[i] = rag.PointToSearchResult(p)
	}
	return out, nil
}

func (b *QdrantBackend) FacetValues(ctx context.Context, collection, key string, must []rag.FieldCondition, limit int) ([]rag.FacetValue, error) {
	return rag.NewStoreFacetCounter(b.store, nil).FacetValues(ctx, collection, key, must, limit)
}

func (b *QdrantBackend) CountDocuments(ctx context.Context, collection string, must []rag.FieldCondition) (int, error) {
	return rag.NewStoreFacetCounter(b.store, nil).CountDocuments(ctx, collection, must)
}

// conditions turns a scope's filters into Qdrant conditions, normalized the
// way sources store them: a field matches any of its values; fields combine
// with AND.
func conditions(filters map[string][]string) []rag.FieldCondition {
	keys := make([]string, 0, len(filters))
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []rag.FieldCondition
	for _, field := range keys {
		var values []string
		for _, v := range filters[field] {
			if n := models.NormalizeFilterValue(field, v); n != "" {
				values = append(values, n)
			}
		}
		if len(values) > 0 {
			out = append(out, rag.FieldCondition{Key: models.FilterKey(field), Match: rag.Match{Any: values}})
		}
	}
	return out
}

// sourceDocument is d as a source would emit it into index.
func sourceDocument(index, company string, d Document) models.SourceDocument {
	var f models.DocumentFilters
	fields := indexFields(index, d)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f.Add(k, fields[k]...)
	}
	props := map[string]string{"title": d.Title}
	if v := d.Fields["work_item_type"]; len(v) > 0 {
		props["type"] = v[0]
	}
	if v := d.Fields["state"]; len(v) > 0 {
		props["state"] = v[0]
	}
	if v := d.Fields["path"]; len(v) > 0 {
		props["path"] = v[0]
	}
	return models.SourceDocument{
		ID:         d.ID,
		Text:       d.Text,
		Tags:       map[string]string{"source_id": "eval-" + company, "source_name": company},
		Properties: props,
		Filters:    f,
	}
}

var unsafeName = regexp.MustCompile(`[^a-z0-9_]+`)

// collectionName is the throwaway Qdrant collection of a company's index
// and collection kind, kept apart from Conduit's own conduit_* collections.
func collectionName(dataset, company, index, coll string) string {
	name := strings.ToLower(strings.Join([]string{"eval", dataset, company, index, coll}, "_"))
	return unsafeName.ReplaceAllString(name, "_")
}
