package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestConfiguredRefs(t *testing.T) {
	src := &models.SourceDefinition{Config: map[string]string{
		"Branch": " main, refs/heads/release/2.0 ,main,",
		"Tags":   "v1.0, refs/tags/v2.0, main",
	}}
	want := []gitRef{{Name: "main"}, {Name: "release/2.0"}, {Name: "v1.0", Tag: true}, {Name: "v2.0", Tag: true}, {Name: "main", Tag: true}}
	if got := configuredRefs(src); !reflect.DeepEqual(got, want) {
		t.Errorf("configuredRefs = %+v, want %+v", got, want)
	}
	if got := configuredRefs(&models.SourceDefinition{}); got != nil {
		t.Errorf("no refs configured = %+v, want nil (the default branch)", got)
	}
}

// Files the same in several refs are one entry listing all of them; a path
// whose content differs gets one entry per content, told apart by its first
// ref.
func TestMergeRefFiles(t *testing.T) {
	main, v1 := gitRef{Name: "main"}, gitRef{Name: "v1.0", Tag: true}
	merged := mergeRefFiles([]gitRef{main, v1}, [][]archiveFile{
		{{path: "a.go", text: "same"}, {path: "b.go", text: "new"}, {path: "c.go", text: "only main"}},
		{{path: "a.go", text: "same"}, {path: "b.go", text: "old"}},
	})
	type row struct{ key, title, refs string }
	var got []row
	for _, f := range merged {
		got = append(got, row{f.docKey(), f.title(), refNames(f.refs)})
	}
	want := []row{
		{"a.go", "a.go", "main, v1.0"},
		{"b.go@main", "b.go (main)", "main"},
		{"b.go@v1.0", "b.go (v1.0)", "v1.0"},
		{"c.go", "c.go", "main"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged = %+v\nwant %+v", got, want)
	}
}

func TestRepoRefsSort(t *testing.T) {
	r := RepoRefs{
		DefaultBranch: "main",
		Branches:      []string{"release/10", "dev", "main", "release/9"},
		Tags:          []string{"v1.9", "v1.10", "v2.0", "v1.2"},
	}
	r.sort()
	if want := []string{"main", "dev", "release/9", "release/10"}; !reflect.DeepEqual(r.Branches, want) {
		t.Errorf("branches = %v, want %v", r.Branches, want)
	}
	if want := []string{"v2.0", "v1.10", "v1.9", "v1.2"}; !reflect.DeepEqual(r.Tags, want) {
		t.Errorf("tags = %v, want %v", r.Tags, want)
	}
}

// An Azure DevOps code source embeds each ticked branch and tag: a file the
// same in both once, with both versions; a changed file once per version,
// each linked to its own version.
func TestAdoFilesSeveralRefs(t *testing.T) {
	zips := map[string][]byte{
		"commit/sha-main": repoZip(t, map[string]string{"src/App.cs": "class App {}", "src/New.cs": "v2"}),
		"commit/sha-v1.0": repoZip(t, map[string]string{"src/App.cs": "class App {}", "src/New.cs": "v1"}),
	}
	srv := httptest.NewServer(adoRefsShim(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		body, ok := zips[q.Get("versionDescriptor.versionType")+"/"+q.Get("versionDescriptor.version")]
		if !ok {
			http.Error(w, "unexpected "+r.URL.String(), http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})))
	defer srv.Close()
	s := &APISource{src: codeSrc(map[string]string{
		"Url":    srv.URL + "/org/proj/_apis/git/repositories/repo/items?api-version=7.1",
		"Branch": "main",
		"Tags":   "v1.0",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range docs {
		got[d.ID] = strings.Join(d.Filters.Fields[models.FilterVersion], ",") + " | " + d.Properties["version"] + " | " + d.Properties["title"]
	}
	want := map[string]string{
		"src-1_capi_/src/App.cs":      "main,v1.0 | main, v1.0 | /src/App.cs",
		"src-1_capi_/src/New.cs@main": "main | main | /src/New.cs (main)",
		"src-1_capi_/src/New.cs@v1.0": "v1.0 | v1.0 | /src/New.cs (v1.0)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("docs = %v\nwant %v", got, want)
	}
	for _, d := range docs {
		if d.ID == "src-1_capi_/src/New.cs@v1.0" && !strings.Contains(d.Properties["web_url"], "version=GTv1.0") {
			t.Errorf("tag version web_url = %q, want the tag", d.Properties["web_url"])
		}
	}
}

// Without a ticked ref, files come from the default branch, named as their
// version.
func TestAdoFilesDefaultBranchIsTheVersion(t *testing.T) {
	zipBody := repoZip(t, map[string]string{"src/App.cs": "x"})
	var gotVersion string
	srv := httptest.NewServer(defaultBranchShim(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.URL.Query().Get("versionDescriptor.version")
		_, _ = w.Write(zipBody)
	})))
	defer srv.Close()
	s := &APISource{src: codeSrc(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/items"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion != "sha-main" || len(docs) != 1 || docs[0].Properties["version"] != "main" {
		t.Errorf("version requested %q, docs %+v; want main", gotVersion, docs)
	}
}

// Commit history over a branch and a tag lists each with its version type;
// a commit in both is embedded once with both versions.
func TestAdoCommitsSeveralRefs(t *testing.T) {
	history := map[string][]string{"branch/main": {"c3", "c2", "c1"}, "tag/v1.0": {"c1"}}
	srv := httptest.NewServer(blobsZipShim(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			q := r.URL.Query()
			ids, ok := history[q.Get("searchCriteria.itemVersion.versionType")+"/"+q.Get("searchCriteria.itemVersion.version")]
			if !ok {
				http.Error(w, "unexpected "+r.URL.String(), http.StatusNotFound)
				return
			}
			var items []map[string]any
			if skip := q.Get("searchCriteria.$skip"); skip == "" || skip == "0" {
				for _, id := range ids {
					items = append(items, map[string]any{"commitId": id, "comment": "commit " + id})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": items})
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(adoChangesResponse{})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s := &APISource{src: commitSrc(map[string]string{
		"Url":    srv.URL + "/_apis/git/repositories/repo/commits",
		"Branch": "main",
		"Tags":   "v1.0",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range docs {
		got[d.ID] = d.Properties["version"]
	}
	want := map[string]string{"src-1_capi_c3": "main", "src-1_capi_c2": "main", "src-1_capi_c1": "main, v1.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commit versions = %v, want %v", got, want)
	}
}

// GitHub: files and commits of a branch and a tag, merged the same way.
func TestGitHubSeveralRefs(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/tarball/main", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(repoTarball(t, map[string]string{"README.md": "same", "app.go": "v2"}))
	})
	f.HandleFunc("/api/v3/repos/acme/app/tarball/v1.0", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(repoTarball(t, map[string]string{"README.md": "same", "app.go": "v1"}))
	})
	docs, err := f.source(t, models.SourceCodeRepo, map[string]string{"Branch": "main", "Tags": "v1.0", "PathFilter": "*.go,*.md"}).
		FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range docs {
		got = append(got, d.ID+" | "+d.Properties["version"])
	}
	sort.Strings(got)
	want := []string{"s_gh_file_README.md | main, v1.0", "s_gh_file_app.go@main | main", "s_gh_file_app.go@v1.0 | v1.0",
		"s_gh_file_docs/link.md | main, v1.0"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v\nwant %v", got, want)
	}

	history := map[string][]string{"main": {"bbb2", "aaa1"}, "v1.0": {"aaa1"}}
	f.HandleFunc("/api/v3/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		var page []map[string]any
		if r.URL.Query().Get("page") == "1" {
			for _, sha := range history[r.URL.Query().Get("sha")] {
				page = append(page, map[string]any{"sha": sha, "commit": map[string]any{"message": "m " + sha}})
			}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	f.HandleFunc("/api/v3/repos/acme/app/commits/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
	})
	commits, err := f.source(t, models.SourceGitCommits, map[string]string{"Branch": "main", "Tags": "v1.0"}).
		FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	gotC := map[string]string{}
	for _, d := range commits {
		gotC[d.Properties["commit"]] = d.Properties["version"]
		if !strings.Contains(d.Text, "Version: "+d.Properties["version"]) {
			t.Errorf("commit %s text lacks its versions: %q", d.Properties["commit"], d.Text)
		}
	}
	if want := map[string]string{"bbb2": "main", "aaa1": "main, v1.0"}; !reflect.DeepEqual(gotC, want) {
		t.Errorf("commit versions = %v, want %v", gotC, want)
	}
}

func TestAdoRepoRefsDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_apis/git/repositories/app"):
			_, _ = w.Write([]byte(`{"defaultBranch":"refs/heads/main"}`))
		case strings.HasSuffix(r.URL.Path, "/_apis/git/repositories/app/refs"):
			switch r.URL.Query().Get("filter") {
			case "heads/":
				if r.URL.Query().Get("continuationToken") == "" {
					w.Header().Set("x-ms-continuationtoken", "next")
					_, _ = w.Write([]byte(`{"value":[{"name":"refs/heads/dev"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"value":[{"name":"refs/heads/main"}]}`))
			case "tags/":
				_, _ = w.Write([]byte(`{"value":[{"name":"refs/tags/v1.0"},{"name":"refs/tags/v1.1"}]}`))
			}
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	refs, err := AdoRepoRefs(context.Background(), &models.SourceDefinition{Config: map[string]string{
		"AdoOrg": srv.URL, "AdoProject": "proj", "AdoRepo": "app",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := RepoRefs{DefaultBranch: "main", Branches: []string{"main", "dev"}, Tags: []string{"v1.1", "v1.0"}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v, want %+v", refs, want)
	}
}

func TestGitHubRepoRefsDiscovery(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"default_branch":"trunk"}`))
	})
	f.HandleFunc("/api/v3/repos/acme/app/branches", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"feature"},{"name":"trunk"}]`))
	})
	f.HandleFunc("/api/v3/repos/acme/app/tags", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"v2"},{"name":"v10"}]`))
	})
	refs, err := GitHubRepoRefs(context.Background(), f.source(t, models.SourceCodeRepo, nil).src, fakeSecrets{"gh": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	want := RepoRefs{DefaultBranch: "trunk", Branches: []string{"trunk", "feature"}, Tags: []string{"v10", "v2"}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v, want %+v", refs, want)
	}
}
