package mcptools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// filterField is one optional filter a search tool accepts. Filters are real
// categories from the source system (see models/filters.go), not Conduit
// configuration.
type filterField struct {
	name string
	desc string
	date bool // since/until bound on the document's primary date
}

var (
	fProject    = filterField{name: models.FilterProject, desc: "Azure DevOps project name"}
	fRepository = filterField{name: models.FilterRepository, desc: "Git repository name"}
	fPath       = filterField{name: models.FilterPath, desc: "File or folder path within the repository, e.g. src/auth — " +
		"a folder matches everything beneath it"}
	fExtension = filterField{name: models.FilterExtension, desc: "File extension without the dot, e.g. cs, ts, md"}
	fAuthor    = filterField{name: models.FilterAuthor, desc: "Commit author's name or email"}
	fWIType    = filterField{name: models.FilterWorkItemType, desc: "Work item type, e.g. Bug, Task, User Story"}
	fState     = filterField{name: models.FilterState, desc: "Work item state, e.g. Active, Resolved, Closed"}
	fIteration = filterField{name: models.FilterIterationPath, desc: `Iteration path, e.g. Project\Sprint 12 — matches that iteration and every child`}
	fAssigned  = filterField{name: models.FilterAssignedTo, desc: "Assignee's display name or unique name (email)"}
	fTag       = filterField{name: models.FilterTag, desc: "Work item tag"}
)

func dateFields(what string) []filterField {
	return []filterField{
		{name: "since", date: true, desc: "Only items whose " + what + " is on or after this date (YYYY-MM-DD or RFC 3339)"},
		{name: "until", date: true, desc: "Only items whose " + what + " is on or before this date (YYYY-MM-DD or RFC 3339)"},
	}
}

var (
	repoFileFilters = []filterField{fProject, fRepository, fPath, fExtension}
	// Area path and date aren't offered: a work item source is already scoped
	// to its area paths, and work items are searched by content, not age.
	workItemFilters = []filterField{fProject, fWIType, fState, fIteration, fAssigned, fTag}
)

// collectionFilters lists the filters each search tool's collection
// supports. Requirements and test cases can come from repo files or work
// items, so they accept both sets.
var collectionFilters = map[string][]filterField{
	models.CollectionWorkItems:     workItemFilters,
	models.CollectionRequirements:  append(append([]filterField{}, repoFileFilters...), workItemFilters[1:]...),
	models.CollectionTestCases:     append(append([]filterField{}, repoFileFilters...), workItemFilters[1:]...),
	models.CollectionSourceCode:    repoFileFilters,
	models.CollectionTestCode:      repoFileFilters,
	models.CollectionDocumentation: repoFileFilters,
	models.CollectionCommits: append([]filterField{fProject, fRepository, fAuthor,
		{name: models.FilterPath, desc: "Changed file or folder path, e.g. src/auth — a folder matches every commit touching a file beneath it"}},
		dateFields("author date")...),
}

// filtersSchema is the JSON Schema of the "filters" object for fields.
func filtersSchema(fields []filterField) map[string]any {
	props := map[string]any{}
	for _, f := range fields {
		if f.date {
			props[f.name] = map[string]any{"type": "string", "description": f.desc}
			continue
		}
		props[f.name] = map[string]any{
			"description": f.desc + ". Case-insensitive; pass an array to match any of several values.",
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
func parseFilters(raw any, fields []filterField) ([]rag.FieldCondition, error) {
	if raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("request.filters must be an object")
	}
	byName := make(map[string]filterField, len(fields))
	for _, f := range fields {
		byName[f.name] = f
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
		if f.date {
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

func filterNames(fields []filterField) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.name
	}
	return strings.Join(names, ", ")
}
