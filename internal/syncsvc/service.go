// Package syncsvc is the Go port of app/sync/service.py: the fetch →
// preprocess → index pipeline with pause/cancel checkpoints, per-phase error
// tracking, and sync-status persistence to conduit-sources.json.
package syncsvc

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
	"github.com/MichalOndrejka/conduit/internal/stats"
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

	stats *stats.Recorder // nil: sync runs aren't recorded
}

// SetRecorder makes every sync run append its outcome, timings, counts and
// API requests to the stats history.
func (s *Service) SetRecorder(r *stats.Recorder) { s.stats = r }

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
		s.progress.Clear(sourceID) // drop a "queued" mark
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

	// A sync that doesn't get going drops the "queued" mark SyncSelected
	// may have set; one that does replaces it with its progress.
	begun := false
	defer func() {
		if !begun {
			s.progress.Clear(sourceID)
		}
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
	if models.IsLiveType(src.Type) {
		log.Printf("source %s is read live by its tool — nothing to sync", sourceID)
		return
	}

	// Progress before the status: the "syncing" status change is what the
	// Sources page sees first, and it should come with "fetching", not with
	// the stale "queued" mark.
	s.progress.Set(sourceID, models.SyncProgress{Phase: "fetching"})
	src.SyncStatus = "syncing"
	src.SyncError = nil
	// What the sync is built from, to tell at the end whether the source
	// was edited meanwhile.
	settings := settingsOf(src)
	if ok, err := s.store.Modify(sourceID, func(cur *models.SourceDefinition) {
		cur.SyncStatus, cur.SyncError = src.SyncStatus, src.SyncError
	}); err != nil || !ok {
		log.Printf("error: saving sync status of source %s: %v (found: %v)", sourceID, err, ok)
		return
	}
	begun = true
	s.control.Register(sourceID)

	// Derived per-source so RequestCancel can abort this sync's in-flight
	// ctx-aware work (HTTP fetches, embedding calls) immediately, instead of
	// waiting for the next checkpoint — which can be minutes away mid-fetch
	// on a large source, and would otherwise keep its concurrency slot stuck.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.root, cancel)()
	s.control.SetCancelFunc(sourceID, cancel)

	// Every HTTP request the fetch makes to the source's API is counted
	// under this ctx (see stats.CountingTransport).
	ctx, requests := stats.WithRequestCounter(ctx)
	started := time.Now()
	run := &stats.SyncRun{SourceID: src.ID, SourceName: src.Name, SourceType: src.Type, Provider: src.StatsProvider()}
	record := func(status string) {
		run.Status = status
		run.DurationMs = time.Since(started).Milliseconds()
		run.Requests, run.RateLimited = requests.Requests(), requests.RateLimited()
		s.stats.RecordSync(run)
	}

	finish := func() {
		s.progress.Clear(sourceID)
		s.control.Clear(sourceID)
		// Only the sync's own fields are written back, onto the source as it
		// is now: an edit made during the sync is kept, not reverted.
		var current *models.SourceDefinition
		ok, err := s.store.Modify(sourceID, func(cur *models.SourceDefinition) {
			reindex := cur.SyncStatus == "needs-reindex" // the index was dropped while syncing
			cur.SyncStatus, cur.SyncError, cur.SyncErrorPhase = src.SyncStatus, src.SyncError, src.SyncErrorPhase
			cur.LastSyncedAt, cur.Identity = src.LastSyncedAt, src.Identity
			// What was indexed is from settings the source no longer has
			// (or from before the index was dropped): not a completed sync
			// of the source as it is.
			if src.SyncStatus == "completed" && (reindex || settingsOf(cur) != settings) {
				cur.SyncStatus = "needs-reindex"
			}
			c := *cur
			current = &c
		})
		pctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		switch {
		case err != nil:
			log.Printf("error: saving sync result: %v", err)
		case !ok:
			// Deleted while it synced: it stays deleted, and so do the points
			// the sync wrote after the delete removed the others.
			log.Printf("source %s was deleted while syncing — removing what the sync indexed", sourceID)
			if err := s.indexer.PurgeSource(pctx, sourceID, ""); err != nil {
				log.Printf("error: %v", err)
			}
		case sources.CollectionFor(current) != sources.CollectionFor(src):
			// Its type changed while it synced: what the sync wrote is in the
			// collection it left.
			if err := s.indexer.PurgeSource(pctx, sourceID, sources.CollectionFor(current)); err != nil {
				log.Printf("error: %v", err)
			}
		}
	}
	cancelledOutcome := func() {
		src.SyncStatus = "idle"
		src.SyncError = nil
		src.SyncErrorPhase = nil
		finish()
		record("cancelled")
	}
	failedOutcome := func(phase string, err error) {
		log.Printf("warning: %s failed for source %s: %v", phase, sourceID, err)
		msg := err.Error()
		src.SyncStatus = "failed"
		src.SyncError = &msg
		src.SyncErrorPhase = &phase
		finish()
		run.ErrorPhase, run.Error = phase, msg
		record("failed")
	}

	impl, err := sources.New(src, s.secrets)
	if err != nil {
		failedOutcome("fetch", err)
		return
	}
	collection := sources.CollectionFor(src)

	// A sync that finds the index changed under it (another process dropped
	// or rewrote the source's points) runs once more from what is there now.
	var phase string
	for attempt := 0; ; attempt++ {
		phase, err = s.pipeline(ctx, src, impl, collection, run)
		if attempt == 0 && errors.Is(err, rag.ErrIndexChanged) {
			log.Printf("warning: %v — syncing source %s again", err, sourceID)
			continue
		}
		break
	}
	switch {
	case isCancelled(err):
		cancelledOutcome()
	case err != nil:
		failedOutcome(phase, err)
	default:
		now := time.Now().UTC()
		src.SyncStatus = "completed"
		src.LastSyncedAt = &now
		src.SyncError = nil
		src.SyncErrorPhase = nil
		finish()
		record("completed")
	}
}

// pipeline runs one fetch → preprocess → index pass for src, recording its
// timings and counts in run. On failure it returns the phase that failed.
//
// It is incremental: the index's own record of the source (fingerprints on
// every point) is read first and handed to the fetch, which skips what is
// provably current (see sources.IndexState); then only new and changed
// documents are preprocessed and embedded, documents whose metadata alone
// changed get new payloads, and those the source no longer has are deleted.
func (s *Service) pipeline(ctx context.Context, src *models.SourceDefinition, impl sources.Source, collection string, run *stats.SyncRun) (string, error) {
	sourceID := src.ID
	checkpoint := func() error { return s.control.Checkpoint(ctx, sourceID) }
	// Built per-sync so a Settings change applies without a restart (the web
	// server and this service share the same *config.AppConfig).
	pre := rag.NewDocumentPreprocessor(s.cfg)
	preprocess := pre.EnabledForType(src.Type)
	preFP := ""
	if preprocess {
		preFP = pre.Fingerprint()
	}

	// ── Phase 0: what the index holds ───────────────────────────────────────
	if err := checkpoint(); err != nil {
		return "", err
	}
	s.progress.Set(sourceID, models.SyncProgress{Phase: "fetching", Message: "Reading what is indexed"})
	phaseStart := time.Now()
	// The source's points belong in its collection only: any elsewhere (it
	// changed type, and the cleanup then failed or a sync of the old type
	// was still writing) would surface in the wrong search tool for good.
	if err := s.indexer.PurgeSource(ctx, sourceID, collection); err != nil {
		return "embed", err
	}
	fp, err := s.indexer.Prepare(ctx, collection, preFP)
	if err != nil {
		return "embed", err
	}
	state, err := s.indexer.LoadState(ctx, collection, sourceID)
	if err != nil {
		return "embed", err
	}
	indexState := &sources.IndexState{IndexFP: fp, State: state}

	// ── Phase 1: fetch ──────────────────────────────────────────────────────
	if err := checkpoint(); err != nil {
		return "", err
	}
	docs, err := impl.FetchDocuments(sources.WithIndexState(ctx, indexState), func(p models.SyncProgress) {
		s.progress.Set(sourceID, p)
	})
	run.FetchMs = time.Since(phaseStart).Milliseconds()
	run.Documents = len(docs)
	if err != nil {
		return "fetch", err
	}
	for _, d := range docs {
		if d.Keep {
			run.NotRefetched++
		}
	}
	// The token owner, so a "me" filter resolves from the cache. Not knowing
	// it only leaves "me" unresolvable for this source; it doesn't fail the
	// sync.
	if ids, ok := impl.(sources.IdentitySource); ok {
		if identity, err := ids.FetchIdentity(ctx); err != nil {
			log.Printf("warning: reading the token owner of source %s: %v", sourceID, err)
		} else {
			src.Identity = identity
		}
	}

	plan, err := s.indexer.Diff(docs, state, fp)
	if err != nil {
		return "fetch", err
	}
	run.Added = plan.Added
	run.Updated = len(plan.Embed) - plan.Added
	run.MetadataOnly = len(plan.MetaOnly)
	run.Unchanged = plan.Unchanged
	run.Deleted = len(plan.Deleted)
	log.Printf("source %s: %d documents — %d new, %d changed, %d metadata only, %d unchanged, %d removed",
		sourceID, len(docs), run.Added, run.Updated, run.MetadataOnly, run.Unchanged, run.Deleted)

	// ── Phase 1.5: optional LLM preprocessing (summarize before indexing) ──
	// Only the documents being embedded. Preprocessing chunks each document
	// first (with overlap, same as indexing) and summarizes chunk-by-chunk,
	// so a single large document never becomes one oversized, expensive LLM
	// call.
	var preprocessedChunks [][]models.TextChunk
	degraded := make([]bool, len(plan.Embed))
	if preprocess && len(plan.Embed) > 0 {
		if err := checkpoint(); err != nil {
			return "", err
		}
		log.Printf("preprocessing %d documents for source %s", len(plan.Embed), sourceID)
		preStart := time.Now()
		s.progress.Set(sourceID, models.SyncProgress{Phase: "preprocessing", Total: len(plan.Embed)})
		var mu sync.Mutex
		preprocessedChunks, err = pre.Preprocess(ctx, plan.Embed, src.Type, rag.PreprocessOptions{
			ProgressCb: func(current, total int) {
				s.progress.Set(sourceID, models.SyncProgress{
					Phase: "preprocessing", Current: current, Total: total,
				})
			},
			Checkpoint: checkpoint,
			Degraded: func(i int) {
				mu.Lock()
				degraded[i] = true
				mu.Unlock()
			},
		})
		run.PreprocessMs = time.Since(preStart).Milliseconds()
		if err != nil {
			return "preprocess", err
		}
	}

	// ── Phase 2: embed & index ──────────────────────────────────────────────
	log.Printf("indexing %d documents for source %s", len(plan.Embed), sourceID)
	s.progress.Set(sourceID, models.SyncProgress{Phase: "indexing", Total: len(plan.Embed)})
	if err := checkpoint(); err != nil {
		return "", err
	}
	indexStart := time.Now()
	res, err := s.indexer.Apply(ctx, collection, sourceID, plan, rag.ApplyOptions{
		ProgressCb: func(current, total int) {
			s.progress.Set(sourceID, models.SyncProgress{
				Phase: "indexing", Current: current, Total: total,
			})
		},
		Checkpoint:        checkpoint,
		PrecomputedChunks: preprocessedChunks,
		Degraded:          degraded,
		Seal:              indexState.Seal,
	})
	run.IndexMs = time.Since(indexStart).Milliseconds()
	run.Chunks, run.EmbeddedChunks = res.Chunks, res.Embedded
	if err != nil {
		return "embed", err
	}
	return "", nil
}

// settingsOf is everything about src a sync's result depends on.
func settingsOf(src *models.SourceDefinition) string {
	cfg, _ := json.Marshal(src.Config) // map keys marshal sorted
	return src.Type + "\x00" + src.Name + "\x00" + string(cfg)
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

// PhaseQueued is the progress phase of a source waiting for a sync slot.
const PhaseQueued = "queued"

// SyncSelected syncs the given source IDs in parallel, capped at maxConcurrentSyncs.
// Sources of the batch that read the same repository archive (a Source Code
// and a Test Code source of one repository, branch and credential) share a
// single download, deleted when the batch ends.
func (s *Service) SyncSelected(ctx context.Context, ids []string) {
	// Every source waiting for a slot shows as queued until its sync starts.
	// One already running keeps its progress (its new request is a no-op);
	// one that doesn't sync (a live source, a disabled one) drops the mark.
	for _, id := range ids {
		if !s.IsSyncing(id) {
			s.progress.Set(id, models.SyncProgress{Phase: PhaseQueued})
		}
	}
	archives := sources.NewArchiveCache()
	defer archives.Close()
	ctx = sources.WithArchiveCache(ctx, archives)
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
