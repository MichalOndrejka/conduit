package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
)

// exploreTimeout bounds one tree-level listing.
const exploreTimeout = 30 * time.Second

// exploredNode is one listed node: where it is, whether it has children to
// explore, which source types it offers and which of them are selected.
type exploredNode struct {
	Ref         providers.NodeRef `json:"ref"`
	Name        string            `json:"name"`
	Detail      string            `json:"detail,omitempty"`
	HasChildren bool              `json:"has_children"`
	Offers      []string          `json:"offers,omitempty"`
	// Selected maps an offered type to "on" (selected here) or "inherited"
	// (covered by a selection further up); unselected types are left out.
	Selected map[string]string `json:"selected,omitempty"`
}

// RegisterExploreTool registers the provider_explore tool. Called once at
// startup, alongside RegisterSourcesTool.
func RegisterExploreTool(s *server.MCPServer, mgr SourceManager) {
	tool := mcp.NewTool("provider_explore",
		mcp.WithDescription(`Browse what a provider's token can see — the Azure DevOps organizations, projects, `+
			`repositories with their folders and files, work item types, pipeline folders and wikis, or the GitHub `+
			`owners, repositories with their folders and files, issue types and workflows — to find what to add as `+
			`sources. Read-only. Omit "node" for the provider's root, then pass a returned node's ref as "node" to `+
			`list its children (has_children). Each node lists the source types it "offers" and which are already `+
			`"selected" ("on", or "inherited" from a selected parent). Add one with manage_source `+
			`{"mode":"select","provider_id":...,"node":<ref>,"type":<offered type>}. provider_id may be omitted when `+
			`only one provider is configured; manage_source list mode lists them.`),
		mcp.WithString("provider_id", mcp.Description("Provider to browse (ids from manage_source list mode)")),
		func(t *mcp.Tool) {
			t.InputSchema.Properties["node"] = nodeRefSchema("A node's ref from a previous call; omit for the root")
		},
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		payload, err := handleExplore(ctx, req.GetArguments(), mgr)
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

func handleExplore(ctx context.Context, args map[string]any, mgr SourceManager) (map[string]any, error) {
	id, _ := args["provider_id"].(string)
	if id = strings.TrimSpace(id); id == "" {
		provs, err := mgr.ListProviders()
		if err != nil {
			return nil, err
		}
		switch len(provs) {
		case 0:
			return nil, fmt.Errorf("no provider is configured: ask the user to add one (Azure DevOps or GitHub, with a token) on the Providers page of the Conduit UI")
		case 1:
			id = provs[0].ID
		default:
			names := make([]string, len(provs))
			for i, p := range provs {
				names[i] = fmt.Sprintf("%s (%s, %s)", p.ID, p.Name, p.Kind)
			}
			return nil, fmt.Errorf("several providers are configured; pass provider_id, one of: %s", strings.Join(names, ", "))
		}
	}
	prov, in, err := mgr.ProviderFor(id)
	if err != nil {
		return nil, err
	}
	var parent providers.NodeRef
	if raw, ok := args["node"]; ok && raw != nil {
		if parent, err = nodeRefOf(raw); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, exploreTimeout)
	defer cancel()
	nodes, err := in.Discover(ctx, mgr.Conn(prov), parent)
	if err != nil {
		return nil, err
	}
	states, err := mgr.States(prov.ID, nodes)
	if err != nil {
		return nil, err
	}
	out := make([]exploredNode, len(nodes))
	for i, n := range nodes {
		out[i] = exploredNode{Ref: n.Ref, Name: n.Name, Detail: n.Detail, HasChildren: n.HasChildren, Offers: n.Offers}
		for t, st := range states[i] {
			if st == sourcemgmt.StateOff {
				continue
			}
			if out[i].Selected == nil {
				out[i].Selected = map[string]string{}
			}
			out[i].Selected[t] = st
		}
	}
	return map[string]any{
		"provider": map[string]string{"id": prov.ID, "name": prov.Name, "kind": prov.Kind},
		"parent":   parent,
		"nodes":    out,
	}, nil
}
