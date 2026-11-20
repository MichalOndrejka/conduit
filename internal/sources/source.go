// Package sources provides the source abstraction. Azure DevOps sources run
// on the generic API source (URL, auth, field mappings, pagination) with
// ADO-specific enrichment; GitHub sources (github.com or GitHub Enterprise
// Server) have their own fetcher, GitHubSource.
package sources

import (
	"context"
	"fmt"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// ProgressCallback mirrors app/sources/base.py.
type ProgressCallback func(models.SyncProgress)

// Source is the Go port of the Source ABC in app/sources/base.py.
type Source interface {
	FetchDocuments(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error)
}

// StatefulSource is a source that carries state from one sync to the next
// (e.g. where a change feed left off), so that it reads only what's new of
// data that never changes once written.
type StatefulSource interface {
	// SetSyncState hands over, before FetchDocuments, the state the last
	// successful sync left (nil: none) and how many of the source's history
	// records the index holds — none while the state says there should be
	// some means they were deleted, and the source starts over.
	SetSyncState(state []byte, indexedHistory int)
	// SyncResult, after FetchDocuments, says how to index the documents and
	// what state to keep once they are stored; nil: a plain full replace,
	// nothing to keep.
	SyncResult() *SyncResult
}

// SyncResult is how a StatefulSource's documents are indexed.
type SyncResult struct {
	// State is kept for the next sync, and only once this sync's documents
	// are stored: a failed sync starts again from the previous state.
	State []byte
	// KeepHistory keeps the source's already-indexed history records
	// (record = change) and adds the new ones; everything else of the
	// source is replaced as usual. False rebuilds the history too.
	KeepHistory bool
	// RemoveHistoryIDs are work item ids whose history records are deleted:
	// the items left the source.
	RemoveHistoryIDs []string
}

// New creates the implementation for a source definition. Provider selects
// the fetch mechanism; the source *type* only selects the target collection.
func New(src *models.SourceDefinition, store secrets.Reader) (Source, error) {
	if src.Type == models.RetiredSourceTestCase {
		return nil, fmt.Errorf("the Test Cases source type was merged into Requirements; recreate this source as a Requirements source")
	}
	switch src.GetConfig("Provider") {
	case "", "api", "custom":
		return &APISource{src: src, secrets: store}, nil
	case "github":
		return newGitHubSource(src, store)
	default:
		return nil, fmt.Errorf(
			"unknown provider %q — the Go backend supports %q (Azure DevOps) and %q",
			src.GetConfig("Provider"), "api", "github",
		)
	}
}

// CollectionFor mirrors collection_for in app/sources/factory.py: the source
// type is a domain category (work items, code, docs…) that routes documents
// to a Qdrant collection. Types are generic labels, not tied to any provider.
// Live types (models.IsLiveType) have no collection: "".
func CollectionFor(src *models.SourceDefinition) string {
	switch src.Type {
	case models.SourceWorkItemQuery:
		return models.CollectionWorkItems
	case models.SourceRequirements:
		return models.CollectionRequirements
	case models.SourceGitCommits:
		return models.CollectionCommits
	case models.SourcePipelineRun, models.SourcePullRequest:
		return "" // read live by their tools, never indexed
	case models.SourceCodeRepo:
		return models.CollectionSourceCode
	case models.SourceTestCodeRepo:
		return models.CollectionTestCode
	default:
		return models.CollectionDocumentation
	}
}

// errLiveSource is what fetching a live source's documents returns.
func errLiveSource(src *models.SourceDefinition) error {
	return fmt.Errorf("%s sources are read live by their tool and are never synced", src.Type)
}
