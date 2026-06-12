// Owner account provisioning — n8n-style single-owner auth.
//
// Two provisioning paths (see docs/deployment-azure.md):
//   - Env-seeded: CONDUIT_OWNER_EMAIL + CONDUIT_OWNER_PASSWORD (bcrypt-hashed
//     in memory at startup; reproducible deploys).
//   - First-run setup: if no owner exists, GET /setup collects email+password
//     once and persists {email, password_hash} to owner.json in the data dir,
//     then disables itself.
package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

const ownerFile = "owner.json"

type ownerRecord struct {
	Email        string `json:"email"`
	PasswordHash string `json:"password_hash"` // bcrypt
}

type OwnerStore struct {
	path  string
	mu    sync.RWMutex
	owner *ownerRecord
}

func NewOwnerStore(dataDir string) (*OwnerStore, error) {
	s := &OwnerStore{path: filepath.Join(dataDir, ownerFile)}

	// Env-seeded owner takes precedence (reproducible demo deploys).
	email := strings.TrimSpace(os.Getenv("CONDUIT_OWNER_EMAIL"))
	password := os.Getenv("CONDUIT_OWNER_PASSWORD")
	if email != "" && password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		s.owner = &ownerRecord{Email: strings.ToLower(email), PasswordHash: string(hash)}
		return s, nil
	}

	// Otherwise load a previously set-up owner, if any.
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil // no owner yet — /setup will provision one
		}
		return nil, err
	}
	var rec ownerRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.Email != "" && rec.PasswordHash != "" {
		s.owner = &rec
	}
	return s, nil
}

// HasOwner reports whether an owner account exists (setup completed or env-seeded).
func (s *OwnerStore) HasOwner() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.owner != nil
}

// Setup creates the owner account on first run. Fails if one already exists.
func (s *OwnerStore) Setup(email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("a valid email is required")
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != nil {
		return errors.New("owner account already exists")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	rec := ownerRecord{Email: email, PasswordHash: string(hash)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return err
	}
	s.owner = &rec
	return nil
}

// Verify checks email+password against the owner account (constant-time via bcrypt).
func (s *OwnerStore) Verify(email, password string) bool {
	s.mu.RLock()
	owner := s.owner
	s.mu.RUnlock()
	if owner == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(email), owner.Email) {
		// Still burn a bcrypt comparison so the timing doesn't leak
		// whether the email matched.
		_ = bcrypt.CompareHashAndPassword([]byte(owner.PasswordHash), []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(owner.PasswordHash), []byte(password)) == nil
}

// Email returns the owner email ("" if none).
func (s *OwnerStore) Email() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.owner == nil {
		return ""
	}
	return s.owner.Email
}
