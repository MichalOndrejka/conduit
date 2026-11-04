package models

import (
	"strconv"
	"strings"
	"time"
)

// Filter fields are real-world categories from the source system (project,
// repository, author, work item state, …) stored on every chunk so search
// can be narrowed by them. They are written by the sources at fetch time and
// matched by the MCP search tools; both sides normalize through FilterValues
// / NormalizeFilterValue below so matching is case-insensitive and
// hierarchical fields match at any level.
const (
	FilterProject       = "project"
	FilterRepository    = "repository"
	FilterPath          = "path"           // file path; hierarchical ("/")
	FilterExtension     = "extension"      // file extension without the dot
	FilterAuthor        = "author"         // commit author name and email
	FilterWorkItemType  = "work_item_type" // Bug, Task, User Story, …
	FilterState         = "state"
	FilterIterationPath = "iteration_path" // hierarchical ("\")
	FilterAssignedTo    = "assigned_to"    // display name and unique name
	FilterTag           = "tags"           // work item tags
	FilterResult        = "result"         // pipeline run result
	FilterBranch        = "branch"         // pipeline run source branch
	FilterAreaPath      = "area_path"      // hierarchical ("\")
	FilterSource        = "source"         // Conduit source name, on every document
	FilterProvider      = "provider"       // azure-devops or github, on every document
	FilterCodeKind      = "code_kind"      // Code only: test or production
)

// AllFilterFields lists every filter field, for creating payload indexes.
var AllFilterFields = []string{
	FilterProject, FilterRepository, FilterPath, FilterExtension, FilterAuthor,
	FilterWorkItemType, FilterState, FilterIterationPath, FilterAssignedTo, FilterTag,
	FilterResult, FilterBranch, FilterAreaPath, FilterSource, FilterProvider, FilterCodeKind,
}

// FilterPrefix namespaces filter fields in a chunk's payload; FilterDateKey
// holds the document's primary date (commit author date) as Unix seconds,
// for since/until range filters.
const (
	FilterPrefix  = "f_"
	FilterDateKey = FilterPrefix + "date"
)

// FilterKey is the payload key a filter field is stored under.
func FilterKey(field string) string { return FilterPrefix + field }

// filterSeparators maps hierarchical fields to their path separator.
var filterSeparators = map[string]string{
	FilterPath:          "/",
	FilterIterationPath: `\`,
	FilterAreaPath:      `\`,
}

// IsHierarchical reports whether field is a path-like field whose values
// store every ancestor (path, iteration_path, area_path).
func IsHierarchical(field string) bool {
	_, ok := filterSeparators[field]
	return ok
}

// FilterSeparator is a hierarchical field's separator ("" otherwise).
func FilterSeparator(field string) string { return filterSeparators[field] }

// FilterDepth is the level of a normalized hierarchical value: 1 for a
// top-level folder ("src"), 2 for "src/app", …; 0 for "".
func FilterDepth(field, normalized string) int {
	if normalized == "" {
		return 0
	}
	return strings.Count(normalized, filterSeparators[field]) + 1
}

// FilterLevelKey is the payload key holding a hierarchical field's values
// at one level ("f_path__2" holds "src/app"), so facets can count the
// children of a folder exactly.
func FilterLevelKey(field string, level int) string {
	return FilterKey(field) + "__" + strconv.Itoa(level)
}

// FilterLevels groups a hierarchical field's stored values by level.
func FilterLevels(field string, values []string) map[int][]string {
	out := map[int][]string{}
	for _, v := range values {
		if d := FilterDepth(field, v); d > 0 {
			out[d] = append(out[d], v)
		}
	}
	return out
}

// NormalizeFilterValue canonicalizes one value of field for storage or
// matching: trimmed and lower-cased, and for hierarchical fields with either
// slash accepted as the separator and leading/trailing separators removed.
func NormalizeFilterValue(field, v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if sep, ok := filterSeparators[field]; ok {
		v = strings.ReplaceAll(strings.ReplaceAll(v, `\`, sep), "/", sep)
		v = strings.Trim(v, sep)
	}
	return v
}

// FilterValues returns the normalized values to store for raw. Hierarchical
// fields store every ancestor too ("src", "src/app", "src/app/main.go"), so a
// filter on any level matches everything beneath it with a plain equality
// match.
func FilterValues(field, raw string) []string {
	v := NormalizeFilterValue(field, raw)
	if v == "" {
		return nil
	}
	sep, ok := filterSeparators[field]
	if !ok {
		return []string{v}
	}
	parts := strings.Split(v, sep)
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[:i+1], sep))
	}
	return out
}

// DocumentFilters accumulates a document's filter fields; the zero value is
// ready to use.
type DocumentFilters struct {
	Fields map[string][]string
	Date   time.Time // commit author date; zero = no date
}

// Add stores raw (normalized, plus ancestors for hierarchical fields) under
// field, skipping empty values and duplicates.
func (f *DocumentFilters) Add(field string, raw ...string) {
	for _, r := range raw {
		for _, v := range FilterValues(field, r) {
			if f.Fields == nil {
				f.Fields = map[string][]string{}
			}
			if !containsString(f.Fields[field], v) {
				f.Fields[field] = append(f.Fields[field], v)
			}
		}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Payload index types (Qdrant field_schema).
const (
	IndexKeyword = "keyword"
	IndexInteger = "integer"
)

// BasePayloadIndexes are the payload indexes every search collection gets
// when it is created (and existing ones at startup): every filter field,
// the date range field, the chunk index (facets count documents by their
// first chunk) and the source ID (disabled sources are excluded from every
// search). Without them Qdrant plans selective filters poorly and can return
// fewer than the requested top results. The per-level keys of hierarchical
// fields (FilterLevelKey) are indexed by the indexer as it writes them.
func BasePayloadIndexes() map[string]string {
	out := map[string]string{
		FilterDateKey:       IndexInteger,
		PayloadChunkIndex:   IndexKeyword,
		TagKey("source_id"): IndexKeyword,
	}
	for _, f := range AllFilterFields {
		out[FilterKey(f)] = IndexKeyword
	}
	return out
}
