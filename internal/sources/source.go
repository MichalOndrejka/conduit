// Package sources provides the generic source abstraction. Unlike the Python
// app (which had nine Azure-DevOps-specific providers), the Go backend keeps
// the backend provider-agnostic by design: any JSON-over-HTTP system is
// configured through the generic API source (URL, auth, field mappings,
// pagination) rather than coded against. ADO, Jira, GitHub, etc. become
// source *configurations*, not source *code*.
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
	case "manual":
		return &ManualSource{src: src}, nil
	case "", "api", "custom":
		return &APISource{src: src, secrets: store}, nil
	default:
		return nil, fmt.Errorf(
			"unknown provider %q — the Go backend supports the generic %q provider and %q",
			src.GetConfig("Provider"), "api", "manual",
		)
	}
}

// CollectionFor mirrors collection_for in app/sources/factory.py: the source
// type is a domain category (work items, code, docs…) that routes documents
// to a Qdrant collection. Types are generic labels, not tied to any provider.
func CollectionFor(src *models.SourceDefinition) string {
	if src.GetConfig("Provider") == "manual" {
		return models.CollectionDocumentation
	}
	switch src.Type {
	case models.SourceWorkItemQuery:
		return models.CollectionWorkItems
	case models.SourceRequirements:
		return models.CollectionRequirements
	case models.SourceTestCase:
		return models.CollectionTestCases
	case models.SourceGitCommits:
		return models.CollectionCommits
	case models.SourceCodeRepo:
		return models.CollectionCode
	case models.SourceTestCodeRepo:
		return models.CollectionTestCode
	default:
		return models.CollectionDocumentation
	}
}
