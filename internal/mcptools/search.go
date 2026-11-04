package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// Searcher is what the search tools run on: rag.SearchService in
// production, the accuracy eval's own index in eval runs — so the eval
// exercises exactly this request handling.
type Searcher interface {
	Search(ctx context.Context, collection, query string, page int, must []rag.FieldCondition) ([]models.SearchResult, bool, error)
	GetChunk(ctx context.Context, collection, sourceDocID string, chunkIndex int) (models.SearchResult, bool, error)
	PatternSearch(ctx context.Context, collection string, match func(string) bool, page int, must []rag.FieldCondition) ([]models.SearchResult, bool, bool, error)
	Facets(ctx context.Context, collection string, req rag.FacetRequest) (rag.FacetResult, error)
}

// SearchToolSpec is one search tool: its name, the collection it searches,
// what it covers and the filters it accepts.
type SearchToolSpec struct {
	Name        string
	Collection  string
	Description string
	Fields      []FilterField
}

// SearchTools are the search tools, one per collection. Collections are
// organized by shape; the agent narrows within one by the company's own
// values, which the facets mode lists.
func SearchTools() []SearchToolSpec {
	specs := []SearchToolSpec{
		{Name: "search_workitem", Collection: models.CollectionWorkItems, Description: "Semantic search over everything tracked as " +
			"work items or issues (Azure DevOps work items, GitHub issues): bugs, tasks, stories, features, and requirements or test " +
			"cases where a team keeps them as work items. Work item types, states, areas and tags are each team's own — filter " +
			"work_item_type to narrow to one kind, e.g. the team's requirement or test case type."},
		{Name: "search_code", Collection: models.CollectionCode, Description: "Semantic search over source code files, production " +
			`code and tests alike. Filter code_kind to "test" for tests (unit, integration, specs) or "production" for the ` +
			"implementation; leave it out to search both."},
		{Name: "search_documentation", Collection: models.CollectionDocumentation, Description: "Semantic search over documents " +
			"kept in repositories and wikis: guides, architecture notes, runbooks, and requirements, specifications or test cases " +
			"a team keeps as files. Folder layouts are each team's own — filter path to a folder to narrow to one kind of document."},
		{Name: "search_commit", Collection: models.CollectionCommits, Description: "Semantic search over git commit history — each " +
			"commit's message, author and complete code diff. Filter by project, repository, author, changed path or author date."},
		{Name: "search_pipeline_run", Collection: models.CollectionPipelineRuns, Description: "Semantic search over completed build " +
			"pipeline runs. Every run has a summary document (result, reason, branch, triggering commit, requester, timing). Failed " +
			"and partially succeeded runs also list their timeline errors/warnings and have separate documents for each failed step " +
			"(its complete log) and each failed test (error message and stack trace); properties.type is run, step or test, and " +
			"step/test documents carry properties.parent_doc_id, the source_doc_id of their run document. Filter by project, " +
			"repository, run result or branch."},
	}
	for i := range specs {
		specs[i].Fields = collectionFilters[specs[i].Collection]
	}
	return specs
}

// requestModeNote follows every search tool's own description.
const requestModeNote = ` Pass request={"mode":"semantic_search","query":...,"page":1} for a ranked search — each ` +
	`call returns a page of the top matches; call again with a higher page number for the next batch. ` +
	`Each result includes chunk_index/total_chunks and has_previous/has_next; if a match looks cut off, pass ` +
	`request={"mode":"retrieve_chunk","source_doc_id":...,"chunk_index":...} (from the result, chunk_index ± 1) ` +
	`to fetch the exact adjacent chunk. For an exact/deterministic text or regex match — e.g. finding all usages ` +
	`of a method or identifier — pass request={"mode":"pattern_search","pattern":...,"page":1} ` +
	`(add "regex":true to treat pattern as a Go RE2 regular expression); page through further matches the ` +
	`same way as semantic_search. Both search modes accept an optional "filters" object to narrow results by ` +
	`source-system fields — e.g. "filters":{"project":"Payments","repository":"api"} — see the schema for ` +
	`the fields this tool supports. Filter values are each team's own, so when unsure which values a field takes ` +
	`here, first call request={"mode":"facets","fields":["work_item_type"]} (any filter fields): it lists each ` +
	`value with its number of documents, within an optional "filters" object; for a path-like field add ` +
	`"prefix":{"path":"docs"} to list what is inside one folder. A filtered search that finds nothing also ` +
	`returns these counts for the filters it used, so a wrong value can be corrected at once. When a result has ` +
	`properties.web_url, it is the browser link to the source item (work item, file, commit, wiki page, pipeline ` +
	`run) — cite it as a markdown link, e.g. [title](web_url), whenever you reference that result in an answer.`

// SearchToolDescription is a tool's full description as agents see it.
func SearchToolDescription(spec SearchToolSpec) string { return spec.Description + requestModeNote }

func registerSearchTool(s *server.MCPServer, search Searcher, spec SearchToolSpec) {
	tool := mcp.NewTool(spec.Name,
		mcp.WithDescription(SearchToolDescription(spec)),
		withRequestParam(spec.Fields),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		payload, query, err := runSearch(ctx, search, spec.Collection, spec.Fields, req.GetArguments())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		recordSearch(ctx, spec.Collection, req.GetArguments(), query, payload)
		data, err := json.Marshal(payload)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	})
}

// RunSearchTool runs one call of a search tool over collection, with args as
// the tool receives them ({"request": {...}}), returning the response
// payload. An error is what the tool reports as its error result.
func RunSearchTool(ctx context.Context, search Searcher, collection string, fields []FilterField, args map[string]any) (map[string]any, error) {
	payload, _, err := runSearch(ctx, search, collection, fields, args)
	return payload, err
}

func runSearch(ctx context.Context, search Searcher, collection string, fields []FilterField, args map[string]any) (map[string]any, chunkQuery, error) {
	query, err := parseChunkQuery(args, fields)
	if err != nil {
		return nil, nil, err
	}
	payload, err := query.resolve(ctx, search, collection)
	if err != nil {
		return nil, nil, err
	}
	return payload, query, nil
}

// chunkQuery is one mode of the "request" oneOf parameter every search tool
// accepts. resolve runs the query and builds the full JSON-able response
// payload itself, so the tool handler never branches on which mode was
// requested — that's decided once, when parseChunkQuery picks the concrete
// type.
type chunkQuery interface {
	resolve(ctx context.Context, search Searcher, collection string) (map[string]any, error)
}

// emptyFilteredNote explains the facets attached to a filtered search that
// found nothing.
const emptyFilteredNote = "Nothing matched these filters. facets lists, for each filter used, the values that exist " +
	"among the documents matching the other filters, with document counts — retry with one of them, or drop the filter."

// semanticSearchRequest is request.mode == "semantic_search": today's ranked
// vector search, returning up to a page's worth of the most relevant matches
// per call (see pageSize in internal/rag/search.go).
type semanticSearchRequest struct {
	Query   string
	Page    int
	Filters []rag.FieldCondition
}

func (q semanticSearchRequest) resolve(ctx context.Context, search Searcher, collection string) (map[string]any, error) {
	results, hasMore, err := search.Search(ctx, collection, q.Query, q.Page, q.Filters)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"results": results, "page": q.Page, "has_more": hasMore}
	if len(results) == 0 {
		payload["results"] = []any{}
		switch {
		case q.Page > 1:
			payload["note"] = fmt.Sprintf("No further matches beyond page %d — this was the last page.", q.Page-1)
		case !attachFilterFacets(ctx, search, collection, q.Filters, payload):
			payload["note"] = "No data embedded for this query — the source may not be synced yet, or nothing matched."
		}
	}
	return payload, nil
}

// retrieveChunkRequest is request.mode == "retrieve_chunk": a deterministic
// fetch of one exact chunk by source_doc_id + chunk_index — no embedding
// call, so it can reliably walk to the chunk before/after a search hit that
// got cut off.
type retrieveChunkRequest struct {
	SourceDocID string
	ChunkIndex  int
}

func (q retrieveChunkRequest) resolve(ctx context.Context, search Searcher, collection string) (map[string]any, error) {
	result, found, err := search.GetChunk(ctx, collection, q.SourceDocID, q.ChunkIndex)
	if err != nil {
		return nil, err
	}
	if !found {
		return map[string]any{
			"results": []any{},
			"note": fmt.Sprintf(
				"No chunk at index %d for source_doc_id %q — check has_previous/has_next on the original result, "+
					"or this document may not be indexed under this collection.",
				q.ChunkIndex, q.SourceDocID),
		}, nil
	}
	return map[string]any{"results": []models.SearchResult{result}}, nil
}

// patternSearchRequest is request.mode == "pattern_search": a deterministic
// literal-substring or regex scan of chunk text — no embedding call, for
// exact matches semantic search can't guarantee (e.g. finding every usage of
// a method or identifier). Returns up to a page's worth of matches per call,
// same as semantic_search. Matcher is built once by parseChunkQuery so
// resolve doesn't need to know whether Pattern is literal or regex.
type patternSearchRequest struct {
	Pattern string
	Filters []rag.FieldCondition
	Page    int
	Matcher func(string) bool
}

func (q patternSearchRequest) resolve(ctx context.Context, search Searcher, collection string) (map[string]any, error) {
	results, hasMore, truncated, err := search.PatternSearch(ctx, collection, q.Matcher, q.Page, q.Filters)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"results": results, "page": q.Page, "has_more": hasMore}
	if len(results) == 0 {
		payload["results"] = []any{}
		switch {
		case truncated:
			payload["note"] = fmt.Sprintf(
				"No match confirmed for page %d within the first several thousand chunks scanned — the collection "+
					"may be larger than this tool scans in one call. Try narrowing the pattern.", q.Page)
		case q.Page > 1:
			payload["note"] = fmt.Sprintf("No further matches beyond page %d — this was the last page.", q.Page-1)
		case !attachFilterFacets(ctx, search, collection, q.Filters, payload):
			payload["note"] = "No matches found for this pattern."
		}
	}
	return payload, nil
}

// facetsRequest is request.mode == "facets": the values of Fields among the
// documents matching Filters, each with its document count — for finding the
// team's own work item types, folders, tags, code kinds or sources before
// filtering by them.
type facetsRequest struct {
	Fields  []rag.FacetField
	Filters []rag.FieldCondition
}

func (q facetsRequest) resolve(ctx context.Context, search Searcher, collection string) (map[string]any, error) {
	res, err := search.Facets(ctx, collection, rag.FacetRequest{Fields: q.Fields, Must: q.Filters})
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"documents": res.Documents, "facets": res.Facets}
	if res.Documents == 0 {
		if len(q.Filters) > 0 {
			payload["note"] = "No documents match these filters."
		} else {
			payload["note"] = "Nothing is embedded here yet — the source may not be synced."
		}
	}
	return payload, nil
}

// attachFilterFacets adds to payload, for each filter field of a search that
// found nothing, the values that exist among the documents matching the
// other filters, so the agent can correct a wrong value in one step. A
// hierarchical value is counted among its siblings (the children of its
// parent folder). Reports whether there were filters to explain.
func attachFilterFacets(ctx context.Context, search Searcher, collection string, conds []rag.FieldCondition, payload map[string]any) bool {
	hints := map[string]rag.FieldFacets{}
	for i, c := range conds {
		if !strings.HasPrefix(c.Key, models.FilterPrefix) || c.Key == models.FilterDateKey {
			continue
		}
		field := strings.TrimPrefix(c.Key, models.FilterPrefix)
		others := slices.Delete(slices.Clone(conds), i, i+1)
		ff := rag.FacetField{Name: field}
		if models.IsHierarchical(field) {
			v, _ := c.Match.Value.(string)
			if v == "" && len(c.Match.Any) > 0 {
				v = c.Match.Any[0]
			}
			if sep := models.FilterSeparator(field); strings.Contains(v, sep) {
				ff.Prefix = v[:strings.LastIndex(v, sep)]
			}
		}
		res, err := search.Facets(ctx, collection, rag.FacetRequest{Fields: []rag.FacetField{ff}, Must: others})
		if err != nil {
			log.Printf("warning: facets for an empty %s search: %v", collection, err)
			continue
		}
		f := res.Facets[field]
		docs := res.Documents
		f.Documents = &docs
		hints[field] = f
	}
	if len(hints) == 0 {
		return false
	}
	payload["facets"] = hints
	payload["note"] = emptyFilteredNote
	return true
}

// parseChunkQuery decodes the required "request" argument into the concrete
// chunkQuery its "mode" field selects; fields are the filters the calling
// tool's collection supports.
func parseChunkQuery(args map[string]any, fields []FilterField) (chunkQuery, error) {
	raw, ok := args["request"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`missing required "request" object argument`)
	}
	mode, _ := raw["mode"].(string)
	switch mode {
	case "semantic_search":
		query, ok := raw["query"].(string)
		if !ok || query == "" {
			return nil, fmt.Errorf(`request.query is required when mode is "semantic_search"`)
		}
		pageF, ok := raw["page"].(float64)
		if !ok {
			return nil, fmt.Errorf(`request.page is required when mode is "semantic_search"`)
		}
		filters, err := parseFilters(raw["filters"], fields)
		if err != nil {
			return nil, err
		}
		return semanticSearchRequest{Query: query, Page: max(int(pageF), 1), Filters: filters}, nil
	case "retrieve_chunk":
		docID, ok := raw["source_doc_id"].(string)
		if !ok || docID == "" {
			return nil, fmt.Errorf(`request.source_doc_id is required when mode is "retrieve_chunk"`)
		}
		idxF, ok := raw["chunk_index"].(float64)
		if !ok {
			return nil, fmt.Errorf(`request.chunk_index is required when mode is "retrieve_chunk"`)
		}
		return retrieveChunkRequest{SourceDocID: docID, ChunkIndex: int(idxF)}, nil
	case "pattern_search":
		pattern, ok := raw["pattern"].(string)
		if !ok || pattern == "" {
			return nil, fmt.Errorf(`request.pattern is required when mode is "pattern_search"`)
		}
		pageF, ok := raw["page"].(float64)
		if !ok {
			return nil, fmt.Errorf(`request.page is required when mode is "pattern_search"`)
		}
		filters, err := parseFilters(raw["filters"], fields)
		if err != nil {
			return nil, err
		}
		useRegex, _ := raw["regex"].(bool)
		var matcher func(string) bool
		if useRegex {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("request.pattern is not a valid regular expression: %w", err)
			}
			matcher = re.MatchString
		} else {
			matcher = func(text string) bool { return strings.Contains(text, pattern) }
		}
		return patternSearchRequest{Pattern: pattern, Filters: filters, Page: max(int(pageF), 1), Matcher: matcher}, nil
	case "facets":
		return parseFacetsRequest(raw, fields)
	default:
		return nil, fmt.Errorf(`request.mode must be "semantic_search", "retrieve_chunk", "pattern_search" or "facets", got %q`, mode)
	}
}

// parseFacetsRequest decodes a facets request: fields to count (any of the
// tool's non-date filter fields), optional filters, and for path-like fields
// an optional prefix and level, each an object keyed by field — or a plain
// value when exactly one path-like field is counted.
func parseFacetsRequest(raw map[string]any, fields []FilterField) (chunkQuery, error) {
	names, err := filterStrings(raw["fields"])
	if err != nil || len(names) == 0 {
		return nil, fmt.Errorf(`request.fields is required when mode is "facets": the filter fields to count, e.g. ["work_item_type"]`)
	}
	countable := map[string]bool{}
	var hierarchical []string
	for _, f := range fields {
		if !f.Date {
			countable[f.Name] = true
		}
	}
	var out []rag.FacetField
	for _, n := range names {
		if !countable[n] {
			return nil, fmt.Errorf("unknown facets field %q — this tool counts: %s", n, facetFieldNames(fields))
		}
		if slices.ContainsFunc(out, func(f rag.FacetField) bool { return f.Name == n }) {
			continue
		}
		if models.IsHierarchical(n) {
			hierarchical = append(hierarchical, n)
		}
		out = append(out, rag.FacetField{Name: n})
	}
	perField := func(key string) (map[string]any, error) {
		v, ok := raw[key]
		if !ok || v == nil {
			return nil, nil
		}
		if m, ok := v.(map[string]any); ok {
			return m, nil
		}
		if len(hierarchical) == 1 {
			return map[string]any{hierarchical[0]: v}, nil
		}
		return nil, fmt.Errorf(`request.%s must be an object keyed by field, e.g. {"path": ...}`, key)
	}
	prefixes, err := perField("prefix")
	if err != nil {
		return nil, err
	}
	levels, err := perField("level")
	if err != nil {
		return nil, err
	}
	for i := range out {
		f := &out[i]
		if v, ok := prefixes[f.Name]; ok {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("request.prefix.%s must be a string", f.Name)
			}
			f.Prefix = models.NormalizeFilterValue(f.Name, s)
		}
		if v, ok := levels[f.Name]; ok {
			n, ok := v.(float64)
			if !ok || n < 1 || n != float64(int(n)) {
				return nil, fmt.Errorf("request.level.%s must be a whole number from 1", f.Name)
			}
			f.Level = int(n)
		}
	}
	for k := range prefixes {
		if !slices.Contains(hierarchical, k) {
			return nil, fmt.Errorf("request.prefix.%s: prefix applies to a counted path-like field (%s)", k, strings.Join(hierarchical, ", "))
		}
	}
	for k := range levels {
		if !slices.Contains(hierarchical, k) {
			return nil, fmt.Errorf("request.level.%s: level applies to a counted path-like field (%s)", k, strings.Join(hierarchical, ", "))
		}
	}
	filters, err := parseFilters(raw["filters"], fields)
	if err != nil {
		return nil, err
	}
	return facetsRequest{Fields: out, Filters: filters}, nil
}

func facetFieldNames(fields []FilterField) string {
	var names []string
	for _, f := range fields {
		if !f.Date {
			names = append(names, f.Name)
		}
	}
	return strings.Join(names, ", ")
}

// RequestSchema is the JSON Schema of a search tool's "request" argument: a
// discriminated union (oneOf) of the four modes, keyed by "mode"; fields
// are the filters the tool's collection supports.
func RequestSchema(fields []FilterField) map[string]any {
	var countable, hierarchical []string
	for _, f := range fields {
		if f.Date {
			continue
		}
		countable = append(countable, f.Name)
		if models.IsHierarchical(f.Name) {
			hierarchical = append(hierarchical, f.Name)
		}
	}
	facetProps := map[string]any{
		"mode": map[string]any{"const": "facets"},
		"fields": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": countable},
			"description": "Filter fields to list the values of, each value with its number of documents (at most " +
				fmt.Sprint(rag.FacetLimit) + " per field, most frequent first; more:true when there are further values)"},
		"filters": filtersSchema(fields),
	}
	if len(hierarchical) > 0 {
		pathProps := func(typ, desc string) map[string]any {
			props := map[string]any{}
			for _, h := range hierarchical {
				props[h] = map[string]any{"type": typ}
			}
			return map[string]any{"type": "object", "properties": props, "additionalProperties": false, "description": desc}
		}
		facetProps["prefix"] = pathProps("string", "Path-like fields: list only what is inside this folder, e.g. {\"path\":\"docs\"}")
		facetProps["level"] = pathProps("integer", "Path-like fields: the depth to list — 1 is the top-level folders. "+
			"Defaults to the level just inside prefix (1 without one).")
	}
	return map[string]any{
		"description": `A ranked semantic search, an exact text/regex search, a deterministic fetch of one exact chunk by ID, ` +
			`or the values of filter fields with their document counts (facets).`,
		"oneOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":  map[string]any{"const": "semantic_search"},
					"query": map[string]any{"type": "string", "description": "Natural-language search query"},
					"page": map[string]any{"type": "number", "description": "Which page of ranked results to return, starting at 1 (the most relevant matches). " +
						"Call again with a higher page number to see the next batch of matches if these aren't sufficient. Start with 1."},
					"filters": filtersSchema(fields),
				},
				"required": []string{"mode", "query", "page"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":          map[string]any{"const": "retrieve_chunk"},
					"source_doc_id": map[string]any{"type": "string", "description": "The source_doc_id field from a previous result returned by this tool"},
					"chunk_index": map[string]any{"type": "number", "description": "The chunk_index to fetch. Use the previous result's " +
						"chunk_index - 1 (or + 1) to walk to the previous (or next) chunk when has_previous/has_next is true."},
				},
				"required": []string{"mode", "source_doc_id", "chunk_index"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":    map[string]any{"const": "pattern_search"},
					"pattern": map[string]any{"type": "string", "description": "Literal text to find, or a regular expression when regex is true"},
					"regex": map[string]any{"type": "boolean", "description": "If true, interpret pattern as a Go RE2 regular expression instead " +
						"of a literal substring. Defaults to false."},
					"filters": filtersSchema(fields),
					"page": map[string]any{"type": "number", "description": "Which page of matches to return, starting at 1 (the first matches found, in " +
						"index order — not ranked). Call again with a higher page number to see the next batch of matches if these aren't sufficient. Start with 1."},
				},
				"required": []string{"mode", "pattern", "page"},
			},
			map[string]any{
				"type":       "object",
				"properties": facetProps,
				"required":   []string{"mode", "fields"},
			},
		},
	}
}

func withRequestParam(fields []FilterField) mcp.ToolOption {
	return func(t *mcp.Tool) {
		t.InputSchema.Properties["request"] = RequestSchema(fields)
		t.InputSchema.Required = append(t.InputSchema.Required, "request")
	}
}
