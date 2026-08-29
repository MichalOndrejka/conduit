package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// ── test setup helpers ───────────────────────────────────────────────────────

func embeddingServer(t *testing.T, vector []float32, fail bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "embedding failed"}`))
			return
		}
		vec := make([]any, len(vector))
		for i, f := range vector {
			vec[i] = f
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	}))
}

func newEmbeddingService(t *testing.T, srv *httptest.Server) *rag.EmbeddingService {
	t.Helper()
	cfg := &config.AppConfig{}
	cfg.Embedding.BaseURL = srv.URL
	cfg.Embedding.MaxInputTokens = 8192
	return rag.NewEmbeddingService(cfg)
}

func newVectorStore(t *testing.T, srv *httptest.Server) *rag.VectorStore {
	t.Helper()
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = srv.URL
	cfg.Embedding.Dimensions = 3
	return rag.NewVectorStore(cfg)
}

// ── Remember ─────────────────────────────────────────────────────────────────

func TestRememberUpsertsSituationAndGuidance(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, []float32{0.1, 0.2, 0.3}, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	id, err := svc.Remember(context.Background(), "the build broke", "check the logs")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a non-empty entry ID")
	}
	if !strings.Contains(gotPath, "/collections/"+Collection+"/points") {
		t.Errorf("path = %q", gotPath)
	}
	points, ok := gotBody["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("points = %v", gotBody["points"])
	}
	point := points[0].(map[string]any)
	if point["id"] != id {
		t.Errorf("upserted point id = %v, want %v", point["id"], id)
	}
	payload := point["payload"].(map[string]any)
	if payload[models.PayloadText] != "the build broke" {
		t.Errorf("payload text = %v", payload[models.PayloadText])
	}
	if payload[models.PropKey("guidance")] != "check the logs" {
		t.Errorf("payload guidance = %v", payload[models.PropKey("guidance")])
	}
	if payload[models.PayloadIndexedAtMs] == nil {
		t.Error("expected indexed_at_ms to be set")
	}
	if payload[models.PropKey("created_at")] == nil {
		t.Error("expected created_at to be set")
	}
}

func TestRememberReturnsErrorWhenEmbedFails(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("qdrant should not be called when embedding fails")
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, nil, true)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	if _, err := svc.Remember(context.Background(), "situation", "guidance"); err == nil {
		t.Fatal("expected an error when embedding fails")
	}
}

func TestRememberRollsBackOnUpsertFailure(t *testing.T) {
	var upsertCalled, deleteCalled bool
	var deletedIDs []string
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			upsertCalled = true
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "upsert failed"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/points/delete") {
			deleteCalled = true
			var body struct {
				Points []string `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			deletedIDs = body.Points
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, []float32{0.1, 0.2, 0.3}, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	id, err := svc.Remember(context.Background(), "situation", "guidance")
	if err == nil {
		t.Fatal("expected an error when upsert fails")
	}
	if id != "" {
		t.Errorf("expected empty id on failure, got %q", id)
	}
	if !upsertCalled {
		t.Error("expected upsert to be attempted")
	}
	if !deleteCalled {
		t.Error("expected rollback delete to be attempted")
	}
	if len(deletedIDs) != 1 || deletedIDs[0] == "" {
		t.Errorf("deleted IDs = %v, want one non-empty entry ID", deletedIDs)
	}
}

// ── Retrieve ─────────────────────────────────────────────────────────────────

func TestRetrieveMapsPointsToExperienceHits(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"points": []map[string]any{
					{
						"id":    "e1",
						"score": 0.876543,
						"payload": map[string]any{
							models.PayloadText:         "situation text",
							models.PropKey("guidance"): "guidance text",
						},
					},
				},
			},
		})
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, []float32{0.1, 0.2, 0.3}, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	hits, err := svc.Retrieve(context.Background(), "what broke", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want 1", hits)
	}
	if hits[0].Situation != "situation text" || hits[0].Guidance != "guidance text" {
		t.Errorf("hit = %+v", hits[0])
	}
	if hits[0].Score != 0.877 {
		t.Errorf("score = %v, want rounded to 0.877", hits[0].Score)
	}
}

func TestRetrieveReturnsErrorWhenEmbedFails(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("qdrant should not be called when embedding fails")
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, nil, true)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	if _, err := svc.Retrieve(context.Background(), "query", 5); err == nil {
		t.Fatal("expected an error when embedding fails")
	}
}

func TestRetrieveReturnsErrorWhenSearchFails(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "search failed"}`))
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, []float32{0.1, 0.2, 0.3}, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	if _, err := svc.Retrieve(context.Background(), "query", 5); err == nil {
		t.Fatal("expected an error when search fails")
	}
}

// ── GetAllPaginated ──────────────────────────────────────────────────────────

func TestGetAllPaginatedSortsNewestFirst(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"points": []map[string]any{
					{
						"id": "older",
						"payload": map[string]any{
							models.PayloadText:           "older situation",
							models.PropKey("guidance"):   "older guidance",
							models.PropKey("created_at"): "2024-01-01T00:00:00Z",
						},
					},
					{
						"id": "newer",
						"payload": map[string]any{
							models.PayloadText:           "newer situation",
							models.PropKey("guidance"):   "newer guidance",
							models.PropKey("created_at"): "2024-06-01T00:00:00Z",
						},
					},
				},
				"next_page_offset": nil,
			},
		})
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, []float32{0.1, 0.2, 0.3}, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	entries, next, err := svc.GetAllPaginated(context.Background(), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Errorf("next offset = %s, want nil", string(next))
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	if entries[0].ID != "newer" || entries[1].ID != "older" {
		t.Errorf("entries not sorted newest-first: %+v", entries)
	}
}

func TestGetAllPaginatedReturnsErrorOnScrollFailure(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "scroll failed"}`))
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, []float32{0.1, 0.2, 0.3}, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	if _, _, err := svc.GetAllPaginated(context.Background(), 20, nil); err == nil {
		t.Fatal("expected an error when scroll fails")
	}
}

// ── Delete / Count ───────────────────────────────────────────────────────────

func TestDeleteSendsIDToVectorStore(t *testing.T) {
	var gotBody map[string]any
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": true})
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, nil, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	if err := svc.Delete(context.Background(), "entry-1"); err != nil {
		t.Fatal(err)
	}
	points, ok := gotBody["points"].([]any)
	if !ok || len(points) != 1 || points[0] != "entry-1" {
		t.Errorf("deleted points = %v, want [entry-1]", gotBody["points"])
	}
}

func TestCountReturnsStoredPointsCount(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"points_count": 42},
		})
	}))
	defer qdrant.Close()
	embed := embeddingServer(t, nil, false)
	defer embed.Close()

	svc := NewService(newVectorStore(t, qdrant), newEmbeddingService(t, embed))
	if got := svc.Count(context.Background()); got != 42 {
		t.Errorf("Count() = %d, want 42", got)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func TestPayloadStringHandlesNilMissingAndNonString(t *testing.T) {
	if got := payloadString(nil, "text"); got != "" {
		t.Errorf("payloadString(nil, ...) = %q, want empty string", got)
	}
	payload := map[string]any{
		"text":  "a string value",
		"count": 7,
	}
	if got := payloadString(payload, "text"); got != "a string value" {
		t.Errorf("payloadString string value = %q", got)
	}
	if got := payloadString(payload, "count"); got != "7" {
		t.Errorf("payloadString non-string value = %q, want %q", got, "7")
	}
	if got := payloadString(payload, "missing"); got != "" {
		t.Errorf("payloadString missing key = %q, want empty string", got)
	}
}

func TestRound3RoundsToThreeDecimalPlaces(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.876543, 0.877},
		{0.8765, 0.877},
		{0.1234, 0.123},
		{1.0, 1.0},
		{0.0, 0.0},
	}
	for _, tc := range cases {
		if got := round3(tc.in); got != tc.want {
			t.Errorf("round3(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
