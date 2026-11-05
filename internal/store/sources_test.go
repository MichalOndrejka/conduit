package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func newStore(t *testing.T) (*SourceConfigStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conduit-sources.json")
	return NewSourceConfigStore(path), path
}

func mustSave(t *testing.T, s *SourceConfigStore, src models.SourceDefinition) {
	t.Helper()
	if err := s.Save(src); err != nil {
		t.Fatalf("Save(%q): %v", src.ID, err)
	}
}

// ── Reads on an empty/missing store ─────────────────────────────────────────

func TestListAllEmptyWhenFileMissing(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ListAll() = %+v, want empty", got)
	}
}

func TestGetReturnsNilForMissingID(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.Get("does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("Get() = %+v, want nil", got)
	}
}

// ── Save / Get / Delete ──────────────────────────────────────────────────────

func TestSaveInsertsNewSource(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "First", Type: models.SourceWorkItemQuery})

	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "First" {
		t.Fatalf("Get(s1) = %+v, want Name=First", got)
	}
}

func TestSaveReplacesExistingSourceByID(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "Original"})
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "Updated"})

	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ListAll() has %d sources, want 1 (replace, not append)", len(all))
	}
	if all[0].Name != "Updated" {
		t.Errorf("Name = %q, want %q", all[0].Name, "Updated")
	}
}

func TestDeleteRemovesSource(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "One"})
	mustSave(t, s, models.SourceDefinition{ID: "s2", Name: "Two"})

	if err := s.Delete("s1"); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "s2" {
		t.Errorf("ListAll() = %+v, want only s2 remaining", all)
	}
}

func TestDeleteMissingIDIsNoop(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "One"})

	if err := s.Delete("nope"); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("ListAll() = %+v, want the untouched source still present", all)
	}
}

// ── Get/ListAll return independent copies ───────────────────────────────────

func TestGetReturnsIndependentCopies(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Config: map[string]string{"Url": "original"}})

	first, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	first.Config["Url"] = "mutated by caller"

	second, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Config["Url"] != "original" {
		t.Errorf("Config[Url] = %q after external mutation, want store's copy unaffected", second.Config["Url"])
	}
}

// ── RenameCredentialReferences ───────────────────────────────────────────────

func TestRenameCredentialReferencesUpdatesMatchingFields(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Config: map[string]string{
		"Password": "old-cred", "Token": "old-cred", "Pat": "unrelated", "ApiKeyValue": "old-cred",
	}})
	mustSave(t, s, models.SourceDefinition{ID: "s2", Config: map[string]string{"Password": "other-cred"}})

	if err := s.RenameCredentialReferences("old-cred", "new-cred"); err != nil {
		t.Fatal(err)
	}

	s1, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if s1.Config["Password"] != "new-cred" || s1.Config["Token"] != "new-cred" || s1.Config["ApiKeyValue"] != "new-cred" {
		t.Errorf("s1.Config = %+v, want Password/Token/ApiKeyValue renamed to new-cred", s1.Config)
	}
	if s1.Config["Pat"] != "unrelated" {
		t.Errorf("s1.Config[Pat] = %q, want untouched (didn't match old name)", s1.Config["Pat"])
	}

	s2, err := s.Get("s2")
	if err != nil {
		t.Fatal(err)
	}
	if s2.Config["Password"] != "other-cred" {
		t.Errorf("s2.Config[Password] = %q, want untouched (different credential)", s2.Config["Password"])
	}
}

func TestRenameCredentialReferencesNoopWhenNoMatch(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Config: map[string]string{"Password": "unrelated"}})

	if err := s.RenameCredentialReferences("no-such-cred", "new-cred"); err != nil {
		t.Fatal(err)
	}
	s1, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if s1.Config["Password"] != "unrelated" {
		t.Errorf("Config[Password] = %q, want untouched", s1.Config["Password"])
	}
}

// ── Import ───────────────────────────────────────────────────────────────────

func TestImportFlatArray(t *testing.T) {
	s, _ := newStore(t)
	data := []byte(`[{"id":"s1","name":"Imported","type":"work-item"}]`)

	n, err := s.Import(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Import() imported %d, want 1", n)
	}
	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "Imported" {
		t.Fatalf("Get(s1) = %+v, want the imported source", got)
	}
}

func TestImportWrapperFormat(t *testing.T) {
	s, _ := newStore(t)
	data := []byte(`{"sources":[{"id":"s1","name":"Wrapped"}]}`)

	n, err := s.Import(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Import() imported %d, want 1", n)
	}
	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "Wrapped" {
		t.Fatalf("Get(s1) = %+v, want the wrapped source", got)
	}
}

func TestImportReassignsCollidingIDs(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "Existing"})

	n, err := s.Import([]byte(`[{"id":"s1","name":"Incoming"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Import() imported %d, want 1", n)
	}

	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll() has %d sources, want 2 (existing kept, import got a fresh id)", len(all))
	}
	var existing, incoming *models.SourceDefinition
	for i := range all {
		switch all[i].Name {
		case "Existing":
			existing = &all[i]
		case "Incoming":
			incoming = &all[i]
		}
	}
	if existing == nil || existing.ID != "s1" {
		t.Errorf("existing source's ID changed: %+v", existing)
	}
	if incoming == nil || incoming.ID == "s1" || incoming.ID == "" {
		t.Errorf("incoming source should have a fresh non-empty ID, got %+v", incoming)
	}
}

func TestImportResetsSyncState(t *testing.T) {
	s, _ := newStore(t)
	errMsg := "boom"
	data, err := json.Marshal(models.SourceDefinition{
		ID: "s1", Name: "Imported", SyncStatus: "failed", SyncError: &errMsg,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Import([]byte("[" + string(data) + "]")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncStatus != "idle" {
		t.Errorf("SyncStatus = %q, want reset to idle", got.SyncStatus)
	}
	if got.SyncError != nil {
		t.Errorf("SyncError = %v, want cleared", got.SyncError)
	}
}

func TestImportInvalidJSONErrors(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Import([]byte(`not json at all`)); err == nil {
		t.Fatal("expected error for malformed import data")
	}
}

// ── ReconcileStaleSync / ResetAllSyncStatus ─────────────────────────────────

func TestReconcileStaleSyncResetsOnlySyncingSources(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", SyncStatus: "syncing"})
	mustSave(t, s, models.SourceDefinition{ID: "s2", SyncStatus: "completed"})

	if err := s.ReconcileStaleSync(); err != nil {
		t.Fatal(err)
	}

	s1, _ := s.Get("s1")
	s2, _ := s.Get("s2")
	if s1.SyncStatus != "idle" {
		t.Errorf("s1.SyncStatus = %q, want idle", s1.SyncStatus)
	}
	if s2.SyncStatus != "completed" {
		t.Errorf("s2.SyncStatus = %q, want untouched", s2.SyncStatus)
	}
}

func TestResetAllSyncStatusClearsErrorAndTimestamp(t *testing.T) {
	s, _ := newStore(t)
	now := time.Now().UTC()
	errMsg := "boom"
	mustSave(t, s, models.SourceDefinition{
		ID: "s1", SyncStatus: "failed", SyncError: &errMsg, LastSyncedAt: &now,
	})

	if err := s.ResetAllSyncStatus("idle"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncStatus != "idle" {
		t.Errorf("SyncStatus = %q, want idle", got.SyncStatus)
	}
	if got.SyncError != nil {
		t.Errorf("SyncError = %v, want cleared", got.SyncError)
	}
	if got.LastSyncedAt != nil {
		t.Errorf("LastSyncedAt = %v, want cleared", got.LastSyncedAt)
	}
}

// ── ExportStripped ────────────────────────────────────────────────────────────

func TestExportStrippedReplacesManualContent(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "manual", Config: map[string]string{
		"Provider": "manual", "Content": "secret uploaded text",
	}})
	mustSave(t, s, models.SourceDefinition{ID: "upload", Config: map[string]string{
		"DocType": "upload", "Content": "another secret upload",
	}})
	mustSave(t, s, models.SourceDefinition{ID: "api", Config: map[string]string{
		"Provider": "api", "Content": "should be kept as-is",
	}})

	out, err := s.ExportStripped()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]models.SourceDefinition{}
	for _, src := range out {
		byID[src.ID] = src
	}
	if byID["manual"].Config["Content"] != models.DocumentPlaceholder {
		t.Errorf("manual Content = %q, want placeholder", byID["manual"].Config["Content"])
	}
	if byID["upload"].Config["Content"] != models.DocumentPlaceholder {
		t.Errorf("upload Content = %q, want placeholder", byID["upload"].Config["Content"])
	}
	if byID["api"].Config["Content"] != "should be kept as-is" {
		t.Errorf("api Content = %q, want unchanged", byID["api"].Config["Content"])
	}
}

func TestExportStrippedDoesNotMutateStore(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "manual", Config: map[string]string{
		"Provider": "manual", "Content": "secret uploaded text",
	}})

	if _, err := s.ExportStripped(); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("manual")
	if err != nil {
		t.Fatal(err)
	}
	if got.Config["Content"] != "secret uploaded text" {
		t.Errorf("stored Content = %q after ExportStripped, want original content untouched", got.Config["Content"])
	}
}

// ── File-format edge cases ───────────────────────────────────────────────────

func TestReadSupportsLegacyWrapperFormatOnDisk(t *testing.T) {
	s, path := newStore(t)
	body := `{"sources":[{"id":"s1","name":"Legacy"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "Legacy" {
		t.Errorf("ListAll() = %+v, want the legacy-wrapped source", all)
	}
}

func TestReadNormalisesCamelCaseKeysFromCSharpExport(t *testing.T) {
	s, path := newStore(t)
	body := `[{"id":"s1","lastSyncedAt":"2024-01-02T03:04:05Z","syncStatus":"completed","syncError":null,"syncErrorPhase":null}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Get(s1) = nil, want the normalised source")
	}
	if got.SyncStatus != "completed" {
		t.Errorf("SyncStatus = %q, want completed (from camelCase syncStatus)", got.SyncStatus)
	}
	if got.LastSyncedAt == nil || !got.LastSyncedAt.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("LastSyncedAt = %v, want 2024-01-02T03:04:05Z (from camelCase lastSyncedAt)", got.LastSyncedAt)
	}
}

func TestReadUnreadableJSONReturnsEmptyNotError(t *testing.T) {
	s, path := newStore(t)
	if err := os.WriteFile(path, []byte(`not json at all`), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListAll()
	if err != nil {
		t.Fatalf("ListAll() error = %v, want nil (unreadable file treated as empty, like Python)", err)
	}
	if len(all) != 0 {
		t.Errorf("ListAll() = %+v, want empty for unreadable file", all)
	}
}

func TestWriteCleansUpTmpFile(t *testing.T) {
	s, path := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1"})

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected .tmp file to be renamed away, stat err = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected final file to exist: %v", err)
	}
}

// ── Cache behavior ────────────────────────────────────────────────────────────

// The store only re-reads conduit-sources.json on first access; after that,
// writes go through the store's own write() and refresh the in-memory cache,
// but changes made to the file by anything else are not picked up. This test
// documents that contract.
func TestCacheServesFromMemoryAfterFirstRead(t *testing.T) {
	s, path := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "Cached"})

	// Overwrite the file directly, bypassing the store.
	if err := os.WriteFile(path, []byte(`[{"id":"s1","name":"Changed on disk"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Cached" {
		t.Errorf("Name = %q, want the cached value %q (store shouldn't re-read on its own)", got.Name, "Cached")
	}
}
