// Package mcptools is the Go port of app/mcp_tools/tools.py: registers the
// collection search tools (one per shape: work items, code, documentation,
// commits, pipeline runs) and the two experience-memory tools on an MCP
// server (mark3labs/mcp-go, streamable HTTP transport).
package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// RegisterTools registers all MCP tools. Called once at startup.
func RegisterTools(s *server.MCPServer, search *rag.SearchService, mem *memory.Service) {

	// ── Knowledge search tools ─────────────────────────────────────────────
	for _, spec := range SearchTools() {
		registerSearchTool(s, search, spec)
	}

	// ── Experience tools ───────────────────────────────────────────────────

	retrieveTool := mcp.NewTool("experience_retrieve",
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
		if tc := stats.ToolCallFrom(ctx); tc != nil {
			tc.Query, tc.Results, tc.Counted = query, len(results), true
			for _, r := range results {
				tc.Hits = append(tc.Hits, rag.Truncate(r.Situation, 200))
			}
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

	rememberTool := mcp.NewTool("experience_remember",
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

// recordSearch adds a search call's details to the stats record, when stats
// are on: what was asked, with which filters, and how many results came back.
func recordSearch(ctx context.Context, collection string, args map[string]any, query chunkQuery, payload map[string]any) {
	tc := stats.ToolCallFrom(ctx)
	if tc == nil {
		return
	}
	tc.Collection, tc.Counted = collection, true
	switch q := query.(type) {
	case semanticSearchRequest:
		tc.Query, tc.Page = q.Query, q.Page
	case patternSearchRequest:
		tc.Query, tc.Page = q.Pattern, q.Page
	case retrieveChunkRequest:
		tc.Query = fmt.Sprintf("%s #%d", q.SourceDocID, q.ChunkIndex)
	case facetsRequest:
		names := make([]string, len(q.Fields))
		for i, f := range q.Fields {
			names[i] = f.Name
		}
		tc.Query = "facets: " + strings.Join(names, ", ")
	}
	if raw, ok := args["request"].(map[string]any); ok {
		if f, ok := raw["filters"].(map[string]any); ok {
			tc.Filters = map[string][]string{}
			for field, v := range f {
				switch t := v.(type) {
				case string:
					tc.Filters[field] = []string{t}
				case []any:
					for _, x := range t {
						if s, ok := x.(string); ok {
							tc.Filters[field] = append(tc.Filters[field], s)
						}
					}
				}
			}
		}
	}
	switch r := payload["results"].(type) {
	case []models.SearchResult:
		tc.Results = len(r)
	case []any:
		tc.Results = len(r)
	}
	if n, ok := payload["documents"].(int); ok {
		tc.Results = n // facets: the documents counted
	}
	tc.HasMore, _ = payload["has_more"].(bool)
}
