package sourcemgmt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/store"
)

// newManager returns a manager with an Azure DevOps provider "ado" and a
// GitHub provider "gh", both with tokens, over a Qdrant stub that accepts
// every request.
func newManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", "")
	sec, err := secrets.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"count":0},"status":"ok"}`))
	}))
	t.Cleanup(qdrant.Close)
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = qdrant.URL
	provs := store.NewProviderStore(filepath.Join(dir, "conduit-providers.json"))
	m := New(store.NewSourceConfigStore(filepath.Join(dir, "conduit-sources.json")), rag.NewVectorStore(cfg), provs, sec)
	if err := m.SaveProvider(&models.Provider{ID: "ado", Name: "Contoso", Kind: "ado", BaseURL: "https://dev.azure.com/contoso"}, "pat"); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveProvider(&models.Provider{ID: "gh", Name: "GitHub", Kind: "github", BaseURL: "https://github.com"}, "ghp"); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSaveProvider(t *testing.T) {
	m := newManager(t)
	if got := m.Resolver().GetValue(models.ProviderTokenName("ado")); got != "pat" {
		t.Errorf("stored token = %q, want pat", got)
	}
	for name, c := range map[string]struct {
		p     models.Provider
		token string
		want  string
	}{
		"kind":      {models.Provider{Name: "X", Kind: "jira", BaseURL: "https://x"}, "t", "unknown provider kind"},
		"name":      {models.Provider{Name: " ", Kind: "ado", BaseURL: "https://x"}, "t", "name is required"},
		"url":       {models.Provider{Name: "X", Kind: "ado", BaseURL: "dev.azure.com"}, "t", "valid http(s) URL"},
		"duplicate": {models.Provider{Name: "contoso", Kind: "ado", BaseURL: "https://x"}, "t", "already exists"},
		"ado token": {models.Provider{Name: "X", Kind: "ado", BaseURL: "https://x"}, "", "token is required"},
	} {
		p := c.p
		if err := m.SaveProvider(&p, c.token); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// GitHub works without a token (public repositories).
	if err := m.SaveProvider(&models.Provider{Name: "Public", Kind: "github", BaseURL: "https://github.com"}, ""); err != nil {
		t.Errorf("tokenless GitHub provider: %v", err)
	}
	// An edit without a token keeps the stored one.
	p, _, _ := m.ProviderFor("ado")
	p.Name = "Contoso DevOps"
	if err := m.SaveProvider(p, ""); err != nil {
		t.Fatal(err)
	}
	if got := m.Resolver().GetValue(models.ProviderTokenName("ado")); got != "pat" {
		t.Errorf("token after edit = %q, want kept", got)
	}
}

func TestDeleteProviderRefusesWhileUsed(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	repo := providers.NodeRef{Kind: providers.NodeRepo, Project: "P", Repo: "api", Path: "/"}
	if _, err := m.Toggle(ctx, "ado", repo, "api", models.SourceGitCommits, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteProvider("ado"); err == nil || !strings.Contains(err.Error(), "P/api — Commit History") {
		t.Errorf("err = %v, want the using source named", err)
	}
	srcs, _ := m.SourcesOf("ado")
	if err := m.Delete(ctx, srcs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteProvider("ado"); err != nil {
		t.Fatal(err)
	}
	if m.HasToken("ado") {
		t.Error("token kept after the provider was deleted")
	}
}

// toggler ticks nodes of one provider and reads back the one source a test
// works with.
type toggler struct {
	t *testing.T
	m *Manager
	p string
}

func (tg toggler) tick(ref providers.NodeRef, srcType string, on bool) {
	tg.t.Helper()
	if _, err := tg.m.Toggle(context.Background(), tg.p, ref, ref.Repo, srcType, on, nil); err != nil {
		tg.t.Fatalf("Toggle(%+v, %s, %v): %v", ref, srcType, on, err)
	}
}

func (tg toggler) sources() []models.SourceDefinition {
	srcs, err := tg.m.SourcesOf(tg.p)
	if err != nil {
		tg.t.Fatal(err)
	}
	return srcs
}

func (tg toggler) only() *models.SourceDefinition {
	tg.t.Helper()
	srcs := tg.sources()
	if len(srcs) != 1 {
		tg.t.Fatalf("sources = %+v, want exactly one", srcs)
	}
	return &srcs[0]
}

func (tg toggler) state(ref providers.NodeRef, srcType string) string {
	tg.t.Helper()
	st, err := tg.m.States(tg.p, []providers.Node{{Ref: ref, Offers: []string{srcType}}})
	if err != nil {
		tg.t.Fatal(err)
	}
	return st[0][srcType]
}

func TestToggleRepositoryFiles(t *testing.T) {
	tg := toggler{t, newManager(t), "ado"}
	repo := providers.NodeRef{Kind: providers.NodeRepo, Project: "P", Repo: "api", Path: "/"}
	src := providers.NodeRef{Kind: providers.NodeFolder, Project: "P", Repo: "api", Path: "/src"}
	deep := providers.NodeRef{Kind: providers.NodeFile, Project: "P", Repo: "api", Path: "/src/a.go"}
	readme := providers.NodeRef{Kind: providers.NodeFile, Project: "P", Repo: "api", Path: "/README.md"}
	code := models.SourceCodeRepo

	tg.tick(repo, code, true)
	s := tg.only()
	if s.Name != "P/api — Source Code" || s.GetConfig(providers.KeyPathFilter) != "" || s.GetConfig(providers.KeyRepo) != "api" ||
		s.GetConfig(providers.KeyProject) != "P" || s.ProviderID != "ado" {
		t.Fatalf("whole repository source = %+v", s)
	}
	if tg.state(repo, code) != StateOn || tg.state(src, code) != StateInherited {
		t.Errorf("states = %s / %s, want on / inherited", tg.state(repo, code), tg.state(src, code))
	}
	tg.tick(repo, code, false)
	if len(tg.sources()) != 0 {
		t.Fatal("unticking the whole repository kept its source")
	}

	tg.tick(deep, code, true)
	tg.tick(src, code, true) // the folder replaces the file beneath it
	tg.tick(readme, code, true)
	if got := tg.only().GetConfig(providers.KeyPathFilter); got != "/src/**,/README.md" {
		t.Errorf("PathFilter = %q, want /src/**,/README.md", got)
	}
	if tg.state(deep, code) != StateInherited || tg.state(readme, code) != StateOn || tg.state(repo, code) != StateOff {
		t.Errorf("states deep/readme/repo = %s/%s/%s", tg.state(deep, code), tg.state(readme, code), tg.state(repo, code))
	}
	tg.tick(src, code, false)
	tg.tick(readme, code, false)
	if len(tg.sources()) != 0 {
		t.Error("unticking every part kept the source")
	}

	// Other types of the same repository are separate sources.
	tg.tick(repo, code, true)
	tg.tick(repo, models.SourceGitCommits, true)
	tg.tick(src, models.SourceDocumentation, true)
	if n := len(tg.sources()); n != 3 {
		t.Errorf("%d sources, want one per type", n)
	}
}

func TestToggleAdoPullRequests(t *testing.T) {
	tg := toggler{t, newManager(t), "ado"}
	group := providers.NodeRef{Kind: providers.NodeRepos, Project: "P"}
	api := providers.NodeRef{Kind: providers.NodeRepo, Project: "P", Repo: "api"}
	web := providers.NodeRef{Kind: providers.NodeRepo, Project: "P", Repo: "web"}
	pr := models.SourcePullRequest

	tg.tick(group, pr, true)
	s := tg.only()
	if s.Name != "P — Pull Requests" || s.GetConfig(providers.KeyRepo) != "" || s.GetConfig(providers.KeyRepositories) != "" {
		t.Fatalf("project source = %+v", s)
	}
	if tg.state(api, pr) != StateInherited {
		t.Errorf("repository state = %q, want inherited", tg.state(api, pr))
	}
	tg.tick(api, pr, false) // a part of a whole selection stays
	if tg.only().GetConfig(providers.KeyRepositories) != "" {
		t.Error("unticking an inherited repository narrowed the source")
	}
	tg.tick(group, pr, false)
	tg.tick(api, pr, true)
	tg.tick(web, pr, true)
	if got := tg.only().GetConfig(providers.KeyRepositories); got != "api,web" {
		t.Errorf("Repositories = %q, want api,web", got)
	}
	if tg.state(group, pr) != StateOff || tg.state(web, pr) != StateOn {
		t.Errorf("states group/web = %s/%s", tg.state(group, pr), tg.state(web, pr))
	}
}

func TestToggleAdoPipelineFolders(t *testing.T) {
	tg := toggler{t, newManager(t), "ado"}
	group := providers.NodeRef{Kind: providers.NodePipelines, Project: "P"}
	team := providers.NodeRef{Kind: providers.NodePipelineFolder, Project: "P", Path: `Team`}
	ci := providers.NodeRef{Kind: providers.NodePipelineFolder, Project: "P", Path: `Team\CI`}
	other := providers.NodeRef{Kind: providers.NodePipelineFolder, Project: "P", Path: `Other`}
	runs := models.SourcePipelineRun

	tg.tick(ci, runs, true)
	tg.tick(other, runs, true)
	tg.tick(team, runs, true) // replaces Team\CI
	s := tg.only()
	if s.Name != "P — Pipeline Runs" || s.GetConfig(providers.KeyPipelineFolder) != `Other,Team` {
		t.Fatalf("source = %+v, want folders Other,Team", s)
	}
	if tg.state(ci, runs) != StateInherited || tg.state(group, runs) != StateOff {
		t.Errorf("states ci/group = %s/%s", tg.state(ci, runs), tg.state(group, runs))
	}
	tg.tick(group, runs, true)
	if got := tg.only().GetConfig(providers.KeyPipelineFolder); got != "" {
		t.Errorf("PipelineFolder = %q after ticking the whole project", got)
	}
}

func TestToggleWorkItemTypes(t *testing.T) {
	tg := toggler{t, newManager(t), "ado"}
	bug := providers.NodeRef{Kind: providers.NodeWorkItemType, Project: "P", ID: "Bug"}
	task := providers.NodeRef{Kind: providers.NodeWorkItemType, Project: "P", ID: "Task"}
	req := providers.NodeRef{Kind: providers.NodeWorkItemType, Project: "P", ID: "Requirement"}

	tg.tick(bug, models.SourceWorkItemQuery, true)
	tg.tick(task, models.SourceWorkItemQuery, true)
	tg.tick(req, models.SourceRequirements, true)
	srcs := tg.sources()
	if len(srcs) != 2 {
		t.Fatalf("sources = %+v, want work items and requirements", srcs)
	}
	for _, s := range srcs {
		switch s.Type {
		case models.SourceWorkItemQuery:
			if s.GetConfig(providers.KeyWorkItemTypes) != "Bug,Task" {
				t.Errorf("work items = %+v", s.Config)
			}
		case models.SourceRequirements:
			if s.GetConfig(providers.KeyFetchMode) != providers.FetchModeWorkItems || s.GetConfig(providers.KeyWorkItemTypes) != "Requirement" {
				t.Errorf("requirements = %+v", s.Config)
			}
		}
	}
	tg.tick(bug, models.SourceWorkItemQuery, false)
	tg.tick(task, models.SourceWorkItemQuery, false)
	if len(tg.sources()) != 1 {
		t.Error("unticking every type kept the work items source")
	}
}

func TestToggleGitHub(t *testing.T) {
	tg := toggler{t, newManager(t), "gh"}
	repo := providers.NodeRef{Kind: providers.NodeRepo, Org: "acme", Repo: "app", Path: "/"}
	issues := providers.NodeRef{Kind: providers.NodeIssues, Org: "acme", Repo: "app"}
	bug := providers.NodeRef{Kind: providers.NodeIssueType, Org: "acme", Repo: "app", ID: "Bug"}
	workflows := providers.NodeRef{Kind: providers.NodePipelines, Org: "acme", Repo: "app"}

	tg.tick(repo, models.SourcePullRequest, true)
	tg.tick(workflows, models.SourcePipelineRun, true)
	tg.tick(bug, models.SourceWorkItemQuery, true)
	if tg.state(issues, models.SourceWorkItemQuery) != StateOff || tg.state(bug, models.SourceWorkItemQuery) != StateOn {
		t.Error("an issue type tick should select only that type")
	}
	tg.tick(issues, models.SourceWorkItemQuery, true)
	for _, s := range tg.sources() {
		if s.GetConfig(providers.KeyOrg) != "acme" || s.GetConfig(providers.KeyRepo) != "app" {
			t.Errorf("%s config = %+v, want owner acme and repository app", s.Name, s.Config)
		}
		if s.Type == models.SourceWorkItemQuery && s.GetConfig(providers.KeyWorkItemTypes) != "" {
			t.Errorf("issues source narrowed after ticking every issue: %+v", s.Config)
		}
		if s.Type == models.SourcePullRequest && s.Name != "acme/app — Pull Requests" {
			t.Errorf("name = %q", s.Name)
		}
	}
	if n := len(tg.sources()); n != 3 {
		t.Errorf("%d sources, want pull requests, runs and issues", n)
	}
}

func TestToggleRejectsWrongNode(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	folder := providers.NodeRef{Kind: providers.NodeFolder, Project: "P", Repo: "api", Path: "/src"}
	if _, err := m.Toggle(ctx, "ado", folder, "", models.SourceGitCommits, true, nil); err == nil {
		t.Error("commit history accepted on a folder")
	}
	if _, err := m.Toggle(ctx, "ado", folder, "", models.SourceCodeRepo, true, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	srcs, _ := m.SourcesOf("ado")
	if _, err := m.Toggle(ctx, "ado", folder, "", models.SourceCodeRepo, false, func(id string) bool { return id == srcs[0].ID }); err == nil ||
		!strings.Contains(err.Error(), "syncing") {
		t.Errorf("err = %v, want a refusal while syncing", err)
	}
}

func TestUpdateSettings(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	src, err := m.Toggle(ctx, "ado", providers.NodeRef{Kind: providers.NodePipelines, Project: "P"}, "", models.SourcePipelineRun, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	off := true
	got, err := m.UpdateSettings(ctx, src.ID, "CI runs", &off, map[string]string{providers.KeyRuns: "12"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "CI runs" || !got.Disabled || got.GetConfig(providers.KeyRuns) != "12" {
		t.Errorf("updated = %+v", got)
	}
	if _, err := m.UpdateSettings(ctx, src.ID, "", nil, map[string]string{providers.KeyRuns: "500"}); err == nil {
		t.Error("Runs 500 accepted")
	}
	if _, err := m.UpdateSettings(ctx, src.ID, "", nil, map[string]string{providers.KeyProject: "Other"}); err == nil ||
		!strings.Contains(err.Error(), "not a setting") {
		t.Errorf("err = %v, want selection keys refused", err)
	}
	if _, err := m.UpdateSettings(ctx, src.ID, "", nil, map[string]string{providers.KeyRuns: ""}); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(src.ID); s.GetConfig(providers.KeyRuns) != "" {
		t.Error(`"" didn't remove the setting`)
	}
}

func TestImportPlan(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	// Already selected here: the commits of P/api.
	repo := providers.NodeRef{Kind: providers.NodeRepo, Project: "P", Repo: "api", Path: "/"}
	if _, err := m.Toggle(ctx, "ado", repo, "api", models.SourceGitCommits, true, nil); err != nil {
		t.Fatal(err)
	}
	file := []byte(`{
	  "providers": [
	    {"id": "ado", "name": "Whatever", "kind": "ado", "base_url": "https://x"},
	    {"id": "new", "name": "contoso", "kind": "ado", "base_url": "https://tfs.example.com/tfs/"},
	    {"id": "bad", "name": "Jira", "kind": "jira", "base_url": "https://jira"}
	  ],
	  "sources": [
	    {"id": "d1", "name": "Again", "type": "commit-history", "provider_id": "ado", "config": {"Project": "P", "Repo": "api"}},
	    {"id": "n1", "name": "Runs", "type": "pipeline-run", "provider_id": "new", "sync_status": "failed", "config": {"Project": "Q"}},
	    {"id": "n2", "name": "Runs twice", "type": "pipeline-run", "provider_id": "new", "config": {"Project": "Q"}},
	    {"id": "o1", "name": "Old", "type": "code", "config": {"Provider": "api"}},
	    {"id": "j1", "name": "Jira issues", "type": "work-item", "provider_id": "bad", "config": {}},
	    {"id": "i1", "name": "No repo", "type": "code", "provider_id": "ado", "config": {"Project": "P"}}
	  ]
	}`)
	plan, err := m.PlanImport(file)
	if err != nil {
		t.Fatal(err)
	}
	prov := map[string]PlannedProvider{}
	for _, p := range plan.Providers {
		prov[p.Action+":"+p.Name] = p
	}
	if _, ok := prov[ImportExists+":Contoso"]; !ok {
		t.Errorf("same-ID provider not kept as the local one: %+v", plan.Providers)
	}
	if p, ok := prov[ImportRename+":contoso (imported)"]; !ok || !p.NeedsToken || p.URL != "https://tfs.example.com/tfs/" {
		t.Errorf("name clash not renamed: %+v", plan.Providers)
	}
	if p, ok := prov[ImportInvalid+":Jira"]; !ok || !strings.Contains(p.Note, "jira") {
		t.Errorf("unknown kind not refused: %+v", plan.Providers)
	}
	want := map[string]string{
		"Again": ImportDuplicate, "Runs": ImportAdd, "Runs twice": ImportDuplicate,
		"Old": ImportNoProvider, "Jira issues": ImportNoProvider, "No repo": ImportInvalid,
	}
	for _, s := range plan.Sources {
		if want[s.Name] != s.Action {
			t.Errorf("%s: action %s (%s), want %s", s.Name, s.Action, s.Note, want[s.Name])
		}
	}
	if plan.AddProviders != 1 || plan.AddSources != 1 {
		t.Errorf("adds %d providers, %d sources; want 1, 1", plan.AddProviders, plan.AddSources)
	}
	if n, _ := m.ListProviders(); len(n) != 2 {
		t.Fatal("planning wrote providers")
	}

	if err := m.ApplyImport(plan); err != nil {
		t.Fatal(err)
	}
	p, _, err := m.ProviderFor("new")
	if err != nil || p.Name != "contoso (imported)" || p.BaseURL != "https://tfs.example.com/tfs" || m.HasToken("new") {
		t.Errorf("imported provider = %+v, %v", p, err)
	}
	srcs, _ := m.SourcesOf("new")
	if len(srcs) != 1 || srcs[0].ID != "n1" || srcs[0].SyncStatus != "idle" {
		t.Errorf("imported sources = %+v", srcs)
	}
	// Importing the same file again adds nothing.
	again, err := m.PlanImport(file)
	if err != nil || again.AddProviders != 0 || again.AddSources != 0 {
		t.Errorf("re-import plan adds %d providers, %d sources (%v); want none", again.AddProviders, again.AddSources, err)
	}
}

func TestImportPlanOldFormat(t *testing.T) {
	m := newManager(t)
	plan, err := m.PlanImport([]byte(`[{"id":"s1","name":"Old","type":"code","config":{"Url":"https://x"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.AddSources != 0 || len(plan.Sources) != 1 || plan.Sources[0].Action != ImportNoProvider {
		t.Errorf("plan = %+v", plan)
	}
	if _, err := m.PlanImport([]byte(`not json`)); err == nil {
		t.Error("garbage planned")
	}
}
