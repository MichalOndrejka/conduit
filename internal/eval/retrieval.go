package eval

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// Runner indexes a dataset into a backend and runs questions against it.
type Runner struct {
	Backend Backend
	Dataset *Dataset
	// Reuse skips indexing a collection that already exists with points,
	// so repeated runs (e.g. an agent run after a retrieval run) don't
	// re-embed the corpus. Leave false after changing the dataset.
	Reuse bool
	// K is the number of documents scored per question (recall@K, MRR@K).
	K int
	// Log receives progress lines; nil discards them.
	Log io.Writer

	mu      sync.Mutex
	vectors map[string][]float32 // query text → embedding
}

// chunksPerSearch is how many chunks one collection search fetches before
// they're grouped into documents: enough that K distinct documents survive
// even when long documents occupy several of the top chunks.
const chunksPerSearch = 50

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

func (r *Runner) k() int {
	if r.K <= 0 {
		return 10
	}
	return r.K
}

// collection is the backend collection of a company's index and kind.
func (r *Runner) collection(company, index, coll string) string {
	return collectionName(r.Dataset.Name, company, index, coll)
}

// Prepare indexes every company's documents into each named index.
func (r *Runner) Prepare(ctx context.Context, indexes []string) error {
	for _, c := range r.Dataset.Companies {
		for _, index := range indexes {
			byColl := map[string][]models.SourceDocument{}
			for _, d := range c.Documents {
				coll := collectionFor(index, d)
				byColl[coll] = append(byColl[coll], sourceDocument(index, c.ID, d))
			}
			colls := make([]string, 0, len(byColl))
			for coll := range byColl {
				colls = append(colls, coll)
			}
			sort.Strings(colls)
			for _, coll := range colls {
				name := r.collection(c.ID, index, coll)
				if r.Reuse && r.Backend.Exists(ctx, name) {
					r.logf("reusing %s", name)
					continue
				}
				if err := r.Backend.Reset(ctx, name); err != nil {
					return fmt.Errorf("resetting %s: %w", name, err)
				}
				r.logf("indexing %d documents into %s", len(byColl[coll]), name)
				if err := r.Backend.Index(ctx, name, byColl[coll]); err != nil {
					return fmt.Errorf("indexing %s: %w", name, err)
				}
			}
		}
	}
	return nil
}

// Cleanup deletes every collection Prepare may have created.
func (r *Runner) Cleanup(ctx context.Context) error {
	for _, c := range r.Dataset.Companies {
		for _, index := range []string{IndexCurrent, IndexMerged} {
			for _, coll := range append(slices.Clone(mergedCollections), CollRequirements, CollTestCases, CollTestCode) {
				if err := r.Backend.Reset(ctx, r.collection(c.ID, index, coll)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *Runner) embed(ctx context.Context, text string) ([]float32, error) {
	r.mu.Lock()
	v, ok := r.vectors[text]
	r.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := r.Backend.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.vectors == nil {
		r.vectors = map[string][]float32{}
	}
	r.vectors[text] = v
	r.mu.Unlock()
	return v, nil
}

// search runs query against targets and returns the ranked results grouped
// by document: each document once, at its best chunk's score, best first.
// Searching several collections merges their results by score, which is
// comparable because every collection uses the same embedding model.
func (r *Runner) search(ctx context.Context, company, query string, targets []Target, limit int) ([]models.SearchResult, error) {
	vector, err := r.embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}
	var all []models.SearchResult
	for _, t := range targets {
		hits, err := r.Backend.Search(ctx, r.collection(company, t.Index, t.Collection), vector, chunksPerSearch, conditions(t.Filters))
		if err != nil {
			return nil, err
		}
		all = append(all, hits...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	seen := map[string]bool{}
	var docs []models.SearchResult
	for _, h := range all {
		if h.SourceDocID == "" || seen[h.SourceDocID] {
			continue
		}
		seen[h.SourceDocID] = true
		docs = append(docs, h)
		if len(docs) == limit {
			break
		}
	}
	return docs, nil
}

// QueryResult is one question's outcome under one layout.
type QueryResult struct {
	Layout   string   `json:"layout"`
	Company  string   `json:"company"`
	QueryID  string   `json:"query_id"`
	Intent   string   `json:"intent"`
	Query    string   `json:"query"`
	Relevant []string `json:"relevant"`
	// Ranked are the retrieved document IDs, best first (at most K). For an
	// agent run: every document the agent saw, in the order it saw them.
	Ranked []string `json:"ranked"`
	// FirstRelevantRank is the 1-based rank of the first relevant document,
	// 0 when none was retrieved.
	FirstRelevantRank int    `json:"first_relevant_rank"`
	Scores            Scores `json:"scores"`
	// Agent-level only.
	Agent *AgentOutcome `json:"agent,omitempty"`
	Error string        `json:"error,omitempty"`
}

// RunRetrieval runs every question under every layout, with each layout's
// targets standing in for an agent that always picks the right tool and
// filters. It isolates what the organization of collections costs ranking.
func (r *Runner) RunRetrieval(ctx context.Context, layouts []Layout) ([]QueryResult, error) {
	var out []QueryResult
	for ci := range r.Dataset.Companies {
		c := &r.Dataset.Companies[ci]
		for _, q := range c.Queries {
			for _, l := range layouts {
				docs, err := r.search(ctx, c.ID, q.Text, l.Targets(c, q), r.k())
				res := QueryResult{Layout: l.Name, Company: c.ID, QueryID: q.ID, Intent: q.Intent, Query: q.Text, Relevant: q.Relevant}
				if err != nil {
					return nil, fmt.Errorf("%s/%s under %s: %w", c.ID, q.ID, l.Name, err)
				}
				for _, d := range docs {
					res.Ranked = append(res.Ranked, d.SourceDocID)
				}
				res.FirstRelevantRank, res.Scores = scoreRanking(res.Ranked, q.Relevant, r.k())
				out = append(out, res)
			}
		}
		r.logf("%s: %d questions × %d layouts done", c.ID, len(c.Queries), len(layouts))
	}
	return out, nil
}
