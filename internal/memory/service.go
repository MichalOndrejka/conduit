// Package memory is the Go port of app/memory/service.py: stores and
// retrieves LLM experience entries in Qdrant.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

const Collection = models.CollectionExperience

type Service struct {
	store     *rag.VectorStore
	embedding *rag.EmbeddingService
}

func NewService(store *rag.VectorStore, embedding *rag.EmbeddingService) *Service {
	return &Service{store: store, embedding: embedding}
}

// ── Write ──────────────────────────────────────────────────────────────────

// Remember embeds situation and stores situation+guidance. Returns the entry UUID.
func (s *Service) Remember(ctx context.Context, situation, guidance string) (string, error) {
	entryID := uuid.NewString()
	vector, err := s.embedding.Embed(ctx, situation)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()

	point := rag.Point{
		ID:     entryID,
		Vector: vector,
		Payload: map[string]any{
			models.PayloadText:           situation,
			models.PropKey("guidance"):   guidance,
			models.PayloadIndexedAtMs:    now.UnixMilli(),
			models.PropKey("created_at"): now.Format(time.RFC3339Nano),
		},
	}
	if err := s.store.Upsert(ctx, Collection, []rag.Point{point}); err != nil {
		log.Printf("warning: upsert failed for experience %s — rolling back", entryID)
		_ = s.store.DeleteByIDs(ctx, Collection, []string{entryID})
		return "", err
	}
	return entryID, nil
}

// ── Read ───────────────────────────────────────────────────────────────────

type ExperienceHit struct {
	Situation string  `json:"situation"`
	Guidance  string  `json:"guidance"`
	Score     float64 `json:"score"`
}

// Retrieve does semantic search over situations; returns matching guidance entries.
func (s *Service) Retrieve(ctx context.Context, query string, topK int) ([]ExperienceHit, error) {
	vector, err := s.embedding.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	points, err := s.store.Search(ctx, Collection, vector, topK, nil)
	if err != nil {
		return nil, err
	}
	results := make([]ExperienceHit, 0, len(points))
	for _, p := range points {
		results = append(results, ExperienceHit{
			Situation: payloadString(p.Payload, models.PayloadText),
			Guidance:  payloadString(p.Payload, models.PropKey("guidance")),
			Score:     round3(p.Score),
		})
	}
	return results, nil
}

type Entry struct {
	ID        string `json:"id"`
	Situation string `json:"situation"`
	Guidance  string `json:"guidance"`
	CreatedAt string `json:"created_at"`
}

// GetAllPaginated returns a page of experience entries for the UI, newest first.
func (s *Service) GetAllPaginated(
	ctx context.Context, limit int, offset json.RawMessage,
) ([]Entry, json.RawMessage, error) {
	points, next, err := s.store.Scroll(ctx, Collection, nil, limit, offset, false)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]Entry, 0, len(points))
	for _, p := range points {
		entries = append(entries, Entry{
			ID:        rag.IDString(p.ID),
			Situation: payloadString(p.Payload, models.PayloadText),
			Guidance:  payloadString(p.Payload, models.PropKey("guidance")),
			CreatedAt: payloadString(p.Payload, models.PropKey("created_at")),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt > entries[j].CreatedAt })
	return entries, next, nil
}

// ── Delete / count ──────────────────────────────────────────────────────────

func (s *Service) Delete(ctx context.Context, entryID string) error {
	return s.store.DeleteByIDs(ctx, Collection, []string{entryID})
}

func (s *Service) Count(ctx context.Context) int {
	return s.store.PointsCount(ctx, Collection)
}

// ── helpers ─────────────────────────────────────────────────────────────────

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	if v, ok := payload[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func round3(f float64) float64 {
	return float64(int(f*1000+0.5)) / 1000
}
