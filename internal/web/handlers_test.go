package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

// ── Health / status ──────────────────────────────────────────────────────────

func TestHandleHealthReportsReadyProbes(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Qdrant    struct{ Status string } `json:"qdrant"`
		Embedding struct{ Status string } `json:"embedding"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Qdrant.Status != "ready" || payload.Embedding.Status != "ready" {
		t.Errorf("Qdrant/Embedding = %+v, want both ready (harness waits for this)", payload)
	}
}

func TestHandleStatusListsSourcesWithSyncState(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", SyncStatus: "completed"})

	resp := h.get("/status")
	var out []struct {
		ID         string `json:"id"`
		SyncStatus string `json:"sync_status"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "s1" || out[0].SyncStatus != "completed" {
		t.Errorf("status = %+v, want one entry for s1/completed", out)
	}
}

func TestHandleStatusReportsVectorCount(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "One", Type: "documentation", SyncStatus: "completed"})
	h.qd.count["conduit_documentation"] = 7

	resp := h.get("/status")
	var out []struct {
		ID          string `json:"id"`
		VectorCount int    `json:"vector_count"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "s1" || out[0].VectorCount != 7 {
		t.Errorf("status = %+v, want one entry for s1 with vector_count 7", out)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mustSave(t *testing.T, h *harness, src models.SourceDefinition) {
	t.Helper()
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	if err := h.sources.Save(src); err != nil {
		t.Fatalf("Save(%q): %v", src.ID, err)
	}
}

// ── Index page ───────────────────────────────────────────────────────────────

func TestHandleIndexRendersSourcesList(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "My Source"})

	resp := h.get("/sources")
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "My Source") {
		t.Error("index page does not mention the saved source name")
	}
}

func TestHandleIndexSortsSourcesByName(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Zebra"})
	mustSave(t, h, models.SourceDefinition{ID: "s2", Name: "alpha"})
	mustSave(t, h, models.SourceDefinition{ID: "s3", Name: "Mango"})

	resp := h.get("/sources")
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	iAlpha := strings.Index(body, "alpha")
	iMango := strings.Index(body, "Mango")
	iZebra := strings.Index(body, "Zebra")
	if iAlpha < 0 || iMango < 0 || iZebra < 0 {
		t.Fatalf("index page missing a source name: alpha=%d mango=%d zebra=%d", iAlpha, iMango, iZebra)
	}
	if !(iAlpha < iMango && iMango < iZebra) {
		t.Errorf("sources not in alphabetical order: alpha=%d mango=%d zebra=%d", iAlpha, iMango, iZebra)
	}
}

// ── Source create ────────────────────────────────────────────────────────────









// ── Source preview ───────────────────────────────────────────────────────────





// ── Source edit ──────────────────────────────────────────────────────────────

func TestSourceEditGetRedirectsWhenMissing(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/sources/does-not-exist/edit")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
}

func TestSourceEditGetShowsForm(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Editable", Type: "work-item"})

	resp := h.get("/sources/s1/edit")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "Editable") {
		t.Error("edit form does not show the source's current name")
	}
}



// ── Source delete / toggle / bulk actions ───────────────────────────────────

func TestSourceDeleteRemovesSourceAndVectors(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Doomed", Type: "work-item"})

	resp := h.postForm("/sources/s1/delete", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	got, err := h.sources.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("Get(s1) = %+v, want nil after delete", got)
	}
	h.qd.mu.Lock()
	defer h.qd.mu.Unlock()
	if len(h.qd.deleteFilterCalls) != 1 {
		t.Errorf("DeleteByFilter calls = %v, want exactly one", h.qd.deleteFilterCalls)
	}
}

func TestSourceToggleFlipsDisabled(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Toggle Me", Disabled: false})

	if resp := h.postForm("/sources/s1/toggle", nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got, _ := h.sources.Get("s1")
	if !got.Disabled {
		t.Fatal("expected Disabled = true after first toggle")
	}

	h.postForm("/sources/s1/toggle", nil)
	got, _ = h.sources.Get("s1")
	if got.Disabled {
		t.Error("expected Disabled = false after second toggle")
	}
}

func TestDisableSelectedMarksMultiple(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "A"})
	mustSave(t, h, models.SourceDefinition{ID: "s2", Name: "B"})

	resp := h.postForm("/sources/disable-selected", url.Values{"ids": {"s1", "s2"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	s1, _ := h.sources.Get("s1")
	s2, _ := h.sources.Get("s2")
	if !s1.Disabled || !s2.Disabled {
		t.Errorf("s1.Disabled=%v s2.Disabled=%v, want both true", s1.Disabled, s2.Disabled)
	}
}

func TestDeleteSelectedRemovesMultiple(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "A"})
	mustSave(t, h, models.SourceDefinition{ID: "s2", Name: "B"})

	resp := h.postForm("/sources/delete-selected", url.Values{"ids": {"s1", "s2"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	all, _ := h.sources.ListAll()
	if len(all) != 0 {
		t.Errorf("ListAll() = %+v, want empty after bulk delete", all)
	}
}

// ── Sync control ─────────────────────────────────────────────────────────────

func TestHandleSyncPauseResumeCancel(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Syncable"})

	resp := h.postForm("/sources/s1/sync/pause", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d", resp.StatusCode)
	}
	// Control state is registered lazily by the real sync run; pausing before
	// that just primes the flag — the important thing here is the endpoint
	// responds and doesn't error.
	if resp := h.postForm("/sources/s1/sync/resume", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d", resp.StatusCode)
	}
	if resp := h.postForm("/sources/s1/sync/cancel", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
}

// ── Export / Import ──────────────────────────────────────────────────────────



func TestHandleImportNoFileShowsError(t *testing.T) {
	h := newHarness(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, h.url("/import"), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "no file uploaded") {
		t.Error("expected the no-file-uploaded error")
	}
}

// ── Credentials ──────────────────────────────────────────────────────────────





// ── Experience ───────────────────────────────────────────────────────────────

func TestHandleExperienceAddRequiresBothFields(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/experience/create", url.Values{"situation": {"only situation"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form)", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "required") {
		t.Error("expected a validation error mentioning the missing field")
	}
}

func TestHandleExperienceAddStoresEntry(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/experience/create", url.Values{
		"situation": {"user hit a flaky test"}, "guidance": {"re-run before assuming flaky"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
}

// ── Settings ─────────────────────────────────────────────────────────────────

func TestHandleSettingsPageRenders(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/settings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHandleSettingsPreprocessingSavesConfig(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/settings/preprocessing", url.Values{
		"system_prompt":         {"custom prompt"},
		"source_type_work_item": {"on"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	if h.cfg.Preprocessing.SystemPrompt != "custom prompt" {
		t.Errorf("SystemPrompt = %q, want %q", h.cfg.Preprocessing.SystemPrompt, "custom prompt")
	}
	if !h.cfg.Preprocessing.SourceTypes["work-item"] {
		t.Error("SourceTypes[work-item] = false, want true (checkbox was on)")
	}
	if h.cfg.Preprocessing.SourceTypes["code"] {
		t.Error("SourceTypes[code] = true, want false (checkbox was absent)")
	}

	// Config.Save persisted it to disk too.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Preprocessing.SystemPrompt != "custom prompt" {
		t.Errorf("reloaded SystemPrompt = %q, want persisted value", reloaded.Preprocessing.SystemPrompt)
	}
}

func TestHandleDeleteAllSourcesRemovesEverything(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "A"})
	mustSave(t, h, models.SourceDefinition{ID: "s2", Name: "B"})

	resp := h.postForm("/settings/delete-all-sources", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	all, _ := h.sources.ListAll()
	if len(all) != 0 {
		t.Errorf("ListAll() = %+v, want empty", all)
	}
}

func TestHandleDeleteAllExperiencesRecreatesCollection(t *testing.T) {
	h := newHarness(t)
	h.qd.mu.Lock()
	h.qd.collections[models.CollectionExperience] = true
	h.qd.mu.Unlock()

	resp := h.postForm("/settings/delete-all-experiences", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.qd.mu.Lock()
	defer h.qd.mu.Unlock()
	if len(h.qd.deleteCollCalls) == 0 || len(h.qd.createCalls) == 0 {
		t.Errorf("deleteCollCalls=%v createCalls=%v, want the experience collection dropped and recreated",
			h.qd.deleteCollCalls, h.qd.createCalls)
	}
}

func TestHandleCleanSourceEmbeddingsResetsSyncStatus(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "A", SyncStatus: "completed"})
	h.qd.mu.Lock()
	h.qd.collections[models.CollectionWorkItems] = true
	h.qd.mu.Unlock()

	resp := h.postForm("/settings/clean-source-embeddings", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got, _ := h.sources.Get("s1")
	if got.SyncStatus != "needs-reindex" {
		t.Errorf("SyncStatus = %q, want needs-reindex", got.SyncStatus)
	}
}

// ── Map ──────────────────────────────────────────────────────────────────────

func TestHandleMapPageRenders(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/map")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHandleMapDataReturnsPointsForCompletedSources(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Synced", Type: "work-item", SyncStatus: "completed"})

	h.qd.mu.Lock()
	h.qd.scroll[models.CollectionWorkItems] = []map[string]any{
		{"id": "p1", "vector": []float32{0.1, 0.2, 0.3}, "payload": map[string]any{"text": "hello world"}},
		{"id": "p2", "vector": []float32{0.4, 0.1, 0.9}, "payload": map[string]any{"text": "second point"}},
	}
	h.qd.mu.Unlock()

	resp := h.get("/api/map-data")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Points []struct {
			Source string  `json:"source"`
			Title  string  `json:"title"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
		} `json:"points"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Points) != 2 {
		t.Fatalf("points = %+v, want 2", payload.Points)
	}
	if payload.Points[0].Source != "Synced" {
		t.Errorf("Points[0].Source = %q, want %q", payload.Points[0].Source, "Synced")
	}
}

func TestHandleMapDataQdrantUnreachableReturnsEmptyWithError(t *testing.T) {
	h := newHarnessNoHealthWait(t)
	resp := h.get("/api/map-data")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Points      []any  `json:"points"`
		QdrantError string `json:"qdrant_error"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Points) != 0 || payload.QdrantError == "" {
		t.Errorf("payload = %+v, want empty points and a qdrant_error while health is not yet ready", payload)
	}
}



// Pipeline-run and pull-request sources are read live: the Sources page
// marks them Live without sync controls, /status skips their vector count,
// and the form has neither Save & sync nor Preview, and asks for Runs.
func TestLiveSourcesHaveNoSyncControls(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "ci", Name: "CI builds", Type: models.SourcePipelineRun,
		Config: map[string]string{"Provider": "api", "Platform": "ado", "AdoOrg": "https://ado.example/org", "AdoProject": "proj", "DefinitionId": "12"}})

	body := bodyString(t, h.get("/sources"))
	card := body[strings.Index(body, `data-source-id="ci"`):]
	card = card[:strings.Index(card, "Delete</button>")]
	if !strings.Contains(card, ">Live</span>") || strings.Contains(card, "syncOne(") ||
		strings.Contains(card, "No data embedded") || strings.Contains(card, "Never synced") || !strings.Contains(card, "/sources/ci/edit") {
		t.Errorf("live source card:\n%s", card)
	}

	var status []struct {
		ID   string `json:"id"`
		Live bool   `json:"live"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, h.get("/status"))), &status); err != nil || len(status) != 1 || !status[0].Live {
		t.Errorf("status = %+v, %v", status, err)
	}

	form := bodyString(t, h.get("/sources/ci/edit"))
	if strings.Contains(form, `value="save_sync"`) || strings.Contains(form, `id="preview-btn"`) ||
		!strings.Contains(form, `name="Runs"`) || strings.Contains(form, `name="Days"`) {
		t.Error("live source form still offers sync/preview or lacks Runs")
	}
}
