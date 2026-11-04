// Package sources provides the source abstraction. Azure DevOps sources run
// on the generic API source (URL, auth, field mappings, pagination) with
// ADO-specific enrichment; GitHub sources (github.com or GitHub Enterprise
// Server) have their own fetcher, GitHubSource.
package sources

import (
	"context"
	"fmt"
	"maps"

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

// CollectionFor routes a source's documents to the collection of their
// shape: work items, code (production and tests), documentation (repository
// and wiki files), commits or pipeline runs. A source still carrying a
// merged legacy type is routed as it migrates (models.MigrateSourceType).
func CollectionFor(src *models.SourceDefinition) string {
	t := src.Type
	if _, ok := legacyTypes[t]; ok {
		migrated := *src
		migrated.Config = maps.Clone(src.Config)
		models.MigrateSourceType(&migrated)
		t = migrated.Type
	}
	switch t {
	case models.SourceWorkItemQuery:
		return models.CollectionWorkItems
	case models.SourceGitCommits:
		return models.CollectionCommits
	case models.SourcePipelineRun:
		return models.CollectionPipelineRuns
	case models.SourceCodeRepo:
		return models.CollectionCode
	default:
		return models.CollectionDocumentation
	}
}

var legacyTypes = map[string]struct{}{
	models.LegacySourceRequirements: {}, models.LegacySourceTestCase: {}, models.LegacySourceTestCode: {},
}

// ProviderName is a source's provider as the provider filter stores it:
// github or azure-devops.
func ProviderName(src *models.SourceDefinition) string {
	if src.GetConfig("Provider") == "github" {
		return "github"
	}
	return "azure-devops"
}

// AddSourceFilters adds the facts every document carries whatever its
// source: the source name and provider, and for Code the code_kind of each
// file (test or production), from the source's Test file patterns or, when
// it has none, the common conventions. code_kind is mirrored into the
// document's properties so results show the value to filter by.
func AddSourceFilters(src *models.SourceDefinition, docs []models.SourceDocument) {
	provider := ProviderName(src)
	var codeKind func(string) string
	if CollectionFor(src) == models.CollectionCode {
		codeKind = models.CodeKindFunc(models.ParseTestPatterns(src.GetConfig(models.ConfigTestFilePatterns)))
	}
	for i := range docs {
		d := &docs[i]
		d.Filters.Add(models.FilterSource, src.Name)
		d.Filters.Add(models.FilterProvider, provider)
		if codeKind == nil {
			continue
		}
		p := d.Properties["path"]
		if p == "" {
			continue // not a file (e.g. a metadata-only item)
		}
		kind := codeKind(p)
		d.Filters.Add(models.FilterCodeKind, kind)
		if d.Properties == nil {
			d.Properties = map[string]string{}
		}
		d.Properties["code_kind"] = kind
	}
}
