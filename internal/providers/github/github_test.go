package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
)

func init() { providers.RetryBaseDelay = time.Millisecond }

func names(nodes []providers.Node) string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return strings.Join(out, ",")
}

func TestDiscover(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s: Authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/v3/user":
			write(map[string]any{"login": "jane"})
		case "/api/v3/user/orgs":
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `<`+srv.URL+`/api/v3/user/orgs?per_page=100&page=2>; rel="next"`)
				write([]any{map[string]any{"login": "zeta"}})
				return
			}
			write([]any{map[string]any{"login": "acme"}})
		case "/api/v3/orgs/acme/repos":
			write([]any{
				map[string]any{"name": "web", "default_branch": "main"},
				map[string]any{"name": "api", "default_branch": "develop", "archived": true},
			})
		case "/api/v3/repos/acme/api/contents":
			write([]any{
				map[string]any{"name": "README.md", "path": "README.md", "type": "file"},
				map[string]any{"name": "src", "path": "src", "type": "dir"},
			})
		case "/api/v3/orgs/acme/issue-types":
			http.NotFound(w, r)
		case "/api/v3/repos/acme/api/actions/workflows":
			write(map[string]any{"workflows": []any{map[string]any{"id": 7, "name": "CI", "path": ".github/workflows/ci.yml", "state": "active"}}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	conn := providers.Conn{Provider: models.Provider{Kind: Kind, BaseURL: srv.URL}, Token: "tok"}
	ctx := context.Background()
	in := Integration{}

	owners, err := in.Discover(ctx, conn, providers.NodeRef{})
	if err != nil {
		t.Fatal(err)
	}
	if names(owners) != "jane,acme,zeta" {
		t.Fatalf("owners = %s, want you first, then both pages of organizations", names(owners))
	}
	repos, err := in.Discover(ctx, conn, owners[1].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if names(repos) != "api,web" || repos[0].Detail != "develop · archived" || len(repos[0].Offers) == 0 {
		t.Fatalf("repos = %+v", repos)
	}
	kids, err := in.Discover(ctx, conn, repos[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if names(kids) != "Issues,Workflows,src,README.md" || kids[2].Ref.Path != "/src" || kids[2].Ref.Kind != providers.NodeFolder {
		t.Fatalf("repository children = %+v", kids)
	}
	types, err := in.Discover(ctx, conn, kids[0].Ref)
	if err != nil || len(types) != 0 {
		t.Errorf("issue types of an owner without them = %+v, %v; want none", types, err)
	}
	flows, err := in.Discover(ctx, conn, kids[1].Ref)
	if err != nil || names(flows) != "CI" || flows[0].Detail != "ci.yml" || len(flows[0].Offers) != 0 {
		t.Errorf("workflows = %+v, %v", flows, err)
	}
}
