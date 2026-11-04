package mcptools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// FilterField is one optional filter a search tool accepts. Filters are
// real categories from the source system (see models/filters.go), not
// Conduit configuration. Exported so the accuracy eval offers its agent
// exactly the real tool surface.
type FilterField struct {
	Name string
	Desc string
	Date bool // since/until bound on the document's primary date
}

var (
	fProject    = FilterField{Name: models.FilterProject, Desc: "Azure DevOps project name, or GitHub owner"}
	fRepository = FilterField{Name: models.FilterRepository, Desc: "Git repository name"}
	fPath       = FilterField{Name: models.FilterPath, Desc: "File or folder path within the repository, e.g. src/auth — " +
		"a folder matches everything beneath it"}
	fExtension = FilterField{Name: models.FilterExtension, Desc: "File extension without the dot, e.g. cs, ts, md"}
	fAuthor    = FilterField{Name: models.FilterAuthor, Desc: "Commit author's name or email"}
	fWIType    = FilterField{Name: models.FilterWorkItemType, Desc: "The team's own work item or issue type, e.g. Bug, Task, " +
		"User Story, Product Requirement, Test Case — types differ per process; use facets to list them"}
	fState     = FilterField{Name: models.FilterState, Desc: "Work item state, e.g. Active, Resolved, Closed"}
	fIteration = FilterField{Name: models.FilterIterationPath, Desc: `Iteration path, e.g. Project\Sprint 12 — matches that iteration and every child`}
	fArea      = FilterField{Name: models.FilterAreaPath, Desc: `Area path, e.g. Project\Team A — matches that area and every child`}
	fAssigned  = FilterField{Name: models.FilterAssignedTo, Desc: "Assignee's display name or unique name (email)"}
	fTag       = FilterField{Name: models.FilterTag, Desc: "Work item tag or issue label"}
	fResult    = FilterField{Name: models.FilterResult, Desc: "Pipeline run result: succeeded, partiallySucceeded, failed or canceled"}
	fBranch    = FilterField{Name: models.FilterBranch, Desc: "Pipeline run source branch, as a full ref (refs/heads/main, " +
		"refs/pull/42/merge) or short name (main, pull/42/merge)"}
	fCodeKind = FilterField{Name: models.FilterCodeKind, Desc: `"test" or "production": whether the file is a test, from ` +
		`test folders and names (tests/, *_test.go, *.spec.ts, *Tests.cs, …) or the source's own test file patterns`}
	fSource   = FilterField{Name: models.FilterSource, Desc: "Conduit source name, as configured in Conduit"}
	fProvider = FilterField{Name: models.FilterProvider, Desc: "Where the data comes from: azure-devops or github"}
)

func dateFields(what string) []FilterField {
	return []FilterField{
		{Name: "since", Date: true, Desc: "Only items whose " + what + " is on or after this date (YYYY-MM-DD or RFC 3339)"},
		{Name: "until", Date: true, Desc: "Only items whose " + what + " is on or before this date (YYYY-MM-DD or RFC 3339)"},
	}
}

func fields(f ...FilterField) []FilterField { return append(f, fSource, fProvider) }

// collectionFilters lists the filters each search tool's collection
// supports; every collection also accepts source and provider.
var collectionFilters = map[string][]FilterField{
	models.CollectionWorkItems:     fields(fProject, fWIType, fState, fArea, fIteration, fAssigned, fTag),
	models.CollectionCode:          fields(fProject, fRepository, fPath, fExtension, fCodeKind),
	models.CollectionDocumentation: fields(fProject, fRepository, fPath, fExtension),
	models.CollectionCommits: append(fields(fProject, fRepository, fAuthor,
		FilterField{Name: models.FilterPath, Desc: "Changed file or folder path, e.g. src/auth — a folder matches every commit touching a file beneath it"}),
		dateFields("author date")...),
	models.CollectionPipelineRuns: fields(fProject, fRepository, fResult, fBranch),
}

// filtersSchema is the JSON Schema of the "filters" object for fields.
func filtersSchema(fields []FilterField) map[string]any {
	props := map[string]any{}
	for _, f := range fields {
		if f.Date {
			props[f.Name] = map[string]any{"type": "string", "description": f.Desc}
			continue
		}
		props[f.Name] = map[string]any{
			"description": f.Desc + ". Case-insensitive; pass an array to match any of several values.",
			"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		}
	}
	return map[string]any{
		"type": "object",
		"description": "Optional: narrow results by source-system fields. Different fields combine with AND; " +
			"several values for one field combine with OR.",
		"properties":           props,
		"additionalProperties": false,
	}
}

// parseFilters converts a request's "filters" object into Qdrant conditions.
func parseFilters(raw any, fields []FilterField) ([]rag.FieldCondition, error) {
	if raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("request.filters must be an object")
	}
	byName := make(map[string]FilterField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic condition order

	var conds []rag.FieldCondition
	var dateRange rag.Range
	for _, k := range keys {
		f, ok := byName[k]
		if !ok {
			return nil, fmt.Errorf("unknown filter %q — this tool supports: %s", k, filterNames(fields))
		}
		if f.Date {
			s, ok := obj[k].(string)
			if !ok {
				return nil, fmt.Errorf("request.filters.%s must be a date string", k)
			}
			ts, err := parseFilterDate(s, k == "until")
			if err != nil {
				return nil, fmt.Errorf("request.filters.%s: %w", k, err)
			}
			if k == "since" {
				dateRange.Gte = &ts
			} else {
				dateRange.Lte = &ts
			}
			continue
		}
		values, err := filterStrings(obj[k])
		if err != nil {
			return nil, fmt.Errorf("request.filters.%s: %w", k, err)
		}
		var norm []string
		for _, v := range values {
			if n := models.NormalizeFilterValue(k, v); n != "" {
				norm = append(norm, n)
			}
		}
		switch len(norm) {
		case 0:
			continue
		case 1:
			conds = append(conds, rag.FieldCondition{Key: models.FilterKey(k), Match: rag.Match{Value: norm[0]}})
		default:
			conds = append(conds, rag.FieldCondition{Key: models.FilterKey(k), Match: rag.Match{Any: norm}})
		}
	}
	if dateRange.Gte != nil || dateRange.Lte != nil {
		conds = append(conds, rag.FieldCondition{Key: models.FilterDateKey, Range: &dateRange})
	}
	return conds, nil
}

func filterStrings(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("array values must be strings")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a string or an array of strings")
}

// parseFilterDate parses YYYY-MM-DD (UTC; an "until" date covers the whole
// day) or RFC 3339, returning Unix seconds.
func parseFilterDate(s string, endOfDay bool) (float64, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return float64(t.Unix()), nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a YYYY-MM-DD or RFC 3339 date", s)
	}
	if endOfDay {
		t = t.Add(24*time.Hour - time.Second)
	}
	return float64(t.Unix()), nil
}

func filterNames(fields []FilterField) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}
