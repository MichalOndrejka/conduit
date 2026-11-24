// Incremental fetching: what a source may skip refetching because the index
// already holds it, current. Only content-addressed inputs qualify — a git
// commit (its message and diff can never change under its SHA) and a
// repository tree at a commit SHA. Anything identified by a revision number
// or a timestamp (work items, issues and their comments) is always
// refetched in full: whether every kind of edit moves those is not
// guaranteed, and the indexer's content hashes still keep unchanged ones
// from being embedded again.
package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// IndexState is what the index holds for the source being fetched, handed
// to its fetch through the context (WithIndexState).
type IndexState struct {
	// IndexFP is the fingerprint this sync embeds under; a stored document
	// embedded under another can't be kept.
	IndexFP string
	State   *rag.SourceState
	// Seal is set by the fetch: the value to stamp on the source's points
	// once the sync succeeds, telling the next sync what they were built
	// from (see sealFor).
	Seal string
}

type indexStateKey struct{}

// WithIndexState hands st to the source fetches run under ctx.
func WithIndexState(ctx context.Context, st *IndexState) context.Context {
	return context.WithValue(ctx, indexStateKey{}, st)
}

func indexStateFrom(ctx context.Context) *IndexState {
	st, _ := ctx.Value(indexStateKey{}).(*IndexState)
	return st
}

// current reports whether document docID is indexed, complete and current,
// built from the same fetch key.
func (s *IndexState) current(docID, key string) bool {
	if s == nil || s.State == nil || key == "" {
		return false
	}
	d := s.State.Docs[docID]
	return d.Current(s.IndexFP) && d.FetchKey == key
}

// sealed reports whether every indexed document of the source is current
// and the source's points all carry seal — written by a sync that completed
// from the same inputs, so nothing needs fetching.
func (s *IndexState) sealed(seal string) bool {
	if s == nil || s.State == nil || seal == "" || len(s.State.Docs) == 0 ||
		s.State.Seal != rag.SealValue(seal, len(s.State.Docs)) {
		return false
	}
	for _, d := range s.State.Docs {
		if !d.Current(s.IndexFP) {
			return false
		}
	}
	return true
}

// keepAll returns a Keep document for every indexed document.
func (s *IndexState) keepAll() []models.SourceDocument {
	docs := make([]models.SourceDocument, 0, len(s.State.Docs))
	for id := range s.State.Docs {
		docs = append(docs, models.SourceDocument{ID: id, Keep: true})
	}
	return docs
}

// fetchKey fingerprints the inputs a document is built from besides
// content-addressed ones: the source's whole configuration and name (any
// setting may shape the document) and parts, the listing data. The ticked
// branches and tags are left to parts — the refs each document was found in
// — so ticking another ref refetches only what it changes.
func fetchKey(src *models.SourceDefinition, parts ...any) string {
	config := make(map[string]string, len(src.Config))
	for k, v := range src.Config {
		if k != "Branch" && k != "Tags" {
			config[k] = v
		}
	}
	cfg, _ := json.Marshal(config) // map keys marshal sorted
	data, err := json.Marshal(parts)
	build := buildFingerprint()
	if err != nil || build == "" {
		return "" // never kept
	}
	return rag.HashOf("fetch", build, src.ID, src.Type, src.Name, string(cfg), string(data))
}

// buildFingerprint identifies the running Conduit binary: a kept document
// was rendered by the code that wrote it, so any other build — whose
// rendering of the same inputs may differ — refetches. Embedding is still
// skipped for documents whose text comes out the same. "" (never kept) when
// the binary can't be read.
var buildFingerprint = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
})

// sealFor is fetchKey for a whole source, under this sync's index
// fingerprint.
func (s *IndexState) sealFor(src *models.SourceDefinition, parts ...any) string {
	key := fetchKey(src, parts...)
	if s == nil || key == "" {
		return ""
	}
	return rag.HashOf("seal", s.IndexFP, key)
}
