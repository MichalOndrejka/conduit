package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
)

// SourceManager reads and edits providers and sources (sourcemgmt.Manager).
type SourceManager interface {
	ListAll() ([]models.SourceDefinition, error)
	Get(id string) (*models.SourceDefinition, error)
	Delete(ctx context.Context, id string) error
	IndexedChunks(ctx context.Context, src *models.SourceDefinition) int
	ListProviders() ([]models.Provider, error)
	ProviderFor(id string) (*models.Provider, providers.Integration, error)
	HasToken(id string) bool
	Conn(p *models.Provider) providers.Conn
	States(providerID string, nodes []providers.Node) ([]sourcemgmt.NodeState, error)
	Toggle(ctx context.Context, providerID string, node providers.NodeRef, name, srcType string, on bool, isSyncing func(string) bool) (*models.SourceDefinition, error)
	UpdateSettings(ctx context.Context, id, name string, disabled *bool, settings map[string]string) (*models.SourceDefinition, error)
}

// sourceSummary is one source in list mode.
type sourceSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	ProviderID string `json:"provider_id"`
	Disabled   bool   `json:"disabled"`
	SyncStatus string `json:"sync_status"`
}

// providerSummary is one provider in list mode — never its token.
type providerSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	BaseURL  string `json:"base_url"`
	HasToken bool   `json:"has_token"`
}

// sourceView is one source with its config, as get/select/update return it.
type sourceView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	ProviderID   string            `json:"provider_id"`
	Disabled     bool              `json:"disabled"`
	SyncStatus   string            `json:"sync_status"`
	LastSyncedAt *time.Time        `json:"last_synced_at"`
	Config       map[string]string `json:"config"`
}

// nodeRefSchema is a provider_explore node ref.
func nodeRefSchema(desc string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": desc,
		"properties": map[string]any{
			"kind": map[string]any{"type": "string"}, "org": map[string]any{"type": "string"},
			"project": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"},
			"path": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"},
		},
		"required": []string{"kind"},
	}
}

func manageRequestSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	mode := func(m string) map[string]any { return map[string]any{"const": m} }
	types := make([]string, len(sourcemgmt.Types))
	for i, t := range sourcemgmt.Types {
		types[i] = t.Value
	}
	sourceType := map[string]any{"type": "string", "enum": types, "description": "Source type: one the node offers"}
	variant := func(m string, props map[string]any, required ...string) map[string]any {
		props["mode"] = mode(m)
		return map[string]any{"type": "object", "properties": props, "required": append([]string{"mode"}, required...)}
	}
	node := nodeRefSchema("The node's ref exactly as provider_explore returned it")
	return map[string]any{
		"description": "List providers and sources, select or deselect what to index, change a source's settings, or delete it.",
		"oneOf": []any{
			variant("list", map[string]any{}),
			variant("get", map[string]any{"source_id": str("Source to read")}, "source_id"),
			variant("select", map[string]any{
				"provider_id": str("Provider the node belongs to"),
				"node":        node,
				"type":        sourceType,
				"name":        str("Optional: the node's name from provider_explore (names a wiki's source)"),
			}, "provider_id", "node", "type"),
			variant("deselect", map[string]any{
				"provider_id": str("Provider the node belongs to"),
				"node":        node,
				"type":        sourceType,
				"confirm": map[string]any{"type": "boolean", "description": "true to deselect; deselecting a source's last part " +
					"deletes the source and its indexed data"},
			}, "provider_id", "node", "type"),
			variant("update", map[string]any{
				"source_id": str("Source to change"),
				"name":      str("New display name"),
				"disabled":  map[string]any{"type": "boolean"},
				"settings": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
					"description": "Settings to change; \"\" removes one. " + strings.Join(providers.SettingKeys, ", ") + ": " +
						"Branch (files/commits; default the repository's default branch), PathFilter (files: comma-separated " +
						"globs, \"/\"-rooted for exact paths, e.g. /src/**,*.md), AreaPaths (Azure DevOps work items: " +
						"comma-separated, sub-areas included), Labels (GitHub issues: comma-separated, any matches), " +
						"Top (commits/issues: max items, newest first), Runs (pipeline runs: default listing size)",
				},
			}, "source_id"),
			variant("delete", map[string]any{
				"source_id": str("Source to delete"),
				"confirm":   map[string]any{"type": "boolean", "description": "true to delete; otherwise only reports what would be deleted"},
			}, "source_id"),
		},
	}
}

// RegisterSourcesTool registers the manage_source tool. Called once at
// startup, alongside RegisterSyncTool.
func RegisterSourcesTool(s *server.MCPServer, mgr SourceManager, runner SyncRunner) {
	tool := mcp.NewTool("manage_source",
		mcp.WithDescription(`Configure what Conduit indexes and reads live. Sources live inside providers — saved Azure `+
			`DevOps or GitHub accounts with their token, which the user adds in the Conduit UI (never ask for a token). `+
			`{"mode":"list"} returns every provider (id, name, kind, base_url, has_token), every source (id, name, type, `+
			`provider_id, disabled, sync_status) and the source types. To add a source, browse the provider with `+
			`provider_explore, then {"mode":"select","provider_id":...,"node":<the node's ref>,"type":<one it offers>}: `+
			`a repository, folder or file becomes Code/Test Code/Documentation/Requirements/Test Cases (one source per `+
			`repository × type, ticked folders and files narrowing it), a repository Commit History, a work item or `+
			`issue type Work Items/Requirements/Test Cases, an Azure DevOps project's Repositories group (or a single `+
			`repository) Pull Requests, its Pipelines group (or a pipeline folder) Pipeline Runs; on GitHub a `+
			`repository's Issues, Workflows or the repository itself. Selecting more parts of the same source extends `+
			`it. {"mode":"deselect",...,"confirm":true} undoes a selection — deselecting a source's last part deletes `+
			`it and its indexed data. {"mode":"get","source_id":...} returns a source with its config; `+
			`{"mode":"update","source_id":...} changes its name, disabled flag or settings (branch, path filter, area `+
			`paths, labels, max items, runs). Selecting doesn't index anything — call sync_source trigger afterwards; `+
			`pipeline-run and pull-request sources are read live and need no sync. `+
			`{"mode":"delete","source_id":...} reports what would be removed; repeat with "confirm":true to delete `+
			`the source and its indexed data.`),
		func(t *mcp.Tool) {
			t.InputSchema.Properties["request"] = manageRequestSchema()
			t.InputSchema.Required = append(t.InputSchema.Required, "request")
		},
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		payload, err := handleManageRequest(ctx, req.GetArguments(), mgr, runner)
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

func handleManageRequest(ctx context.Context, args map[string]any, mgr SourceManager, runner SyncRunner) (map[string]any, error) {
	req, ok := args["request"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`request is required, e.g. {"mode":"list"}`)
	}
	str := func(key string) string { s, _ := req[key].(string); return strings.TrimSpace(s) }
	switch mode := str("mode"); mode {
	case "list":
		all, err := mgr.ListAll()
		if err != nil {
			return nil, fmt.Errorf("reading sources: %w", err)
		}
		sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
		out := make([]sourceSummary, 0, len(all))
		for _, src := range all {
			out = append(out, sourceSummary{
				ID: src.ID, Name: src.Name, Type: src.Type, ProviderID: src.ProviderID,
				Disabled: src.Disabled, SyncStatus: syncStatusOf(src),
			})
		}
		provs, err := mgr.ListProviders()
		if err != nil {
			return nil, fmt.Errorf("reading providers: %w", err)
		}
		ps := make([]providerSummary, 0, len(provs))
		for _, p := range provs {
			ps = append(ps, providerSummary{ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, HasToken: mgr.HasToken(p.ID)})
		}
		types := make([]map[string]string, len(sourcemgmt.Types))
		for i, t := range sourcemgmt.Types {
			types[i] = map[string]string{"type": t.Value, "label": t.Label, "description": t.Description}
		}
		res := map[string]any{"providers": ps, "sources": out, "types": types}
		if len(ps) == 0 {
			res["note"] = "No provider is configured yet: ask the user to add one (Azure DevOps or GitHub, with a token) on the Providers page of the Conduit UI."
		}
		return res, nil

	case "get":
		src, err := lookup(mgr, req)
		if err != nil {
			return nil, err
		}
		return map[string]any{"source": viewOf(*src)}, nil

	case "select", "deselect":
		ref, err := nodeRefOf(req["node"])
		if err != nil {
			return nil, err
		}
		on := mode == "select"
		if confirm, _ := req["confirm"].(bool); !on && !confirm {
			return map[string]any{
				"note": `Nothing changed. Repeat with "confirm":true to deselect; if this is the source's last selected part, ` +
					`the source and its indexed data are deleted.`,
			}, nil
		}
		srcType := str("type")
		src, err := mgr.Toggle(ctx, str("provider_id"), ref, str("name"), srcType, on, runner.IsSyncing)
		if err != nil {
			return nil, err
		}
		if src == nil {
			if on {
				return nil, fmt.Errorf("nothing was selected")
			}
			return map[string]any{"deleted": true, "note": "That was the source's last selected part: the source and its indexed data were deleted."}, nil
		}
		out := map[string]any{"source": viewOf(*src)}
		if on {
			out["next"] = nextStep(*src, "index")
		} else {
			out["next"] = nextStep(*src, "re-index")
		}
		return out, nil

	case "update":
		src, err := lookup(mgr, req)
		if err != nil {
			return nil, err
		}
		if runner.IsSyncing(src.ID) {
			return nil, fmt.Errorf("source %q is syncing; cancel the sync in the UI or wait for it to finish", src.Name)
		}
		var disabled *bool
		if v, ok := req["disabled"]; ok {
			d, isBool := v.(bool)
			if !isBool {
				return nil, fmt.Errorf("disabled must be a boolean")
			}
			disabled = &d
		}
		settings := map[string]string{}
		if raw, ok := req["settings"]; ok {
			patch, isMap := raw.(map[string]any)
			if !isMap {
				return nil, fmt.Errorf("settings must be an object of string values")
			}
			for k, v := range patch {
				val, err := configValue(v)
				if err != nil {
					return nil, fmt.Errorf("settings.%s: %w", k, err)
				}
				settings[k] = val
			}
		}
		if v, ok := req["name"]; ok {
			if n, _ := v.(string); strings.TrimSpace(n) == "" {
				return nil, fmt.Errorf("name must be a non-empty string")
			}
		}
		updated, err := mgr.UpdateSettings(ctx, src.ID, str("name"), disabled, settings)
		if err != nil {
			return nil, err
		}
		return map[string]any{"source": viewOf(*updated), "next": nextStep(*updated, "re-index")}, nil

	case "delete":
		src, err := lookup(mgr, req)
		if err != nil {
			return nil, err
		}
		if runner.IsSyncing(src.ID) {
			return nil, fmt.Errorf("source %q is syncing; cancel the sync in the UI or wait for it to finish", src.Name)
		}
		if confirm, _ := req["confirm"].(bool); !confirm {
			return map[string]any{
				"would_delete": map[string]any{
					"id": src.ID, "name": src.Name, "type": src.Type,
					"indexed_chunks": mgr.IndexedChunks(ctx, src),
				},
				"note": `Nothing deleted. Repeat with "confirm":true to delete the source and its indexed data.`,
			}, nil
		}
		if err := mgr.Delete(ctx, src.ID); err != nil {
			return nil, fmt.Errorf("deleting source: %w", err)
		}
		return map[string]any{"deleted": src.ID}, nil

	default:
		return nil, fmt.Errorf(`request.mode must be "list", "get", "select", "deselect", "update" or "delete", got %q`, mode)
	}
}

// nodeRefOf decodes a node ref passed back from provider_explore.
func nodeRefOf(v any) (providers.NodeRef, error) {
	var ref providers.NodeRef
	m, ok := v.(map[string]any)
	if !ok {
		return ref, fmt.Errorf("node is required: a node's ref as provider_explore returned it")
	}
	data, _ := json.Marshal(m)
	if err := json.Unmarshal(data, &ref); err != nil {
		return ref, fmt.Errorf("node: %w", err)
	}
	if ref.Kind == "" {
		return ref, fmt.Errorf("node.kind is required")
	}
	return ref, nil
}

func lookup(mgr SourceManager, req map[string]any) (*models.SourceDefinition, error) {
	id, _ := req["source_id"].(string)
	if id == "" {
		return nil, fmt.Errorf("source_id is required")
	}
	src, err := mgr.Get(id)
	if err != nil {
		return nil, fmt.Errorf("reading source: %w", err)
	}
	if src == nil {
		return nil, fmt.Errorf("no source with id %q; list mode shows the ids", id)
	}
	return src, nil
}

// configValue accepts strings, plus numbers and booleans that models often
// send for settings like Top.
func configValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	default:
		return "", fmt.Errorf("must be a string")
	}
}

func syncStatusOf(src models.SourceDefinition) string {
	if src.SyncStatus == "" {
		return "idle"
	}
	return src.SyncStatus
}

// viewOf copies src for output.
func viewOf(src models.SourceDefinition) sourceView {
	cfg := make(map[string]string, len(src.Config))
	for k, v := range src.Config {
		cfg[k] = v
	}
	return sourceView{
		ID: src.ID, Name: src.Name, Type: src.Type, ProviderID: src.ProviderID, Disabled: src.Disabled,
		SyncStatus: syncStatusOf(src), LastSyncedAt: src.LastSyncedAt, Config: cfg,
	}
}

// nextStep tells the agent what to do with a saved source: sync it, or for
// a live source, just use its tool.
func nextStep(src models.SourceDefinition, verb string) string {
	switch src.Type {
	case models.SourcePipelineRun:
		return "Nothing to sync: search_pipeline_run reads these pipelines live with this source_id."
	case models.SourcePullRequest:
		return "Nothing to sync: search_pull_request reads these pull requests live with this source_id."
	}
	return "Call sync_source trigger with this id to " + verb + " it."
}
