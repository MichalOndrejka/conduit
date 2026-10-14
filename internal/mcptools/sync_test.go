package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
)

type fakeSources []models.SourceDefinition

func (f fakeSources) ListAll() ([]models.SourceDefinition, error) { return f, nil }

type fakeRunner struct {
	mu       sync.Mutex
	syncing  map[string]bool
	progress *syncctl.ProgressStore
	started  chan []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{syncing: map[string]bool{}, progress: syncctl.NewProgressStore(), started: make(chan []string, 1)}
}

func (f *fakeRunner) SyncSelected(_ context.Context, ids []string) { f.started <- ids }
func (f *fakeRunner) IsSyncing(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncing[id]
}
func (f *fakeRunner) Progress() *syncctl.ProgressStore { return f.progress }

func setupSync(t *testing.T, runner *fakeRunner) (*server.MCPServer, context.Context) {
	t.Helper()
	synced := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	errMsg, phase := "HTTP 401", "fetch"
	sources := fakeSources{
		{ID: "wi", Name: "Work items", Type: models.SourceWorkItemQuery, SyncStatus: "completed", LastSyncedAt: &synced,
			Config: map[string]string{"Password": "ado-pat"}},
		{ID: "code", Name: "Code", Type: models.SourceCodeRepo, SyncStatus: "syncing"},
		{ID: "docs", Name: "Docs", Type: models.SourceDocumentation, SyncStatus: "failed", SyncError: &errMsg, SyncErrorPhase: &phase},
		{ID: "old", Name: "Old", Type: models.SourceDocumentation, Disabled: true},
	}
	s := server.NewMCPServer("test", "0.0.0")
	RegisterSyncTool(s, sources, runner)
	return s, context.Background()
}

func TestSyncStatusListsEverySourceWithoutConfig(t *testing.T) {
	runner := newFakeRunner()
	runner.syncing["code"] = true
	runner.progress.Set("code", models.SyncProgress{Phase: "indexing", Current: 3, Total: 10})
	s, ctx := setupSync(t, runner)

	result := callTool(t, s, ctx, "sync_sources", map[string]any{"request": map[string]any{"mode": "status"}})
	text := resultText(t, result)
	if result.IsError {
		t.Fatalf("status failed: %s", text)
	}
	if strings.Contains(text, "ado-pat") {
		t.Error("status leaked source config")
	}
	var out struct {
		Sources []syncSourceStatus `json:"sources"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 4 {
		t.Fatalf("got %d sources, want 4", len(out.Sources))
	}
	byID := map[string]syncSourceStatus{}
	for _, st := range out.Sources {
		byID[st.ID] = st
	}
	if st := byID["wi"]; st.SyncStatus != "completed" || st.LastSyncedAt == nil || st.Progress != nil {
		t.Errorf("wi status = %+v", st)
	}
	if st := byID["code"]; st.SyncStatus != "syncing" || st.Progress == nil || st.Progress.Current != 3 {
		t.Errorf("code status = %+v", st)
	}
	if st := byID["docs"]; st.SyncError == nil || *st.SyncError != "HTTP 401" || *st.SyncErrorPhase != "fetch" {
		t.Errorf("docs status = %+v", st)
	}
	if st := byID["old"]; !st.Disabled || st.SyncStatus != "idle" {
		t.Errorf("old status = %+v", st)
	}
}

func TestSyncStatusFiltersBySourceIDs(t *testing.T) {
	s, ctx := setupSync(t, newFakeRunner())
	text := resultText(t, callTool(t, s, ctx, "sync_sources", map[string]any{"request": map[string]any{
		"mode": "status", "source_ids": []any{"docs", "missing"},
	}}))
	var out struct {
		Sources  []syncSourceStatus `json:"sources"`
		NotFound []string           `json:"not_found"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 1 || out.Sources[0].ID != "docs" {
		t.Errorf("sources = %+v, want only docs", out.Sources)
	}
	if len(out.NotFound) != 1 || out.NotFound[0] != "missing" {
		t.Errorf("not_found = %v", out.NotFound)
	}
}

func TestSyncTriggerStartsOnlyEligibleSources(t *testing.T) {
	runner := newFakeRunner()
	runner.syncing["code"] = true
	s, ctx := setupSync(t, runner)

	result := callTool(t, s, ctx, "sync_sources", map[string]any{"request": map[string]any{
		"mode": "trigger", "source_ids": []any{"wi", "docs", "wi", "code", "old", "missing"},
	}})
	text := resultText(t, result)
	if result.IsError {
		t.Fatalf("trigger failed: %s", text)
	}
	var out struct {
		Outcomes map[string]string `json:"outcomes"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"wi": "started", "docs": "started", "code": "already_syncing", "old": "disabled", "missing": "not_found"}
	for id, w := range want {
		if out.Outcomes[id] != w {
			t.Errorf("outcome[%s] = %q, want %q", id, out.Outcomes[id], w)
		}
	}
	select {
	case ids := <-runner.started:
		if strings.Join(ids, ",") != "wi,docs" {
			t.Errorf("synced %v, want [wi docs]", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("no sync started")
	}
}

func TestSyncTriggerRejectsBadRequests(t *testing.T) {
	s, ctx := setupSync(t, newFakeRunner())
	for name, req := range map[string]map[string]any{
		"no ids":        {"mode": "trigger"},
		"empty ids":     {"mode": "trigger", "source_ids": []any{}},
		"non-string id": {"mode": "trigger", "source_ids": []any{1}},
		"unknown mode":  {"mode": "delete"},
	} {
		if result := callTool(t, s, ctx, "sync_sources", map[string]any{"request": req}); !result.IsError {
			t.Errorf("%s: expected a tool error, got %s", name, resultText(t, result))
		}
	}
}
