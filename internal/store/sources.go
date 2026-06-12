// Package store holds the JSON-file-backed stores. sources.go is the Go port
// of app/store/source_config.py — reads and writes the same
// conduit-sources.json the Python app uses (including camelCase key
// normalization from C# exports and the manual-content export placeholder).
package store

import (
	"encoding/json"
	"errors"
	"os"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// credentialFields mirrors rename_credential_references in source_config.py.
var credentialFields = []string{"Pat", "Token", "Password", "ApiKeyValue"}

type SourceConfigStore struct {
	path string
	mu   sync.Mutex
	// Parsed-file cache: /status is polled every few seconds per open tab,
	// and the file only changes through this store, so reads are served from
	// memory and the cache is refreshed on every write.
	cache  []models.SourceDefinition
	loaded bool
}

func NewSourceConfigStore(path string) *SourceConfigStore {
	return &SourceConfigStore{path: path}
}

// ── Reads ───────────────────────────────────────────────────────────────────

func (s *SourceConfigStore) ListAll() ([]models.SourceDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Get returns a source by ID (nil if not found).
func (s *SourceConfigStore) Get(id string) (*models.SourceDefinition, error) {
	sources, err := s.ListAll()
	if err != nil {
		return nil, err
	}
	for i := range sources {
		if sources[i].ID == id {
			return &sources[i], nil
		}
	}
	return nil, nil
}

// ── Writes ──────────────────────────────────────────────────────────────────

// Save inserts or replaces a source by ID.
func (s *SourceConfigStore) Save(source models.SourceDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.read()
	if err != nil {
		return err
	}
	replaced := false
	for i := range sources {
		if sources[i].ID == source.ID {
			sources[i] = source
			replaced = true
			break
		}
	}
	if !replaced {
		sources = append(sources, source)
	}
	return s.write(sources)
}

func (s *SourceConfigStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.read()
	if err != nil {
		return err
	}
	out := sources[:0]
	for _, src := range sources {
		if src.ID != id {
			out = append(out, src)
		}
	}
	return s.write(out)
}

// RenameCredentialReferences cascades a credential rename into source configs.
func (s *SourceConfigStore) RenameCredentialReferences(oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sources, err := s.read()
	if err != nil {
		return err
	}
	changed := false
	for i := range sources {
		for _, f := range credentialFields {
			if sources[i].Config[f] == oldName {
				sources[i].Config[f] = newName
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return s.write(sources)
}

// ExportStripped returns sources for download, replacing manual document
// content with the placeholder so uploads don't leak into exports.
func (s *SourceConfigStore) ExportStripped() ([]models.SourceDefinition, error) {
	sources, err := s.ListAll()
	if err != nil {
		return nil, err
	}
	out := make([]models.SourceDefinition, len(sources))
	for i, src := range sources {
		cfg := make(map[string]string, len(src.Config))
		for k, v := range src.Config {
			cfg[k] = v
		}
		if cfg["Provider"] == "manual" || cfg["DocType"] == "upload" {
			cfg["Content"] = models.DocumentPlaceholder
		}
		src.Config = cfg
		out[i] = src
	}
	return out, nil
}

// ── File I/O ────────────────────────────────────────────────────────────────

// read returns the sources, from cache when warm; caller must hold s.mu.
// Returned values are deep copies so callers can mutate them freely before
// Save without corrupting the cache.
func (s *SourceConfigStore) read() ([]models.SourceDefinition, error) {
	if s.loaded {
		return cloneSources(s.cache), nil
	}
	sources, err := s.readFile()
	if err != nil {
		return nil, err
	}
	s.cache = cloneSources(sources)
	s.loaded = true
	return sources, nil
}

func (s *SourceConfigStore) readFile() ([]models.SourceDefinition, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []models.SourceDefinition{}, nil
		}
		return nil, err
	}

	// Support both the flat [...] format and a legacy {"sources": [...]}.
	var rawList []json.RawMessage
	if err := json.Unmarshal(data, &rawList); err != nil {
		var wrapper struct {
			Sources []json.RawMessage `json:"sources"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			return []models.SourceDefinition{}, nil // unreadable file → empty, like Python
		}
		rawList = wrapper.Sources
	}

	sources := make([]models.SourceDefinition, 0, len(rawList))
	for _, raw := range rawList {
		var src models.SourceDefinition
		if err := json.Unmarshal(normaliseKeys(raw), &src); err != nil {
			continue
		}
		if src.Config == nil {
			src.Config = map[string]string{}
		}
		sources = append(sources, src)
	}
	return sources, nil
}

// write persists sources and refreshes the cache; caller must hold s.mu.
func (s *SourceConfigStore) write(sources []models.SourceDefinition) error {
	data, err := json.MarshalIndent(sources, "", "  ")
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
	s.cache = cloneSources(sources)
	s.loaded = true
	return nil
}

func cloneSources(in []models.SourceDefinition) []models.SourceDefinition {
	out := make([]models.SourceDefinition, len(in))
	copy(out, in)
	for i := range out {
		cfg := make(map[string]string, len(out[i].Config))
		for k, v := range out[i].Config {
			cfg[k] = v
		}
		out[i].Config = cfg
	}
	return out
}

// normaliseKeys accepts camelCase keys from C# exports, mirroring
// _normalise_keys in source_config.py.
func normaliseKeys(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	mapping := map[string]string{
		"lastSyncedAt":   "last_synced_at",
		"syncStatus":     "sync_status",
		"syncError":      "sync_error",
		"syncErrorPhase": "sync_error_phase",
	}
	changed := false
	for from, to := range mapping {
		if v, ok := m[from]; ok {
			m[to] = v
			delete(m, from)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}
