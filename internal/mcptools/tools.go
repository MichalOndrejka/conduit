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

// chunkQuery is one mode of the "request" oneOf parameter every search tool
// accepts. resolve runs the query and builds the full JSON-able response
// payload itself, so the tool handler never branches on which mode was
// requested — that's decided once, when parseChunkQuery picks the concrete
// type.
type chunkQuery interface {
	resolve(ctx context.Context, search *rag.SearchService, collection string) (map[string]any, error)
}

// semanticSearchRequest is request.mode == "semantic_search": today's ranked
// vector search, returning only the single most relevant match per call.
type semanticSearchRequest struct {
	Query      string
	Page       int
	SourceName string
}

func (q semanticSearchRequest) resolve(ctx context.Context, search *rag.SearchService, collection string) (map[string]any, error) {
	var tags map[string]string
	if q.SourceName != "" {
		tags = map[string]string{"source_name": q.SourceName}
	}
	results, hasMore, err := search.Search(ctx, collection, q.Query, q.Page, tags)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"results": results, "page": q.Page, "has_more": hasMore}
	if len(results) == 0 {
		payload["results"] = []any{}
		if q.Page == 1 {
			payload["note"] = "No data embedded for this query — the source may not be synced yet, or nothing matched."
		} else {
			payload["note"] = fmt.Sprintf("No further matches beyond page %d — this was the last page.", q.Page-1)
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

func (q retrieveChunkRequest) resolve(ctx context.Context, search *rag.SearchService, collection string) (map[string]any, error) {
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

// parseChunkQuery decodes the required "request" argument into the concrete
// chunkQuery its "mode" field selects.
func parseChunkQuery(args map[string]any) (chunkQuery, error) {
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
		page := int(pageF)
		if page < 1 {
			page = 1
		}
		sourceName, _ := raw["source_name"].(string)
		return semanticSearchRequest{Query: query, Page: page, SourceName: sourceName}, nil
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
	default:
		return nil, fmt.Errorf(`request.mode must be "semantic_search" or "retrieve_chunk", got %q`, mode)
	}
}

// requestParamSchema is a discriminated union (JSON Schema oneOf) of the two
// chunkQuery shapes above, keyed by a "mode" field.
func requestParamSchema() map[string]any {
	return map[string]any{
		"description": `Either a ranked semantic search or a deterministic fetch of one exact chunk by ID.`,
		"oneOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":  map[string]any{"const": "semantic_search"},
					"query": map[string]any{"type": "string", "description": "Natural-language search query"},
					"page": map[string]any{"type": "number", "description": "Which result to return by relevance rank, starting at 1 (the most relevant match). " +
						"Call again with a higher page number to see the next-most-relevant match if this one isn't sufficient. Start with 1."},
					"source_name": map[string]any{"type": "string", "description": "Optional: restrict results to a single source by name"},
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
		},
	}
}

func withRequestParam() mcp.ToolOption {
	return func(t *mcp.Tool) {
		t.InputSchema.Properties["request"] = requestParamSchema()
		t.InputSchema.Required = append(t.InputSchema.Required, "request")
	}
}

// RegisterTools registers all MCP tools. Called once at startup.
func RegisterTools(s *server.MCPServer, search *rag.SearchService, mem *memory.Service) {

	// ── Knowledge search tools ─────────────────────────────────────────────
	// request.mode="semantic_search" returns only the single most relevant
	// match — the rest of the ranked list is reachable by paging, never
	// dumped in one response. request.mode="retrieve_chunk" fetches one exact
	// chunk of a document by ID (see chunk_index/has_previous/has_next on any
	// result) — deterministic, no embedding call, for walking to a chunk that
	// got cut off by the chunker.
	const requestModeNote = ` Pass request={"mode":"semantic_search","query":...,"page":1} for a ranked search. ` +
		`Each result includes chunk_index/total_chunks and has_previous/has_next; if a match looks cut off, pass ` +
		`request={"mode":"retrieve_chunk","source_doc_id":...,"chunk_index":...} (from the result, chunk_index ± 1) ` +
		`to fetch the exact adjacent chunk.`
	makeSearchTool := func(collection, name, description string) {
		tool := mcp.NewTool(name,
			mcp.WithDescription(description+requestModeNote),
			withRequestParam(),
		)
		s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := parseChunkQuery(req.GetArguments())
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			payload, err := query.resolve(ctx, search, collection)
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
