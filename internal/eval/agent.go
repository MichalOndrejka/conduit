package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go/v2"

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

// agentTool is one search tool offered to the agent in a layout.
type agentTool struct {
	name        string
	description string
	collections []string // collections it searches (merged across them)
	filters     []string // filter fields it accepts
	facets      bool     // offers the facets mode
}

// agentToolsFor returns a layout's tools: today's six type-specific tools,
// or the merged layout's three shape tools with facets.
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
		extra := []string{}
		catNote := ""
		if layout == "merged_category" {
			extra = []string{FieldCategory}
			catNote = " Every item also has a category set by the team that configured its source (e.g. requirement, test_case, work_item, documentation); use facets to see the categories in use."
		}
		return []agentTool{
			{name: "search_workitem", collections: []string{CollWorkItems}, filters: append(slices.Clone(workItem), extra...), facets: true,
				description: "Semantic search over everything tracked in the work tracking system (Azure DevOps work items or GitHub issues): bugs, tasks, stories, requirements, test cases, whatever types the team uses. Work item types and tags are the team's own; use facets to see which exist." + catNote},
			{name: "search_code", collections: []string{CollCode}, filters: append(append(slices.Clone(repoFile), FieldCodeKind), extra...), facets: true,
				description: "Semantic search over source code files, production code and tests alike. Filter code_kind to \"test\" or \"production\" to get only one of them." + catNote},
			{name: "search_documentation", collections: []string{CollDocumentation}, filters: append(slices.Clone(repoFile), extra...), facets: true,
				description: "Semantic search over documents kept in repositories or wikis: guides, architecture notes, runbooks, and any requirements or test cases a team keeps as files. Folder layouts are the team's own; use facets on path to see them." + catNote},
		}, nil
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

// toolSchema is the input schema of an agent tool.
func toolSchema(t agentTool) anthropic.ToolInputSchemaParam {
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
	if t.facets {
		modes = append(modes, "facets")
		modeDesc += ` "facets": counts the distinct values of the given fields among the items matching filters (no query needed) - use it to learn the exact work item types, tags, folders or categories before filtering.`
	}
	props := map[string]any{
		"mode":    map[string]any{"type": "string", "enum": modes, "description": modeDesc},
		"query":   map[string]any{"type": "string", "description": "Natural-language search query (semantic_search)."},
		"page":    map[string]any{"type": "integer", "description": "Result page, starting at 1 (semantic_search)."},
		"filters": map[string]any{"type": "object", "properties": filterProps, "additionalProperties": false, "description": "Optional filters; different fields combine with AND."},
	}
	if t.facets {
		props["fields"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": t.filters}, "description": "Fields to count (facets)."}
	}
	return anthropic.ToolInputSchemaParam{Properties: props, Required: []string{"mode"}}
}

type toolInput struct {
	Mode    string         `json:"mode"`
	Query   string         `json:"query"`
	Page    int            `json:"page"`
	Filters map[string]any `json:"filters"`
	Fields  []string       `json:"fields"`
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

// runTool executes one tool call for company under the layout's index.
func (r *Runner) runTool(ctx context.Context, company, index string, t agentTool, raw json.RawMessage) (string, []string, error) {
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
	case "facets":
		if !t.facets {
			return "", nil, fmt.Errorf("this tool has no facets mode")
		}
		return r.facets(ctx, company, targets, in.Fields, t.filters)
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

// facets counts distinct values of fields across the targets' matching
// documents (each document once), most frequent first, at most 25 per field.
func (r *Runner) facets(ctx context.Context, company string, targets []Target, fields, allowed []string) (string, []string, error) {
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("facets needs fields")
	}
	for _, f := range fields {
		if !slices.Contains(allowed, f) {
			return "", nil, fmt.Errorf("unknown field %q; this tool supports: %s", f, strings.Join(allowed, ", "))
		}
	}
	counts := map[string]map[string]int{}
	seen := map[string]bool{}
	for _, t := range targets {
		points, err := r.Backend.Points(ctx, r.collection(company, t.Index, t.Collection), conditions(t.Filters))
		if err != nil {
			return "", nil, err
		}
		for _, p := range points {
			if seen[p.DocID] {
				continue
			}
			seen[p.DocID] = true
			for _, f := range fields {
				if counts[f] == nil {
					counts[f] = map[string]int{}
				}
				for _, v := range p.Fields[f] {
					counts[f][v]++
				}
			}
		}
	}
	type valueCount struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	out := map[string][]valueCount{}
	for _, f := range fields {
		var vcs []valueCount
		for v, n := range counts[f] {
			vcs = append(vcs, valueCount{v, n})
		}
		sort.Slice(vcs, func(i, j int) bool {
			if vcs[i].Count != vcs[j].Count {
				return vcs[i].Count > vcs[j].Count
			}
			return vcs[i].Value < vcs[j].Value
		})
		if len(vcs) > 25 {
			vcs = vcs[:25]
		}
		out[f] = vcs
	}
	data, _ := json.Marshal(map[string]any{"documents": len(seen), "facets": out})
	return string(data), nil, nil
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

// pointFields extracts a chunk's filter fields from its payload.
func pointFields(payload map[string]any) map[string][]string {
	out := map[string][]string{}
	for k, v := range payload {
		if !strings.HasPrefix(k, models.FilterPrefix) || k == models.FilterDateKey {
			continue
		}
		field := strings.TrimPrefix(k, models.FilterPrefix)
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					out[field] = append(out[field], s)
				}
			}
		case []string:
			out[field] = append(out[field], t...)
		case string:
			out[field] = append(out[field], t)
		}
	}
	return out
}
