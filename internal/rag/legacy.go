package rag

import (
	"context"
	"log"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// RemoveFromLegacyCollections deletes a source's points from the
// collections of the merged legacy source types (models.LegacyCollections),
// once it has re-synced into its new collection or been deleted, and drops a
// legacy collection left empty. Best-effort: failures are logged, and the
// source's next sync tries again.
func (v *VectorStore) RemoveFromLegacyCollections(ctx context.Context, sourceID string) {
	existing, err := v.ListCollections(ctx)
	if err != nil {
		return
	}
	for _, coll := range models.LegacyCollections {
		if !contains(existing, coll) {
			continue
		}
		filter := &Filter{Must: []FieldCondition{{Key: models.TagKey("source_id"), Match: Match{Value: sourceID}}}}
		if err := v.DeleteByFilter(ctx, coll, filter); err != nil {
			log.Printf("warning: could not remove source %s from legacy collection %s: %v", sourceID, coll, err)
			continue
		}
		v.dropIfEmpty(ctx, coll)
	}
}

// DropEmptyLegacyCollections deletes every legacy collection that holds no
// points (called at startup).
func (v *VectorStore) DropEmptyLegacyCollections(ctx context.Context) {
	existing, err := v.ListCollections(ctx)
	if err != nil {
		return
	}
	for _, coll := range models.LegacyCollections {
		if contains(existing, coll) {
			v.dropIfEmpty(ctx, coll)
		}
	}
}

func (v *VectorStore) dropIfEmpty(ctx context.Context, coll string) {
	n, err := v.CountExact(ctx, coll, nil)
	if err != nil || n > 0 {
		return
	}
	if err := v.DeleteCollection(ctx, coll); err != nil {
		log.Printf("warning: could not drop empty legacy collection %s: %v", coll, err)
		return
	}
	log.Printf("dropped legacy collection %s: every source in it has re-synced into its new collection", coll)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
