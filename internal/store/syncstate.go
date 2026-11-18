package store

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// Sync state is what a source carries from one sync to the next (e.g. where
// the work item revisions feed left off): one file per source in a
// sync-state folder beside the sources file. It goes with the source, and
// with every source's sync status when that is reset.

var safeSourceID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (s *SourceConfigStore) syncStatePath(id string) (string, bool) {
	if !safeSourceID.MatchString(id) {
		return "", false
	}
	return filepath.Join(filepath.Dir(s.path), "sync-state", id+".json"), true
}

// SyncState returns the source's sync state; nil when it has none.
func (s *SourceConfigStore) SyncState(id string) ([]byte, error) {
	p, ok := s.syncStatePath(id)
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// SaveSyncState stores the source's sync state (atomically: a crash leaves
// the previous state, never half of one).
func (s *SourceConfigStore) SaveSyncState(id string, state []byte) error {
	p, ok := s.syncStatePath(id)
	if !ok {
		return errors.New("source id not usable as a file name: " + id)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, state, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// DeleteSyncState drops the source's sync state, so its next sync starts
// over. A source without one is not an error.
func (s *SourceConfigStore) DeleteSyncState(id string) error {
	p, ok := s.syncStatePath(id)
	if !ok {
		return nil
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
