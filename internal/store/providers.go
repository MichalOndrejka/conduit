package store

import (
	"encoding/json"
	"errors"
	"os"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// ProviderStore persists providers to conduit-providers.json, next to
// conduit-sources.json. Like SourceConfigStore it serves reads from an
// in-memory cache refreshed on every write.
type ProviderStore struct {
	path   string
	mu     sync.Mutex
	cache  []models.Provider
	loaded bool
}

func NewProviderStore(path string) *ProviderStore {
	return &ProviderStore{path: path}
}

func (s *ProviderStore) ListAll() ([]models.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Get returns a provider by ID (nil if not found).
func (s *ProviderStore) Get(id string) (*models.Provider, error) {
	provs, err := s.ListAll()
	if err != nil {
		return nil, err
	}
	for i := range provs {
		if provs[i].ID == id {
			return &provs[i], nil
		}
	}
	return nil, nil
}

// Save inserts or replaces a provider by ID.
func (s *ProviderStore) Save(prov models.Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	provs, err := s.read()
	if err != nil {
		return err
	}
	replaced := false
	for i := range provs {
		if provs[i].ID == prov.ID {
			provs[i] = prov
			replaced = true
			break
		}
	}
	if !replaced {
		provs = append(provs, prov)
	}
	return s.write(provs)
}

func (s *ProviderStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	provs, err := s.read()
	if err != nil {
		return err
	}
	out := provs[:0]
	for _, c := range provs {
		if c.ID != id {
			out = append(out, c)
		}
	}
	return s.write(out)
}

// Import merges providers into the store, keeping their IDs so imported
// sources still reference them. A provider whose ID already exists is
// left untouched. Returns the number added.
func (s *ProviderStore) Import(provs []models.Provider) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.read()
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, c := range existing {
		known[c.ID] = true
	}
	added := 0
	for _, c := range provs {
		if c.ID == "" || known[c.ID] {
			continue
		}
		known[c.ID] = true
		existing = append(existing, c)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	return added, s.write(existing)
}

// read returns a copy of the providers; caller must hold s.mu.
func (s *ProviderStore) read() ([]models.Provider, error) {
	if !s.loaded {
		provs, err := s.readFile()
		if err != nil {
			return nil, err
		}
		s.cache = provs
		s.loaded = true
	}
	return append([]models.Provider(nil), s.cache...), nil
}

func (s *ProviderStore) readFile() ([]models.Provider, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.Provider{}, nil
		}
		return nil, err
	}
	var provs []models.Provider
	if err := json.Unmarshal(data, &provs); err != nil {
		return nil, err
	}
	return provs, nil
}

// write persists provs atomically and refreshes the cache; caller must hold s.mu.
func (s *ProviderStore) write(provs []models.Provider) error {
	data, err := json.MarshalIndent(provs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.cache = append([]models.Provider(nil), provs...)
	s.loaded = true
	return nil
}
