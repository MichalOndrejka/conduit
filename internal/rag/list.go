// Ordered listing: one result per document, sorted by a numeric payload
// field (a date, a duration, a count) that the sources computed at sync
// time. It answers "latest", "last failed", "slowest" and the like from the
// index alone — Conduit acts as a cache, so a tool call never asks the
// source system.
package rag

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// ListPageSize is how many documents List returns per page.
const ListPageSize = 20

// MaxListPage caps how deep List pages: Qdrant can't combine order_by with
// an offset, so page n re-reads n pages' worth of points.
const MaxListPage = 50

// OrderBy sorts a listing by a numeric payload field.
type OrderBy struct {
	Key  string // payload key, e.g. s_finished or f_date
	Desc bool
	// Schema is the Qdrant index type the key needs: "integer" or "float".
	Schema string
}

// fieldIndexes remembers payload indexes already ensured, per collection.
var fieldIndexes sync.Map

// EnsureFieldIndex creates a payload index on key if this process hasn't
// already (Qdrant's index creation is idempotent). order_by needs one.
func (v *VectorStore) EnsureFieldIndex(ctx context.Context, collection, key, schema string) error {
	id := collection + "\x00" + key
	if _, done := fieldIndexes.Load(id); done {
		return nil
	}
	err := v.do(ctx, http.MethodPut, "/collections/"+collection+"/index?wait=true",
		map[string]any{"field_name": key, "field_schema": schema}, nil)
	if err != nil {
		return err
	}
	fieldIndexes.Store(id, true)
	return nil
}

// EnsureListIndexes creates the payload index of every sort key, per
// collection, so the first sorted listing doesn't wait for one to build.
func (v *VectorStore) EnsureListIndexes(ctx context.Context, indexes map[string][]OrderBy) error {
	var errs []error
	for coll, orders := range indexes {
		for _, o := range orders {
			schema := o.Schema
			if schema == "" {
				schema = "float"
			}
			if err := v.EnsureFieldIndex(ctx, coll, o.Key, schema); err != nil && !errors.Is(err, ErrCollectionNotFound) {
				errs = append(errs, fmt.Errorf("%s.%s: %w", coll, o.Key, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ScrollOrdered returns up to limit points matching filter, sorted by order.
// Points without the order key are left out by Qdrant.
func (v *VectorStore) ScrollOrdered(ctx context.Context, collection string, filter *Filter, limit int, order OrderBy) ([]ScrolledPoint, error) {
	direction := "asc"
	if order.Desc {
		direction = "desc"
	}
	body := map[string]any{
		"limit":        limit,
		"with_payload": true,
		"with_vector":  false,
		"order_by":     map[string]any{"key": order.Key, "direction": direction},
	}
	if filter != nil {
		body["filter"] = filter
	}
	var resp struct {
		Result struct {
			Points []ScrolledPoint `json:"points"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodPost, "/collections/"+collection+"/points/scroll", body, &resp); err != nil {
		if errors.Is(err, ErrCollectionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return resp.Result.Points, nil
}

// List returns one result per document (its first chunk) matching must,
// sorted by order, ListPageSize per page. With order.Key empty the
// documents come in storage order.
func (s *SearchService) List(
	ctx context.Context, collection string, must []FieldCondition, order OrderBy, page int,
) (results []models.SearchResult, hasMore bool, err error) {
	if page < 1 {
		page = 1
	}
	if page > MaxListPage {
		return nil, false, fmt.Errorf("page %d is beyond the deepest listable page (%d) — narrow the filters instead", page, MaxListPage)
	}
	conds := append(append([]FieldCondition{}, must...),
		FieldCondition{Key: models.PayloadChunkIndex, Match: Match{Value: "0"}})
	filter := buildFilter(conds, s.excludedSourceIDs())
	need := page*ListPageSize + 1

	var points []ScrolledPoint
	if order.Key != "" {
		schema := order.Schema
		if schema == "" {
			schema = "float"
		}
		if err := s.store.EnsureFieldIndex(ctx, collection, order.Key, schema); err != nil && !errors.Is(err, ErrCollectionNotFound) {
			return nil, false, err
		}
		points, err = s.store.ScrollOrdered(ctx, collection, filter, need, order)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "index") {
			// The collection was recreated (e.g. for a new embedding model)
			// since the index was made: make it again and retry once.
			fieldIndexes.Delete(collection + "\x00" + order.Key)
			if ierr := s.store.EnsureFieldIndex(ctx, collection, order.Key, schema); ierr == nil {
				points, err = s.store.ScrollOrdered(ctx, collection, filter, need, order)
			}
		}
		if err != nil {
			return nil, false, err
		}
	} else {
		var offset []byte
		for len(points) < need {
			batch, next, err := s.store.Scroll(ctx, collection, filter, need-len(points), offset, false)
			if err != nil {
				return nil, false, err
			}
			points = append(points, batch...)
			if next == nil {
				break
			}
			offset = next
		}
	}

	start := (page - 1) * ListPageSize
	if start >= len(points) {
		return []models.SearchResult{}, false, nil
	}
	end := min(page*ListPageSize, len(points))
	for _, p := range points[start:end] {
		results = append(results, PointToSearchResult(ScoredPoint{ID: p.ID, Payload: p.Payload}))
	}
	return results, len(points) > page*ListPageSize, nil
}

// AsOf maps each source among results to when it last synced (RFC 3339),
// so every answer says how fresh the cached data is.
func (s *SearchService) AsOf(results []models.SearchResult) map[string]string {
	ids := map[string]bool{}
	for _, r := range results {
		if id := r.Tags["source_id"]; id != "" {
			ids[id] = true
		}
	}
	out := map[string]string{}
	if len(ids) == 0 || s == nil || s.sources == nil {
		return out
	}
	all, err := s.sources.ListAll()
	if err != nil {
		return out
	}
	for _, src := range all {
		if !ids[src.ID] {
			continue
		}
		if src.LastSyncedAt != nil {
			out[src.ID] = src.LastSyncedAt.UTC().Format("2006-01-02T15:04:05Z")
		} else {
			out[src.ID] = "never"
		}
	}
	return out
}

// Identities are the token owners every enabled source recorded at its last
// sync, for resolving a "me" filter.
func (s *SearchService) Identities() []string {
	if s == nil || s.sources == nil {
		return nil
	}
	all, err := s.sources.ListAll()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, src := range all {
		if src.Disabled {
			continue
		}
		for _, id := range src.Identity {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
