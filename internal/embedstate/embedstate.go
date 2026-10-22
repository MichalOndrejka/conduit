// Package embedstate records which embedding model built the indexed vectors,
// so a model change made outside the Settings page (an env var such as
// EMBEDDING_MODEL in docker-compose, or a hand-edited config.json) is caught
// at startup instead of silently mixing vectors from two models in one
// collection.
package embedstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/store"
)

const fileName = "embedding-state.json"

// State is the part of the embedding config that determines vector
// compatibility. BaseURL is deliberately excluded: moving Ollama to another
// host serves the same model and keeps existing vectors valid.
type State struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

func stateOf(ec config.EmbeddingConfig) State {
	return State{Model: ec.Model, Dimensions: ec.Dimensions}
}

// Load returns the recorded state; ok is false if none was recorded yet.
func Load(dataDir string) (st State, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dataDir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, false, fmt.Errorf("%s: %w", fileName, err)
	}
	return st, true, nil
}

// Save records ec as the config the current vectors were built with.
func Save(dataDir string, ec config.EmbeddingConfig) error {
	data, err := json.MarshalIndent(stateOf(ec), "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, fileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DropAll deletes every collection, recreates an empty experience collection
// (memory upserts don't create it), and flags all sources for reindex — what
// a vector-incompatible embedding change requires.
func DropAll(ctx context.Context, vectors *rag.VectorStore, sources *store.SourceConfigStore, dimensions int) error {
	existing, err := vectors.ListCollections(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range existing {
		for _, col := range models.AllCollections {
			if name == col {
				errs = append(errs, vectors.DeleteCollection(ctx, col))
			}
		}
	}
	errs = append(errs,
		vectors.CreateCollection(ctx, models.CollectionExperience, dimensions),
		sources.ResetAllSyncStatus("needs-reindex"),
	)
	return errors.Join(errs...)
}

// Reconcile compares the configured embedding model/dimensions with the ones
// recorded in dataDir. On a change it drops all collections (including
// experience memories, which were embedded with the old model) and flags
// sources for reindex, then records the new state. On first run it only
// records the state. changed reports whether a drop happened.
//
// If the drop fails (e.g. Qdrant unreachable), the old state is kept so the
// next start retries; sources are still flagged so the UI shows they need
// reindexing.
func Reconcile(ctx context.Context, dataDir string, ec config.EmbeddingConfig, vectors *rag.VectorStore, sources *store.SourceConfigStore) (changed bool, prev State, err error) {
	prev, ok, err := Load(dataDir)
	if err != nil {
		return false, prev, err
	}
	if !ok {
		return false, prev, Save(dataDir, ec)
	}
	if prev == stateOf(ec) {
		return false, prev, nil
	}
	if err := DropAll(ctx, vectors, sources, ec.Dimensions); err != nil {
		_ = sources.ResetAllSyncStatus("needs-reindex")
		return true, prev, fmt.Errorf("dropping collections: %w", err)
	}
	return true, prev, Save(dataDir, ec)
}
