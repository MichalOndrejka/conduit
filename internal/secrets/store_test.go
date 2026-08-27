package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// Fixture generated with Python `cryptography`:
//
//	k = Fernet.generate_key(); Fernet(k).encrypt(b"super-secret-PAT-value")
//
// Proves fernet-go reads the same credentials.enc.json the Python app writes.
const (
	pyKey   = "7eb3JdWVeiA5KposNEi9M9p8hSiVgSX21i2v9kcJnLc="
	pyToken = "gAAAAABqK-vF8Mzuj3vi8TTuf35g75EJMfAfuG1kxvF3F6KFX9LdW0LrVp0nKXWmAPbjcNoN20KTiAV5o_Gs0_7PrIYozfvY3opetPJ03tW4supnOEfwKHQ="
)

func TestDecryptsPythonFernetStore(t *testing.T) {
	dir := t.TempDir()
	// Existing Python-written stores include a "note" field; it must be ignored
	// cleanly now that credentials no longer carry notes.
	data := []byte(`{"ado-pat": {"note": "Azure DevOps PAT", "value": "` + pyToken + `"}}`)
	if err := os.WriteFile(filepath.Join(dir, storeFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.GetValue("ado-pat"); got != "super-secret-PAT-value" {
		t.Errorf("GetValue = %q, want the Python-encrypted plaintext", got)
	}
}

func TestRoundTripAndReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create("github-token", "ghp_abc123"); err != nil {
		t.Fatal(err)
	}

	// A fresh store instance must decrypt what the first one wrote.
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.GetValue("github-token"); got != "ghp_abc123" {
		t.Errorf("reloaded value = %q", got)
	}
}

func TestGeneratesKeyFileWhenNoEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", "")

	if _, err := New(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, keyFile)); err != nil {
		t.Errorf(".secret_key not created: %v", err)
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)

	if err := s.Create("", "v"); err == nil {
		t.Error("empty name accepted")
	}
	if err := s.Create("a/b", "v"); err == nil {
		t.Error("name with '/' accepted")
	}
	_ = s.Create("dup", "v")
	if err := s.Create("dup", "v"); err == nil {
		t.Error("duplicate name accepted")
	}
}

func TestNewRejectsInvalidEnvKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", "not-a-valid-fernet-key")

	if _, err := New(dir); err == nil {
		t.Error("expected error for invalid CONDUIT_SECRET_KEY")
	}
}

func TestNewRejectsInvalidKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", "")
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("garbage-key"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := New(dir); err == nil {
		t.Error("expected error for invalid key file contents")
	}
}

func TestNewReusesExistingKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", "")

	s1, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Create("k", "v1"); err != nil {
		t.Fatal(err)
	}

	// A second store in the same dir must reuse the generated .secret_key
	// file (rather than generating a new one) and decrypt what s1 wrote.
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.GetValue("k"); got != "v1" {
		t.Errorf("GetValue = %q, want %q", got, "v1")
	}
}

func TestNewErrorsOnCorruptStoreFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	if err := os.WriteFile(filepath.Join(dir, storeFile), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := New(dir); err == nil {
		t.Error("expected error for corrupt store file")
	}
}

func TestLoadSkipsEntriesThatFailToDecrypt(t *testing.T) {
	dir := t.TempDir()
	// pyToken was encrypted with pyKey, not the key used to open the store
	// below, so it must be silently skipped rather than causing an error.
	data := []byte(`{"ado-pat": {"value": "` + pyToken + `"}}`)
	if err := os.WriteFile(filepath.Join(dir, storeFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUIT_SECRET_KEY", "")

	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.GetValue("ado-pat"); got != "" {
		t.Errorf("GetValue = %q, want empty for undecryptable entry", got)
	}
	if s.Has("ado-pat") {
		t.Error("Has reports an entry that couldn't be decrypted")
	}
}

func TestCreateRejectsEmptyValue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)

	if err := s.Create("name", ""); err == nil {
		t.Error("empty value accepted")
	}
}

func TestListAllReturnsSortedNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)

	for _, name := range []string{"zeta", "alpha", "mid"} {
		if err := s.Create(name, "v"); err != nil {
			t.Fatal(err)
		}
	}

	got := s.ListAll()
	if len(got) != 3 {
		t.Fatalf("ListAll returned %d entries, want 3", len(got))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, w := range want {
		if got[i].Name != w || got[i].ID != w {
			t.Errorf("ListAll()[%d] = %+v, want Name/ID %q", i, got[i], w)
		}
	}
}

func TestHas(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)
	_ = s.Create("known", "v")

	if !s.Has("known") {
		t.Error("Has(known) = false, want true")
	}
	if s.Has("unknown") {
		t.Error("Has(unknown) = true, want false")
	}
}

func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)
	if err := s.Create("orig", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("other", "v2"); err != nil {
		t.Fatal(err)
	}

	t.Run("not found", func(t *testing.T) {
		old, err := s.Update("missing", "new", "v")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if old != "" {
			t.Errorf("oldName = %q, want empty for missing credential", old)
		}
	})

	t.Run("invalid new name", func(t *testing.T) {
		if _, err := s.Update("orig", "", "v"); err == nil {
			t.Error("expected error for empty new name")
		}
		if _, err := s.Update("orig", "a/b", "v"); err == nil {
			t.Error("expected error for new name containing '/'")
		}
	})

	t.Run("rename clash", func(t *testing.T) {
		if _, err := s.Update("orig", "other", "v"); err == nil {
			t.Error("expected error when renaming onto an existing credential")
		}
	})

	t.Run("value only update keeps name", func(t *testing.T) {
		old, err := s.Update("orig", "orig", "v1-updated")
		if err != nil {
			t.Fatal(err)
		}
		if old != "orig" {
			t.Errorf("oldName = %q, want %q", old, "orig")
		}
		if got := s.GetValue("orig"); got != "v1-updated" {
			t.Errorf("GetValue(orig) = %q, want %q", got, "v1-updated")
		}
	})

	t.Run("rename with existing value", func(t *testing.T) {
		old, err := s.Update("orig", "renamed", "")
		if err != nil {
			t.Fatal(err)
		}
		if old != "orig" {
			t.Errorf("oldName = %q, want %q", old, "orig")
		}
		if s.Has("orig") {
			t.Error("old name still present after rename")
		}
		if got := s.GetValue("renamed"); got != "v1-updated" {
			t.Errorf("GetValue(renamed) = %q, want preserved value %q", got, "v1-updated")
		}
	})

	t.Run("survives reload", func(t *testing.T) {
		s2, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := s2.GetValue("renamed"); got != "v1-updated" {
			t.Errorf("reloaded GetValue(renamed) = %q", got)
		}
	})
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)
	if err := s.Create("gone", "v"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete("gone"); err != nil {
		t.Fatal(err)
	}
	if s.Has("gone") {
		t.Error("credential still present after Delete")
	}

	// Deleting a name that doesn't exist is a no-op, not an error.
	if err := s.Delete("never-existed"); err != nil {
		t.Errorf("Delete of missing name returned error: %v", err)
	}
}

func TestSourcesUsing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)
	if err := s.Create("my-pat", "v"); err != nil {
		t.Fatal(err)
	}

	sources := []models.SourceDefinition{
		{Name: "src-a", Config: map[string]string{"Pat": "my-pat"}},
		{Name: "src-b", Config: map[string]string{"Token": "other-token"}},
		{Name: "src-c", Config: map[string]string{"Password": "my-pat"}},
	}

	got := s.SourcesUsing("my-pat", sources)
	want := []string{"src-a", "src-c"}
	if len(got) != len(want) {
		t.Fatalf("SourcesUsing = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("SourcesUsing[%d] = %q, want %q", i, got[i], w)
		}
	}

	if got := s.SourcesUsing("unknown-cred", sources); got != nil {
		t.Errorf("SourcesUsing for unknown credential = %v, want nil", got)
	}
}

func TestMissingReferences(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", pyKey)
	s, _ := New(dir)
	if err := s.Create("present", "v"); err != nil {
		t.Fatal(err)
	}

	sources := []models.SourceDefinition{
		{Name: "src-a", Config: map[string]string{"Pat": "present"}},
		{Name: "src-b", Config: map[string]string{"Token": "missing-cred"}},
		{Name: "src-c", Config: map[string]string{}},
	}

	got := s.MissingReferences(sources)
	if len(got) != 1 {
		t.Fatalf("MissingReferences = %+v, want 1 entry", got)
	}
	if got[0].SourceName != "src-b" || got[0].CredentialName != "missing-cred" {
		t.Errorf("MissingReferences[0] = %+v, want {src-b missing-cred}", got[0])
	}
}
