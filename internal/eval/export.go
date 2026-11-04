package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// intentOf is the intent an exported document carries: its source type's,
// with Code split into code and test code by its code_kind. Requirements and
// test cases are whatever the team keeps them as (work items or files), so
// only labelled questions say which documents are which.
func intentOf(sourceType string, d models.SourceDocument) string {
	switch sourceType {
	case models.SourceWorkItemQuery:
		return IntentWorkItem
	case models.SourceCodeRepo:
		if slices.Contains(d.Filters.Fields[models.FilterCodeKind], models.CodeKindTest) {
			return IntentTestCode
		}
		return IntentCode
	case models.SourceGitCommits:
		return IntentCommit
	case models.SourcePipelineRun:
		return IntentPipelineRun
	default:
		return IntentDocumentation
	}
}

// shapeOf is where a source's documents live.
func shapeOf(src models.SourceDefinition) string {
	switch src.Type {
	case models.SourceWorkItemQuery:
		return ShapeWorkItem
	case models.SourceGitCommits:
		return ShapeCommit
	case models.SourcePipelineRun:
		return ShapePipelineRun
	default:
		return ShapeFile
	}
}

// ExportOptions select what Export fetches.
type ExportOptions struct {
	Company   string   // company ID in the dataset; default "real"
	SourceIDs []string // empty = every enabled source
	Log       func(format string, args ...any)
}

// Export fetches every selected source's documents, exactly as a sync would,
// into a single-company dataset without questions. Label questions against
// it in a separate queries file (see QueriesFile); keep both out of git.
//
// Documents carry the filters a sync would write (source, provider,
// code_kind). Scopes saying how the company's requirements or test cases are
// narrowed (a work item type, a folder) go in the queries file.
func Export(ctx context.Context, all []models.SourceDefinition, store secrets.Reader, opts ExportOptions) (*Dataset, error) {
	if opts.Company == "" {
		opts.Company = "real"
	}
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c := Company{ID: opts.Company, Description: "Exported from this Conduit's configured sources.", Scopes: map[string][]Scope{}}
	seen := map[string]bool{}
	for _, src := range all {
		if len(opts.SourceIDs) > 0 && !slices.Contains(opts.SourceIDs, src.ID) {
			continue
		}
		if len(opts.SourceIDs) == 0 && src.Disabled {
			continue
		}
		models.MigrateSourceType(&src) // a legacy type, as the store would re-type it
		s, err := sources.New(&src, store)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", src.Name, err)
		}
		logf("fetching %s (%s)", src.Name, src.Type)
		docs, err := s.FetchDocuments(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", src.Name, err)
		}
		sources.AddSourceFilters(&src, docs)
		shape := shapeOf(src)
		for _, d := range docs {
			intent := intentOf(src.Type, d)
			if seen[d.ID] {
				logf("skipping duplicate document %s", d.ID)
				continue
			}
			seen[d.ID] = true
			title := d.Properties["title"]
			if title == "" {
				title = d.ID
			}
			c.Documents = append(c.Documents, Document{ID: d.ID, Intent: intent, Shape: shape, Title: title, Text: d.Text, Fields: d.Filters.Fields})
		}
		logf("  %d documents", len(docs))
	}
	sort.Slice(c.Documents, func(i, j int) bool { return c.Documents[i].ID < c.Documents[j].ID })
	return &Dataset{Name: opts.Company, Description: "Private dataset exported from configured sources; do not commit.", Companies: []Company{c}}, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// WriteJSON writes v to path, creating its directory.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
