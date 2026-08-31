package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

	resp := h.get("/")
	body := bodyString(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "My Source") {
		t.Error("index page does not mention the saved source name")
	}
}

// ── Source create ────────────────────────────────────────────────────────────

func TestSourceCreateGetShowsTypePicker(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/sources/create")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "Work Items") {
		t.Error("type picker page does not list Work Items")
	}
}

func TestSourceCreateGetShowsFormForType(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/sources/create?type=work-item")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestSourceCreatePostRequiresName(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/create", url.Values{
		"type": {"work-item"}, "provider": {"manual"}, "Content": {"hello"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form with error)", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "Name is required") {
		t.Error("expected the name-required error message in the response")
	}
	all, _ := h.sources.ListAll()
	if len(all) != 0 {
		t.Errorf("ListAll() = %+v, want nothing saved", all)
	}
}

func TestSourceCreatePostValidatesManualContent(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/create", url.Values{
		"name": {"My Doc"}, "type": {"documentation"}, "provider": {"manual"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "Content is required") {
		t.Error("expected the content-required validation error")
	}
}

func TestSourceCreatePostSavesValidManualSource(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/create", url.Values{
		"name": {"My Doc"}, "type": {"documentation"}, "provider": {"manual"},
		"Content": {"the document body"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", resp.StatusCode)
	}
	all, err := h.sources.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "My Doc" || all[0].GetConfig("Content") != "the document body" {
		t.Errorf("ListAll() = %+v, want the saved manual source", all)
	}
}

func TestSourceCreatePostSavesValidAPISource(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/create", url.Values{
		"name": {"My API Source"}, "type": {"work-item"}, "provider": {"api"},
		"Url": {"https://api.example.com/items"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	all, err := h.sources.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].GetConfig("Url") != "https://api.example.com/items" {
		t.Errorf("ListAll() = %+v, want the saved API source", all)
	}
}

func TestSourceCreatePostRejectsInvalidURL(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/create", url.Values{
		"name": {"Bad"}, "type": {"work-item"}, "provider": {"api"}, "Url": {"not-a-url"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (validation error)", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "valid http") {
		t.Error("expected the URL validation error")
	}
}

// ── Source preview ───────────────────────────────────────────────────────────

func TestSourcePreviewFetchesSampleDocuments(t *testing.T) {
	h := newHarness(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"First item","body":"hello"},{"title":"Second item","body":"world"}]`))
	}))
	defer api.Close()

	resp := h.postForm("/sources/preview", url.Values{
		"type": {"work-item"}, "provider": {"api"}, "Url": {api.URL},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Ok        bool                           `json:"ok"`
		Documents []struct{ Title, Text string } `json:"documents"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Ok || len(out.Documents) != 2 || out.Documents[0].Title != "First item" {
		t.Errorf("preview = %+v, want two items starting with First item", out)
	}
}

func TestSourcePreviewPaginatesAcrossOffset(t *testing.T) {
	h := newHarness(t)
	items := make([]string, 12)
	for i := range items {
		items[i] = fmt.Sprintf(`{"title":"Item %d"}`, i)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer api.Close()

	fetchPage := func(offset string) struct {
		Ok        bool                           `json:"ok"`
		Documents []struct{ Title, Text string } `json:"documents"`
		Offset    int                            `json:"offset"`
		Limit     int                            `json:"limit"`
		HasMore   bool                           `json:"hasMore"`
	} {
		resp := h.postForm("/sources/preview", url.Values{
			"type": {"work-item"}, "provider": {"api"}, "Url": {api.URL}, "offset": {offset},
		})
		var out struct {
			Ok        bool                           `json:"ok"`
			Documents []struct{ Title, Text string } `json:"documents"`
			Offset    int                            `json:"offset"`
			Limit     int                            `json:"limit"`
			HasMore   bool                           `json:"hasMore"`
		}
		if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := fetchPage("0")
	if !first.Ok || len(first.Documents) != 5 || first.Offset != 0 || !first.HasMore {
		t.Errorf("page 1 = %+v, want 5 docs, offset 0, hasMore true", first)
	}
	if first.Documents[0].Title != "Item 0" {
		t.Errorf("page 1 first doc = %q, want Item 0", first.Documents[0].Title)
	}

	second := fetchPage("5")
	if !second.Ok || len(second.Documents) != 5 || second.Offset != 5 || !second.HasMore {
		t.Errorf("page 2 = %+v, want 5 docs, offset 5, hasMore true", second)
	}
	if second.Documents[0].Title != "Item 5" {
		t.Errorf("page 2 first doc = %q, want Item 5", second.Documents[0].Title)
	}

	third := fetchPage("10")
	if !third.Ok || len(third.Documents) != 2 || third.Offset != 10 || third.HasMore {
		t.Errorf("page 3 = %+v, want 2 docs, offset 10, hasMore false", third)
	}
}

func TestSourcePreviewSurfacesFetchError(t *testing.T) {
	h := newHarness(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer api.Close()

	resp := h.postForm("/sources/preview", url.Values{
		"type": {"work-item"}, "provider": {"api"}, "Url": {api.URL},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Ok || !strings.Contains(out.Error, "401") {
		t.Errorf("preview = %+v, want an HTTP 401 error surfaced", out)
	}
}

func TestSourcePreviewReturnsValidationError(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/preview", url.Values{
		"type": {"work-item"}, "provider": {"api"}, "Url": {"not-a-url"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Ok || !strings.Contains(out.Error, "valid http") {
		t.Errorf("preview = %+v, want a URL validation error", out)
	}
}

func TestSourcePreviewManualUsesExistingContentWhenBlank(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Doc", Type: "documentation", Config: map[string]string{
		"Provider": "manual", "Title": "Doc", "Content": "stored content",
	}})

	resp := h.postForm("/sources/preview", url.Values{
		"id": {"s1"}, "type": {"documentation"}, "provider": {"manual"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Ok        bool                           `json:"ok"`
		Documents []struct{ Title, Text string } `json:"documents"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Ok || len(out.Documents) != 1 || out.Documents[0].Text != "stored content" {
		t.Errorf("preview = %+v, want the stored manual content", out)
	}
}

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

func TestSourceEditPostUpdatesSource(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Old Name", Type: "work-item", Config: map[string]string{
		"Provider": "api", "Url": "https://api.example.com/items",
	}})

	resp := h.postForm("/sources/s1/edit", url.Values{
		"name": {"New Name"}, "type": {"work-item"}, "provider": {"api"},
		"Url": {"https://api.example.com/items"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	got, err := h.sources.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New Name" {
		t.Errorf("Name = %q, want %q", got.Name, "New Name")
	}
}

// Changing a source's type moves it to a different Qdrant collection — the
// old collection's vectors for this source must be cleaned up and the sync
// status reset, since a re-sync only replaces vectors within the *new*
// collection (see handleSourceEditPost's collection-change branch).
func TestSourceEditPostCollectionChangeCleansUpOldVectors(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mustSave(t, h, models.SourceDefinition{
		ID: "s1", Name: "Moving", Type: "work-item", SyncStatus: "completed", LastSyncedAt: &now,
		Config: map[string]string{"Provider": "api", "Url": "https://api.example.com/items"},
	})

	resp := h.postForm("/sources/s1/edit", url.Values{
		"name": {"Moving"}, "type": {"code"}, "provider": {"api"},
		"Url": {"https://dev.azure.com/org/proj/_apis/git/repositories/repo/items"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}

	h.qd.mu.Lock()
	deleted := append([]string(nil), h.qd.deleteFilterCalls...)
	h.qd.mu.Unlock()
	found := false
	for _, c := range deleted {
		if c == models.CollectionWorkItems {
			found = true
		}
	}
	if !found {
		t.Errorf("DeleteByFilter calls = %v, want a cleanup of the old collection %q", deleted, models.CollectionWorkItems)
	}

	got, err := h.sources.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncStatus != "idle" {
		t.Errorf("SyncStatus = %q, want reset to idle after a collection change", got.SyncStatus)
	}
	if got.LastSyncedAt != nil {
		t.Errorf("LastSyncedAt = %v, want cleared", got.LastSyncedAt)
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

func TestHandleExportReturnsStrippedSources(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Manual Doc", Config: map[string]string{
		"Provider": "manual", "Content": "sensitive uploaded text",
	}})

	resp := h.get("/export")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out []models.SourceDefinition
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Config["Content"] != models.DocumentPlaceholder {
		t.Errorf("exported = %+v, want manual Content replaced with the placeholder", out)
	}
}

func TestHandleImportMergesSources(t *testing.T) {
	h := newHarness(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "conduit-sources.json")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(`[{"id":"imported-1","name":"Imported Source"}]`))
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, h.url("/import"), &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered index)", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "Imported 1 source") {
		t.Error("expected the import-count banner in the response")
	}
	got, err := h.sources.Get("imported-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "Imported Source" {
		t.Errorf("Get(imported-1) = %+v, want the imported source", got)
	}
}

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

func TestHandleCredentialCreatePersists(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/credentials/create", url.Values{"name": {"my-token"}, "value": {"secret-value"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	if !h.secrets.Has("my-token") {
		t.Error("credential was not persisted")
	}
}

func TestHandleCredentialCreateDuplicateNameErrors(t *testing.T) {
	h := newHarness(t)
	h.postForm("/credentials/create", url.Values{"name": {"dup"}, "value": {"v1"}})

	resp := h.postForm("/credentials/create", url.Values{"name": {"dup"}, "value": {"v2"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form with error)", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "already exists") {
		t.Error("expected a duplicate-name error message")
	}
}

func TestHandleCredentialEditRenamesAndCascadesToSources(t *testing.T) {
	h := newHarness(t)
	if err := h.secrets.Create("old-name", "secret"); err != nil {
		t.Fatal(err)
	}
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Uses cred", Config: map[string]string{
		"Provider": "api", "Password": "old-name",
	}})

	resp := h.postForm("/credentials/old-name/edit", url.Values{"name": {"new-name"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	if h.secrets.Has("old-name") {
		t.Error("old credential name should no longer exist")
	}
	if !h.secrets.Has("new-name") {
		t.Error("new credential name should exist")
	}
	got, err := h.sources.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Config["Password"] != "new-name" {
		t.Errorf("source's Password reference = %q, want cascaded rename to %q", got.Config["Password"], "new-name")
	}
}

func TestHandleCredentialDeleteRemoves(t *testing.T) {
	h := newHarness(t)
	if err := h.secrets.Create("to-delete", "secret"); err != nil {
		t.Fatal(err)
	}
	resp := h.postForm("/credentials/to-delete/delete", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if h.secrets.Has("to-delete") {
		t.Error("credential should have been deleted")
	}
}

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
