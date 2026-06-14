package secrets

import (
	"os"
	"path/filepath"
	"testing"
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
