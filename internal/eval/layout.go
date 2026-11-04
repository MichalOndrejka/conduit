package eval

import (
	"maps"
	"slices"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// Physical indexes: how documents are distributed over collections. Every
// layout searches one of them.
const (
	// IndexCurrent is today's organization: one collection per source type
	// (intent), whatever the document's shape.
	IndexCurrent = "current"
	// IndexMerged is the proposed one: one collection per shape (work items,
	// code, documentation, …), every document carrying its filter fields
	// plus the derived category and code_kind.
	IndexMerged = "merged"
)

// Collection kinds of each index.
const (
	CollRequirements  = "requirements"
	CollTestCases     = "test_cases"
	CollWorkItems     = "work_items"
	CollCode          = "code"
	CollTestCode      = "test_code"
	CollDocumentation = "documentation"
	CollCommits       = "commits"
	CollPipelineRuns  = "pipeline_runs"
)

var mergedCollections = []string{CollWorkItems, CollCode, CollDocumentation, CollCommits, CollPipelineRuns}

// Fields the merged index derives at index time.
const (
	// FieldCategory is the proposed per-source purpose label (requirements,
	// test cases, …), set by the user on the source in the merged design;
	// here it is the document's intent.
	FieldCategory = "category"
	// FieldCodeKind is "test" or "production", derived from the file path.
	FieldCodeKind = models.FilterCodeKind
)

// currentCollection is where today's layout stores a document of intent:
// the collection of its source type.
func currentCollection(intent string) string {
	switch intent {
	case IntentRequirement:
		return CollRequirements
	case IntentTestCase:
		return CollTestCases
	case IntentWorkItem:
		return CollWorkItems
	case IntentCode:
		return CollCode
	case IntentTestCode:
		return CollTestCode
	case IntentCommit:
		return CollCommits
	case IntentPipelineRun:
		return CollPipelineRuns
	default:
		return CollDocumentation
	}
}

// mergedCollection is where the merged layout stores d: by shape, with
// repository files split into code and documentation.
func mergedCollection(d Document) string {
	switch d.Shape {
	case ShapeWorkItem:
		return CollWorkItems
	case ShapeCommit:
		return CollCommits
	case ShapePipelineRun:
		return CollPipelineRuns
	}
	if d.Intent == IntentCode || d.Intent == IntentTestCode {
		return CollCode
	}
	return CollDocumentation
}

// collectionFor is d's collection in index.
func collectionFor(index string, d Document) string {
	if index == IndexMerged {
		return mergedCollection(d)
	}
	return currentCollection(d.Intent)
}

// indexFields are the filter fields d is stored with in index: its own,
// plus in the merged index the category and, for code, the code kind.
func indexFields(index string, d Document) map[string][]string {
	out := maps.Clone(d.Fields)
	if out == nil {
		out = map[string][]string{}
	}
	if index != IndexMerged {
		return out
	}
	out[FieldCategory] = []string{d.Intent}
	if mergedCollection(d) == CollCode && len(out[FieldCodeKind]) == 0 {
		// As a sync derives it (sources.AddSourceFilters), from the path.
		kind := models.CodeKindProduction
		for _, p := range d.Fields["path"] {
			if models.IsTestPath(p) {
				kind = models.CodeKindTest
			}
		}
		out[FieldCodeKind] = []string{kind}
	}
	return out
}

// Target is one collection a layout searches for a question, with filters.
type Target struct {
	Index      string
	Collection string
	Filters    map[string][]string
}

// Layout is one way of answering a question: which index, which collections
// and which filters. Retrieval-level runs use Targets as an ideal agent
// would; agent-level runs give the model the layout's tools instead.
type Layout struct {
	Name        string
	Description string
	Index       string
	Targets     func(c *Company, q Query) []Target
}

// Layouts are the compared layouts, baseline first.
var Layouts = []Layout{
	{
		Name:        "current",
		Description: "Today: one collection per source type; the agent narrows by picking the type's search tool.",
		Index:       IndexCurrent,
		Targets: func(_ *Company, q Query) []Target {
			return []Target{{Index: IndexCurrent, Collection: currentCollection(q.Intent)}}
		},
	},
	{
		Name:        "merged_nofilter",
		Description: "Merged collections searched without narrowing: the cost of merging when the agent doesn't filter.",
		Index:       IndexMerged,
		Targets: func(_ *Company, q Query) []Target {
			return unfilteredMergedTargets(q.Intent, nil)
		},
	},
	{
		Name:        "merged_fields",
		Description: "Merged collections narrowed by the company's own fields (work item types, tags, paths, code_kind), as an agent that found them through facets would.",
		Index:       IndexMerged,
		Targets: func(c *Company, q Query) []Target {
			scopes := c.Scopes[q.Intent]
			if len(scopes) == 0 {
				return unfilteredMergedTargets(q.Intent, nil)
			}
			out := make([]Target, len(scopes))
			for i, s := range scopes {
				out[i] = Target{Index: IndexMerged, Collection: s.Collection, Filters: s.Filters}
			}
			return out
		},
	},
	{
		Name:        "merged_category",
		Description: "Merged collections narrowed by a per-source category label (the intent).",
		Index:       IndexMerged,
		Targets: func(_ *Company, q Query) []Target {
			return unfilteredMergedTargets(q.Intent, map[string][]string{FieldCategory: {q.Intent}})
		},
	},
}

// LayoutByName returns the named layout.
func LayoutByName(name string) (Layout, bool) {
	i := slices.IndexFunc(Layouts, func(l Layout) bool { return l.Name == name })
	if i < 0 {
		return Layout{}, false
	}
	return Layouts[i], true
}

// unfilteredMergedTargets are the merged collections a document of intent
// can live in, each with filters.
func unfilteredMergedTargets(intent string, filters map[string][]string) []Target {
	var colls []string
	switch intent {
	case IntentRequirement, IntentTestCase:
		colls = []string{CollWorkItems, CollDocumentation} // either a work item or a file
	case IntentWorkItem:
		colls = []string{CollWorkItems}
	case IntentCode, IntentTestCode:
		colls = []string{CollCode}
	case IntentCommit:
		colls = []string{CollCommits}
	case IntentPipelineRun:
		colls = []string{CollPipelineRuns}
	default:
		colls = []string{CollDocumentation}
	}
	out := make([]Target, len(colls))
	for i, c := range colls {
		out[i] = Target{Index: IndexMerged, Collection: c, Filters: filters}
	}
	return out
}
