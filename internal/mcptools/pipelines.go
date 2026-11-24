package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// logPageLines is how many log lines one log page returns.
const logPageLines = 500

const (
	// stepsPageSize, testsPageSize and matchesPageSize page steps mode,
	// tests mode and log search matches.
	stepsPageSize   = 100
	testsPageSize   = 50
	matchesPageSize = 20
	// matchContext is how many lines around a log search match are shown.
	matchContext = 3
)

func pipelineRequestSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	sourceID := str("A configured pipeline-run source (ids from source_sync status mode). May be omitted when exactly " +
		"one pipeline-run source is enabled")
	runID := num("The run's id: Azure DevOps buildId, GitHub Actions run id")
	runURL := str("Instead of source_id + run_id: the run's web link, e.g. " +
		"https://dev.azure.com/org/Project/_build/results?buildId=1234 or https://github.com/owner/repo/actions/runs/567")
	variant := func(m string, props map[string]any, required ...string) map[string]any {
		props["mode"] = map[string]any{"const": m}
		return map[string]any{"type": "object", "properties": props, "required": append([]string{"mode"}, required...)}
	}
	return map[string]any{
		"description": "List a source's pipelines and their runs, then explore one run: its failures and context, all its steps, all its tests, and any step's log.",
		"oneOf": []any{
			variant("pipelines", map[string]any{"source_id": sourceID}),
			variant("list", map[string]any{
				"source_id": sourceID,
				"pipeline": str("Optional: only this pipeline's runs, when the source has several (IDs or a folder) — " +
					"its id, or its exact name (any case; GitHub also the workflow file, e.g. ci.yml), from pipelines mode " +
					"or a run's pipeline field. A name several pipelines share is an error that lists their ids"),
				"top": num(fmt.Sprintf("How many runs, newest first (default: the source's Runs setting, else %d; at most %d)",
					sources.DefaultPipelineRuns, sources.MaxPipelineRuns)),
				"result": str("Optional: Azure DevOps succeeded, partiallySucceeded, failed or canceled; GitHub success, " +
					"failure, cancelled, timed_out, …"),
				"status": str("Optional: Azure DevOps completed, inProgress, notStarted, cancelling; GitHub completed, " +
					"in_progress, queued, …"),
				"branch": str("Optional: branch name (main) or full ref (refs/heads/main, refs/pull/42/merge)"),
				"reason": str("Optional: what started the run — Azure DevOps manual, individualCI, batchedCI, schedule, " +
					"pullRequest, …; GitHub event push, pull_request, schedule, workflow_dispatch, …"),
				"requested_for": str("Optional: who the run was requested for (Azure DevOps) or triggered by (GitHub login)"),
			}),
			variant("get", map[string]any{
				"source_id": sourceID, "run_id": runID, "url": runURL,
				"page": num("Page of failed/flaky tests, 20 per page, starting at 1"),
			}),
			variant("steps", map[string]any{
				"source_id": sourceID, "run_id": runID, "url": runURL,
				"result": str("Optional: only steps with these results, comma-separated (Azure DevOps succeeded, failed, " +
					"skipped, canceled, succeededWithIssues; GitHub success, failure, skipped, …)"),
				"type": str("Optional: only stage, job or task (Azure DevOps); job or step (GitHub)"),
				"name": str("Optional: only steps whose Stage > Job > Task path contains this text (any case)"),
				"page": num(fmt.Sprintf("Page of %d steps, starting at 1", stepsPageSize)),
			}),
			variant("tests", map[string]any{
				"source_id": sourceID, "run_id": runID, "url": runURL,
				"outcome": str("Optional: only these outcomes, comma-separated (Passed, Failed, NotExecuted, Inconclusive, …)"),
				"name": str("Optional: only tests whose name or title contains this text (any case)"),
				"page": num(fmt.Sprintf("Page of %d tests, starting at 1", testsPageSize)),
			}),
			variant("log", map[string]any{
				"source_id": sourceID, "run_id": runID, "url": runURL,
				"log_id": num("Any step's log_id, from steps or get mode (GitHub: the job id)"),
				"search": str(fmt.Sprintf("Optional: return only the lines containing this text (any case), each with %d lines "+
					"around it and its page; page then pages the matches, %d at a time", matchContext, matchesPageSize)),
				"page": num(fmt.Sprintf("Page of %d log lines, starting at 1 (default: the last page, where the error usually is)",
					logPageLines)),
			}, "log_id"),
		},
	}
}

// RegisterPipelineTool registers the search_pipeline_run tool. Called once at
// startup, alongside RegisterTools.
func RegisterPipelineTool(s *server.MCPServer, srcs SourceLister, store secrets.Reader) {
	tool := mcp.NewTool("search_pipeline_run",
		mcp.WithDescription(`Read pipeline runs (Azure DevOps builds, GitHub Actions workflow runs) live from the `+
			`provider — anything about a run, not only its failures. Read-only; nothing is indexed, so every answer is current. `+
			`A source covers one or several pipelines (Azure DevOps by ids or a whole folder; GitHub workflows): `+
			`{"mode":"pipelines"} lists them with id, name, folder (or workflow file) and web_url. `+
			`{"mode":"list"} returns the latest runs of the source's pipelines merged, newest first (5 by default), each `+
			`naming its pipeline, with result, status, reason, branch, commit, requester, timing and web_url — narrow to `+
			`one pipeline with "pipeline" (id or exact name) and filter by result, status, branch, reason or requested_for `+
			`(e.g. last failed build of Deploy on main: {"mode":"list","pipeline":"Deploy","result":"failed","branch":"main","top":1}). `+
			`{"mode":"get","run_id":...} (or "url": a run link with buildId=… or …/actions/runs/…) summarizes one run: `+
			`timeline issues, every failed step of every attempt — so a run rerun to green still shows attempt 1's `+
			`failures — failed and flaky tests with error and stack trace (Azure DevOps; 20 per page), the commits it built, `+
			`its artifacts (metadata only) and its context: pipeline YAML file, parameters, queue-time and pipeline `+
			`variables (secrets masked) and linked work items (Azure DevOps). `+
			`{"mode":"steps"} lists every stage, job and task (GitHub job and step) of every attempt whatever its result, `+
			`with timing, error/warning counts and log_id — filter by result, type or name. `+
			`{"mode":"tests"} lists every test result, passed ones too (Azure DevOps) — filter by outcome or name. `+
			`{"mode":"log","log_id":...} reads ANY step's log (a succeeded one too), 500 lines per page, the last page by `+
			`default; add "search" to get just the matching lines with context and their page. `+
			`Completed runs are cached in memory, so asking again is cheap. Cite web_url links when referring to a run, `+
			`step or test.`),
		func(t *mcp.Tool) {
			t.InputSchema.Properties["request"] = pipelineRequestSchema()
			t.InputSchema.Required = append(t.InputSchema.Required, "request")
		},
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		payload, err := handlePipelineRequest(ctx, req.GetArguments(), srcs, store)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	})
}

func handlePipelineRequest(ctx context.Context, args map[string]any, srcs SourceLister, store secrets.Reader) (map[string]any, error) {
	req, ok := args["request"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`request is required, e.g. {"mode":"list"}`)
	}
	str := func(key string) string { s, _ := req[key].(string); return strings.TrimSpace(s) }
	num := func(key string) int64 { n, _ := req[key].(float64); return int64(n) }

	all, err := srcs.ListAll()
	if err != nil {
		return nil, fmt.Errorf("reading sources: %w", err)
	}

	mode := str("mode")
	switch mode {
	case "pipelines":
		src, err := pipelineSource(all, str("source_id"))
		if err != nil {
			return nil, err
		}
		recordSource(ctx, src)
		p, err := sources.PipelineProviderFor(src, store)
		if err != nil {
			return nil, err
		}
		pipelines, cached, err := p.Pipelines(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"source": sourceRef(src), "cached": cached, "pipelines": pipelines}, nil

	case "list":
		src, err := pipelineSource(all, str("source_id"))
		if err != nil {
			return nil, err
		}
		recordSource(ctx, src)
		p, err := sources.PipelineProviderFor(src, store)
		if err != nil {
			return nil, err
		}
		runs, err := p.List(ctx, sources.PipelineRunFilter{
			Top: int(num("top")), Result: str("result"), Status: str("status"), Branch: str("branch"),
			Reason: str("reason"), RequestedFor: str("requested_for"), Pipeline: str("pipeline"),
		})
		if err != nil {
			return nil, err
		}
		out := map[string]any{"source": sourceRef(src), "runs": runs}
		if len(runs) == 0 {
			out["note"] = "No run matched — check the filter values (Azure DevOps and GitHub name results and statuses differently)."
		}
		return out, nil

	case "get", "steps", "tests", "log":
	default:
		return nil, fmt.Errorf(`request.mode must be "pipelines", "list", "get", "steps", "tests" or "log", got %q`, mode)
	}

	var (
		p     *sources.PipelineProvider
		src   *models.SourceDefinition
		runID int64
	)
	if u := str("url"); u != "" {
		r, err := sources.ResolvePipelineRunURL(u, all, store)
		if err != nil {
			return nil, err
		}
		p, src, runID = r.Provider, r.Source, r.RunID
	} else {
		if runID = num("run_id"); runID <= 0 {
			return nil, fmt.Errorf("%s mode requires run_id (or url, the run's web link)", mode)
		}
		if src, err = pipelineSource(all, str("source_id")); err != nil {
			return nil, err
		}
		if p, err = sources.PipelineProviderFor(src, store); err != nil {
			return nil, err
		}
	}
	recordSource(ctx, src)

	if mode == "get" {
		run, cached, err := p.Get(ctx, runID)
		if err != nil {
			return nil, err
		}
		page := max(int(num("page")), 1)
		shown := *run // the cached run is shared: page a copy
		tests, hasMore := sources.PageOf(run.Tests, page)
		// Attachments are read for the page's tests only.
		tests, notes, err := p.TestAttachments(ctx, runID, tests)
		if err != nil {
			return nil, err
		}
		shown.Tests = tests
		if len(notes) > 0 {
			shown.Notes = append(slices.Clone(run.Notes), notes...)
		}
		return map[string]any{
			"credentials_from": sourceRef(src), "cached": cached, "run": shown,
			"total_tests": len(run.Tests), "tests_page": page, "tests_has_more": hasMore,
		}, nil
	}

	switch mode {
	case "steps":
		steps, cached, err := p.Steps(ctx, runID)
		if err != nil {
			return nil, err
		}
		results, kind, name := csvSet(str("result")), strings.ToLower(str("type")), strings.ToLower(str("name"))
		var shown []sources.PipelineStepNode
		for _, st := range steps {
			if (len(results) == 0 || results[strings.ToLower(st.Result)]) && (kind == "" || st.Type == kind) &&
				(name == "" || strings.Contains(strings.ToLower(st.Path), name)) {
				shown = append(shown, st)
			}
		}
		page := max(int(num("page")), 1)
		paged, more := pageSlice(shown, page, stepsPageSize)
		return map[string]any{
			"credentials_from": sourceRef(src), "cached": cached, "run_id": runID,
			"total_steps": len(steps), "matched": len(shown), "page": page, "has_more": more, "steps": paged,
		}, nil
	case "tests":
		tests, cached, err := p.Tests(ctx, runID)
		if err != nil {
			return nil, err
		}
		outcomes, name := csvSet(str("outcome")), strings.ToLower(str("name"))
		var shown []sources.PipelineTest
		for _, t := range tests {
			if (len(outcomes) == 0 || outcomes[strings.ToLower(t.Outcome)]) &&
				(name == "" || strings.Contains(strings.ToLower(t.Name), name) || strings.Contains(strings.ToLower(t.Title), name)) {
				shown = append(shown, t)
			}
		}
		page := max(int(num("page")), 1)
		paged, more := pageSlice(shown, page, testsPageSize)
		return map[string]any{
			"credentials_from": sourceRef(src), "cached": cached, "run_id": runID,
			"total_tests": len(tests), "matched": len(shown), "page": page, "has_more": more, "tests": paged,
		}, nil
	}

	logID := num("log_id")
	if logID <= 0 {
		return nil, fmt.Errorf("log mode requires log_id, a step's log_id from steps or get mode")
	}
	text, cached, err := p.Log(ctx, runID, logID)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	pages := max((len(lines)+logPageLines-1)/logPageLines, 1)
	if q := str("search"); q != "" {
		ql := strings.ToLower(q)
		var matches []map[string]any
		for i, l := range lines {
			if strings.Contains(strings.ToLower(l), ql) {
				from, to := max(i-matchContext, 0), min(i+matchContext+1, len(lines))
				matches = append(matches, map[string]any{
					"line": i + 1, "page": i/logPageLines + 1, "text": strings.Join(lines[from:to], "\n"),
				})
			}
		}
		page := max(int(num("page")), 1)
		paged, more := pageSlice(matches, page, matchesPageSize)
		return map[string]any{
			"credentials_from": sourceRef(src), "cached": cached, "run_id": runID, "log_id": logID,
			"search": q, "total_matches": len(matches), "matches_page": page, "matches_has_more": more, "matches": paged,
			"total_lines": len(lines), "total_pages": pages,
		}, nil
	}
	page := int(num("page"))
	if page <= 0 {
		page = pages
	}
	if page > pages {
		return nil, fmt.Errorf("log %d has %d pages of %d lines; page %d doesn't exist", logID, pages, logPageLines, page)
	}
	start := (page - 1) * logPageLines
	end := min(start+logPageLines, len(lines))
	return map[string]any{
		"credentials_from": sourceRef(src), "cached": cached,
		"run_id": runID, "log_id": logID,
		"page": page, "total_pages": pages, "total_lines": len(lines),
		"first_line": start + 1, "last_line": end,
		"text": strings.Join(lines[start:end], "\n"),
	}, nil
}

// pipelineSource picks the pipeline-run source by id, or the only enabled one.
func pipelineSource(all []models.SourceDefinition, id string) (*models.SourceDefinition, error) {
	if id != "" {
		for i := range all {
			if all[i].ID == id {
				if all[i].Type != models.SourcePipelineRun {
					return nil, fmt.Errorf("source %q is a %s source, not a pipeline-run source", all[i].Name, all[i].Type)
				}
				return &all[i], nil
			}
		}
		return nil, fmt.Errorf("no source has id %q — source_sync status mode lists them", id)
	}
	var found []*models.SourceDefinition
	var names []string
	for i := range all {
		if all[i].Type == models.SourcePipelineRun && !all[i].Disabled {
			found = append(found, &all[i])
			names = append(names, fmt.Sprintf("%s (%s)", all[i].ID, all[i].Name))
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("no pipeline-run source is configured — add one for the pipeline, or pass the run's url")
	default:
		return nil, fmt.Errorf("several pipeline-run sources are configured; pass source_id, one of: %s", strings.Join(names, ", "))
	}
}

// recordSource names, in the stats record, the configured source whose API a
// live tool call reads.
func recordSource(ctx context.Context, src *models.SourceDefinition) {
	if tc := stats.ToolCallFrom(ctx); tc != nil && src != nil {
		tc.SourceID, tc.SourceName, tc.Provider = src.ID, src.Name, src.StatsProvider()
	}
}

func sourceRef(src *models.SourceDefinition) map[string]string {
	return map[string]string{"id": src.ID, "name": src.Name}
}

// pageSlice returns page (from 1) of all, size items at a time, and whether
// more follow.
func pageSlice[T any](all []T, page, size int) ([]T, bool) {
	start := (max(page, 1) - 1) * size
	if start >= len(all) {
		return []T{}, false
	}
	end := min(start+size, len(all))
	return all[start:end], end < len(all)
}

// csvSet is a comma-separated filter's values, lower-cased; empty when none.
func csvSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(s, ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out[v] = true
		}
	}
	return out
}
