// Package secrets is the Go port of app/store/secrets_store.py.
//
// It reads and writes the same Fernet-encrypted credentials.enc.json that the
// Python app produces, using the same key sources (CONDUIT_SECRET_KEY env var
// or the .secret_key file in the data dir), so no credential migration is
// needed when switching runtimes.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/fernet/fernet-go"

	"github.com/MichalOndrejka/conduit/internal/models"
)

const (
	keyFile   = ".secret_key"
	storeFile = "credentials.enc.json"
)

// Reader is the read-only view of the store that consumers (embedding,
// sources) depend on. Defined once here so each package doesn't re-declare
// its own copy.
type Reader interface {
	GetValue(name string) string
}

type entry struct {
	Note  string `json:"note"`
	Value string `json:"value"` // plaintext in cache, Fernet token on disk
}

type Store struct {
	dir       string
	storePath string
	key       *fernet.Key
	mu        sync.RWMutex
	cache     map[string]entry
}

// New loads (or generates) the Fernet key and decrypts the existing store.
func New(dataDir string) (*Store, error) {
	s := &Store{
		dir:       dataDir,
		storePath: filepath.Join(dataDir, storeFile),
		cache:     map[string]entry{},
	}
	if err := s.loadKey(); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// ── Key management ──────────────────────────────────────────────────────────

func (s *Store) loadKey() error {
	if envKey := strings.TrimSpace(os.Getenv("CONDUIT_SECRET_KEY")); envKey != "" {
		k, err := fernet.DecodeKey(envKey)
		if err != nil {
			return fmt.Errorf("invalid CONDUIT_SECRET_KEY: %w", err)
		}
		s.key = k
		return nil
	}
	keyPath := filepath.Join(s.dir, keyFile)
	if data, err := os.ReadFile(keyPath); err == nil {
		k, err := fernet.DecodeKey(strings.TrimSpace(string(data)))
		if err != nil {
			return fmt.Errorf("invalid key in %s: %w", keyPath, err)
		}
		s.key = k
		return nil
	}
	k := &fernet.Key{}
	if err := k.Generate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o600) // no-op on Windows; ACLs follow OS defaults
	if runtime.GOOS == "windows" {
		mode = 0o644
	}
	if err := os.WriteFile(keyPath, []byte(k.Encode()), mode); err != nil {
		return err
	}
	s.key = k
	return nil
}

// ── Persistence ─────────────────────────────────────────────────────────────

func (s *Store) load() error {
	data, err := os.ReadFile(s.storePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var raw map[string]entry
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for name, e := range raw {
		// ttl 0 = no expiry check, matching Python Fernet.decrypt semantics
		plain := fernet.VerifyAndDecrypt([]byte(e.Value), 0, []*fernet.Key{s.key})
		if plain == nil {
			continue // skip entries that can't be decrypted (wrong key / corrupt)
		}
		s.cache[name] = entry{Note: e.Note, Value: string(plain)}
	}
	return nil
}

// save persists the cache; caller must hold s.mu.
func (s *Store) save() error {
	out := map[string]entry{}
	for name, e := range s.cache {
		tok, err := fernet.EncryptAndSign([]byte(e.Value), s.key)
		if err != nil {
			return err
		}
		out[name] = entry{Note: e.Note, Value: string(tok)}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := s.storePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.storePath)
}

// ── Accessors / CRUD ────────────────────────────────────────────────────────

// GetValue returns the plaintext value for a credential name ("" if missing).
func (s *Store) GetValue(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache[name].Value
}

func (s *Store) ListAll() []models.CredentialInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.CredentialInfo, 0, len(s.cache))
	for name, e := range s.cache {
		out = append(out, models.CredentialInfo{ID: name, Name: name, Note: e.Note})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func validateName(name string) error {
	if name == "" {
		return errors.New("credential name cannot be empty")
	}
	if strings.Contains(name, "/") {
		return errors.New("credential name cannot contain '/'")
	}
	return nil
}

func (s *Store) Create(name, note, value string) error {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cache[name]; exists {
		return fmt.Errorf("a credential named %q already exists", name)
	}
	s.cache[name] = entry{Note: strings.TrimSpace(note), Value: value}
	return s.save()
}

// Update renames/updates a credential. Returns oldName so callers can cascade
// renames to sources; returns "" if oldName does not exist.
func (s *Store) Update(oldName, newName, note, value string) (string, error) {
	newName = strings.TrimSpace(newName)
	if err := validateName(newName); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, exists := s.cache[oldName]
	if !exists {
		return "", nil
	}
	if newName != oldName {
		if _, clash := s.cache[newName]; clash {
			return "", fmt.Errorf("a credential named %q already exists", newName)
		}
	}
	delete(s.cache, oldName)
	e.Note = strings.TrimSpace(note)
	if value != "" {
		e.Value = value
	}
	s.cache[newName] = e
	if err := s.save(); err != nil {
		return "", err
	}
	return oldName, nil
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, name)
	return s.save()
}

func (s *Store) Has(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.cache[name]
	return ok
}

// secretFields mirrors sources_using in app/store/secrets_store.py.
var secretFields = []string{"Pat", "Token", "Password", "ApiKeyValue"}

// SourcesUsing returns the names of sources whose config references cred name.
func (s *Store) SourcesUsing(name string, sources []models.SourceDefinition) []string {
	s.mu.RLock()
	_, ok := s.cache[name]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	var result []string
	for _, src := range sources {
		for _, f := range secretFields {
			if src.Config[f] == name {
				result = append(result, src.Name)
				break
			}
		}
	}
	return result
}
