package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

type fakeCreds map[string]string

func (f fakeCreds) GetValue(name string) string { return f[name] }

// fakeGitHubPR serves a GitHub Enterprise Server with pull request
// acme/app#7, which changes 25 files.
func fakeGitHubPR(t *testing.T) (*httptest.Server, fakeSources) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	api := "/api/v3/repos/acme/app/pulls/7"
	mux.HandleFunc(api, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "head": map[string]any{"sha": "h"}, "base": map[string]any{"sha": "b"}})
	})
	mux.HandleFunc(api+"/files", func(w http.ResponseWriter, r *http.Request) {
		var files []any
		if r.URL.Query().Get("page") == "1" {
			for i := range 25 {
				files = append(files, map[string]any{"filename": "f" + string(rune('a'+i)) + ".go", "status": "modified",
					"additions": 1, "deletions": 0, "patch": "@@ -1 +1,2 @@\n x\n+y"})
			}
		}
		_ = json.NewEncoder(w).Encode(files)
	})
	return srv, fakeSources{{ID: "gh", Name: "App", Config: map[string]string{
		"Provider": "github", "GitHubUrl": srv.URL, "GitHubOwner": "acme", "GitHubRepo": "app"}}}
}

func TestPullRequest_FilesOmitsDiffsAndPages(t *testing.T) {
	srv, srcs := fakeGitHubPR(t)
	payload, err := handlePullRequestRequest(context.Background(), map[string]any{"request": map[string]any{
		"mode": "files", "url": srv.URL + "/acme/app/pull/7", "page": float64(2)}}, srcs, fakeCreds{})
	if err != nil {
		t.Fatal(err)
	}
	files := payload["files"].([]sources.PRFile)
	if len(files) != 5 || payload["total_files"] != 25 || payload["has_more"] != false || files[0].Diff != "" || files[0].Additions != 1 {
		t.Fatalf("payload %+v", payload)
	}
	if cred := payload["credentials_from"].(map[string]string); cred["source_id"] != "gh" {
		t.Errorf("credentials_from %v", cred)
	}
}

func TestPullRequest_DiffOneFile(t *testing.T) {
	srv, srcs := fakeGitHubPR(t)
	payload, err := handlePullRequestRequest(context.Background(), map[string]any{"request": map[string]any{
		"mode": "diff", "url": srv.URL + "/acme/app/pull/7", "path": "fc.go"}}, srcs, fakeCreds{})
	if err != nil {
		t.Fatal(err)
	}
	files := payload["files"].([]sources.PRFile)
	if len(files) != 1 || files[0].Path != "fc.go" || !strings.Contains(files[0].Diff, "+y") {
		t.Fatalf("files %+v", files)
	}
	if _, paged := payload["page"]; paged {
		t.Error("a single-file diff isn't paged")
	}
}

func TestPullRequest_RequestErrors(t *testing.T) {
	srcs := fakeSources{{ID: "gh", Config: map[string]string{"Provider": "github", "GitHubOwner": "acme", "GitHubRepo": "app"}}}
	for name, req := range map[string]map[string]any{
		"unknown mode":    {"mode": "approve", "url": "https://github.com/acme/app/pull/1"},
		"missing url":     {"mode": "get"},
		"unmatched url":   {"mode": "get", "url": "https://example.com/acme/app/pull/1"},
		"list without id": {"mode": "list"},
		"list unknown id": {"mode": "list", "source_id": "nope"},
		"list bad status": {"mode": "list", "source_id": "gh", "status": "open"},
		"not a pr url":    {"mode": "get", "url": "https://github.com/acme/app/issues/1"},
		"non-repo source": {"mode": "list", "source_id": "wi"},
		"request not obj": nil,
	} {
		all := append(fakeSources{}, srcs...)
		all = append(all, models.SourceDefinition{ID: "wi", Config: map[string]string{"AdoOrg": "https://dev.azure.com/acme", "AdoProject": "P"}})
		args := map[string]any{"request": req}
		if req == nil {
			args = map[string]any{}
		}
		if _, err := handlePullRequestRequest(context.Background(), args, all, fakeCreds{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestPullRequestSchema_HasIndexedAndLiveModes(t *testing.T) {
	var modes []string
	for _, v := range pullRequestRequestSchema()["oneOf"].([]any) {
		m := v.(map[string]any)["properties"].(map[string]any)["mode"].(map[string]any)["const"].(string)
		modes = append(modes, m)
	}
	got := strings.Join(modes, ",")
	if got != "semantic_search,retrieve_chunk,pattern_search,list,get,files,diff,threads,iterations,commits" {
		t.Fatalf("modes %s", got)
	}
}

func TestSearchIndexedPullRequests_BadRequest(t *testing.T) {
	// A malformed search is refused before the vector store is touched.
	res, err := searchIndexedPullRequests(context.Background(), nil, map[string]any{"request": map[string]any{
		"mode": "semantic_search", "page": float64(1)}})
	if err != nil || !res.IsError {
		t.Fatalf("got %+v, %v; want a tool error for a missing query", res, err)
	}
	res, _ = searchIndexedPullRequests(context.Background(), nil, map[string]any{"request": map[string]any{
		"mode": "semantic_search", "query": "retry", "page": float64(1), "filters": map[string]any{"result": "failed"}}})
	if !res.IsError {
		t.Fatal("want a tool error for a filter pull requests don't have")
	}
}
