package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
)

func (h *harness) postJSON(path string, v any) map[string]any {
	h.t.Helper()
	data, _ := json.Marshal(v)
	resp, err := h.client.Post(h.url(path), "application/json", bytes.NewReader(data))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("%s: %v", path, err)
	}
	return out
}

func (h *harness) getJSON(path string) map[string]any {
	h.t.Helper()
	resp := h.get(path)
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("%s: %v", path, err)
	}
	return out
}

// addProvider creates an Azure DevOps provider through the form and returns
// its ID.
func (h *harness) addProvider(baseURL string) string {
	h.t.Helper()
	resp := h.postForm("/providers/create", url.Values{
		"name": {"Contoso"}, "kind": {"ado"}, "base_url_ado": {baseURL}, "token_ado": {"pat"}, "api_version": {"7.0"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("create provider: status %d: %s", resp.StatusCode, bodyString(h.t, resp))
	}
	id := strings.TrimPrefix(resp.Header.Get("Location"), "/providers/")
	if id == "" {
		h.t.Fatal("create provider didn't redirect to its browse page")
	}
	return id
}

func TestProviderCreateValidatesAndStoresToken(t *testing.T) {
	h := newHarness(t)
	body := bodyString(t, h.postForm("/providers/create", url.Values{"name": {"X"}, "kind": {"ado"}, "base_url_ado": {"https://dev.azure.com"}}))
	if !strings.Contains(body, "personal access token is required") {
		t.Error("ADO provider without a token accepted")
	}
	id := h.addProvider("https://dev.azure.com/contoso")
	if got := h.secrets.GetValue(models.ProviderTokenName(id)); got != "pat" {
		t.Errorf("token = %q, want stored", got)
	}
	page := bodyString(t, h.get("/providers"))
	if !strings.Contains(page, "Contoso") || !strings.Contains(page, "https://dev.azure.com/contoso") || strings.Contains(page, "pat\"") {
		t.Error("providers page should list the provider, never its token")
	}
	edit := bodyString(t, h.get("/providers/"+id+"/edit"))
	if !strings.Contains(edit, "Stored — leave blank to keep it") || !strings.Contains(edit, `value="7.0"`) {
		t.Error("edit form doesn't show the stored state")
	}
	if resp := h.get("/credentials"); resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/providers" {
		t.Errorf("/credentials = %d %s, want a redirect to /providers", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// Browse lists the tree with selection states; a tick creates a source,
// which the settings page edits and the Sources page lists under its
// provider; the provider can't be deleted while it has sources.
func TestBrowseSelectAndSettings(t *testing.T) {
	h := newHarness(t)
	ado := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pat, _ := r.BasicAuth(); pat != "pat" {
			t.Errorf("%s: token %q", r.URL.Path, pat)
		}
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
	defer ado.Close()
	id := h.addProvider(ado.URL)

	if page := bodyString(t, h.get("/providers/" + id)); !strings.Contains(page, "Contoso") || !strings.Contains(page, `id="tree"`) {
		t.Fatal("browse page doesn't render the tree")
	}
	root := h.getJSON("/api/providers/" + id + "/discover")
	if root["ok"] != true {
		t.Fatalf("discover root = %+v", root)
	}
	repos := providers.NodeRef{Kind: providers.NodeRepos, Project: "P"}
	ref, _ := json.Marshal(repos)
	listed := h.getJSON("/api/providers/" + id + "/discover?ref=" + url.QueryEscape(string(ref)))
	nodes := listed["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["name"] != "api" {
		t.Fatalf("repositories = %+v", listed)
	}
	repo := providers.NodeRef{Kind: providers.NodeRepo, Project: "P", Repo: "api", Path: "/"}
	if out := h.postJSON("/api/providers/"+id+"/select", map[string]any{"ref": repo, "name": "api", "type": models.SourceGitCommits, "on": true}); out["ok"] != true {
		t.Fatalf("select = %+v", out)
	}
	if out := h.postJSON("/api/providers/"+id+"/select", map[string]any{"ref": repo, "type": models.SourcePipelineRun, "on": true}); out["ok"] != false {
		t.Errorf("runs on a repository accepted: %+v", out)
	}
	states := h.postJSON("/api/providers/"+id+"/states", map[string]any{"nodes": []providers.Node{{Ref: repo, Offers: []string{models.SourceGitCommits}}}})
	if st := states["states"].([]any)[0].(map[string]any); st[models.SourceGitCommits] != "on" {
		t.Errorf("states = %+v", states)
	}

	all, _ := h.sources.ListAll()
	if len(all) != 1 {
		t.Fatalf("sources = %+v", all)
	}
	src := all[0]
	settings := bodyString(t, h.get("/sources/"+src.ID+"/edit"))
	if !strings.Contains(settings, "Max commits") || !strings.Contains(settings, `name="Branch"`) || strings.Contains(settings, `name="PathFilter"`) {
		t.Error("commit history settings should offer branch and max commits, not a path filter")
	}
	resp := h.postForm("/sources/"+src.ID+"/edit", url.Values{"name": {"API history"}, "Branch": {"dev"}, "Top": {"50"}, "enabled": {"on"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save settings: %d %s", resp.StatusCode, bodyString(t, resp))
	}
	saved, _ := h.sources.Get(src.ID)
	if saved.Name != "API history" || saved.GetConfig("Branch") != "dev" || saved.GetConfig("Top") != "50" || saved.Disabled {
		t.Errorf("saved = %+v", saved)
	}
	if body := bodyString(t, h.postForm("/sources/"+src.ID+"/edit", url.Values{"name": {"x"}, "Top": {"-1"}})); !strings.Contains(body, "positive number") {
		t.Error("invalid Top accepted")
	}

	if list := bodyString(t, h.get("/sources")); !strings.Contains(list, "API history") || !strings.Contains(list, `href="/providers/`+id+`"`) {
		t.Error("Sources page doesn't link the source's provider")
	}
	if body := bodyString(t, h.postForm("/providers/"+id+"/delete", nil)); !strings.Contains(body, "delete them first") {
		t.Error("provider with sources deleted")
	}
}

func TestLegacySourceIsFlagged(t *testing.T) {
	h := newHarness(t)
	if err := h.sources.Save(models.SourceDefinition{ID: "old", Name: "Old ADO", Type: models.SourceCodeRepo,
		Config: map[string]string{"Provider": "api", "Url": "https://x"}}); err != nil {
		t.Fatal(err)
	}
	if body := bodyString(t, h.get("/sources")); !strings.Contains(body, "No provider") {
		t.Error("a source without a provider isn't flagged")
	}
}

// uploadJSON posts data as the "file" field of a multipart form and decodes
// the JSON answer.
func (h *harness) uploadJSON(path string, data []byte) map[string]any {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "conduit-configuration.json")
	_, _ = fw.Write(data)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.url(path), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("%s: %v", path, err)
	}
	return out
}

// Export carries providers (never tokens) and sources; Settings shows the
// Configuration card; importing previews first, writes nothing until
// confirmed, and sends the browser to Providers.
func TestExportImportCarriesProviders(t *testing.T) {
	h := newHarness(t)
	id := h.addProvider("https://dev.azure.com/contoso")
	if err := h.sources.Save(models.SourceDefinition{ID: "s1", Name: "Runs", Type: models.SourcePipelineRun, ProviderID: id,
		Config: map[string]string{"Project": "P"}}); err != nil {
		t.Fatal(err)
	}
	if page := bodyString(t, h.get("/settings")); !strings.Contains(page, "Configuration") || !strings.Contains(page, "1 provider · 1 source") {
		t.Error("Settings lacks the Configuration card with counts")
	}
	if page := bodyString(t, h.get("/sources")); strings.Contains(page, `href="/export"`) {
		t.Error("the Sources page still offers Export")
	}
	resp := h.get("/export")
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(data, []byte(`"pat"`)) {
		t.Fatal("export contains the token")
	}

	// Into a fresh instance: the provider arrives without its token.
	h2 := newHarness(t)
	preview := h2.uploadJSON("/import/preview", data)
	plan, _ := preview["plan"].(map[string]any)
	if preview["ok"] != true || plan["add_providers"] != float64(1) || plan["add_sources"] != float64(1) {
		t.Fatalf("preview = %+v", preview)
	}
	if p := plan["providers"].([]any)[0].(map[string]any); p["needs_token"] != true || p["kind"] != "Azure DevOps" {
		t.Errorf("planned provider = %+v", p)
	}
	if all, _ := h2.sources.ListAll(); len(all) != 0 {
		t.Fatal("the preview wrote sources")
	}
	done := h2.uploadJSON("/import", data)
	if done["ok"] != true || !strings.HasPrefix(done["redirect"].(string), "/providers?notice=") {
		t.Fatalf("import = %+v", done)
	}
	p, _, err := h2.mgr.ProviderFor(id)
	if err != nil || p.Name != "Contoso" || h2.mgr.HasToken(id) {
		t.Errorf("imported provider = %+v, %v; want it, without a token", p, err)
	}
	// The same file again adds nothing.
	if again := h2.uploadJSON("/import/preview", data)["plan"].(map[string]any); again["add_sources"] != float64(0) {
		t.Errorf("re-import plan = %+v", again)
	}
	if bad := h2.uploadJSON("/import/preview", []byte("nope")); bad["ok"] != false {
		t.Errorf("garbage preview = %+v", bad)
	}
}
