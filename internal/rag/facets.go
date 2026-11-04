package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// FacetLimit is how many values facets return per field, most frequent
// first; FieldFacets.More says when there are further values.
const FacetLimit = 25

// FacetField is one field to count. For a hierarchical field (path,
// iteration_path, area_path), Prefix restricts the values to those beneath
// one folder and Level picks the depth counted: 1 is the top-level folders.
// Level defaults to the level just below Prefix (1 without one), so
// {Prefix: "docs"} lists the folders and files directly inside docs/.
type FacetField struct {
	Name   string
	Prefix string // normalized
	Level  int
}

// FacetRequest asks for the document counts of Fields' values among the
// documents matching Must.
type FacetRequest struct {
	Fields []FacetField
	Must   []FieldCondition
	Limit  int // per field; default FacetLimit
}

// FacetValue is one value and the number of documents (not chunks) that
// carry it.
type FacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// FieldFacets are one field's values.
type FieldFacets struct {
	// Level and Prefix echo what a hierarchical field was counted at.
	Level  int          `json:"level,omitempty"`
	Prefix string       `json:"prefix,omitempty"`
	Values []FacetValue `json:"values"`
	// More is set when further values exist beyond the ones listed.
	More bool `json:"more,omitempty"`
	// Documents is how many documents the counts were taken over, when it
	// differs per field (the empty-result hints, each counted with the other
	// filters only).
	Documents *int `json:"documents,omitempty"`
}

// FacetResult is a facets response: the documents matching the request's
// filters and each field's values among them.
type FacetResult struct {
	Documents int                    `json:"documents"`
	Facets    map[string]FieldFacets `json:"facets"`
}

// FacetCounter counts values over documents: FacetValues returns up to
// limit values of a payload key, most frequent first, with how many matching
// documents carry each; CountDocuments counts the matching documents.
type FacetCounter interface {
	FacetValues(ctx context.Context, collection, key string, must []FieldCondition, limit int) ([]FacetValue, error)
	CountDocuments(ctx context.Context, collection string, must []FieldCondition) (int, error)
}

// Facets runs req against counter. Shared by the MCP search tools and the
// accuracy eval, so both count the same way.
func Facets(ctx context.Context, counter FacetCounter, collection string, req FacetRequest) (FacetResult, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = FacetLimit
	}
	docs, err := counter.CountDocuments(ctx, collection, req.Must)
	if err != nil {
		return FacetResult{}, err
	}
	out := FacetResult{Documents: docs, Facets: map[string]FieldFacets{}}
	for _, f := range req.Fields {
		ff, err := fieldFacets(ctx, counter, collection, f, req.Must, limit)
		if err != nil {
			return FacetResult{}, err
		}
		out.Facets[f.Name] = ff
	}
	return out, nil
}

func fieldFacets(ctx context.Context, counter FacetCounter, collection string, f FacetField, must []FieldCondition, limit int) (FieldFacets, error) {
	if !models.IsHierarchical(f.Name) {
		if f.Prefix != "" || f.Level != 0 {
			return FieldFacets{}, fmt.Errorf("prefix and level apply only to path-like fields (path, iteration_path, area_path), not %s", f.Name)
		}
		values, err := counter.FacetValues(ctx, collection, models.FilterKey(f.Name), must, limit+1)
		if err != nil {
			return FieldFacets{}, err
		}
		return capped(FieldFacets{}, values, limit), nil
	}

	prefixDepth := models.FilterDepth(f.Name, f.Prefix)
	level := f.Level
	if level == 0 {
		level = prefixDepth + 1
	}
	if level <= prefixDepth {
		return FieldFacets{}, fmt.Errorf("level %d of %s is not below prefix %q (level %d)", level, f.Name, f.Prefix, prefixDepth)
	}
	scoped := must
	if f.Prefix != "" {
		scoped = append(append([]FieldCondition{}, must...),
			FieldCondition{Key: models.FilterKey(f.Name), Match: Match{Value: f.Prefix}})
	}
	key := models.FilterLevelKey(f.Name, level)
	if f.Prefix == "" {
		values, err := counter.FacetValues(ctx, collection, key, scoped, limit+1)
		if err != nil {
			return FieldFacets{}, err
		}
		return capped(FieldFacets{Level: level}, values, limit), nil
	}

	// A document can carry several values of one field (a commit touching
	// files in several folders), so values outside the prefix can still
	// occur among the prefix's documents; they are dropped here, asking for
	// more values until enough remain or the key has no more.
	within := f.Prefix + models.FilterSeparator(f.Name)
	for ask := limit + 1; ; ask *= 4 {
		values, err := counter.FacetValues(ctx, collection, key, scoped, ask)
		if err != nil {
			return FieldFacets{}, err
		}
		var kept []FacetValue
		for _, v := range values {
			if strings.HasPrefix(v.Value, within) {
				kept = append(kept, v)
			}
		}
		if len(kept) > limit || len(values) < ask {
			return capped(FieldFacets{Level: level, Prefix: f.Prefix}, kept, limit), nil
		}
	}
}

// capped lists at most limit of values (asked for limit+1) in ff.
func capped(ff FieldFacets, values []FacetValue, limit int) FieldFacets {
	if len(values) > limit {
		values, ff.More = values[:limit], true
	}
	if values == nil {
		values = []FacetValue{}
	}
	ff.Values = values
	return ff
}

// firstChunk restricts a filter to each document's first chunk, so facet
// and count results are document counts rather than chunk counts.
var firstChunk = FieldCondition{Key: models.PayloadChunkIndex, Match: Match{Value: "0"}}

// storeFacets counts with Qdrant's facet and count APIs over a collection,
// excluding the given (disabled) sources.
type storeFacets struct {
	store   *VectorStore
	exclude []string
}

// NewStoreFacetCounter counts documents in the store's collections,
// leaving out excludeSourceIDs.
func NewStoreFacetCounter(store *VectorStore, excludeSourceIDs []string) FacetCounter {
	return storeFacets{store: store, exclude: excludeSourceIDs}
}

func (s storeFacets) filter(must []FieldCondition) *Filter {
	return buildFilter(append(append([]FieldCondition{}, must...), firstChunk), s.exclude)
}

func (s storeFacets) FacetValues(ctx context.Context, collection, key string, must []FieldCondition, limit int) ([]FacetValue, error) {
	hits, err := s.store.Facet(ctx, collection, key, s.filter(must), limit)
	if err != nil {
		return nil, err
	}
	out := make([]FacetValue, len(hits))
	for i, h := range hits {
		out[i] = FacetValue{Value: h.Value, Count: h.Count}
	}
	return out, nil
}

func (s storeFacets) CountDocuments(ctx context.Context, collection string, must []FieldCondition) (int, error) {
	return s.store.CountExact(ctx, collection, s.filter(must))
}
