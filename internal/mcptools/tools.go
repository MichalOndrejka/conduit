// Package mcptools is the Go port of app/mcp_tools/tools.py: registers the
// seven collection search tools and the two experience-memory tools on an MCP
// server (mark3labs/mcp-go, streamable HTTP transport).
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// RegisterTools registers all MCP tools. Called once at startup.
func RegisterTools(s *server.MCPServer, search *rag.SearchService, mem *memory.Service) {

	// ── Knowledge search tools ─────────────────────────────────────────────
	// Each call returns only the single most relevant match — the rest of the
	// ranked list is reachable by paging, never dumped in one response.
	const paginationNote = " Returns only the single most relevant match. " +
		"If it isn't sufficient, call again with a higher page number to see the next-most-relevant match."
	makeSearchTool := func(collection, name, description string) {
		tool := mcp.NewTool(name,
			mcp.WithDescription(description+paginationNote),
			mcp.WithString("query", mcp.Required(),
				mcp.Description("Natural-language search query")),
			mcp.WithNumber("page",
				mcp.Description("Which result to return by relevance rank, starting at 1 (the most relevant match). "+
					"Call again with a higher page number to see the next-most-relevant match if this one isn't sufficient. Default 1.")),
			mcp.WithString("source_name",
				mcp.Description("Optional: restrict results to a single source by name")),
		)
		s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			page := req.GetInt("page", 1)
			if page < 1 {
				page = 1
			}
			var tags map[string]string
			if sourceName := req.GetString("source_name", ""); sourceName != "" {
				tags = map[string]string{"source_name": sourceName}
			}
			results, hasMore, err := search.Search(ctx, collection, query, page, tags)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			payload := map[string]any{"results": results, "page": page, "has_more": hasMore}
			if len(results) == 0 {
				payload["results"] = []any{}
				if page == 1 {
					payload["note"] = "No data embedded for this query — the source may not be synced yet, or nothing matched."
				} else {
					payload["note"] = fmt.Sprintf("No further matches beyond page %d — this was the last page.", page-1)
				}
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		})
	}

	makeSearchTool(models.CollectionWorkItems, "search_workitem",
		"Semantic search over work items (bugs, tasks, defects). "+
			"Optionally filter by source_name to target a specific source.")
	makeSearchTool(models.CollectionRequirements, "search_requirement",
		"Semantic search over requirements (features, user stories, epics). "+
			"Optionally filter by source_name to target a specific source.")
	makeSearchTool(models.CollectionSourceCode, "search_source_code",
		"Semantic search over production source code (classes, methods, functions). "+
			"Does not include test files — use search_test_code for tests.")
	makeSearchTool(models.CollectionTestCode, "search_test_code",
		"Semantic search over test code — unit tests, integration tests and specs. "+
			"Use this to find test coverage, test patterns, or examples of how code is tested.")
	makeSearchTool(models.CollectionTestCases, "search_testcase",
		"Semantic search over test cases including test steps.")
	makeSearchTool(models.CollectionDocumentation, "search_documentation",
		"Semantic search over wiki pages, repo documentation and uploaded documents.")
	makeSearchTool(models.CollectionCommits, "search_commit",
		"Semantic search over git commit history — messages, authors and change summaries.")

	// ── Experience tools ───────────────────────────────────────────────────

	retrieveTool := mcp.NewTool("retrieve_experience",
		mcp.WithDescription(
			"ALWAYS call this tool at the START of every new task, conversation, or user request. "+
				"It recalls relevant past experience: guidance on how to handle similar situations, "+
				"known mistakes and their fixes, user preferences, and decisions from previous sessions. "+
				"Returns guidance strings that should be followed for the current task. "+
				"Pass a query describing the current situation or task."),
		mcp.WithString("query", mcp.Required(),
			mcp.Description("Description of the current situation or task")),
		mcp.WithNumber("top_k",
			mcp.Description("Number of entries to return (default 5)")),
	)
	s.AddTool(retrieveTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		topK := req.GetInt("top_k", 5)
		results, err := mem.Retrieve(ctx, query, topK)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var payload any
		if len(results) == 0 {
			payload = map[string]any{"experience": []any{}, "note": "No relevant experience found."}
		} else {
			payload = map[string]any{"experience": results}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	})

	rememberTool := mcp.NewTool("remember",
		mcp.WithDescription(
			"ALWAYS use this tool to store any information worth retaining across sessions. "+
				"situation: describe the trigger — what kind of task, prompt, or context should surface this rule. "+
				"guidance: the exact instruction to follow — what to do, avoid, or apply in that situation. "+
				"Call this proactively whenever you learn something the user would want enforced in future conversations."),
		mcp.WithString("situation", mcp.Required(),
			mcp.Description("The trigger: task, prompt, or context that should surface this rule")),
		mcp.WithString("guidance", mcp.Required(),
			mcp.Description("The exact instruction to follow in that situation")),
	)
	s.AddTool(rememberTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		situation, err := req.RequireString("situation")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		guidance, err := req.RequireString("guidance")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		entryID, err := mem.Remember(ctx, situation, guidance)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, _ := json.Marshal(map[string]string{"status": "stored", "entry_id": entryID})
		return mcp.NewToolResultText(string(data)), nil
	})
}
