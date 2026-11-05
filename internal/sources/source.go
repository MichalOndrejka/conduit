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

// New creates the implementation for a source definition. Provider selects
// the fetch mechanism; the source *type* only selects the target collection.
func New(src *models.SourceDefinition, store secrets.Reader) (Source, error) {
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
func CollectionFor(src *models.SourceDefinition) string {
	switch src.Type {
	case models.SourceWorkItemQuery:
		return models.CollectionWorkItems
	case models.SourceRequirements:
		return models.CollectionRequirements
	case models.SourceTestCase:
		return models.CollectionTestCases
	case models.SourceGitCommits:
		return models.CollectionCommits
	case models.SourcePipelineRun:
		return models.CollectionPipelineRuns
	case models.SourcePullRequest:
		return models.CollectionPullRequests
	case models.SourceCodeRepo:
		return models.CollectionSourceCode
	case models.SourceTestCodeRepo:
		return models.CollectionTestCode
	default:
		return models.CollectionDocumentation
	}
}
