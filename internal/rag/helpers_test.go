package rag

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// indexRequests records the payload indexes a fake Qdrant was asked to
// create and answers those requests, so fakes written for other endpoints
// don't mistake them for upserts or collection creation.
type indexRequests struct {
	mu   sync.Mutex
	keys map[string]string // collection/field → schema
}

func (ir *indexRequests) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/index") {
			var body struct {
				FieldName   string `json:"field_name"`
				FieldSchema string `json:"field_schema"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			coll := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/collections/"), "/index")
			ir.mu.Lock()
			if ir.keys == nil {
				ir.keys = map[string]string{}
			}
			ir.keys[coll+"/"+body.FieldName] = body.FieldSchema
			ir.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"status": "completed"}})
			return
		}
		h(w, r)
	}
}

func (ir *indexRequests) has(coll, field string) (string, bool) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	s, ok := ir.keys[coll+"/"+field]
	return s, ok
}

// skipIndexRequests answers payload-index requests ahead of h.
func skipIndexRequests(h http.HandlerFunc) http.HandlerFunc {
	return (&indexRequests{}).wrap(h)
}
