package store

import (
	"path/filepath"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestSyncStateGoesWithTheSource(t *testing.T) {
	s := NewSourceConfigStore(filepath.Join(t.TempDir(), "sources.json"))
	for _, id := range []string{"a", "b"} {
		if err := s.Save(models.SourceDefinition{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSyncState(id, []byte(`{"token":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.SyncState("a"); string(got) != `{"token":"a"}` {
		t.Errorf("state a = %s", got)
	}
	if got, err := s.SyncState("none"); got != nil || err != nil {
		t.Errorf("missing state = %s, %v; want nil", got, err)
	}

	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SyncState("a"); got != nil {
		t.Error("a deleted source's state was kept")
	}
	if err := s.ResetAllSyncStatus("idle"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SyncState("b"); got != nil {
		t.Error("resetting every source's sync status kept a state")
	}
}

func TestSyncStateRefusesUnsafeIDs(t *testing.T) {
	s := NewSourceConfigStore(filepath.Join(t.TempDir(), "sources.json"))
	if err := s.SaveSyncState("../escape", []byte("x")); err == nil {
		t.Error("a source id with a path in it was used as a file name")
	}
}
