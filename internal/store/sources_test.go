package store

import (
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

func TestOnChangeReportsTheChangedSource(t *testing.T) {
	s := NewSourceConfigStore(filepath.Join(t.TempDir(), "sources.json"))
	var got []string
	s.OnChange(func(id string) { got = append(got, id) })

	if err := s.Save(models.SourceDefinition{ID: "a", Name: "A", Config: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetAllSyncStatus("idle"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != "a" || got[2] != "" {
		t.Errorf("changes = %q, want [a a \"\"] (\"\" = any source)", got)
	}
}

func TestParseSourcesFormats(t *testing.T) {
	for name, data := range map[string]string{
		"flat":    `[{"id":"s1","name":"A","provider_id":"p1"}]`,
		"wrapper": `{"providers":[{"id":"p1"}],"sources":[{"id":"s1","name":"A","provider_id":"p1"}]}`,
		"camel":   `[{"id":"s1","name":"A","provider_id":"p1","syncStatus":"failed"}]`,
	} {
		srcs, err := ParseSources([]byte(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(srcs) != 1 || srcs[0].ID != "s1" || srcs[0].ProviderID != "p1" || srcs[0].Config == nil {
			t.Errorf("%s: %+v", name, srcs)
		}
	}
	if _, err := ParseSources([]byte(`not json at all`)); err == nil {
		t.Error("malformed data parsed")
	}
}

func TestAddAllReassignsCollidingIDs(t *testing.T) {
	s, path := newStore(t)
	mustSave(t, s, models.SourceDefinition{ID: "s1", Name: "Existing"})
	if err := s.AddAll([]models.SourceDefinition{{ID: "s1", Name: "Incoming"}, {Name: "No ID"}, {ID: "s9", Name: "Kept"}}); err != nil {
		t.Fatal(err)
	}
	all, err := NewSourceConfigStore(path).ListAll()
	if err != nil || len(all) != 4 {
		t.Fatalf("ListAll = %+v, %v", all, err)
	}
	ids := map[string]string{}
	for _, src := range all {
		ids[src.Name] = src.ID
	}
	if ids["Existing"] != "s1" || ids["Incoming"] == "s1" || ids["Incoming"] == "" || ids["No ID"] == "" || ids["Kept"] != "s9" {
		t.Errorf("ids = %v", ids)
	}
}
