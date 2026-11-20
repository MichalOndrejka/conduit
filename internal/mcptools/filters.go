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
	// person fields accept "me": the token owners the sources recorded at
	// their last sync.
	person bool
}

var (
	fProject    = filterField{name: models.FilterProject, desc: "Azure DevOps project name"}
	fRepository = filterField{name: models.FilterRepository, desc: "Git repository name"}
	fPath       = filterField{name: models.FilterPath, desc: "File or folder path within the repository, e.g. src/auth — " +
		"a folder matches everything beneath it"}
	fExtension  = filterField{name: models.FilterExtension, desc: "File extension without the dot, e.g. cs, ts, md"}
	fAuthor     = filterField{name: models.FilterAuthor, desc: `Commit author's name or email; "me" for the token owner`, person: true}
	fWIType     = filterField{name: models.FilterWorkItemType, desc: "Work item type, singular, e.g. Bug, Task, User Story, Software Requirement"}
	fState      = filterField{name: models.FilterState, desc: "Work item state, e.g. Active, Resolved, Closed"}
	fIteration  = filterField{name: models.FilterIterationPath, desc: `Iteration path, e.g. Project\Sprint 12 — matches that iteration and every child`}
	fAssigned   = filterField{name: models.FilterAssignedTo, desc: `Assignee's display name or unique name (email); "me" for the token owner`, person: true}
	fTag        = filterField{name: models.FilterTag, desc: "Work item tag"}
	fWorkItemID = filterField{name: models.FilterID, desc: "Work item ID (Azure DevOps) or issue number (GitHub), e.g. 1234 " +
		"(AB#1234 and #1234 work too): opens the item another result refers to — a Links entry, a commit message, " +
		"a pull request's linked work items"}
	fVersion = filterField{name: models.FilterVersion, desc: "Branch or tag of the repository, e.g. main, release/2.0, v2.1: " +
		"only files and commits in that version. A file or commit that is the same in several versions is embedded " +
		"once and matches each of them; results name their versions"}
	fCommit = filterField{name: models.FilterCommit, desc: "Commit SHA, full or abbreviated (7 or more characters): " +
		"opens the commit another result refers to"}
	fRecord = filterField{name: models.FilterRecord, desc: `"item" (the default) for the work items as they are now, ` +
		`"change" for their history — one record per revision: who changed which fields from what to what, and when. ` +
		`History is left out unless this is "change" (or both) or changed_by / changed_field is set`}
	fChangedBy = filterField{name: models.FilterChangedBy, desc: `Who made a change (history records): display name ` +
		`or unique name (email); "me" for the token owner`, person: true}
	fChangedField = filterField{name: models.FilterChangedField, desc: "Field a change touched (history records), " +
		"reference or short name, e.g. System.State or State, AssignedTo, StoryPoints; Comment for added comments"}
)

func dateFields(what string) []filterField {
	return []filterField{
		{name: "since", date: true, desc: "Only items whose " + what + " is on or after this date (YYYY-MM-DD or RFC 3339)"},
		{name: "until", date: true, desc: "Only items whose " + what + " is on or before this date (YYYY-MM-DD or RFC 3339)"},
	}
}

var (
	repoFileFilters = []filterField{fProject, fRepository, fVersion, fPath, fExtension}
	// Area path isn't offered: a work item source is already scoped to its
	// area paths. The date is a history record's change date, an item's
	// last-changed date.
	workItemFilters = append([]filterField{fProject, fWIType, fState, fIteration, fAssigned, fTag, fWorkItemID,
		fRecord, fChangedBy, fChangedField}, dateFields("change date (for an item: when it was last changed)")...)
	// Requirements and test cases kept as repository files have no change
	// date, and the date filters say so rather than drop them silently.
	reqTestFilters = append(append(append([]filterField{}, repoFileFilters...), workItemFilters[1:len(workItemFilters)-2]...),
		dateFields("change date — only work items have one (for an item: when it was last changed); "+
			"repository files have none and are left out by a date filter")...)
)

// collectionFilters lists the filters each search tool's collection
// supports. Requirements (test cases included) can come from repo files or
// work items, so they accept both sets.
var collectionFilters = map[string][]filterField{
	models.CollectionWorkItems:     workItemFilters,
	models.CollectionRequirements:  reqTestFilters,
	models.CollectionSourceCode:    repoFileFilters,
	models.CollectionTestCode:      repoFileFilters,
	models.CollectionDocumentation: repoFileFilters,
	models.CollectionCommits: append([]filterField{fProject, fRepository, fVersion, fCommit, fAuthor,
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
	return parseFiltersFor(raw, fields, nil)
}

// parseFiltersFor is parseFilters with "me" on a person field replaced by
// me, the identities the sources recorded at sync.
func parseFiltersFor(raw any, fields []filterField, me []string) ([]rag.FieldCondition, error) {
	obj := map[string]any{}
	if raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("request.filters must be an object")
		}
		obj = m
	}
	byName := make(map[string]filterField, len(fields))
	for _, f := range fields {
		byName[f.name] = f
	}
	recordConds, err := recordConditions(obj, byName)
	if err != nil {
		return nil, err
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
		if k == models.FilterRecord {
			continue // recordConditions
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
		if f.person {
			var expanded []string
			for _, v := range values {
				if !strings.EqualFold(strings.TrimSpace(v), "me") {
					expanded = append(expanded, v)
					continue
				}
				if len(me) == 0 {
					return nil, fmt.Errorf(`request.filters.%s: "me" is unknown — no source has recorded its token owner yet; `+
						`sync a source (source_sync trigger) or pass a name or email`, k)
				}
				expanded = append(expanded, me...)
			}
			values = expanded
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
	return append(conds, recordConds...), nil
}

// historyRecord matches work item history records (one revision each).
var historyRecord = rag.FieldCondition{Key: models.FilterKey(models.FilterRecord), Match: rag.Match{Value: "change"}}

// recordConditions is how a tool offering the record filter treats work
// item history. "item" is anything that isn't a history record, so items
// indexed before records were stored, and requirements or test cases kept
// as repository files, count too. History records only appear when asked
// for — record "change", or a filter that only they carry (changed_by,
// changed_field) — so looking up an item by id or state isn't swamped by
// one record per revision.
func recordConditions(obj map[string]any, byName map[string]filterField) ([]rag.FieldCondition, error) {
	if _, offered := byName[models.FilterRecord]; !offered {
		return nil, nil
	}
	raw, given := obj[models.FilterRecord]
	if !given {
		_, by := obj[models.FilterChangedBy]
		_, field := obj[models.FilterChangedField]
		if by || field {
			return []rag.FieldCondition{historyRecord}, nil
		}
		not := historyRecord
		not.Not = true
		return []rag.FieldCondition{not}, nil
	}
	values, err := filterStrings(raw)
	if err != nil {
		return nil, fmt.Errorf("request.filters.record: %w", err)
	}
	var item, change bool
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "item":
			item = true
		case "change":
			change = true
		default:
			return nil, fmt.Errorf(`request.filters.record: %q is not "item" or "change"`, v)
		}
	}
	switch {
	case item && change:
		return nil, nil
	case change:
		return []rag.FieldCondition{historyRecord}, nil
	default:
		not := historyRecord
		not.Not = true
		return []rag.FieldCondition{not}, nil
	}
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
