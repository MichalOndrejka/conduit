package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func newProvStore(t *testing.T) (*ProviderStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conduit-providers.json")
	return NewProviderStore(path), path
}

func mustSaveProv(t *testing.T, s *ProviderStore, c models.Provider) {
	t.Helper()
	if err := s.Save(c); err != nil {
		t.Fatalf("Save(%q): %v", c.ID, err)
	}
}

func TestProviderStoreEmptyWhenFileMissing(t *testing.T) {
	s, _ := newProvStore(t)
	provs, err := s.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 0 {
		t.Errorf("ListAll = %+v, want empty", provs)
	}
	if c, err := s.Get("nope"); err != nil || c != nil {
		t.Errorf("Get(missing) = %+v, %v; want nil, nil", c, err)
	}
}

func TestProviderStoreSaveReplaceDeletePersist(t *testing.T) {
	s, path := newProvStore(t)
	mustSaveProv(t, s, models.Provider{ID: "c1", Name: "First", Kind: "ado", BaseURL: "https://dev.azure.com/org"})
	mustSaveProv(t, s, models.Provider{ID: "c2", Name: "Second", Kind: "ado"})
	mustSaveProv(t, s, models.Provider{ID: "c1", Name: "Renamed", Kind: "ado"})

	// A fresh store reads what the first one wrote.
	fresh := NewProviderStore(path)
	c1, err := fresh.Get("c1")
	if err != nil || c1 == nil {
		t.Fatalf("Get(c1) = %+v, %v", c1, err)
	}
	if c1.Name != "Renamed" {
		t.Errorf("c1.Name = %q, want Renamed (replaced by ID)", c1.Name)
	}
	if all, _ := fresh.ListAll(); len(all) != 2 {
		t.Errorf("ListAll len = %d, want 2", len(all))
	}

	if err := s.Delete("c1"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Get("c1"); c != nil {
		t.Errorf("c1 still present after Delete")
	}
	if c, _ := s.Get("c2"); c == nil {
		t.Errorf("Delete removed the wrong provider")
	}
}

func TestProviderStoreGetReturnsCopy(t *testing.T) {
	s, _ := newProvStore(t)
	mustSaveProv(t, s, models.Provider{ID: "c1", Name: "Original"})
	c, _ := s.Get("c1")
	c.Name = "mutated"
	again, _ := s.Get("c1")
	if again.Name != "Original" {
		t.Errorf("Name = %q after mutating a returned copy, want Original", again.Name)
	}
}

func TestProviderStoreUnreadableFileIsAnError(t *testing.T) {
	s, path := newProvStore(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An error (not an empty list) keeps a later Save from overwriting the
	// damaged file with nothing.
	if _, err := s.ListAll(); err == nil {
		t.Error("ListAll on corrupt file returned no error")
	}
}

func TestProviderStoreImportKeepsIDsAndSkipsExisting(t *testing.T) {
	s, _ := newProvStore(t)
	mustSaveProv(t, s, models.Provider{ID: "c1", Name: "Local"})
	added, err := s.Import([]models.Provider{
		{ID: "c1", Name: "Imported duplicate"},
		{ID: "c2", Name: "Imported"},
		{Name: "No ID"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	c1, _ := s.Get("c1")
	if c1.Name != "Local" {
		t.Errorf("c1.Name = %q, want the existing provider kept", c1.Name)
	}
	if c2, _ := s.Get("c2"); c2 == nil {
		t.Error("c2 not imported under its own ID")
	}
}
