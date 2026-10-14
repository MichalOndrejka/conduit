package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/syncctl"
)

// SourceLister reads the configured sources (store.SourceConfigStore).
type SourceLister interface {
	ListAll() ([]models.SourceDefinition, error)
}

// SyncRunner starts syncs and reports their live state (syncsvc.Service).
type SyncRunner interface {
	SyncSelected(ctx context.Context, ids []string)
	IsSyncing(sourceID string) bool
	Progress() *syncctl.ProgressStore
}

// Trigger outcomes, one per requested source.
const (
	syncStarted        = "started"
	syncAlreadyRunning = "already_syncing"
	syncDisabled       = "disabled"
	syncNotFound       = "not_found"
)

// syncSourceStatus is one source's sync state as reported by the tool. The
// source's config is never included — it holds endpoint and credential names.
type syncSourceStatus struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Type           string               `json:"type"`
	Disabled       bool                 `json:"disabled"`
	SyncStatus     string               `json:"sync_status"`
	LastSyncedAt   *time.Time           `json:"last_synced_at"`
	SyncError      *string              `json:"sync_error,omitempty"`
	SyncErrorPhase *string              `json:"sync_error_phase,omitempty"`
	Progress       *models.SyncProgress `json:"progress,omitempty"`
}

func syncRequestSchema() map[string]any {
	ids := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return map[string]any{
		"description": "Start syncs, or report the sync status of sources.",
		"oneOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":       map[string]any{"const": "status"},
					"source_ids": ids("Sources to report; omit to report every configured source"),
				},
				"required": []string{"mode"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":       map[string]any{"const": "trigger"},
					"source_ids": ids("Sources to sync, by id (from status mode)"),
				},
				"required": []string{"mode", "source_ids"},
			},
		},
	}
}

// RegisterSyncTool registers the sync_sources tool. Called once at startup,
// alongside RegisterTools.
func RegisterSyncTool(s *server.MCPServer, sources SourceLister, runner SyncRunner) {
	tool := mcp.NewTool("sync_sources",
		mcp.WithDescription(`Start syncs of Conduit's sources and check their status. `+
			`A sync re-fetches a source from its system and replaces its indexed documents, so search results reflect `+
			`the source as of its last successful sync (last_synced_at). `+
			`Pass request={"mode":"status"} to list every source with its id, type, sync_status (idle, syncing, `+
			`completed, failed), last_synced_at, any sync error, and live progress while syncing; add "source_ids" `+
			`to report only those. Pass request={"mode":"trigger","source_ids":[...]} to start syncing those sources `+
			`in the background — it returns immediately with a per-source outcome (started, already_syncing, `+
			`disabled, not_found); poll status mode to follow progress until sync_status is completed or failed.`),
		func(t *mcp.Tool) {
			t.InputSchema.Properties["request"] = syncRequestSchema()
			t.InputSchema.Required = append(t.InputSchema.Required, "request")
		},
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		payload, err := handleSyncRequest(req.GetArguments(), sources, runner)
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

func handleSyncRequest(args map[string]any, sources SourceLister, runner SyncRunner) (map[string]any, error) {
	req, ok := args["request"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`request is required: {"mode":"status"} or {"mode":"trigger","source_ids":[...]}`)
	}
	ids, err := stringList(req["source_ids"])
	if err != nil {
		return nil, err
	}
	all, err := sources.ListAll()
	if err != nil {
		return nil, fmt.Errorf("reading sources: %w", err)
	}
	byID := make(map[string]models.SourceDefinition, len(all))
	for _, src := range all {
		byID[src.ID] = src
	}

	switch mode, _ := req["mode"].(string); mode {
	case "status":
		out := []syncSourceStatus{}
		var unknown []string
		if len(ids) == 0 {
			for _, src := range all {
				out = append(out, statusOf(src, runner))
			}
		}
		for _, id := range ids {
			if src, ok := byID[id]; ok {
				out = append(out, statusOf(src, runner))
			} else {
				unknown = append(unknown, id)
			}
		}
		payload := map[string]any{"sources": out}
		if len(unknown) > 0 {
			payload["not_found"] = unknown
		}
		return payload, nil

	case "trigger":
		if len(ids) == 0 {
			return nil, fmt.Errorf("trigger mode requires a non-empty source_ids list")
		}
		outcomes := map[string]string{}
		var start []string
		for _, id := range ids {
			if _, seen := outcomes[id]; seen {
				continue
			}
			src, ok := byID[id]
			switch {
			case !ok:
				outcomes[id] = syncNotFound
			case src.Disabled:
				outcomes[id] = syncDisabled
			case runner.IsSyncing(id):
				outcomes[id] = syncAlreadyRunning
			default:
				outcomes[id] = syncStarted
				start = append(start, id)
			}
		}
		if len(start) > 0 {
			// Detached from the request context: the sync must outlive this
			// tool call, which returns immediately.
			go runner.SyncSelected(context.Background(), start)
		}
		return map[string]any{"outcomes": outcomes}, nil

	default:
		return nil, fmt.Errorf(`request.mode must be "status" or "trigger", got %q`, mode)
	}
}

func statusOf(src models.SourceDefinition, runner SyncRunner) syncSourceStatus {
	st := syncSourceStatus{
		ID: src.ID, Name: src.Name, Type: src.Type, Disabled: src.Disabled,
		SyncStatus: src.SyncStatus, LastSyncedAt: src.LastSyncedAt,
		SyncError: src.SyncError, SyncErrorPhase: src.SyncErrorPhase,
	}
	if st.SyncStatus == "" {
		st.SyncStatus = "idle"
	}
	if runner.IsSyncing(src.ID) {
		st.SyncStatus = "syncing"
		if p, ok := runner.Progress().Get(src.ID); ok {
			st.Progress = &p
		}
	}
	return st
}

// stringList reads an optional JSON array of strings.
func stringList(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("source_ids must be an array of strings")
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("source_ids must be an array of non-empty strings")
		}
		out = append(out, s)
	}
	return out, nil
}
