// Qdrant REST client — Go port of app/rag/vector_store.py.
//
// Deliberately uses Qdrant's HTTP API (same port 6333 / HTTPS 443 the Python
// qdrant-client targets) instead of the official gRPC client, so the existing
// container deployment (QDRANT_URL/QDRANT_API_KEY) works unchanged.
// Only the handful of operations Conduit needs are implemented.
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

// VectorStore reads its connection details from *config.AppConfig on every
// call, so Qdrant URL/API-key/dimensions changes made via the Settings page
// take effect on the next request without requiring a restart.
type VectorStore struct {
	cfg        *config.AppConfig
	httpClient *http.Client
}

func NewVectorStore(cfg *config.AppConfig) *VectorStore {
	return &VectorStore{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (v *VectorStore) baseURL() string {
	return strings.TrimRight(v.cfg.Qdrant.URL, "/")
}

// ── Wire types ──────────────────────────────────────────────────────────────

// Filter is a minimal Qdrant filter: {"must": [...], "must_not": [...]}.
// must_not excludes a record if it matches ANY of the listed conditions —
// used to OR together multiple disabled source_id exclusions.
type Filter struct {
	Must    []FieldCondition `json:"must,omitempty"`
	MustNot []FieldCondition `json:"must_not,omitempty"`
}

type FieldCondition struct {
	Key   string `json:"key"`
	Match Match  `json:"match"`
}

type Match struct {
	Value any `json:"value"`
}

// TagFilter mirrors _build_filter in app/rag/vector_store.py.
func TagFilter(tags map[string]string) *Filter {
	if len(tags) == 0 {
		return nil
	}
	f := &Filter{}
	for k, v := range tags {
		f.Must = append(f.Must, FieldCondition{Key: models.TagKey(k), Match: Match{Value: v}})
	}
	return f
}

type Point struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// ScoredPoint is a search hit; ScrolledPoint a scroll record.
type ScoredPoint struct {
	ID      json.RawMessage `json:"id"`
	Score   float64         `json:"score"`
	Payload map[string]any  `json:"payload"`
}

type ScrolledPoint struct {
	ID      json.RawMessage `json:"id"`
	Payload map[string]any  `json:"payload"`
	Vector  json.RawMessage `json:"vector"`
}

// IDString renders a Qdrant point ID (string or integer JSON) as a string.
// Shared by every package that decodes points — keep the one copy here.
func IDString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// ── HTTP plumbing ───────────────────────────────────────────────────────────

func (v *VectorStore) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, v.baseURL()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if v.cfg.Qdrant.APIKey != "" {
		req.Header.Set("api-key", v.cfg.Qdrant.APIKey)
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qdrant %s %s: HTTP %d: %s", method, path, resp.StatusCode, Truncate(string(data), 300))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Truncate caps a string at n bytes with an ellipsis (for error snippets).
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── Collections ─────────────────────────────────────────────────────────────

func (v *VectorStore) ListCollections(ctx context.Context) ([]string, error) {
	var resp struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodGet, "/collections", nil, &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.Result.Collections))
	for _, c := range resp.Result.Collections {
		names = append(names, c.Name)
	}
	return names, nil
}

func (v *VectorStore) CollectionExists(ctx context.Context, name string) bool {
	names, err := v.ListCollections(ctx)
	if err != nil {
		return false
	}
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// CreateCollection creates a cosine-distance collection; dimensions <= 0 uses
// the configured embedding dimensions.
func (v *VectorStore) CreateCollection(ctx context.Context, name string, dimensions int) error {
	size := dimensions
	if size <= 0 {
		size = v.cfg.Embedding.Dimensions
	}
	body := map[string]any{
		"vectors": map[string]any{"size": size, "distance": "Cosine"},
	}
	err := v.do(ctx, http.MethodPut, "/collections/"+name, body, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		// Another concurrent sync (same collection, different source) won the
		// race to create it — fine, since both compute the same target size.
		return nil
	}
	return err
}

func (v *VectorStore) DeleteCollection(ctx context.Context, name string) error {
	return v.do(ctx, http.MethodDelete, "/collections/"+name, nil, nil)
}

// PointsCount returns the stored points count for a collection (0 on error),
// mirroring MemoryService.count in app/memory/service.py.
func (v *VectorStore) PointsCount(ctx context.Context, name string) int {
	var resp struct {
		Result struct {
			PointsCount int `json:"points_count"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodGet, "/collections/"+name, nil, &resp); err != nil {
		return 0
	}
	return resp.Result.PointsCount
}

// VectorSize returns a collection's configured vector dimension.
func (v *VectorStore) VectorSize(ctx context.Context, name string) (int, error) {
	var resp struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size int `json:"size"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodGet, "/collections/"+name, nil, &resp); err != nil {
		return 0, err
	}
	return resp.Result.Config.Params.Vectors.Size, nil
}

// ── Points ──────────────────────────────────────────────────────────────────

func (v *VectorStore) Upsert(ctx context.Context, collection string, points []Point) error {
	body := map[string]any{"points": points}
	return v.do(ctx, http.MethodPut, "/collections/"+collection+"/points?wait=true", body, nil)
}

// excludeSourceIDs, if non-empty, excludes points tagged with any of those
// source IDs — used to keep disabled sources out of search results without
// deleting their vectors (they reappear immediately if the source is
// re-enabled, no re-sync needed).
func (v *VectorStore) Search(
	ctx context.Context, collection string, vector []float32, limit, offset int, tags map[string]string, excludeSourceIDs []string,
) ([]ScoredPoint, error) {
	body := map[string]any{
		"query":        vector,
		"limit":        limit,
		"with_payload": true,
	}
	if offset > 0 {
		body["offset"] = offset
	}
	f := TagFilter(tags)
	if len(excludeSourceIDs) > 0 {
		if f == nil {
			f = &Filter{}
		}
		for _, id := range excludeSourceIDs {
			f.MustNot = append(f.MustNot, FieldCondition{Key: models.TagKey("source_id"), Match: Match{Value: id}})
		}
	}
	if f != nil {
		body["filter"] = f
	}
	var resp struct {
		Result struct {
			Points []ScoredPoint `json:"points"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodPost, "/collections/"+collection+"/points/query", body, &resp); err != nil {
		return nil, err
	}
	return resp.Result.Points, nil
}

// Scroll pages through a collection. offset may be nil for the first page;
// the returned next offset is nil when exhausted.
func (v *VectorStore) Scroll(
	ctx context.Context, collection string, filter *Filter, limit int, offset json.RawMessage, withVectors bool,
) ([]ScrolledPoint, json.RawMessage, error) {
	body := map[string]any{
		"limit":        limit,
		"with_payload": true,
		"with_vector":  withVectors,
	}
	if filter != nil {
		body["filter"] = filter
	}
	if len(offset) > 0 {
		body["offset"] = offset
	}
	var resp struct {
		Result struct {
			Points         []ScrolledPoint `json:"points"`
			NextPageOffset json.RawMessage `json:"next_page_offset"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodPost, "/collections/"+collection+"/points/scroll", body, &resp); err != nil {
		return nil, nil, err
	}
	next := resp.Result.NextPageOffset
	if string(next) == "null" {
		next = nil
	}
	return resp.Result.Points, next, nil
}

func (v *VectorStore) DeleteByFilter(ctx context.Context, collection string, filter *Filter) error {
	body := map[string]any{"filter": filter}
	return v.do(ctx, http.MethodPost, "/collections/"+collection+"/points/delete?wait=true", body, nil)
}

func (v *VectorStore) DeleteByIDs(ctx context.Context, collection string, ids []string) error {
	body := map[string]any{"points": ids}
	return v.do(ctx, http.MethodPost, "/collections/"+collection+"/points/delete?wait=true", body, nil)
}

func (v *VectorStore) Count(ctx context.Context, collection string, filter *Filter) int {
	// exact: true — Conduit creates no payload indexes, so Qdrant's
	// approximate count for a filtered query is always 0, which would make
	// every source look like it has no embedded data.
	body := map[string]any{"exact": true}
	if filter != nil {
		body["filter"] = filter
	}
	var resp struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := v.do(ctx, http.MethodPost, "/collections/"+collection+"/points/count", body, &resp); err != nil {
		return 0
	}
	return resp.Result.Count
}

// HealthCheck errors if Qdrant is unreachable.
func (v *VectorStore) HealthCheck(ctx context.Context) error {
	_, err := v.ListCollections(ctx)
	return err
}

// ── Conversion ──────────────────────────────────────────────────────────────

// PointToSearchResult mirrors point_to_search_result in app/rag/vector_store.py.
func PointToSearchResult(p ScoredPoint) models.SearchResult {
	payload := p.Payload
	text, _ := payload[models.PayloadText].(string)
	tags := map[string]string{}
	props := map[string]string{}
	for k, val := range payload {
		sv := fmt.Sprintf("%v", val)
		if strings.HasPrefix(k, models.TagPrefix) {
			tags[k[len(models.TagPrefix):]] = sv
		} else if strings.HasPrefix(k, models.PropPrefix) {
			props[k[len(models.PropPrefix):]] = sv
		}
	}
	return models.SearchResult{
		ID:         IDString(p.ID),
		Score:      p.Score,
		Text:       text,
		Tags:       tags,
		Properties: props,
	}
}
