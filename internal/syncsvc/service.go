// Package syncsvc is the Go port of app/sync/service.py: the fetch → index
// pipeline with pause/cancel checkpoints, per-phase error tracking, and
// sync-status persistence to conduit-sources.json.
//
// Note: the optional LLM preprocessing phase (Phase 1.5 in Python) is not
// ported — it is disabled for the demo deployment per the migration plan. If
// preprocessing is enabled in config a warning is logged and the phase is
// skipped.
package syncsvc

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
	"github.com/MichalOndrejka/conduit/internal/store"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
)

type Service struct {
	cfg      *config.AppConfig
	store    *store.SourceConfigStore
	secrets  secrets.Reader
	indexer  *rag.DocumentIndexer
	progress *syncctl.ProgressStore
	control  *syncctl.ControlStore

	// inflight guards against concurrent syncs of the same source: a second
	// Sync while one is running would reset its pause/cancel state, clobber
	// its status writes, and its replace-delete could wipe points the first
	// run just wrote.
	mu       sync.Mutex
	inflight map[string]bool
}

func New(
	cfg *config.AppConfig,
	st *store.SourceConfigStore,
	secretsStore secrets.Reader,
	indexer *rag.DocumentIndexer,
	progress *syncctl.ProgressStore,
	control *syncctl.ControlStore,
) *Service {
	return &Service{
		cfg: cfg, store: st, secrets: secretsStore,
		indexer: indexer, progress: progress, control: control,
		inflight: map[string]bool{},
	}
}

func (s *Service) Progress() *syncctl.ProgressStore { return s.progress }
func (s *Service) Control() *syncctl.ControlStore   { return s.control }

// Sync runs the full pipeline for one source. Mirrors SyncService.sync.
// A no-op if a sync for this source is already running.
func (s *Service) Sync(ctx context.Context, sourceID string) {
	s.mu.Lock()
	if s.inflight[sourceID] {
		s.mu.Unlock()
		log.Printf("sync already running for source %s — ignoring duplicate request", sourceID)
		return
	}
	s.inflight[sourceID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, sourceID)
		s.mu.Unlock()
	}()

	src, err := s.store.Get(sourceID)
	if err != nil || src == nil {
		log.Printf("error: source not found: %s", sourceID)
		return
	}

	src.SyncStatus = "syncing"
	src.SyncError = nil
	if err := s.store.Save(*src); err != nil {
		log.Printf("error: saving sync status: %v", err)
		return
	}

	s.progress.Set(sourceID, models.SyncProgress{Phase: "fetching"})
	s.control.Register(sourceID)

	finish := func() {
		s.progress.Clear(sourceID)
		s.control.Clear(sourceID)
		if err := s.store.Save(*src); err != nil {
			log.Printf("error: saving sync result: %v", err)
		}
	}
	cancelledOutcome := func() {
		src.SyncStatus = "idle"
		src.SyncError = nil
		src.SyncErrorPhase = nil
		finish()
	}
	failedOutcome := func(phase string, err error) {
		log.Printf("warning: %s failed for source %s: %v", phase, sourceID, err)
		msg := err.Error()
		src.SyncStatus = "failed"
		src.SyncError = &msg
		src.SyncErrorPhase = &phase
		finish()
	}

	impl, err := sources.New(src, s.secrets)
	if err != nil {
		failedOutcome("fetch", err)
		return
	}
	collection := sources.CollectionFor(src)

	// ── Phase 1: fetch ──────────────────────────────────────────────────────
	if err := s.control.Checkpoint(ctx, sourceID); err != nil {
		cancelledOutcome()
		return
	}
	docs, err := impl.FetchDocuments(ctx, func(p models.SyncProgress) {
		s.progress.Set(sourceID, p)
	})
	if err != nil {
		if errors.Is(err, syncctl.ErrSyncCancelled) {
			cancelledOutcome()
			return
		}
		failedOutcome("fetch", err)
		return
	}

	// ── Phase 1.5: preprocessing not ported (see package doc) ──────────────
	if s.cfg.Preprocessing.Enabled {
		log.Printf("warning: preprocessing is enabled in config but not supported by the Go backend — skipping")
	}

	log.Printf("indexing %d documents for source %s", len(docs), sourceID)
	s.progress.Set(sourceID, models.SyncProgress{Phase: "indexing", Total: len(docs)})

	// ── Phase 2: embed & index ──────────────────────────────────────────────
	if err := s.control.Checkpoint(ctx, sourceID); err != nil {
		cancelledOutcome()
		return
	}
	err = s.indexer.IndexBatch(ctx, collection, docs, rag.IndexBatchOptions{
		ProgressCb: func(current, total int) {
			s.progress.Set(sourceID, models.SyncProgress{
				Phase: "indexing", Current: current, Total: total,
			})
		},
		ReplaceSourceID: src.ID,
		Checkpoint: func() error {
			return s.control.Checkpoint(ctx, sourceID)
		},
	})
	switch {
	case errors.Is(err, syncctl.ErrSyncCancelled):
		cancelledOutcome()
	case err != nil:
		failedOutcome("embed", err)
	default:
		now := time.Now().UTC()
		src.SyncStatus = "completed"
		src.LastSyncedAt = &now
		src.SyncError = nil
		src.SyncErrorPhase = nil
		finish()
	}
}

// SyncAll syncs every source sequentially, mirroring SyncService.sync_all.
func (s *Service) SyncAll(ctx context.Context) {
	srcs, err := s.store.ListAll()
	if err != nil {
		log.Printf("error: listing sources: %v", err)
		return
	}
	for _, src := range srcs {
		s.Sync(ctx, src.ID)
	}
}
