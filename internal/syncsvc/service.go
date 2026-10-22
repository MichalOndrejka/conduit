// Package syncsvc is the Go port of app/sync/service.py: the fetch →
// preprocess → index pipeline with pause/cancel checkpoints, per-phase error
// tracking, and sync-status persistence to conduit-sources.json.
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

	// root is cancelled by Shutdown. Every sync is tied to it regardless of
	// the caller's ctx (handlers pass context.Background()), so a container
	// stop aborts in-flight work and records it as idle instead of leaving
	// the source stuck in "syncing". closing (guarded by mu) refuses new
	// syncs once shutdown has begun, so running.Add never races running.Wait.
	root    context.Context
	stopAll context.CancelFunc
	running sync.WaitGroup
	closing bool
}

func New(
	cfg *config.AppConfig,
	st *store.SourceConfigStore,
	secretsStore secrets.Reader,
	indexer *rag.DocumentIndexer,
	progress *syncctl.ProgressStore,
	control *syncctl.ControlStore,
) *Service {
	root, stopAll := context.WithCancel(context.Background())
	return &Service{
		cfg: cfg, store: st, secrets: secretsStore,
		indexer: indexer, progress: progress, control: control,
		inflight: map[string]bool{},
		root:     root, stopAll: stopAll,
	}
}

// Shutdown refuses new syncs, cancels running ones, and waits until each has
// persisted its final status, or ctx ends first.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.stopAll()

	done := make(chan struct{})
	go func() {
		s.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Progress() *syncctl.ProgressStore { return s.progress }
func (s *Service) Control() *syncctl.ControlStore   { return s.control }

// IsSyncing reports whether a sync of sourceID is running right now.
func (s *Service) IsSyncing(sourceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight[sourceID]
}

// Sync runs the full pipeline for one source. Mirrors SyncService.sync.
// A no-op if a sync for this source is already running.
func (s *Service) Sync(ctx context.Context, sourceID string) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		log.Printf("shutting down — ignoring sync request for source %s", sourceID)
		return
	}
	if s.inflight[sourceID] {
		s.mu.Unlock()
		log.Printf("sync already running for source %s — ignoring duplicate request", sourceID)
		return
	}
	s.inflight[sourceID] = true
	s.running.Add(1)
	s.mu.Unlock()
	defer s.running.Done()
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
	if src.Disabled {
		log.Printf("source %s is disabled — skipping sync", sourceID)
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

	// Derived per-source so RequestCancel can abort this sync's in-flight
	// ctx-aware work (HTTP fetches, embedding calls) immediately, instead of
	// waiting for the next checkpoint — which can be minutes away mid-fetch
	// on a large source, and would otherwise keep its concurrency slot stuck.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.root, cancel)()
	s.control.SetCancelFunc(sourceID, cancel)

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
		if isCancelled(err) {
			cancelledOutcome()
			return
		}
		failedOutcome("fetch", err)
		return
	}

	// ── Phase 1.5: optional LLM preprocessing (summarize before indexing) ──
	// Built per-sync so a Settings change applies without a restart (the web
	// server and this service share the same *config.AppConfig). Preprocessing
	// chunks each document first (with overlap, same as indexing) and
	// summarizes chunk-by-chunk, so a single large document never becomes one
	// oversized, expensive LLM call.
	var preprocessedChunks [][]models.TextChunk
	if pre := rag.NewDocumentPreprocessor(s.cfg); pre.EnabledForType(src.Type) {
		if err := s.control.Checkpoint(ctx, sourceID); err != nil {
			cancelledOutcome()
			return
		}
		log.Printf("preprocessing %d documents for source %s", len(docs), sourceID)
		s.progress.Set(sourceID, models.SyncProgress{Phase: "preprocessing", Total: len(docs)})
		preprocessedChunks, err = pre.Preprocess(ctx, docs, src.Type, rag.PreprocessOptions{
			ProgressCb: func(current, total int) {
				s.progress.Set(sourceID, models.SyncProgress{
					Phase: "preprocessing", Current: current, Total: total,
				})
			},
			Checkpoint: func() error { return s.control.Checkpoint(ctx, sourceID) },
		})
		if err != nil {
			if isCancelled(err) {
				cancelledOutcome()
				return
			}
			failedOutcome("preprocess", err)
			return
		}
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
		ReplaceSourceID:   src.ID,
		PrecomputedChunks: preprocessedChunks,
		Checkpoint: func() error {
			return s.control.Checkpoint(ctx, sourceID)
		},
	})
	switch {
	case isCancelled(err):
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

// isCancelled reports whether err resulted from a sync cancellation — either
// the cooperative ErrSyncCancelled returned at a checkpoint, or a
// context.Canceled surfacing from ctx-aware work (HTTP calls, …) aborted
// directly by RequestCancel's cancelFunc.
func isCancelled(err error) bool {
	return errors.Is(err, syncctl.ErrSyncCancelled) || errors.Is(err, context.Canceled)
}

// maxConcurrentSyncs caps how many sources sync simultaneously, preventing
// the embedding endpoint and external APIs from being overloaded when the
// user selects many sources at once.
const maxConcurrentSyncs = 4

// SyncSelected syncs the given source IDs in parallel, capped at maxConcurrentSyncs.
func (s *Service) SyncSelected(ctx context.Context, ids []string) {
	sem := make(chan struct{}, maxConcurrentSyncs)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			s.Sync(ctx, id)
		}(id)
	}
	wg.Wait()
}
