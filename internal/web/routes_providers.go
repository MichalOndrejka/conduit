// Provider handlers: the Providers page (an Azure DevOps or GitHub account
// with its token), a provider's browse tree (everything its token sees,
// ticked into sources), a source's settings page, and the sources
// export/import.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/providers/registry"
	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
)

// discoverTimeout bounds one tree-level listing.
const discoverTimeout = 30 * time.Second

// ── Providers page ──────────────────────────────────────────────────────────

type providerView struct {
	models.Provider
	Label    string
	HasToken bool
	Sources  int
}

type kindOption struct {
	Kind, Label, DefaultURL, Hint string
}

func kindOptions() []kindOption {
	var out []kindOption
	for _, in := range registry.All() {
		out = append(out, kindOption{in.Kind(), in.Label(), in.DefaultBaseURL(), in.BaseURLHint()})
	}
	return out
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	s.renderProviders(w, r.URL.Query().Get("error"), r.URL.Query().Get("notice"))
}

func (s *Server) renderProviders(w http.ResponseWriter, errMsg, notice string) {
	provs, err := s.mgr.ListProviders()
	if err != nil {
		httpError(w, err)
		return
	}
	all, _ := s.sources.ListAll()
	views := make([]providerView, 0, len(provs))
	for _, p := range provs {
		v := providerView{Provider: p, Label: p.Kind, HasToken: s.mgr.HasToken(p.ID)}
		if in, ok := registry.Get(p.Kind); ok {
			v.Label = in.Label()
		}
		for _, src := range all {
			if src.ProviderID == p.ID {
				v.Sources++
			}
		}
		views = append(views, v)
	}
	s.render(w, s.providersTmpl, "base", map[string]any{
		"Active": "providers", "Providers": views, "Error": errMsg, "Notice": notice,
	})
}

func (s *Server) renderProviderForm(w http.ResponseWriter, p *models.Provider, isNew bool, errMsg string) {
	kind := p.Kind
	if kind == "" {
		kind = registry.All()[0].Kind()
	}
	s.render(w, s.providerFormTmpl, "base", map[string]any{
		"Active": "providers", "Provider": p, "IsNew": isNew, "Kind": kind,
		"Kinds": kindOptions(), "HasToken": !isNew && s.mgr.HasToken(p.ID), "Error": errMsg,
	})
}

func (s *Server) handleProviderCreateGet(w http.ResponseWriter, r *http.Request) {
	s.renderProviderForm(w, &models.Provider{Kind: r.URL.Query().Get("kind")}, true, "")
}

// providerFromForm reads the provider form; the base URL comes from the
// chosen kind's tab.
func providerFromForm(r *http.Request, p *models.Provider) string {
	_ = r.ParseForm()
	p.Name = r.FormValue("name")
	p.Kind = r.FormValue("kind")
	p.BaseURL = r.FormValue("base_url_" + p.Kind)
	p.SkipTLSVerify = r.FormValue("skip_tls_verify") == "on"
	p.APIVersion = ""
	if p.Kind == "ado" {
		p.APIVersion = r.FormValue("api_version")
	}
	return r.FormValue("token_" + p.Kind)
}

func (s *Server) handleProviderCreatePost(w http.ResponseWriter, r *http.Request) {
	p := &models.Provider{}
	token := providerFromForm(r, p)
	if err := s.mgr.SaveProvider(p, token); err != nil {
		p.ID = ""
		s.renderProviderForm(w, p, true, err.Error())
		return
	}
	http.Redirect(w, r, "/providers/"+p.ID, http.StatusSeeOther)
}

func (s *Server) handleProviderEditGet(w http.ResponseWriter, r *http.Request) {
	p, _, err := s.mgr.ProviderFor(r.PathValue("id"))
	if err != nil {
		http.Redirect(w, r, "/providers", http.StatusFound)
		return
	}
	s.renderProviderForm(w, p, false, "")
}

func (s *Server) handleProviderEditPost(w http.ResponseWriter, r *http.Request) {
	p, _, err := s.mgr.ProviderFor(r.PathValue("id"))
	if err != nil {
		http.Redirect(w, r, "/providers", http.StatusFound)
		return
	}
	kind := p.Kind
	token := providerFromForm(r, p)
	if p.Kind != kind {
		// Sources compiled for one kind can't be read by another.
		if used, _ := s.mgr.SourcesOf(p.ID); len(used) > 0 {
			p.Kind = kind
			s.renderProviderForm(w, p, false, "A provider with sources can't change its kind — delete its sources first.")
			return
		}
	}
	if err := s.mgr.SaveProvider(p, token); err != nil {
		s.renderProviderForm(w, p, false, err.Error())
		return
	}
	http.Redirect(w, r, "/providers", http.StatusSeeOther)
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeleteProvider(r.PathValue("id")); err != nil {
		s.renderProviders(w, err.Error(), "")
		return
	}
	http.Redirect(w, r, "/providers", http.StatusSeeOther)
}

// handleProviderTest checks that the provider answers with its token.
func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	p, in, err := s.mgr.ProviderFor(r.PathValue("id"))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), discoverTimeout)
	defer cancel()
	if err := in.Test(ctx, s.mgr.Conn(p)); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ── Browse ──────────────────────────────────────────────────────────────────

// typeOption labels a source type on the browse page's toggles.
type typeOption struct{ Value, Label string }

// shortTypeLabels are the compact toggle labels of the browse tree.
var shortTypeLabels = map[string]string{
	models.SourceCodeRepo:      "Code",
	models.SourceTestCodeRepo:  "Test Code",
	models.SourceDocumentation: "Docs",
	models.SourceRequirements:  "Requirements",
	models.SourceTestCase:      "Test Cases",
	models.SourceGitCommits:    "Commits",
	models.SourceWorkItemQuery: "Work Items",
	models.SourcePipelineRun:   "Runs",
	models.SourcePullRequest:   "Pull Requests",
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p, in, err := s.mgr.ProviderFor(r.PathValue("id"))
	if err != nil {
		http.Redirect(w, r, "/providers", http.StatusFound)
		return
	}
	labels := make([]typeOption, 0, len(sourcemgmt.Types))
	for _, t := range sourcemgmt.Types {
		labels = append(labels, typeOption{t.Value, shortTypeLabels[t.Value]})
	}
	s.render(w, s.browseTmpl, "base", map[string]any{
		"Active":        "providers",
		"Provider":      p,
		"ProviderLabel": in.Label(),
		"HasToken":      s.mgr.HasToken(p.ID),
		"Sources":       s.selectedSources(p.ID),
		"TypeLabels":    labels,
	})
}

// browseNode is a discovered node with its selection state.
type browseNode struct {
	providers.Node
	State sourcemgmt.NodeState `json:"state"`
}

// handleDiscover lists one level of the provider's tree: ?ref= is the parent
// node's ref as JSON (absent = the root).
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	var parent providers.NodeRef
	if raw := r.URL.Query().Get("ref"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &parent); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "bad ref: " + err.Error()})
			return
		}
	}
	p, in, err := s.mgr.ProviderFor(r.PathValue("id"))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), discoverTimeout)
	defer cancel()
	nodes, err := in.Discover(ctx, s.mgr.Conn(p), parent)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	states, err := s.mgr.States(p.ID, nodes)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]browseNode, len(nodes))
	for i, n := range nodes {
		out[i] = browseNode{Node: n, State: states[i]}
	}
	writeJSON(w, map[string]any{"ok": true, "nodes": out})
}

// handleStates recomputes the selection state of the nodes the page shows,
// from the stored sources only (no provider requests).
func (s *Server) handleStates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nodes []providers.Node `json:"nodes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	states, err := s.mgr.States(r.PathValue("id"), req.Nodes)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "states": states, "sources": s.selectedSources(r.PathValue("id"))})
}

// handleSelect applies one tick. Configuration only — Sync fetches.
func (s *Server) handleSelect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref  providers.NodeRef `json:"ref"`
		Name string            `json:"name"`
		Type string            `json:"type"`
		On   bool              `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.mgr.Toggle(r.Context(), r.PathValue("id"), req.Ref, req.Name, req.Type, req.On, s.sync.IsSyncing); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// selectedSource summarizes one source of the provider for the browse
// page's sidebar.
type selectedSource struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
	Live   bool   `json:"live"`
}

func (s *Server) selectedSources(providerID string) []selectedSource {
	srcs, _ := s.mgr.SourcesOf(providerID)
	sort.Slice(srcs, func(i, j int) bool { return strings.ToLower(srcs[i].Name) < strings.ToLower(srcs[j].Name) })
	out := make([]selectedSource, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, selectedSource{
			ID: src.ID, Name: src.Name, Type: sourcemgmt.Label(src.Type), Detail: selectionDetail(&src),
			Live: models.IsLiveType(src.Type),
		})
	}
	return out
}

// selectionDetail names what a source's ticks narrowed it to.
func selectionDetail(src *models.SourceDefinition) string {
	for _, k := range []string{providers.KeyPathFilter, providers.KeyWorkItemTypes, providers.KeyRepositories, providers.KeyPipelineFolder} {
		if v := src.GetConfig(k); v != "" {
			return strings.ReplaceAll(v, ",", ", ")
		}
	}
	return ""
}

// handleProviderSync syncs every enabled, synced (not live) source of one
// provider.
func (s *Server) handleProviderSync(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.mgr.SourcesOf(r.PathValue("id"))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var ids []string
	for _, src := range srcs {
		if !src.Disabled && !models.IsLiveType(src.Type) {
			ids = append(ids, src.ID)
		}
	}
	go s.sync.SyncSelected(context.Background(), ids)
	writeJSON(w, map[string]any{"ok": true, "started": len(ids)})
}

// ── Source settings ─────────────────────────────────────────────────────────

// settingField is one editable setting of a source's settings page.
type settingField struct {
	Key, Label, Tip, Value, Placeholder string
	Number                              bool
}

// settingFields lists the settings that apply to src.
func settingFields(src *models.SourceDefinition, kind string) []settingField {
	cfg := src.Config
	var out []settingField
	add := func(key, label, tip, placeholder string, number bool) {
		out = append(out, settingField{Key: key, Label: label, Tip: tip, Value: cfg[key], Placeholder: placeholder, Number: number})
	}
	switch {
	case src.Type == models.SourcePipelineRun:
		add(providers.KeyRuns, "Runs", fmt.Sprintf("How many of the latest runs search_pipeline_run lists when the agent doesn't ask for a number (default 5, at most 100)."), "5", true)
	case src.Type == models.SourcePullRequest:
	case providers.IsWorkItemType(src.Type, cfg):
		if kind == "ado" {
			add(providers.KeyAreaPaths, "Area paths", `Comma-separated, e.g. MyProject\Team A. Sub-areas are included. Empty = every area of the project.`, `MyProject\Team A`, false)
		} else {
			add(providers.KeyLabels, "Labels", "Comma-separated labels; an issue with any of them is fetched. Empty = every issue.", "bug, feature", false)
		}
		add(providers.KeyTop, "Max items", "Most recently changed first. Empty = every item.", "", true)
	case src.Type == models.SourceGitCommits:
		add(providers.KeyBranch, "Branch", "Branch whose history is indexed. Empty = the repository's default branch.", "main", false)
		add(providers.KeyTop, "Max commits", "Most recent first. Every commit embeds its full message and the complete diff of every changed file. Empty = every commit.", "100", true)
	default: // repository files
		add(providers.KeyBranch, "Branch", "Branch the files are read from. Empty = the repository's default branch.", "main", false)
		add(providers.KeyPathFilter, "Path filter", `Comma-separated globs; ticks in the browse tree edit it. "/"-rooted entries match from the repository root (/src/**, /README.md), others any file (*.md, **/*.cs). Empty = every file.`, "/src/**, *.md", false)
	}
	return out
}

// scopeRow is one read-only line of what a source covers.
type scopeRow struct{ Label, Value string }

func scopeRows(src *models.SourceDefinition) []scopeRow {
	var out []scopeRow
	add := func(label, key string) {
		if v := src.GetConfig(key); v != "" {
			out = append(out, scopeRow{label, strings.ReplaceAll(v, ",", ", ")})
		}
	}
	add("Organization", providers.KeyOrg)
	add("Project", providers.KeyProject)
	add("Repository", providers.KeyRepo)
	add("Work item types", providers.KeyWorkItemTypes)
	add("Repositories", providers.KeyRepositories)
	add("Pipeline folders", providers.KeyPipelineFolder)
	return out
}

func (s *Server) renderSourceSettings(w http.ResponseWriter, src *models.SourceDefinition, errMsg string) {
	data := map[string]any{
		"Active": "sources", "Source": src, "TypeLabel": sourcemgmt.Label(src.Type),
		"Live": models.IsLiveType(src.Type), "Scope": scopeRows(src), "Error": errMsg,
	}
	kind := ""
	if p, in, err := s.mgr.ProviderFor(src.ProviderID); err == nil {
		data["Provider"], data["ProviderLabel"] = p, in.Label()
		kind = p.Kind
	} else {
		data["Legacy"] = true
	}
	data["Fields"] = settingFields(src, kind)
	s.render(w, s.sourceSettingsTmpl, "base", data)
}

func (s *Server) handleSourceSettingsGet(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("id"))
	if err != nil || src == nil {
		http.Redirect(w, r, "/sources", http.StatusFound)
		return
	}
	s.renderSourceSettings(w, src, "")
}

func (s *Server) handleSourceSettingsPost(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("id"))
	if err != nil || src == nil {
		http.Redirect(w, r, "/sources", http.StatusFound)
		return
	}
	if s.sync.IsSyncing(src.ID) {
		s.renderSourceSettings(w, src, "This source is syncing — wait for it to finish or cancel it, then change its settings.")
		return
	}
	_ = r.ParseForm()
	kind := ""
	if p, _, err := s.mgr.ProviderFor(src.ProviderID); err == nil {
		kind = p.Kind
	}
	settings := map[string]string{}
	for _, f := range settingFields(src, kind) {
		settings[f.Key] = r.FormValue(f.Key)
	}
	disabled := r.FormValue("enabled") != "on"
	updated, err := s.mgr.UpdateSettings(r.Context(), src.ID, r.FormValue("name"), &disabled, settings)
	if err != nil {
		// Show what was typed, with the error.
		src.Name = r.FormValue("name")
		for k, v := range settings {
			src.Config[k] = v
		}
		s.renderSourceSettings(w, src, err.Error())
		return
	}
	if r.FormValue("action") == "save_sync" && !models.IsLiveType(updated.Type) && !updated.Disabled {
		go s.sync.SyncSelected(context.Background(), []string{updated.ID})
	}
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

// ── Export / import (Settings → Configuration) ──────────────────────────────

func (s *Server) handleExport(w http.ResponseWriter, _ *http.Request) {
	doc, err := s.mgr.Export()
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="conduit-configuration.json"`)
	writeJSON(w, doc)
}

// importPlan reads the uploaded file and plans its import.
func (s *Server) importPlan(r *http.Request) (*sourcemgmt.ImportPlan, error) {
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, fmt.Errorf("no file uploaded")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32<<20)) // 32 MiB cap
	if err != nil {
		return nil, err
	}
	return s.mgr.PlanImport(data)
}

// handleImportPreview shows what importing the uploaded file would do,
// without writing anything.
func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	plan, err := s.importPlan(r)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "plan": plan})
}

// handleImport imports the uploaded file (the same one the preview
// showed), then sends the browser to Providers, where added providers wait
// for their tokens.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	plan, err := s.importPlan(r)
	if err == nil {
		err = s.mgr.ApplyImport(plan)
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	plural := func(n int, word string) string {
		if n == 1 {
			return "1 " + word
		}
		return fmt.Sprintf("%d %ss", n, word)
	}
	msg := fmt.Sprintf("Imported %s and %s.", plural(plan.AddProviders, "provider"), plural(plan.AddSources, "source"))
	if plan.AddProviders > 0 {
		msg += " Set each new provider's token before syncing."
	}
	writeJSON(w, map[string]any{"ok": true, "redirect": "/providers?notice=" + url.QueryEscape(msg)})
}
