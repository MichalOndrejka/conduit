package mcptools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// indexedPRModes are the search_pull_request modes served from the
// conduit_pull_requests collection (pull-request sources) rather than live.
var indexedPRModes = map[string]bool{"semantic_search": true, "pattern_search": true, "retrieve_chunk": true}

func pullRequestRequestSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	mode := func(m string) map[string]any { return map[string]any{"const": m} }
	prURL := str("The pull request's web URL, e.g. https://dev.azure.com/org/Project/_git/repo/pullrequest/123 " +
		"or https://github.com/owner/repo/pull/45")
	page := num("Page to return, starting at 1")
	from := num("Optional: compare from this iteration (0 = the merge base with the target branch, the default)")
	to := num("Optional: compare up to this iteration (default: the latest)")
	variant := func(m string, props map[string]any, required ...string) map[string]any {
		props["mode"] = mode(m)
		return map[string]any{"type": "object", "properties": props, "required": append([]string{"mode"}, required...)}
	}
	indexed, _ := requestParamSchema(collectionFilters[models.CollectionPullRequests], nil, false)["oneOf"].([]any)
	return map[string]any{
		"description": "Search embedded pull requests by meaning or text, list pull requests, or read one pull request's " +
			"details, changed files, diffs, comment threads, iterations or commits.",
		"oneOf": append(indexed, []any{
			variant("list", map[string]any{
				"source_id": str("A configured Azure DevOps or GitHub source of the repository (ids from sync_source status mode)"),
				"status":    map[string]any{"type": "string", "enum": []string{"active", "completed", "abandoned", "all"}, "description": "Default active"},
				"author":    str(`Optional: "me" (the token's owner), an email (Azure DevOps) or a login (GitHub)`),
				"reviewer": str(`Optional: "me", an email (Azure DevOps) or a login (GitHub). On GitHub this matches pull ` +
					`requests whose review is still requested from that user`),
				"target_branch": str("Optional: target branch name, e.g. main"),
				"page":          page,
			}, "source_id"),
			variant("get", map[string]any{"url": prURL}, "url"),
			variant("files", map[string]any{"url": prURL, "page": page, "from_iteration": from, "to_iteration": to}, "url"),
			variant("diff", map[string]any{
				"url":  prURL,
				"path": str("One changed file to diff (from files mode); omit to diff a page of files instead"),
				"page": page,
				"include_content": map[string]any{"type": "boolean", "description": "Also return each file's full content at " +
					"the pull request head, for the code around each change. Default false"},
				"from_iteration": from, "to_iteration": to,
			}, "url"),
			variant("threads", map[string]any{"url": prURL, "page": page}, "url"),
			variant("iterations", map[string]any{"url": prURL}, "url"),
			variant("commits", map[string]any{"url": prURL, "page": page}, "url"),
		}...),
	}
}

// RegisterPullRequestTool registers the search_pull_request tool. Called once at
// startup, alongside RegisterTools.
func RegisterPullRequestTool(s *server.MCPServer, search *rag.SearchService, srcs SourceLister, store secrets.Reader) {
	tool := mcp.NewTool("search_pull_request",
		mcp.WithDescription(`Find Azure DevOps and GitHub pull requests, and read them live for code review. Read-only. `+
			`To find a pull request without its URL — by topic, the code or work item it touched, its discussion, `+
			`author or reviewer — use request={"mode":"semantic_search","query":...,"page":1} (or "pattern_search" for `+
			`exact text) over the pull requests embedded by pull-request sources: each result holds one pull request's `+
			`title, description, status, branches, reviewers and votes, linked work items, commit messages, changed-file `+
			`list and comment threads (no diffs), and is a snapshot from the last sync. Narrow with "filters" (id, project, `+
			`repository, author, reviewer, state, branch = target branch, since/until on the creation date). Pass a `+
			`result's properties.web_url as "url" to the live modes below for current details and diffs; if a match `+
			`looks cut off, {"mode":"retrieve_chunk","source_doc_id":...,"chunk_index":...} fetches the adjacent chunk. `+
			`The live modes read the provider directly, so they are always current. Every live mode but list takes the `+
			`pull request's web URL, which is matched to a configured source of the same organization or server for `+
			`credentials. `+
			`Typical review: request={"mode":"get","url":...} for the title, description, branches, reviewers and votes, `+
			`linked work items and checks; {"mode":"files","url":...} for the changed files with +/- counts; then `+
			`{"mode":"diff","url":...,"path":...} for one file's unified diff (merge base → head), or "page" for 20 files `+
			`at a time — add "include_content":true for the full file at head; {"mode":"threads","url":...} for existing `+
			`review comments, so you don't repeat points already raised. {"mode":"iterations"} lists the updates `+
			`(Azure DevOps pushes; GitHub commits), and files/diff accept from_iteration/to_iteration to see only what `+
			`changed since an earlier iteration. {"mode":"commits"} lists the commits. {"mode":"list","source_id":...} `+
			`finds pull requests by status, author, reviewer ("me" works) and target branch. Paged modes return 20 `+
			`items per page with has_more. "incomplete" lists anything the provider itself left out (GitHub omits `+
			`patches of binary or very large files and caps listings) — say so in your review rather than assuming `+
			`the files are unchanged. Cite web_url links when referring to the pull request, work items or checks.`),
		func(t *mcp.Tool) {
			t.InputSchema.Properties["request"] = pullRequestRequestSchema()
			t.InputSchema.Required = append(t.InputSchema.Required, "request")
		},
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		if r, ok := args["request"].(map[string]any); ok {
			if mode, _ := r["mode"].(string); indexedPRModes[mode] {
				return searchIndexedPullRequests(ctx, search, args)
			}
		}
		payload, err := handlePullRequestRequest(ctx, args, srcs, store)
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

// searchIndexedPullRequests serves the semantic_search, pattern_search and
// retrieve_chunk modes from the embedded pull requests, as the collection
// search tools do.
func searchIndexedPullRequests(ctx context.Context, search *rag.SearchService, args map[string]any) (*mcp.CallToolResult, error) {
	collection := models.CollectionPullRequests
	query, err := parseChunkQuery(args, collection, collectionFilters[collection], nil, search.Identities())
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	payload, err := query.resolve(ctx, search, collection)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	addAsOf(payload, search)
	if q, ok := query.(semanticSearchRequest); ok && q.Page == 1 {
		if results, isEmpty := payload["results"].([]any); isEmpty && len(results) == 0 {
			payload["note"] = "No embedded pull request matched — add or sync a Pull Requests source for the repository, " +
				`or use {"mode":"list","source_id":...} to browse pull requests live.`
		}
	}
	recordSearch(ctx, collection, args, query, payload)
	data, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func handlePullRequestRequest(ctx context.Context, args map[string]any, srcs SourceLister, store secrets.Reader) (map[string]any, error) {
	req, ok := args["request"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`request is required, e.g. {"mode":"get","url":"<pull request URL>"}`)
	}
	str := func(key string) string { s, _ := req[key].(string); return s }
	num := func(key string) int { n, _ := req[key].(float64); return int(n) }
	page := max(num("page"), 1)

	all, err := srcs.ListAll()
	if err != nil {
		return nil, fmt.Errorf("reading sources: %w", err)
	}

	mode := str("mode")
	if mode == "list" {
		id := str("source_id")
		if id == "" {
			return nil, fmt.Errorf("list mode requires source_id")
		}
		switch status := str("status"); status {
		case "", sources.PRStatusActive, sources.PRStatusCompleted, sources.PRStatusAbandoned, sources.PRStatusAll:
		default:
			return nil, fmt.Errorf(`request.status must be "active", "completed", "abandoned" or "all", got %q`, status)
		}
		for i := range all {
			if all[i].ID != id {
				continue
			}
			p, err := sources.PullRequestProviderFor(&all[i], store)
			if err != nil {
				return nil, err
			}
			prs, hasMore, err := p.List(ctx, sources.PRListFilter{
				Status: str("status"), Author: str("author"), Reviewer: str("reviewer"),
				TargetBranch: str("target_branch"), Page: page,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"source":        map[string]string{"id": all[i].ID, "name": all[i].Name},
				"pull_requests": prs, "page": page, "has_more": hasMore,
			}, nil
		}
		return nil, fmt.Errorf("no source has id %q — sync_source status mode lists them", id)
	}

	switch mode {
	case "get", "files", "diff", "threads", "iterations", "commits":
	default:
		return nil, fmt.Errorf(`request.mode must be "semantic_search", "pattern_search", "retrieve_chunk", "list", "get", `+
			`"files", "diff", "threads", "iterations" or "commits", got %q`, mode)
	}
	if str("url") == "" {
		return nil, fmt.Errorf("%s mode requires url, the pull request's web URL", mode)
	}
	pr, err := sources.ResolvePullRequestURL(str("url"), all, store)
	if err != nil {
		return nil, err
	}
	p := pr.Provider
	out := map[string]any{
		"pull_request":     map[string]any{"id": pr.ID, "web_url": p.WebURL(pr.ID)},
		"credentials_from": map[string]string{"source_id": pr.Source.ID, "source_name": pr.Source.Name},
	}

	switch mode {
	case "get":
		details, err := p.Get(ctx, pr.ID)
		if err != nil {
			return nil, err
		}
		out["pull_request"] = details

	case "iterations":
		iters, err := p.Iterations(ctx, pr.ID)
		if err != nil {
			return nil, err
		}
		out["iterations"] = iters

	case "files", "diff":
		changes, err := p.Changes(ctx, pr.ID, sources.PRRange{From: num("from_iteration"), To: num("to_iteration")})
		if err != nil {
			return nil, err
		}
		path := ""
		if mode == "diff" {
			path = str("path")
		}
		selected, hasMore, err := sources.SelectChanges(changes.Files, path, page)
		if err != nil {
			return nil, err
		}
		withContent, _ := req["include_content"].(bool)
		files, err := p.Diff(ctx, selected, mode == "diff" && withContent)
		if err != nil {
			return nil, err
		}
		if mode == "files" {
			for i := range files {
				files[i].Diff = ""
			}
		}
		out["head_commit"] = changes.HeadCommit
		out["total_files"] = len(changes.Files)
		out["files"] = files
		if path == "" {
			out["page"], out["has_more"] = page, hasMore
		}
		if len(changes.Incomplete) > 0 {
			out["incomplete"] = changes.Incomplete
		}

	case "threads":
		threads, notes, err := p.Threads(ctx, pr.ID)
		if err != nil {
			return nil, err
		}
		items, hasMore := sources.PageOf(threads, page)
		out["threads"], out["total_threads"], out["page"], out["has_more"] = items, len(threads), page, hasMore
		if len(notes) > 0 {
			out["notes"] = notes
		}

	case "commits":
		commits, incomplete, err := p.Commits(ctx, pr.ID)
		if err != nil {
			return nil, err
		}
		items, hasMore := sources.PageOf(commits, page)
		out["commits"], out["total_commits"], out["page"], out["has_more"] = items, len(commits), page, hasMore
		if len(incomplete) > 0 {
			out["incomplete"] = incomplete
		}
	}
	return out, nil
}
