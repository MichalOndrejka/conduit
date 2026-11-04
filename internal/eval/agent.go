package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/MichalOndrejka/conduit/internal/mcptools"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// AgentOptions configure an agent-level run.
type AgentOptions struct {
	Model        string // default DefaultAgentModel
	Effort       string // low | medium | high | xhigh | max; default medium
	MaxToolCalls int    // per question; default 8
	Concurrency  int    // questions in flight; default 4
}

// DefaultAgentModel is the model agent-level runs use unless told otherwise.
const DefaultAgentModel = "claude-opus-5-5"

// pageSize matches Conduit's search tools (rag.SearchService): five results
// per page.
const pageSize = 5

// AgentOutcome is what the agent did for one question.
type AgentOutcome struct {
	// Answer are the document IDs the agent named as answering the
	// question, in its order.
	Answer          []string `json:"answer"`
	AnswerRecall    float64  `json:"answer_recall"`
	AnswerPrecision float64  `json:"answer_precision"`
	// SeenRecall is the share of relevant documents that appeared in any
	// tool result, whether or not the agent picked them.
	SeenRecall float64       `json:"seen_recall"`
	ToolCalls  int           `json:"tool_calls"`
	Calls      []ToolCallLog `json:"calls"`
	StopReason string        `json:"stop_reason"`
	FinalText  string        `json:"final_text"`
	Usage      TokenUsage    `json:"usage"`
}

// ToolCallLog is one tool call and the documents it returned.
type ToolCallLog struct {
	Tool     string          `json:"tool"`
	Input    json.RawMessage `json:"input"`
	Returned []string        `json:"returned,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// TokenUsage sums a question's API usage.
type TokenUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// agentTool is one search tool offered to the agent in a layout. The
// baseline's tools (today's type-specific ones) are described here; the
// merged layouts offer the real MCP search tools (real), run through the
// same request handling as in production.
type agentTool struct {
	name        string
	description string
	collections []string // collections it searches (merged across them)
	filters     []string // filter fields it accepts (baseline tools)

	real   *mcptools.SearchToolSpec // merged layouts: the real tool
	kind   string                   // the eval collection kind real searches
	fields []mcptools.FilterField   // real's filters, plus any the layout adds
}

// realKinds maps the real search tools' collections to eval collection
// kinds; tools of collections the eval datasets don't cover are left out.
var realKinds = map[string]string{
	models.CollectionWorkItems:     CollWorkItems,
	models.CollectionCode:          CollCode,
	models.CollectionDocumentation: CollDocumentation,
}

// agentToolsFor returns a layout's tools: today's six type-specific tools,
// or the real shape tools (search_workitem, search_code,
// search_documentation) with their facets mode.
func agentToolsFor(layout string) ([]agentTool, error) {
	repoFile := []string{"project", "repository", "path", "extension"}
	workItem := []string{"project", "work_item_type", "state", "tags", "assigned_to", "iteration_path"}
	switch layout {
	case "current":
		return []agentTool{
			{name: "search_workitem", collections: []string{CollWorkItems}, filters: workItem,
				description: "Semantic search over work items (bugs, tasks, user stories, backlog items, issues)."},
			{name: "search_requirement", collections: []string{CollRequirements}, filters: append(slices.Clone(repoFile), workItem[1:]...),
				description: "Semantic search over requirements (features, user stories, epics, specifications), kept as work items or as repository files."},
			{name: "search_testcase", collections: []string{CollTestCases}, filters: append(slices.Clone(repoFile), workItem[1:]...),
				description: "Semantic search over test cases including test steps, kept as work items or as repository files."},
			{name: "search_source_code", collections: []string{CollCode}, filters: repoFile,
				description: "Semantic search over production source code (classes, methods, functions). Does not include test files - use search_test_code for tests."},
			{name: "search_test_code", collections: []string{CollTestCode}, filters: repoFile,
				description: "Semantic search over test code - unit tests, integration tests and specs."},
			{name: "search_documentation", collections: []string{CollDocumentation}, filters: repoFile,
				description: "Semantic search over wiki pages and repository documentation."},
		}, nil
	case "merged_fields", "merged_category":
		var tools []agentTool
		for _, spec := range mcptools.SearchTools() {
			kind, ok := realKinds[spec.Collection]
			if !ok {
				continue
			}
			t := agentTool{name: spec.Name, description: mcptools.SearchToolDescription(spec), collections: []string{kind},
				real: &spec, kind: kind, fields: slices.Clone(spec.Fields)}
			if layout == "merged_category" {
				t.fields = append(t.fields, mcptools.FilterField{Name: FieldCategory,
					Desc: "Category set by the team that configured the item's source, e.g. requirement, test_case, work_item, documentation"})
				t.description += " Every item also has a category set by the team that configured its source (e.g. requirement, " +
					"test_case, work_item, documentation); use facets to see the categories in use."
			}
			tools = append(tools, t)
		}
		return tools, nil
	}
	return nil, fmt.Errorf("layout %q has no agent tools (agent layouts: current, merged_fields, merged_category)", layout)
}

// AgentLayouts are the layouts an agent run can use.
var AgentLayouts = []string{"current", "merged_fields", "merged_category"}

const agentSystemPrompt = `You answer questions about a software company's engineering knowledge base using the search tools provided. The knowledge base is the company's real data: its work items or issues, source code, tests and documentation.

Find the specific items that answer the question. Search as often as you need; refine queries and filters when results are off-target. Results show each item's source_doc_id.

When you are done, give a one-sentence answer, then list the source_doc_id of every item that directly answers the question, each on its own line in the form:
ANSWER: <source_doc_id>
List only items that answer the question, most relevant first. If nothing answers it, write ANSWER: none.`

// toolSchema is the input schema of an agent tool: a real tool's MCP input
// schema, or the baseline's own.
func toolSchema(t agentTool) anthropic.ToolInputSchemaParam {
	if t.real != nil {
		return anthropic.ToolInputSchemaParam{
			Properties: map[string]any{"request": mcptools.RequestSchema(t.fields)},
			Required:   []string{"request"},
		}
	}
	filterProps := map[string]any{}
	for _, f := range t.filters {
		filterProps[f] = map[string]any{
			"description": "Match any of these values (case-insensitive). Hierarchical fields (path, iteration_path) match a folder and everything beneath it.",
			"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		}
	}
	modes := []string{"semantic_search"}
	modeDesc := `"semantic_search": ranked search for query, 5 results per page.`
	props := map[string]any{
		"mode":    map[string]any{"type": "string", "enum": modes, "description": modeDesc},
		"query":   map[string]any{"type": "string", "description": "Natural-language search query (semantic_search)."},
		"page":    map[string]any{"type": "integer", "description": "Result page, starting at 1 (semantic_search)."},
		"filters": map[string]any{"type": "object", "properties": filterProps, "additionalProperties": false, "description": "Optional filters; different fields combine with AND."},
	}
	return anthropic.ToolInputSchemaParam{Properties: props, Required: []string{"mode"}}
}

type toolInput struct {
	Mode    string         `json:"mode"`
	Query   string         `json:"query"`
	Page    int            `json:"page"`
	Filters map[string]any `json:"filters"`
}

// filterValues normalizes a tool's filters object to field → values,
// rejecting fields the tool doesn't offer.
func filterValues(raw map[string]any, allowed []string) (map[string][]string, error) {
	out := map[string][]string{}
	for k, v := range raw {
		if !slices.Contains(allowed, k) {
			return nil, fmt.Errorf("unknown filter %q; this tool supports: %s", k, strings.Join(allowed, ", "))
		}
		switch t := v.(type) {
		case string:
			out[k] = []string{t}
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					out[k] = append(out[k], s)
				}
			}
		default:
			return nil, fmt.Errorf("filter %q must be a string or an array of strings", k)
		}
	}
	return out, nil
}

// runTool executes one tool call for company under the layout's index,
// returning the tool's result text and the documents it returned.
func (r *Runner) runTool(ctx context.Context, company, index string, t agentTool, raw json.RawMessage) (string, []string, error) {
	if t.real != nil {
		return r.runRealTool(ctx, company, index, t, raw)
	}
	var in toolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", nil, fmt.Errorf("invalid input: %w", err)
	}
	filters, err := filterValues(in.Filters, t.filters)
	if err != nil {
		return "", nil, err
	}
	targets := make([]Target, len(t.collections))
	for i, c := range t.collections {
		targets[i] = Target{Index: index, Collection: c, Filters: filters}
	}
	switch in.Mode {
	case "semantic_search", "":
		if strings.TrimSpace(in.Query) == "" {
			return "", nil, fmt.Errorf("semantic_search needs a query")
		}
		page := max(in.Page, 1)
		docs, err := r.search(ctx, company, in.Query, targets, page*pageSize+1)
		if err != nil {
			return "", nil, err
		}
		start := min((page-1)*pageSize, len(docs))
		end := min(start+pageSize, len(docs))
		type item struct {
			SourceDocID string            `json:"source_doc_id"`
			Score       float64           `json:"score"`
			Properties  map[string]string `json:"properties"`
			Text        string            `json:"text"`
		}
		items := []item{}
		var ids []string
		for _, d := range docs[start:end] {
			props := map[string]string{}
			for k, v := range d.Properties {
				props[k] = v
			}
			items = append(items, item{SourceDocID: d.SourceDocID, Score: d.Score, Properties: props, Text: rag.Truncate(d.Text, 700)})
			ids = append(ids, d.SourceDocID)
		}
		out, _ := json.Marshal(map[string]any{"results": items, "page": page, "has_more": len(docs) > end})
		return string(out), ids, nil
	}
	return "", nil, fmt.Errorf("unknown mode %q", in.Mode)
}

// runRealTool runs a real search tool's request handling
// (mcptools.RunSearchTool) over the eval index, and returns its response as
// the MCP tool would.
func (r *Runner) runRealTool(ctx context.Context, company, index string, t agentTool, raw json.RawMessage) (string, []string, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", nil, fmt.Errorf("invalid input: %w", err)
	}
	payload, err := mcptools.RunSearchTool(ctx, evalSearcher{r: r, company: company, index: index}, t.kind, t.fields, args)
	if err != nil {
		return "", nil, err
	}
	var ids []string
	if results, ok := payload["results"].([]models.SearchResult); ok {
		for _, res := range results {
			ids = append(ids, res.SourceDocID)
		}
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	return string(out), ids, nil
}

// evalSearcher is mcptools.Searcher over one company's eval index: a
// collection argument is an eval collection kind.
type evalSearcher struct {
	r       *Runner
	company string
	index   string
}

func (e evalSearcher) name(kind string) string { return e.r.collection(e.company, e.index, kind) }

// Search pages chunks like rag.SearchService: pageSize per page, ranked.
func (e evalSearcher) Search(ctx context.Context, kind, query string, page int, must []rag.FieldCondition) ([]models.SearchResult, bool, error) {
	vector, err := e.r.embed(ctx, query)
	if err != nil {
		return nil, false, fmt.Errorf("embedding query: %w", err)
	}
	page = max(page, 1)
	hits, err := e.r.Backend.Search(ctx, e.name(kind), vector, page*pageSize+1, must)
	if err != nil {
		return nil, false, err
	}
	start := min((page-1)*pageSize, len(hits))
	end := min(start+pageSize, len(hits))
	return hits[start:end], len(hits) > end, nil
}

func (e evalSearcher) GetChunk(context.Context, string, string, int) (models.SearchResult, bool, error) {
	return models.SearchResult{}, false, errors.New("retrieve_chunk is not available in the eval; eval documents are single chunks")
}

func (e evalSearcher) PatternSearch(context.Context, string, func(string) bool, int, []rag.FieldCondition) ([]models.SearchResult, bool, bool, error) {
	return nil, false, false, errors.New("pattern_search is not available in the eval; use semantic_search")
}

func (e evalSearcher) Facets(ctx context.Context, kind string, req rag.FacetRequest) (rag.FacetResult, error) {
	return rag.Facets(ctx, e.r.Backend, e.name(kind), req)
}

var answerLine = regexp.MustCompile(`(?m)^\s*ANSWER:\s*(\S+)`)

// RunAgent has Claude answer every question under each agent layout and
// scores the documents it names. Layouts run one after another so each one's
// identical tools and system prompt stay prompt-cached across its questions.
func (r *Runner) RunAgent(ctx context.Context, client anthropic.Client, layouts []string, opts AgentOptions) ([]QueryResult, error) {
	if opts.Model == "" {
		opts.Model = DefaultAgentModel
	}
	if opts.Effort == "" {
		opts.Effort = "medium"
	}
	if opts.MaxToolCalls <= 0 {
		opts.MaxToolCalls = 8
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	type job struct {
		company *Company
		query   Query
	}
	var jobs []job
	for ci := range r.Dataset.Companies {
		for _, q := range r.Dataset.Companies[ci].Queries {
			jobs = append(jobs, job{&r.Dataset.Companies[ci], q})
		}
	}
	var out []QueryResult
	for _, layoutName := range layouts {
		layout, ok := LayoutByName(layoutName)
		if !ok {
			return nil, fmt.Errorf("unknown layout %q", layoutName)
		}
		tools, err := agentToolsFor(layoutName)
		if err != nil {
			return nil, err
		}
		results := make([]QueryResult, len(jobs))
		var wg sync.WaitGroup
		sem := make(chan struct{}, opts.Concurrency)
		for i, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, j job) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = r.answer(ctx, client, layout, tools, j.company, j.query, opts)
			}(i, j)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.logf("agent: %s done (%d questions)", layoutName, len(jobs))
		out = append(out, results...)
	}
	return out, nil
}

// answer runs the tool loop for one question.
func (r *Runner) answer(ctx context.Context, client anthropic.Client, layout Layout, tools []agentTool, c *Company, q Query, opts AgentOptions) QueryResult {
	res := QueryResult{Layout: layout.Name, Company: c.ID, QueryID: q.ID, Intent: q.Intent, Query: q.Text, Relevant: q.Relevant}
	outcome := &AgentOutcome{}
	res.Agent = outcome

	params := make([]anthropic.ToolUnionParam, len(tools))
	byName := map[string]agentTool{}
	for i, t := range tools {
		tp := anthropic.ToolParam{Name: t.name, Description: anthropic.String(t.description), InputSchema: toolSchema(t)}
		params[i] = anthropic.ToolUnionParam{OfTool: &tp}
		byName[t.name] = t
	}
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(q.Text))}
	seen := map[string]bool{}

	// The tool budget is enforced by refusing further calls; a few extra
	// turns let the model answer after being told the budget is spent.
	for turn := 0; turn < opts.MaxToolCalls+3; turn++ {
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(opts.Model),
			MaxTokens: 16000,
			// Tools render before the system prompt, so caching the system
			// block caches both: identical for every question of a layout.
			System:       []anthropic.TextBlockParam{{Text: agentSystemPrompt, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
			Tools:        params,
			Messages:     messages,
			OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(opts.Effort)},
		})
		if err != nil {
			var apiErr *anthropic.Error
			if errors.As(err, &apiErr) {
				res.Error = fmt.Sprintf("API error %d: %s", apiErr.StatusCode, apiErr.Error())
			} else {
				res.Error = err.Error()
			}
			break
		}
		outcome.Usage.Input += resp.Usage.InputTokens
		outcome.Usage.Output += resp.Usage.OutputTokens
		outcome.Usage.CacheRead += resp.Usage.CacheReadInputTokens
		outcome.Usage.CacheWrite += resp.Usage.CacheCreationInputTokens
		outcome.StopReason = string(resp.StopReason)
		messages = append(messages, resp.ToParam())

		if resp.StopReason == anthropic.StopReasonRefusal {
			// No fallback model: an answer from a different model would
			// not measure the configured one.
			res.Error = "refused: " + string(resp.StopDetails.Category)
			break
		}
		if resp.StopReason != anthropic.StopReasonToolUse {
			var text strings.Builder
			for _, block := range resp.Content {
				if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
					text.WriteString(tb.Text)
				}
			}
			outcome.FinalText = text.String()
			if resp.StopReason == anthropic.StopReasonMaxTokens {
				res.Error = "hit max_tokens before answering"
			}
			break
		}

		var toolResults []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			input := json.RawMessage(tu.JSON.Input.Raw())
			logEntry := ToolCallLog{Tool: tu.Name, Input: input}
			var content string
			isError := false
			t, known := byName[tu.Name]
			switch {
			case outcome.ToolCalls >= opts.MaxToolCalls:
				content, isError = fmt.Sprintf("Tool budget of %d calls is used up. Answer now with what you have.", opts.MaxToolCalls), true
			case !known:
				content, isError = "unknown tool "+tu.Name, true
			default:
				outcome.ToolCalls++
				text, ids, err := r.runTool(ctx, c.ID, layout.Index, t, input)
				if err != nil {
					content, isError = err.Error(), true
				} else {
					content = text
					logEntry.Returned = ids
					for _, id := range ids {
						if !seen[id] {
							seen[id] = true
							res.Ranked = append(res.Ranked, id)
						}
					}
				}
			}
			if isError {
				logEntry.Error = content
			}
			outcome.Calls = append(outcome.Calls, logEntry)
			toolResults = append(toolResults, anthropic.NewToolResultBlock(tu.ID, content, isError))
		}
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}

	for _, m := range answerLine.FindAllStringSubmatch(outcome.FinalText, -1) {
		id := strings.Trim(m[1], "`*.,;")
		if id != "" && !strings.EqualFold(id, "none") && !slices.Contains(outcome.Answer, id) {
			outcome.Answer = append(outcome.Answer, id)
		}
	}
	// The ranking scored for an agent is its answer, in its order;
	// SeenRecall covers everything its searches returned.
	res.FirstRelevantRank, res.Scores = scoreRanking(outcome.Answer, q.Relevant, r.k())
	_, seenScores := scoreRanking(res.Ranked, q.Relevant, len(res.Ranked)+1)
	outcome.SeenRecall = seenScores.RecallAtK
	outcome.AnswerRecall = res.Scores.RecallAtK
	if len(outcome.Answer) > 0 {
		hits := 0
		for _, id := range outcome.Answer {
			if slices.Contains(q.Relevant, id) {
				hits++
			}
		}
		outcome.AnswerPrecision = float64(hits) / float64(len(outcome.Answer))
	}
	res.Ranked = outcome.Answer
	return res
}
