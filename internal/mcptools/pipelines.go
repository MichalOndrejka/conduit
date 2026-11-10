package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// logPageLines is how many log lines one log page returns.
const logPageLines = 500

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
		"description": "List a pipeline's latest runs, read one run's failures, or read one failed step's log.",
		"oneOf": []any{
			variant("list", map[string]any{
				"source_id": sourceID,
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
			variant("log", map[string]any{
				"source_id": sourceID, "run_id": runID, "url": runURL,
				"log_id": num("A failed step's log_id, from get mode"),
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
			`provider. Read-only; nothing is indexed, so every answer is current. `+
			`{"mode":"list"} returns the latest runs of a configured pipeline, newest first (5 by default), with result, `+
			`status, reason, branch, commit, requester, timing and web_url — filter by result, status, branch, reason `+
			`or requested_for (e.g. last failed build on main: {"mode":"list","result":"failed","branch":"main","top":1}). `+
			`{"mode":"get","run_id":...} (or "url": a run link with buildId=… or …/actions/runs/…) explains one run: `+
			`timeline issues (errors and warnings), every failed step of every attempt — so a run rerun to green still `+
			`shows attempt 1's failures — with its log_id, failed and flaky tests with error message and stack trace `+
			`(Azure DevOps; 20 per page), the commits the run built since the previous run, and its artifacts `+
			`(metadata only). A step's result never says why it failed: read its log with {"mode":"log","run_id":...,`+
			`"log_id":...}, which returns the last 500 lines by default; page backwards for earlier lines. `+
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
	case "list":
		src, err := pipelineSource(all, str("source_id"))
		if err != nil {
			return nil, err
		}
		p, err := sources.PipelineProviderFor(src, store)
		if err != nil {
			return nil, err
		}
		runs, err := p.List(ctx, sources.PipelineRunFilter{
			Top: int(num("top")), Result: str("result"), Status: str("status"), Branch: str("branch"),
			Reason: str("reason"), RequestedFor: str("requested_for"),
		})
		if err != nil {
			return nil, err
		}
		out := map[string]any{"source": sourceRef(src), "runs": runs}
		if len(runs) == 0 {
			out["note"] = "No run matched — check the filter values (Azure DevOps and GitHub name results and statuses differently)."
		}
		return out, nil

	case "get", "log":
	default:
		return nil, fmt.Errorf(`request.mode must be "list", "get" or "log", got %q`, mode)
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

	if mode == "get" {
		run, cached, err := p.Get(ctx, runID)
		if err != nil {
			return nil, err
		}
		page := max(int(num("page")), 1)
		shown := *run // the cached run is shared: page a copy
		tests, hasMore := sources.PageOf(run.Tests, page)
		shown.Tests = tests
		return map[string]any{
			"credentials_from": sourceRef(src), "cached": cached, "run": shown,
			"total_tests": len(run.Tests), "tests_page": page, "tests_has_more": hasMore,
		}, nil
	}

	logID := num("log_id")
	if logID <= 0 {
		return nil, fmt.Errorf("log mode requires log_id, a failed step's log_id from get mode")
	}
	text, cached, err := p.Log(ctx, runID, logID)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	pages := max((len(lines)+logPageLines-1)/logPageLines, 1)
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

func sourceRef(src *models.SourceDefinition) map[string]string {
	return map[string]string{"id": src.ID, "name": src.Name}
}
