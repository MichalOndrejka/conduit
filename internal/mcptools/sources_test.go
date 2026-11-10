package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
	"github.com/MichalOndrejka/conduit/internal/store"
)

// newSourceManager returns a real manager with one Azure DevOps provider
// "ado" at adoURL, over a Qdrant stub that accepts every request.
func newSourceManager(t *testing.T, adoURL string) *sourcemgmt.Manager {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CONDUIT_SECRET_KEY", "")
	sec, err := secrets.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"count":7},"status":"ok"}`))
	}))
	t.Cleanup(qdrant.Close)
	cfg := &config.AppConfig{}
	cfg.Qdrant.URL = qdrant.URL
	m := sourcemgmt.New(store.NewSourceConfigStore(filepath.Join(dir, "s.json")), rag.NewVectorStore(cfg),
		store.NewProviderStore(filepath.Join(dir, "p.json")), sec)
	if err := m.SaveProvider(&models.Provider{ID: "ado", Name: "Contoso", Kind: "ado", BaseURL: adoURL}, "pat"); err != nil {
		t.Fatal(err)
	}
	return m
}

func manage(t *testing.T, m SourceManager, runner *fakeRunner, req map[string]any) (map[string]any, error) {
	t.Helper()
	return handleManageRequest(context.Background(), map[string]any{"request": req}, m, runner)
}

// roundTrip re-decodes v as an MCP client would see it.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeAdo serves one project with one repository.
func fakeAdo(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_apis/projects":
			_, _ = w.Write([]byte(`{"value":[{"name":"P"}]}`))
		case "/P/_apis/git/repositories":
			_, _ = w.Write([]byte(`{"value":[{"name":"api","defaultBranch":"refs/heads/main"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An agent explores the provider, selects what it found, changes a setting,
// and deletes the source again.
func TestExploreSelectUpdateDelete(t *testing.T) {
	m := newSourceManager(t, fakeAdo(t).URL)
	runner := newFakeRunner()
	ctx := context.Background()

	root, err := handleExplore(ctx, map[string]any{}, m) // the only provider
	if err != nil {
		t.Fatal(err)
	}
	nodes := roundTrip(t, root)["nodes"].([]any)
	project := nodes[0].(map[string]any)
	if project["name"] != "P" || project["has_children"] != true {
		t.Fatalf("root = %+v", nodes)
	}
	groups, err := handleExplore(ctx, map[string]any{"provider_id": "ado", "node": project["ref"]}, m)
	if err != nil {
		t.Fatal(err)
	}
	reposGroup := roundTrip(t, groups)["nodes"].([]any)[0].(map[string]any)
	repos, err := handleExplore(ctx, map[string]any{"node": reposGroup["ref"]}, m)
	if err != nil {
		t.Fatal(err)
	}
	repo := roundTrip(t, repos)["nodes"].([]any)[0].(map[string]any)

	out, err := manage(t, m, runner, map[string]any{"mode": "select", "provider_id": "ado", "node": repo["ref"], "type": models.SourceCodeRepo})
	if err != nil {
		t.Fatal(err)
	}
	src := out["source"].(sourceView)
	if src.Name != "P/api — Source Code" || src.ProviderID != "ado" || !strings.Contains(out["next"].(string), "sync_source") {
		t.Fatalf("select = %+v", out)
	}
	again, _ := handleExplore(ctx, map[string]any{"node": reposGroup["ref"]}, m)
	sel := roundTrip(t, again)["nodes"].([]any)[0].(map[string]any)["selected"].(map[string]any)
	if sel[models.SourceCodeRepo] != "on" {
		t.Errorf("selected = %+v, want code on", sel)
	}

	out, err = manage(t, m, runner, map[string]any{"mode": "update", "source_id": src.ID, "name": "API code",
		"settings": map[string]any{"Branch": "dev", "PathFilter": "/src/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if v := out["source"].(sourceView); v.Name != "API code" || v.Config["Branch"] != "dev" || v.Config["PathFilter"] != "/src/**" {
		t.Errorf("update = %+v", v)
	}
	if _, err := manage(t, m, runner, map[string]any{"mode": "update", "source_id": src.ID, "settings": map[string]any{"Repo": "x"}}); err == nil {
		t.Error("update changed a selection key")
	}

	list, _ := manage(t, m, runner, map[string]any{"mode": "list"})
	l := roundTrip(t, list)
	if p := l["providers"].([]any)[0].(map[string]any); p["id"] != "ado" || p["has_token"] != true || p["kind"] != "ado" {
		t.Errorf("providers = %+v", l["providers"])
	}
	if strings.Contains(string(mustJSON(t, list)), "pat") {
		t.Error("list leaks the token")
	}

	preview, _ := manage(t, m, runner, map[string]any{"mode": "delete", "source_id": src.ID})
	if preview["would_delete"].(map[string]any)["indexed_chunks"] != 7 {
		t.Errorf("preview = %+v", preview)
	}
	runner.syncing = map[string]bool{src.ID: true}
	if _, err := manage(t, m, runner, map[string]any{"mode": "delete", "source_id": src.ID, "confirm": true}); err == nil {
		t.Error("deleted a syncing source")
	}
	runner.syncing = map[string]bool{}
	if _, err := manage(t, m, runner, map[string]any{"mode": "delete", "source_id": src.ID, "confirm": true}); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(src.ID); s != nil {
		t.Error("source still there after delete")
	}
}

func TestManageDeselectNeedsConfirm(t *testing.T) {
	m := newSourceManager(t, "https://dev.azure.com/contoso")
	runner := newFakeRunner()
	group := map[string]any{"kind": "pipelines", "project": "P"}
	if _, err := manage(t, m, runner, map[string]any{"mode": "select", "provider_id": "ado", "node": group, "type": models.SourcePipelineRun}); err != nil {
		t.Fatal(err)
	}
	out, _ := manage(t, m, runner, map[string]any{"mode": "deselect", "provider_id": "ado", "node": group, "type": models.SourcePipelineRun})
	if _, ok := out["note"]; !ok {
		t.Fatalf("deselect without confirm = %+v, want a note", out)
	}
	if all, _ := m.ListAll(); len(all) != 1 {
		t.Fatal("deselect without confirm changed something")
	}
	out, err := manage(t, m, runner, map[string]any{"mode": "deselect", "provider_id": "ado", "node": group, "type": models.SourcePipelineRun, "confirm": true})
	if err != nil || out["deleted"] != true {
		t.Fatalf("deselect = %+v, %v", out, err)
	}
}

func TestManageRejectsBadInput(t *testing.T) {
	m := newSourceManager(t, "https://dev.azure.com/contoso")
	runner := newFakeRunner()
	for name, req := range map[string]map[string]any{
		"mode":         {"mode": "create"},
		"no node":      {"mode": "select", "provider_id": "ado", "type": models.SourceCodeRepo},
		"bad provider": {"mode": "select", "provider_id": "x", "node": map[string]any{"kind": "repo", "project": "P", "repo": "r"}, "type": models.SourceCodeRepo},
		"wrong type":   {"mode": "select", "provider_id": "ado", "node": map[string]any{"kind": "workitemtype", "project": "P", "id": "Bug"}, "type": models.SourceCodeRepo},
		"no source":    {"mode": "get", "source_id": "nope"},
	} {
		if _, err := manage(t, m, runner, req); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
