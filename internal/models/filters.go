package models

import (
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
	FilterBranch        = "branch"         // pipeline run source branch; pull request target branch
	FilterReviewer      = "reviewer"       // pull request reviewer name and unique name / login
	FilterBuildID       = "build_id"       // pipeline run id: the run and its step/test documents
	FilterID            = "id"             // work item id, issue number, pull request id/number
	FilterCommit        = "commit"         // commit SHA; any prefix of 7+ characters matches

	// Pipeline runs, their steps and tests, deployments and test history.
	FilterType           = "type"            // document kind: run, step, test, deployment, test_history
	FilterPipeline       = "pipeline"        // pipeline (definition / workflow) name and id
	FilterStatus         = "status"          // run status at sync: completed, inprogress, notstarted
	FilterReason         = "reason"          // what started the run: manual, ci, pullrequest, schedule, …
	FilterRequestedFor   = "requested_for"   // who the run was requested for / triggered by
	FilterBuildNumber    = "build_number"    // run number, e.g. 20261105.3
	FilterRerun          = "rerun"           // "true" when the run had more than one attempt
	FilterFailedStage    = "failed_stage"    // names of stages that failed in any attempt
	FilterFailedJob      = "failed_job"      // names of jobs that failed in any attempt
	FilterFailedStep     = "failed_step"     // names of steps (tasks) that failed in any attempt
	FilterFailedTest     = "failed_test"     // names of tests that failed or were flaky
	FilterContainsCommit = "contains_commit" // SHAs (and 7+ prefixes) of the commits a run built since the previous run
	FilterEnvironment    = "environment"     // deployment environment
	FilterFlaky          = "flaky"           // "true" for a test that passed only on retry
	FilterAttempt        = "attempt"         // pipeline attempt a failed step or test belongs to
)

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
}

// minCommitPrefix is the shortest commit SHA prefix the commit filter
// matches (git's own default abbreviation).
const minCommitPrefix = 7

// NormalizeFilterValue canonicalizes one value of field for storage or
// matching: trimmed and lower-cased, and for hierarchical fields with either
// slash accepted as the separator and leading/trailing separators removed.
// IDs drop the reference prefixes they are written with (AB#123, #123,
// !45 for a pull request).
func NormalizeFilterValue(field, v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if field == FilterID {
		v = strings.TrimPrefix(v, "ab")
		v = strings.TrimLeft(v, "#!")
	}
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
	if field == FilterCommit || field == FilterContainsCommit {
		// Every prefix from git's short form up, so a short SHA from a log,
		// a link or a build summary matches with plain equality.
		out := []string{v}
		for n := minCommitPrefix; n < len(v); n++ {
			out = append(out, v[:n])
		}
		return out
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
	Date   time.Time // commit author date, pull request creation date; zero = no date
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

// Clone returns a copy whose fields can be extended without touching f, for
// documents that share most of their filters.
func (f DocumentFilters) Clone() DocumentFilters {
	out := DocumentFilters{Date: f.Date, Fields: make(map[string][]string, len(f.Fields))}
	for k, v := range f.Fields {
		out.Fields[k] = append([]string(nil), v...)
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
