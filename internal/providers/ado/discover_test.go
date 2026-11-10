package ado

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
)

func names(nodes []providers.Node) string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return strings.Join(out, ",")
}

func TestDiscoverOrganizationsOfServicesPAT(t *testing.T) {
	vssps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_apis/profile/profiles/me":
			writeJSON(w, map[string]any{"id": "me-1"})
		case "/_apis/accounts":
			if r.URL.Query().Get("memberId") != "me-1" {
				t.Errorf("memberId = %q", r.URL.Query().Get("memberId"))
			}
			writeJSON(w, map[string]any{"value": []any{map[string]any{"accountName": "zeta"}, map[string]any{"accountName": "Alpha"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer vssps.Close()
	defer func(u string) { vsspsURL = u }(vsspsURL)
	vsspsURL = vssps.URL

	nodes, err := Integration{}.Discover(context.Background(), testConn("https://dev.azure.com/", "pat"), providers.NodeRef{})
	if err != nil {
		t.Fatal(err)
	}
	if names(nodes) != "Alpha,zeta" || nodes[0].Ref.Kind != providers.NodeOrg || nodes[0].Ref.Org != "Alpha" {
		t.Errorf("orgs = %+v", nodes)
	}
	if got := OrgURL("https://dev.azure.com", "Alpha"); got != "https://dev.azure.com/Alpha" {
		t.Errorf("orgURL = %q", got)
	}
}

func TestDiscoverServerCollectionsThenProjects(t *testing.T) {
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tfs/_apis/projectCollections":
			writeJSON(w, map[string]any{"value": []any{map[string]any{"name": "DefaultCollection"}}})
		case "/tfs/DefaultCollection/_apis/projects":
			tok := r.URL.Query().Get("continuationToken")
			tokens = append(tokens, tok)
			if tok == "" {
				w.Header().Set("x-ms-continuationtoken", "next")
				writeJSON(w, map[string]any{"value": []any{map[string]any{"name": "Beta"}}})
				return
			}
			writeJSON(w, map[string]any{"value": []any{map[string]any{"name": "alpha"}}})
		default:
			http.NotFound(w, r) // the server root itself has no projects
		}
	}))
	defer srv.Close()
	conn := testConn(srv.URL+"/tfs", "pat")

	root, err := Integration{}.Discover(context.Background(), conn, providers.NodeRef{})
	if err != nil {
		t.Fatal(err)
	}
	if names(root) != "DefaultCollection" || root[0].Ref.Kind != providers.NodeOrg {
		t.Fatalf("root = %+v, want the server's collections", root)
	}
	projects, err := Integration{}.Discover(context.Background(), conn, root[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if names(projects) != "alpha,Beta" || projects[0].Ref.Org != "DefaultCollection" {
		t.Errorf("projects = %+v, want both pages sorted, under the collection", projects)
	}
	if strings.Join(tokens, "|") != "|next" {
		t.Errorf("continuation tokens sent = %q", tokens)
	}
}

func TestDiscoverSingleOrganizationURLListsProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/org/_apis/projects" {
			writeJSON(w, map[string]any{"value": []any{map[string]any{"name": "P"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	nodes, err := Integration{}.Discover(context.Background(), testConn(srv.URL+"/org", "pat"), providers.NodeRef{})
	if err != nil {
		t.Fatal(err)
	}
	if names(nodes) != "P" || nodes[0].Ref.Org != "" {
		t.Errorf("nodes = %+v, want the project with no organization (the URL is one)", nodes)
	}
}

func TestDiscoverRepoSkipsEmptyAndDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []any{
			map[string]any{"name": "live", "defaultBranch": "refs/heads/develop"},
			map[string]any{"name": "empty"},
			map[string]any{"name": "old", "defaultBranch": "refs/heads/main", "isDisabled": true},
		}})
	}))
	defer srv.Close()
	nodes, err := Integration{}.Discover(context.Background(), testConn(srv.URL, "pat"), providers.NodeRef{Kind: providers.NodeRepos, Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		switch n.Name {
		case "live":
			if n.Detail != "develop" || !n.HasChildren || len(n.Offers) == 0 {
				t.Errorf("live = %+v", n)
			}
		case "empty", "old":
			if n.HasChildren || len(n.Offers) != 0 {
				t.Errorf("%s offers selection: %+v", n.Name, n)
			}
		}
	}
}

func TestDiscoverPipelineFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []any{
			map[string]any{"id": 1, "name": "root-ci", "path": `\`},
			map[string]any{"id": 2, "name": "api-ci", "path": `\Team\CI`},
			map[string]any{"id": 3, "name": "api-nightly", "path": `\Team\CI\Nightly`},
			map[string]any{"id": 4, "name": "web-ci", "path": `\Team`},
			map[string]any{"id": 5, "name": "other", "path": `\Teams`},
		}})
	}))
	defer srv.Close()
	conn := testConn(srv.URL, "pat")
	group := providers.NodeRef{Kind: providers.NodePipelines, Project: "p"}

	root, err := Integration{}.Discover(context.Background(), conn, group)
	if err != nil {
		t.Fatal(err)
	}
	if names(root) != "Team,Teams,root-ci" || root[0].Ref.Kind != providers.NodePipelineFolder || len(root[0].Offers) != 1 {
		t.Fatalf("root = %+v, want folders (selectable) then the root's pipelines", root)
	}
	if root[2].Ref.Kind != providers.NodePipeline || len(root[2].Offers) != 0 || root[2].Detail != "id 1" {
		t.Errorf("pipeline leaf = %+v, want an unselectable leaf naming its id", root[2])
	}
	team, err := Integration{}.Discover(context.Background(), conn, root[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if names(team) != "CI,web-ci" || team[0].Ref.Path != `Team\CI` {
		t.Errorf("Team = %+v, want CI (Team/CI) and web-ci only — not Teams", team)
	}
}

func TestDiscoverWikis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"value": []any{
			map[string]any{"name": "P.wiki", "type": "projectWiki", "repositoryId": "r1", "mappedPath": "/"},
			map[string]any{"name": "Docs", "type": "codeWiki", "repositoryId": "r2", "mappedPath": "/docs/wiki"},
		}})
	}))
	defer srv.Close()
	nodes, err := Integration{}.Discover(context.Background(), testConn(srv.URL, "pat"), providers.NodeRef{Kind: providers.NodeWikis, Project: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Ref.Repo != "r2" || nodes[0].Ref.Kind != providers.NodeFolder || nodes[0].Ref.Path != "/docs/wiki" {
		t.Fatalf("code wiki = %+v", nodes)
	}
	if nodes[1].Ref.Kind != providers.NodeRepo || nodes[1].Ref.Repo != "r1" || nodes[1].Offers[0] != models.SourceDocumentation {
		t.Errorf("project wiki = %+v", nodes[1])
	}
}
