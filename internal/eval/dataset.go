// Package eval measures how accurately an agent finds the right documents
// under different ways of organizing sources into collections ("layouts"),
// so a change such as merging source types can be judged by numbers rather
// than intuition.
//
// A dataset holds one or more companies. Each company is a self-contained
// knowledge base (its own collections, as in its own Conduit deployment):
// documents tagged with their intent (what kind of thing they are) and shape
// (where they live), plus labelled questions naming the documents that answer
// them. The same documents are indexed once per physical layout and every
// question is run against every layout.
package eval

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// Intents are the kinds of documents the evaluated layouts must keep apart.
// They mirror today's source types; commits and pipeline runs are unaffected
// by the layouts under test but may appear in an exported dataset.
const (
	IntentRequirement   = "requirement"
	IntentTestCase      = "test_case"
	IntentWorkItem      = "work_item"
	IntentCode          = "code"
	IntentTestCode      = "test_code"
	IntentDocumentation = "documentation"
	IntentCommit        = "commit"
	IntentPipelineRun   = "pipeline_run"
)

var intents = []string{
	IntentRequirement, IntentTestCase, IntentWorkItem, IntentCode,
	IntentTestCode, IntentDocumentation, IntentCommit, IntentPipelineRun,
}

// Shapes say where a document lives, which decides its collection in a
// merged layout and which filters it can carry.
const (
	ShapeWorkItem    = "work_item"    // ADO work item or GitHub issue
	ShapeFile        = "file"         // repository file or wiki page
	ShapeCommit      = "commit"       // commit with its diff
	ShapePipelineRun = "pipeline_run" // pipeline run, step or test
)

var shapes = []string{ShapeWorkItem, ShapeFile, ShapeCommit, ShapePipelineRun}

// Dataset is an eval corpus: one or more companies.
type Dataset struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Companies   []Company `json:"companies"`
}

// Company is one self-contained knowledge base.
type Company struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Scopes says, per intent, how an agent that knows this company's
	// vocabulary narrows a merged layout to that intent: which merged
	// collections to search and with which filters. It is the "ideal agent"
	// of the merged_fields layout; an intent without a scope falls back to
	// searching its collections unfiltered.
	Scopes    map[string][]Scope `json:"scopes,omitempty"`
	Documents []Document         `json:"documents"`
	Queries   []Query            `json:"queries"`
}

// Scope is one merged collection to search, with filters.
type Scope struct {
	Collection string              `json:"collection"` // a Merged* collection kind
	Filters    map[string][]string `json:"filters,omitempty"`
}

// Document is one indexed item.
type Document struct {
	ID     string `json:"id"`
	Intent string `json:"intent"`
	Shape  string `json:"shape"`
	Title  string `json:"title"`
	// Text is the full text as Conduit would index it (a work item's fields
	// and description, a file's path and content, …).
	Text string `json:"text"`
	// Fields are the document's filter fields as the source would write them
	// (project, repository, path, work_item_type, state, tags, …). The layout
	// adds its own derived fields (category, code_kind) at index time.
	Fields map[string][]string `json:"fields,omitempty"`
}

// Query is one labelled question.
type Query struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Intent string `json:"intent"` // the kind of document the asker wants
	// Relevant are the IDs of the documents that answer the question.
	Relevant []string `json:"relevant"`
	Note     string   `json:"note,omitempty"`
}

//go:embed datasets/synthetic.json
var syntheticJSON []byte

// BuiltinSynthetic is the name that selects the committed synthetic dataset.
const BuiltinSynthetic = "synthetic"

// LoadDataset reads the dataset at path, or the committed synthetic one for
// BuiltinSynthetic, and validates it. A queries file, when given, adds
// labelled questions to the dataset's companies (matched by company ID), so
// private questions can be kept apart from an exported corpus.
func LoadDataset(path, queriesPath string) (*Dataset, error) {
	var data []byte
	if path == BuiltinSynthetic {
		data = syntheticJSON
	} else {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var ds Dataset
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, fmt.Errorf("dataset %s: %w", path, err)
	}
	if queriesPath != "" {
		if err := ds.addQueries(queriesPath); err != nil {
			return nil, err
		}
	}
	if err := ds.Validate(); err != nil {
		return nil, fmt.Errorf("dataset %s: %w", path, err)
	}
	return &ds, nil
}

// QueriesFile is the format of a separate questions file: questions per
// company ID, plus optional scopes that extend or replace the dataset's.
type QueriesFile struct {
	Companies []struct {
		ID      string             `json:"id"`
		Scopes  map[string][]Scope `json:"scopes,omitempty"`
		Queries []Query            `json:"queries"`
	} `json:"companies"`
}

func (ds *Dataset) addQueries(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var qf QueriesFile
	if err := json.Unmarshal(data, &qf); err != nil {
		return fmt.Errorf("queries %s: %w", path, err)
	}
	for _, qc := range qf.Companies {
		i := slices.IndexFunc(ds.Companies, func(c Company) bool { return c.ID == qc.ID })
		if i < 0 {
			return fmt.Errorf("queries %s: no company %q in the dataset", path, qc.ID)
		}
		c := &ds.Companies[i]
		c.Queries = append(c.Queries, qc.Queries...)
		for intent, scopes := range qc.Scopes {
			if c.Scopes == nil {
				c.Scopes = map[string][]Scope{}
			}
			c.Scopes[intent] = scopes
		}
	}
	return nil
}

// Validate reports the first structural problem: unknown intents or shapes,
// duplicate IDs, questions naming documents that don't exist.
func (ds *Dataset) Validate() error {
	if len(ds.Companies) == 0 {
		return fmt.Errorf("no companies")
	}
	seenCompany := map[string]bool{}
	for _, c := range ds.Companies {
		if c.ID == "" || seenCompany[c.ID] {
			return fmt.Errorf("company ID %q is empty or repeated", c.ID)
		}
		seenCompany[c.ID] = true
		docs := map[string]Document{}
		for _, d := range c.Documents {
			if d.ID == "" || docs[d.ID].ID != "" {
				return fmt.Errorf("%s: document ID %q is empty or repeated", c.ID, d.ID)
			}
			if !slices.Contains(intents, d.Intent) {
				return fmt.Errorf("%s/%s: unknown intent %q", c.ID, d.ID, d.Intent)
			}
			if !slices.Contains(shapes, d.Shape) {
				return fmt.Errorf("%s/%s: unknown shape %q", c.ID, d.ID, d.Shape)
			}
			if strings.TrimSpace(d.Text) == "" {
				return fmt.Errorf("%s/%s: empty text", c.ID, d.ID)
			}
			docs[d.ID] = d
		}
		for intent, scopes := range c.Scopes {
			if !slices.Contains(intents, intent) {
				return fmt.Errorf("%s: scope for unknown intent %q", c.ID, intent)
			}
			for _, s := range scopes {
				if !slices.Contains(mergedCollections, s.Collection) {
					return fmt.Errorf("%s: scope for %s names unknown collection %q (one of %s)",
						c.ID, intent, s.Collection, strings.Join(mergedCollections, ", "))
				}
			}
		}
		seenQuery := map[string]bool{}
		for _, q := range c.Queries {
			if q.ID == "" || seenQuery[q.ID] {
				return fmt.Errorf("%s: query ID %q is empty or repeated", c.ID, q.ID)
			}
			seenQuery[q.ID] = true
			if !slices.Contains(intents, q.Intent) {
				return fmt.Errorf("%s/%s: unknown intent %q", c.ID, q.ID, q.Intent)
			}
			if strings.TrimSpace(q.Text) == "" || len(q.Relevant) == 0 {
				return fmt.Errorf("%s/%s: a query needs text and at least one relevant document", c.ID, q.ID)
			}
			for _, id := range q.Relevant {
				if docs[id].ID == "" {
					return fmt.Errorf("%s/%s: relevant document %q does not exist", c.ID, q.ID, id)
				}
			}
		}
	}
	return nil
}

// Filter keeps only the named companies and, when queryIDs is non-empty,
// only those questions. Empty arguments keep everything.
func (ds *Dataset) Filter(companies, queryIDs []string) {
	if len(companies) > 0 {
		ds.Companies = slices.DeleteFunc(ds.Companies, func(c Company) bool { return !slices.Contains(companies, c.ID) })
	}
	if len(queryIDs) > 0 {
		for i := range ds.Companies {
			c := &ds.Companies[i]
			c.Queries = slices.DeleteFunc(c.Queries, func(q Query) bool { return !slices.Contains(queryIDs, q.ID) })
		}
	}
}

// Summary describes the dataset's size, e.g. for the report header.
func (ds *Dataset) Summary() string {
	var docs, queries int
	byIntent := map[string]int{}
	for _, c := range ds.Companies {
		docs += len(c.Documents)
		queries += len(c.Queries)
		for _, q := range c.Queries {
			byIntent[q.Intent]++
		}
	}
	keys := make([]string, 0, len(byIntent))
	for k := range byIntent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, byIntent[k])
	}
	return fmt.Sprintf("%s: %d companies, %d documents, %d queries (%s)",
		ds.Name, len(ds.Companies), docs, queries, strings.Join(parts, ", "))
}
