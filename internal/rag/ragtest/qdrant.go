// Package ragtest provides an in-memory fake of the Qdrant REST endpoints
// Conduit uses, for tests that exercise indexing end to end.
package ragtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// Point is one stored point.
type Point struct {
	Vector  []float32
	Payload map[string]any
}

// Qdrant is the fake. Its methods are safe for concurrent use.
type Qdrant struct {
	mu          sync.Mutex
	Collections map[string]map[string]*Point // collection → id → point
	Sizes       map[string]int
	// Requests counts requests by "METHOD pattern".
	Requests map[string]int
	// FailUpsertAfter, when > 0, makes upserts fail once that many have
	// succeeded.
	FailUpsertAfter int
	upserts         int
}

func New() *Qdrant {
	return &Qdrant{Collections: map[string]map[string]*Point{}, Sizes: map[string]int{}, Requests: map[string]int{}}
}

type condition struct {
	Key   string `json:"key"`
	Match struct {
		Value any      `json:"value"`
		Any   []string `json:"any"`
	} `json:"match"`
}

type filter struct {
	Must    []condition `json:"must"`
	MustNot []condition `json:"must_not"`
}

func (c condition) matches(p map[string]any) bool {
	v, ok := p[c.Key]
	if !ok {
		return false
	}
	vals := []any{v}
	if list, isList := v.([]any); isList {
		vals = list
	}
	for _, x := range vals {
		if c.Match.Value != nil && fmt.Sprint(x) == fmt.Sprint(c.Match.Value) {
			return true
		}
		for _, a := range c.Match.Any {
			if fmt.Sprint(x) == a {
				return true
			}
		}
	}
	return false
}

func (f *filter) matches(p map[string]any) bool {
	if f == nil {
		return true
	}
	for _, c := range f.Must {
		if !c.matches(p) {
			return false
		}
	}
	for _, c := range f.MustNot {
		if c.matches(p) {
			return false
		}
	}
	return true
}

// Count is the number of points in collection matching key=value (all
// points when key is "").
func (q *Qdrant) Count(collection, key string, value any) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, p := range q.Collections[collection] {
		if key == "" || fmt.Sprint(p.Payload[key]) == fmt.Sprint(value) {
			n++
		}
	}
	return n
}

// Payloads returns copies of the payloads in collection, by point ID.
func (q *Qdrant) Payloads(collection string) map[string]map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[string]map[string]any{}
	for id, p := range q.Collections[collection] {
		cp := map[string]any{}
		for k, v := range p.Payload {
			cp[k] = v
		}
		out[id] = cp
	}
	return out
}

// Mutate runs fn on the stored points under the lock (to simulate outside
// changes).
func (q *Qdrant) Mutate(fn func(cols map[string]map[string]*Point)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	fn(q.Collections)
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

// normalize round-trips v through JSON, as a real server stores it.
func normalize(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func (q *Qdrant) Handler() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, col map[string]*Point, name string)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.Requests[pattern]++
			name := r.PathValue("name")
			col := q.Collections[name]
			if col == nil && pattern != "PUT /collections/{name}" && pattern != "GET /collections" {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, map[string]any{"status": map[string]any{"error": "Not found: collection " + name}})
				return
			}
			fn(w, r, col, name)
		})
	}
	handle("GET /collections", func(w http.ResponseWriter, _ *http.Request, _ map[string]*Point, _ string) {
		var cols []map[string]string
		for name := range q.Collections {
			cols = append(cols, map[string]string{"name": name})
		}
		writeJSON(w, map[string]any{"result": map[string]any{"collections": cols}})
	})
	handle("PUT /collections/{name}", func(w http.ResponseWriter, r *http.Request, _ map[string]*Point, name string) {
		var body struct {
			Vectors struct {
				Size int `json:"size"`
			} `json:"vectors"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		q.Collections[name] = map[string]*Point{}
		q.Sizes[name] = body.Vectors.Size
		writeJSON(w, map[string]any{"result": true})
	})
	handle("GET /collections/{name}", func(w http.ResponseWriter, _ *http.Request, col map[string]*Point, name string) {
		writeJSON(w, map[string]any{"result": map[string]any{
			"points_count": len(col),
			"config":       map[string]any{"params": map[string]any{"vectors": map[string]any{"size": q.Sizes[name]}}},
		}})
	})
	handle("DELETE /collections/{name}", func(w http.ResponseWriter, _ *http.Request, _ map[string]*Point, name string) {
		delete(q.Collections, name)
		delete(q.Sizes, name)
		writeJSON(w, map[string]any{"result": true})
	})
	handle("PUT /collections/{name}/index", func(w http.ResponseWriter, _ *http.Request, _ map[string]*Point, _ string) {
		writeJSON(w, map[string]any{"result": true})
	})
	handle("PUT /collections/{name}/points", func(w http.ResponseWriter, r *http.Request, col map[string]*Point, _ string) {
		var body struct {
			Points []struct {
				ID      string         `json:"id"`
				Vector  []float32      `json:"vector"`
				Payload map[string]any `json:"payload"`
			} `json:"points"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if q.FailUpsertAfter > 0 && q.upserts >= q.FailUpsertAfter {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"status": map[string]any{"error": "injected upsert failure"}})
			return
		}
		q.upserts++
		for _, p := range body.Points {
			col[p.ID] = &Point{Vector: p.Vector, Payload: normalize(p.Payload)}
		}
		writeJSON(w, map[string]any{"result": true})
	})
	handle("POST /collections/{name}/points", func(w http.ResponseWriter, r *http.Request, col map[string]*Point, _ string) {
		var body struct {
			IDs        []string `json:"ids"`
			WithVector bool     `json:"with_vector"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var out []map[string]any
		for _, id := range body.IDs {
			if p, ok := col[id]; ok {
				rec := map[string]any{"id": id, "payload": p.Payload}
				if body.WithVector {
					rec["vector"] = p.Vector
				}
				out = append(out, rec)
			}
		}
		writeJSON(w, map[string]any{"result": out})
	})
	handle("POST /collections/{name}/points/scroll", func(w http.ResponseWriter, r *http.Request, col map[string]*Point, _ string) {
		var body struct {
			Filter      *filter         `json:"filter"`
			Limit       int             `json:"limit"`
			Offset      json.RawMessage `json:"offset"`
			WithPayload json.RawMessage `json:"with_payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var include []string
		var sel struct {
			Include []string `json:"include"`
		}
		if json.Unmarshal(body.WithPayload, &sel) == nil {
			include = sel.Include
		}
		ids := make([]string, 0, len(col))
		for id, p := range col {
			if body.Filter.matches(p.Payload) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		var offset string
		_ = json.Unmarshal(body.Offset, &offset)
		start := sort.SearchStrings(ids, offset)
		limit := body.Limit
		if limit <= 0 {
			limit = 10
		}
		end := min(start+limit, len(ids))
		var points []map[string]any
		for _, id := range ids[start:end] {
			payload := col[id].Payload
			if include != nil {
				sub := map[string]any{}
				for _, k := range include {
					if v, ok := payload[k]; ok {
						sub[k] = v
					}
				}
				payload = sub
			}
			points = append(points, map[string]any{"id": id, "payload": payload})
		}
		var next any
		if end < len(ids) {
			next = ids[end]
		}
		writeJSON(w, map[string]any{"result": map[string]any{"points": points, "next_page_offset": next}})
	})
	handle("POST /collections/{name}/points/count", func(w http.ResponseWriter, r *http.Request, col map[string]*Point, _ string) {
		var body struct {
			Filter *filter `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		n := 0
		for _, p := range col {
			if body.Filter.matches(p.Payload) {
				n++
			}
		}
		writeJSON(w, map[string]any{"result": map[string]any{"count": n}})
	})
	handle("POST /collections/{name}/points/payload", func(w http.ResponseWriter, r *http.Request, col map[string]*Point, _ string) {
		var body struct {
			Payload map[string]any `json:"payload"`
			Filter  *filter        `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, p := range col {
			if body.Filter.matches(p.Payload) {
				for k, v := range normalize(body.Payload) {
					p.Payload[k] = v
				}
			}
		}
		writeJSON(w, map[string]any{"result": true})
	})
	handle("POST /collections/{name}/points/delete", func(w http.ResponseWriter, r *http.Request, col map[string]*Point, _ string) {
		var body struct {
			Filter *filter  `json:"filter"`
			Points []string `json:"points"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, id := range body.Points {
			delete(col, id)
		}
		if body.Filter != nil {
			for id, p := range col {
				if body.Filter.matches(p.Payload) {
					delete(col, id)
				}
			}
		}
		writeJSON(w, map[string]any{"result": true})
	})
	return mux
}
