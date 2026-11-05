package syncsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/stats"
)

func syncRuns(t *testing.T, rec *stats.Recorder) []*stats.SyncRun {
	t.Helper()
	events := rec.Events()
	var runs []*stats.SyncRun
	for _, e := range events {
		if e.Sync != nil {
			runs = append(runs, e.Sync)
		}
	}
	return runs
}

func TestSyncRecordsRunInStats(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
			{"id": 1, "title": "First item"}, {"id": 2, "title": "Second item"},
		}})
	}))
	defer apiSrv.Close()

	svc, _, _, src := setupPipeline(t, apiSrv.URL)
	rec := stats.New()
	svc.SetRecorder(rec)
	svc.Sync(context.Background(), src.ID)

	runs := syncRuns(t, rec)
	if len(runs) != 1 {
		t.Fatalf("sync runs = %d", len(runs))
	}
	r := runs[0]
	if r.Status != "completed" || r.SourceID != src.ID || r.SourceName != "Fake API" || r.Provider != "azure-devops" {
		t.Errorf("run = %+v", r)
	}
	if r.Documents != 2 || r.Chunks != 2 || r.Requests != 1 || r.RateLimited != 0 {
		t.Errorf("counts = documents %d, chunks %d, requests %d, rate limited %d; want 2, 2, 1, 0", r.Documents, r.Chunks, r.Requests, r.RateLimited)
	}
	if r.DurationMs < r.FetchMs || r.DurationMs < r.IndexMs {
		t.Errorf("timings = %+v", r)
	}
}

func TestSyncRecordsFailureAndRateLimits(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer apiSrv.Close()

	svc, _, _, src := setupPipeline(t, apiSrv.URL)
	rec := stats.New()
	svc.SetRecorder(rec)
	svc.Sync(context.Background(), src.ID)

	runs := syncRuns(t, rec)
	if len(runs) != 1 || runs[0].Status != "failed" || runs[0].ErrorPhase != "fetch" || runs[0].Error == "" {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[0].Requests < 1 || runs[0].RateLimited != runs[0].Requests {
		t.Errorf("requests %d, rate limited %d: every request was refused", runs[0].Requests, runs[0].RateLimited)
	}
}
