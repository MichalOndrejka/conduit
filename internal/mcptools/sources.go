package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// SourceManager reads and edits source configs (sourcemgmt.Manager).
type SourceManager interface {
	ListAll() ([]models.SourceDefinition, error)
	Get(id string) (*models.SourceDefinition, error)
	Save(ctx context.Context, oldCollection string, src *models.SourceDefinition) error
	Delete(ctx context.Context, id string) error
	CredentialNames() []string
	IndexedChunks(ctx context.Context, src *models.SourceDefinition) int
}

// sourceSummary is one source in list mode.
type sourceSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Provider   string `json:"provider"`
	Disabled   bool   `json:"disabled"`
	SyncStatus string `json:"sync_status"`
}

// sourceView is one source with its config, as get/create/update return it.
// Config holds credential names only — secret values never live there.
type sourceView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Disabled     bool              `json:"disabled"`
	SyncStatus   string            `json:"sync_status"`
	LastSyncedAt *time.Time        `json:"last_synced_at"`
	Config       map[string]string `json:"config"`
}

func manageRequestSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	mode := func(m string) map[string]any { return map[string]any{"const": m} }
	config := func(desc string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description": desc + " Allowed keys: " + strings.Join(sourcemgmt.APIConfigKeys, ", ") +
				" (plus UI preset keys " + strings.Join(sourcemgmt.PresetConfigKeys, ", ") + "). " +
				"Token, Password and ApiKeyValue take the NAME of a stored credential from list mode, never a secret value.",
		}
	}
	types := make([]string, len(sourcemgmt.Types))
	for i, t := range sourcemgmt.Types {
		types[i] = t.Value
	}
	sourceType := map[string]any{"type": "string", "enum": types, "description": "Source type (decides the search tool)"}
	return map[string]any{
		"description": "List, read, create, update or delete source configs.",
		"oneOf": []any{
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"mode": mode("list")},
				"required":   []string{"mode"},
			},
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"mode": mode("get"), "source_id": str("Source to read")},
				"required":   []string{"mode", "source_id"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":     mode("create"),
					"name":     str("Display name, also the source_name filter of the search tools"),
					"type":     sourceType,
					"config":   config("The API source's config, same keys as the UI and the sources export file."),
					"disabled": map[string]any{"type": "boolean"},
				},
				"required": []string{"mode", "name", "type", "config"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":      mode("update"),
					"source_id": str("Source to change"),
					"name":      str("New display name"),
					"type":      sourceType,
					"config":    config(`Keys to change, merged into the existing config; "" removes a key.`),
					"disabled":  map[string]any{"type": "boolean"},
				},
				"required": []string{"mode", "source_id"},
			},
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":      mode("delete"),
					"source_id": str("Source to delete"),
					"confirm":   map[string]any{"type": "boolean", "description": "true to delete; otherwise only reports what would be deleted"},
				},
				"required": []string{"mode", "source_id"},
			},
		},
	}
}

// RegisterSourcesTool registers the manage_sources tool. Called once at
// startup, alongside RegisterSyncTool.
func RegisterSourcesTool(s *server.MCPServer, mgr SourceManager, runner SyncRunner) {
	tool := mcp.NewTool("manage_sources",
		mcp.WithDescription(`Configure Conduit's API sources — the systems its search tools index. `+
			`Pass request={"mode":"list"} for every source (id, name, type, provider, disabled, sync_status), `+
			`the stored credential names and the source types. `+
			`{"mode":"get","source_id":...} returns one source with its config. `+
			`{"mode":"create","name":...,"type":...,"config":{...}} adds an API source; `+
			`{"mode":"update","source_id":...,...} changes name, type, disabled or merges config keys. `+
			`Credentials are referenced by name only: ask the user which stored credential to use and never ask for `+
			`or pass a token or password. Saving doesn't index anything — call sync_sources trigger afterwards. `+
			`{"mode":"delete","source_id":...} reports what would be removed; repeat with "confirm":true to delete `+
			`the source and its indexed data. Manual (pasted) document sources can only be edited in the UI.`),
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
	switch mode, _ := req["mode"].(string); mode {
	case "list":
		all, err := mgr.ListAll()
		if err != nil {
			return nil, fmt.Errorf("reading sources: %w", err)
		}
		out := make([]sourceSummary, 0, len(all))
		for _, src := range all {
			out = append(out, sourceSummary{
				ID: src.ID, Name: src.Name, Type: src.Type, Provider: src.GetConfig("Provider"),
				Disabled: src.Disabled, SyncStatus: syncStatusOf(src),
			})
		}
		types := make([]map[string]string, len(sourcemgmt.Types))
		for i, t := range sourcemgmt.Types {
			types[i] = map[string]string{"type": t.Value, "label": t.Label, "description": t.Description}
		}
		return map[string]any{"sources": out, "credentials": credentialNames(mgr), "types": types}, nil

	case "get":
		src, err := lookup(mgr, req)
		if err != nil {
			return nil, err
		}
		return map[string]any{"source": viewOf(*src)}, nil

	case "create":
		src := models.SourceDefinition{ID: uuid.NewString(), SyncStatus: "idle", Config: map[string]string{"Provider": "api"}}
		if err := applyEdits(&src, req, mgr, true); err != nil {
			return nil, err
		}
		if err := mgr.Save(ctx, "", &src); err != nil {
			return nil, fmt.Errorf("saving source: %w", err)
		}
		return map[string]any{"source": viewOf(src), "next": "Call sync_sources trigger with this id to index it."}, nil

	case "update":
		src, err := lookup(mgr, req)
		if err != nil {
			return nil, err
		}
		if err := editable(*src, runner); err != nil {
			return nil, err
		}
		oldCollection := sources.CollectionFor(src)
		if err := applyEdits(src, req, mgr, false); err != nil {
			return nil, err
		}
		if err := mgr.Save(ctx, oldCollection, src); err != nil {
			return nil, fmt.Errorf("saving source: %w", err)
		}
		return map[string]any{"source": viewOf(*src), "next": "Call sync_sources trigger with this id to re-index it."}, nil

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
		return nil, fmt.Errorf(`request.mode must be "list", "get", "create", "update" or "delete", got %q`, mode)
	}
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

// editable refuses edits the tool must not make: manual sources (UI-only)
// and sources mid-sync (the sync would index the old config).
func editable(src models.SourceDefinition, runner SyncRunner) error {
	if src.GetConfig("Provider") == "manual" {
		return fmt.Errorf("source %q is a manual document; edit it in the UI", src.Name)
	}
	if runner.IsSyncing(src.ID) {
		return fmt.Errorf("source %q is syncing; cancel the sync in the UI or wait for it to finish", src.Name)
	}
	return nil
}

// applyEdits applies name/type/disabled/config from req to src and validates
// the result. create requires name, type and config; update applies only the
// fields present.
func applyEdits(src *models.SourceDefinition, req map[string]any, mgr SourceManager, create bool) error {
	before := copyConfig(src.Config)
	if src.Config == nil {
		src.Config = map[string]string{}
	}

	if v, ok := req["name"]; ok || create {
		name, _ := v.(string)
		if name = strings.TrimSpace(name); name == "" {
			return fmt.Errorf("name must be a non-empty string")
		}
		src.Name = name
	}
	if v, ok := req["type"]; ok || create {
		t, _ := v.(string)
		if !sourcemgmt.ValidType(t) {
			return fmt.Errorf("type %q is not a source type; list mode shows the types", t)
		}
		src.Type = t
	}
	if v, ok := req["disabled"]; ok {
		d, isBool := v.(bool)
		if !isBool {
			return fmt.Errorf("disabled must be a boolean")
		}
		src.Disabled = d
	}
	if v, ok := req["config"]; ok || create {
		patch, isMap := v.(map[string]any)
		if !isMap {
			return fmt.Errorf("config must be an object of string values")
		}
		for key, raw := range patch {
			if !slices.Contains(sourcemgmt.APIConfigKeys, key) && !slices.Contains(sourcemgmt.PresetConfigKeys, key) {
				return fmt.Errorf("config key %q is not allowed; allowed keys: %s", key, strings.Join(sourcemgmt.APIConfigKeys, ", "))
			}
			val, err := configValue(raw)
			if err != nil {
				return fmt.Errorf("config.%s: %w", key, err)
			}
			if val == "" {
				delete(src.Config, key)
			} else {
				src.Config[key] = val
			}
		}
	}

	if err := checkCredentials(src.Config, before, credentialNames(mgr)); err != nil {
		return err
	}
	if msg := sourcemgmt.Validate(src.Type, src.Config); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// checkCredentials requires every credential field that this edit sets to
// name a stored credential. A value that isn't one is most likely a pasted
// secret, which would otherwise be stored in plain conduit-sources.json; it
// is not echoed back. Fields left unchanged are not re-checked, so a source
// whose credential was deleted can still be renamed.
func checkCredentials(cfg, before map[string]string, known []string) error {
	for _, key := range sourcemgmt.CredentialKeys {
		name := cfg[key]
		if name == "" || name == before[key] || slices.Contains(known, name) {
			continue
		}
		stored := "none yet"
		if len(known) > 0 {
			stored = strings.Join(known, ", ")
		}
		return fmt.Errorf("config.%s must be the name of a stored credential, never a secret value, and the "+
			"given value is not one. Stored credentials: %s. If the right one is missing, ask the user to add it at "+
			"/credentials in the Conduit UI", key, stored)
	}
	return nil
}

// configValue accepts strings, plus numbers and booleans that models often
// send for fields like Top or VerifySSL.
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

func credentialNames(mgr SourceManager) []string {
	names := mgr.CredentialNames()
	if names == nil {
		names = []string{}
	}
	return names
}

func syncStatusOf(src models.SourceDefinition) string {
	if src.SyncStatus == "" {
		return "idle"
	}
	return src.SyncStatus
}

// viewOf copies src for output. Manual document content is replaced with the
// placeholder, as in the sources export, to keep responses small.
func viewOf(src models.SourceDefinition) sourceView {
	cfg := copyConfig(src.Config)
	if cfg["Provider"] == "manual" || cfg["DocType"] == "upload" {
		cfg["Content"] = models.DocumentPlaceholder
	}
	return sourceView{
		ID: src.ID, Name: src.Name, Type: src.Type, Disabled: src.Disabled,
		SyncStatus: syncStatusOf(src), LastSyncedAt: src.LastSyncedAt, Config: cfg,
	}
}

// copyConfig returns a copy of m (never nil).
func copyConfig(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
