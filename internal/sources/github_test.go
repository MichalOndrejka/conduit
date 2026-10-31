package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// fakeGitHub serves a GitHub Enterprise Server API at {srv.URL}/api/v3 for
// repository acme/app, recording the Authorization header it receives.
type fakeGitHub struct {
	*http.ServeMux
	srv  *httptest.Server
	mu   sync.Mutex
	auth string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{ServeMux: http.NewServeMux()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		f.mu.Unlock()
		f.ServeMux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// handleJSON serves v as JSON at the repository-relative path.
func (f *fakeGitHub) handleJSON(path string, v any) {
	f.HandleFunc("/api/v3/repos/acme/app"+path, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(v)
	})
}

func (f *fakeGitHub) source(t *testing.T, srcType string, cfg map[string]string) *GitHubSource {
	t.Helper()
	full := map[string]string{
		"Provider": "github", "GitHubUrl": f.srv.URL, "GitHubOwner": "acme", "GitHubRepo": "app", "Token": "gh",
	}
	for k, v := range cfg {
		full[k] = v
	}
	g, err := newGitHubSource(&models.SourceDefinition{ID: "s", Name: "GH", Type: srcType, Config: full}, fakeSecrets{"gh": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGitHubBases(t *testing.T) {
	cases := map[string][2]string{
		"":                             {"https://api.github.com", "https://github.com"},
		"https://github.com/":          {"https://api.github.com", "https://github.com"},
		"https://api.github.com":       {"https://api.github.com", "https://github.com"},
		"https://ghe.corp":             {"https://ghe.corp/api/v3", "https://ghe.corp"},
		"https://ghe.corp/api/v3":      {"https://ghe.corp/api/v3", "https://ghe.corp"},
		"https://octo.ghe.com":         {"https://api.octo.ghe.com", "https://octo.ghe.com"},
		"https://api.octo.ghe.com":     {"https://api.octo.ghe.com", "https://octo.ghe.com"},
		"http://10.0.0.5:8080/github/": {"http://10.0.0.5:8080/github/api/v3", "http://10.0.0.5:8080/github"},
	}
	for in, want := range cases {
		api, web, err := GitHubBases(in)
		if err != nil || api != want[0] || web != want[1] {
			t.Errorf("GitHubBases(%q) = %q, %q, %v; want %q, %q", in, api, web, err, want[0], want[1])
		}
	}
	if _, _, err := GitHubBases("ftp://x"); err == nil {
		t.Error("expected an error for a non-http URL")
	}
}

func TestGitHubFetchFiles(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("", map[string]any{"default_branch": "main"})
	f.HandleFunc("/api/v3/repos/acme/app/commits/main", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.sha" {
			t.Errorf("commit SHA requested with Accept %q", r.Header.Get("Accept"))
		}
		fmt.Fprint(w, "abc123")
	})
	f.handleJSON("/git/trees/abc123", map[string]any{"tree": []map[string]any{
		{"path": "docs", "type": "tree", "sha": "t1"},
		{"path": "docs/guide.md", "type": "blob", "sha": "b1", "size": 12},
		{"path": "docs/logo.png", "type": "blob", "sha": "b2", "size": 4},
		{"path": "docs/huge.md", "type": "blob", "sha": "b3", "size": maxRepoFileBytes + 1},
		{"path": "main.go", "type": "blob", "sha": "b4", "size": 10},
	}})
	f.HandleFunc("/api/v3/repos/acme/app/git/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("sha") {
		case "b1":
			fmt.Fprint(w, "# Guide\nhello")
		case "b2":
			w.Write([]byte{0x89, 'P', 'N', 0})
		default:
			t.Errorf("unexpected blob %s fetched", r.PathValue("sha"))
		}
	})

	docs, err := f.source(t, models.SourceDocumentation, map[string]string{"PathFilter": "docs/*"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("got %d docs, want 3 (main.go filtered out)", len(docs))
	}
	if f.auth != "token tok" { // handlers are done: FetchDocuments returned
		t.Errorf("Authorization = %q", f.auth)
	}
	byTitle := map[string]models.SourceDocument{}
	for _, d := range docs {
		byTitle[d.Properties["title"]] = d
	}
	guide := byTitle["docs/guide.md"]
	if guide.Text != "docs/guide.md\n\n# Guide\nhello" {
		t.Errorf("guide text = %q", guide.Text)
	}
	if want := f.srv.URL + "/acme/app/blob/main/docs/guide.md"; guide.Properties["web_url"] != want {
		t.Errorf("web_url = %q, want %q", guide.Properties["web_url"], want)
	}
	if guide.Properties["repository"] != "app" || guide.Properties["project"] != "acme" {
		t.Errorf("props = %v", guide.Properties)
	}
	for _, p := range []string{"docs/logo.png", "docs/huge.md"} {
		if !strings.Contains(byTitle[p].Text, "binary or large file skipped") {
			t.Errorf("%s text = %q, want it skipped", p, byTitle[p].Text)
		}
	}
}

func TestGitHubFetchFilesFailsOnTruncatedTree(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/commits/dev", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "abc") })
	f.handleJSON("/git/trees/abc", map[string]any{"tree": []any{}, "truncated": true})
	_, err := f.source(t, models.SourceCodeRepo, map[string]string{"Branch": "dev"}).FetchDocuments(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "more files") {
		t.Errorf("err = %v, want a truncated-tree error", err)
	}
}

func TestGitHubFetchCommits(t *testing.T) {
	f := newFakeGitHub(t)
	// 150 commits over two pages; Top caps them at 120.
	f.HandleFunc("/api/v3/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sha") != "main" {
			t.Errorf("sha = %q", r.URL.Query().Get("sha"))
		}
		start := map[string]int{"1": 0, "2": 100}[r.URL.Query().Get("page")]
		var page []map[string]any
		for i := start; i < min(start+100, 150); i++ {
			page = append(page, map[string]any{
				"sha": fmt.Sprintf("c%03d", i),
				"commit": map[string]any{
					"author":  map[string]any{"name": "Ann", "email": "ann@x", "date": "2026-10-01T10:00:00Z"},
					"message": fmt.Sprintf("Change %d\n\nDetails", i),
				},
				"author": map[string]any{"login": "ann-gh"},
			})
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	f.HandleFunc("/api/v3/repos/acme/app/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
			{"filename": "a.go", "status": "modified", "additions": 1, "deletions": 1, "patch": "@@ -1 +1 @@\n-x\n+y"},
			{"filename": "new.go", "previous_filename": "old.go", "status": "renamed"},
			{"filename": "img.png", "status": "added", "additions": 0, "deletions": 0},
			{"filename": "big.sql", "status": "modified", "additions": 90000, "deletions": 3},
		}})
	})

	docs, err := f.source(t, models.SourceGitCommits, map[string]string{"Branch": "main", "Top": "120"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 120 {
		t.Fatalf("got %d docs, want 120", len(docs))
	}
	d := docs[0]
	for _, want := range []string{
		"Commit c000", "Author: Ann <ann@x>", "Files changed: 4", "Change 0\n\nDetails",
		"--- a.go\n+++ a.go\n@@ -1 +1 @@\n-x\n+y",
		"--- old.go\n+++ new.go",
		"(added, no content changes)",
		"(modified, +90000 -3; GitHub returns no patch for binary or very large files)",
	} {
		if !strings.Contains(d.Text, want) {
			t.Errorf("commit text lacks %q:\n%s", want, d.Text)
		}
	}
	if d.Properties["title"] != "Change 0 (c000)" || d.ID != "s_commit_c000" {
		t.Errorf("props = %v, id %q", d.Properties, d.ID)
	}
	if d.Filters.Date.IsZero() {
		t.Error("commit date filter not set")
	}
}

func TestGitHubCommitNotesFileLimit(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		files := make([]map[string]any, githubPageSize)
		for i := range files {
			files[i] = map[string]any{"filename": fmt.Sprintf("f%d", i), "status": "added", "additions": 1, "patch": "+x"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	})
	d, err := f.source(t, models.SourceGitCommits, nil).fetchCommitDiff(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if d.filesChanged != githubMaxCommitFiles || !strings.Contains(d.diff, "at most 3000 changed files") {
		t.Errorf("filesChanged = %d; diff tail %q", d.filesChanged, d.diff[len(d.diff)-120:])
	}
}

func TestGitHubFetchIssues(t *testing.T) {
	f := newFakeGitHub(t)
	issue := func(n int, updated string, labels ...string) map[string]any {
		ls := make([]map[string]any, len(labels))
		for i, l := range labels {
			ls[i] = map[string]any{"name": l}
		}
		return map[string]any{
			"number": n, "title": fmt.Sprintf("Issue %d", n), "state": "closed", "state_reason": "not_planned",
			"body": "Body text", "user": map[string]any{"login": "bob"}, "labels": ls,
			"assignees": []map[string]any{{"login": "carol"}}, "comments": 1, "updated_at": updated,
			"html_url": fmt.Sprintf("https://ghe/acme/app/issues/%d", n),
		}
	}
	f.HandleFunc("/api/v3/repos/acme/app/issues", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("state") != "all" || q.Get("sort") != "created" {
			t.Errorf("query = %v, want every issue sorted by creation", q)
		}
		pr := issue(9, "2026-10-09T00:00:00Z", "bug")
		pr["pull_request"] = map[string]any{"url": "x"}
		switch r.URL.Query().Get("labels") {
		case "bug":
			_ = json.NewEncoder(w).Encode([]any{issue(1, "2026-10-01T00:00:00Z", "bug"), issue(2, "2026-10-02T00:00:00Z", "bug", "ui"), pr})
		case "ui":
			_ = json.NewEncoder(w).Encode([]any{issue(2, "2026-10-02T00:00:00Z", "bug", "ui"), issue(3, "2026-10-03T00:00:00Z", "ui")})
		default:
			t.Errorf("unexpected labels %q", r.URL.Query().Get("labels"))
		}
	})
	f.HandleFunc("/api/v3/repos/acme/app/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{
			"user": map[string]any{"login": "dave"}, "created_at": "2026-10-05T00:00:00Z", "body": "Comment " + r.PathValue("n"),
		}})
	})

	docs, err := f.source(t, models.SourceWorkItemQuery, map[string]string{"Labels": "bug, ui"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	if got := strings.Join(ids, ","); got != "s_gh_issue_3,s_gh_issue_2,s_gh_issue_1" {
		t.Fatalf("doc ids = %s, want issues 3,2,1 (merged, newest first, PR excluded)", got)
	}
	d := docs[1]
	for _, want := range []string{"Issue 2\n", "State: closed (not planned)", "Labels: bug, ui", "Assignees: carol", "Body text", "Comment by dave on 2026-10-05T00:00:00Z:\nComment 2"} {
		if !strings.Contains(d.Text, want) {
			t.Errorf("issue text lacks %q:\n%s", want, d.Text)
		}
	}
	if d.Properties["web_url"] != "https://ghe/acme/app/issues/2" || d.Properties["type"] != "Issue" {
		t.Errorf("props = %v", d.Properties)
	}
}

func TestGitHubFetchWorkflowRuns(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/actions/workflows/ci.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("status") != "completed" || !strings.HasPrefix(q.Get("created"), ">=") {
			t.Errorf("query = %v", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "workflow_runs": []any{
			map[string]any{"id": 11, "name": "CI", "run_number": 7, "conclusion": "failure", "head_branch": "main", "head_sha": "abc",
				"run_started_at": "2026-10-01T10:00:00Z", "updated_at": "2026-10-01T10:05:00Z"},
			map[string]any{"id": 10, "name": "CI", "run_number": 6, "conclusion": "success", "head_branch": "main"},
		}})
	})
	f.handleJSON("/actions/runs/11/jobs", map[string]any{"jobs": []any{
		map[string]any{"id": 101, "name": "build", "conclusion": "failure",
			"steps": []any{map[string]any{"name": "Checkout", "conclusion": "success"}, map[string]any{"name": "Test", "conclusion": "failure"}}},
		map[string]any{"id": 102, "name": "lint", "conclusion": "timed_out"},
		map[string]any{"id": 103, "name": "docs", "conclusion": "success"},
	}})
	f.handleJSON("/check-runs/101/annotations", []any{
		map[string]any{"path": "a_test.go", "start_line": 12, "annotation_level": "failure", "message": "expected 1, got 2"},
	})
	f.handleJSON("/check-runs/102/annotations", []any{})
	f.HandleFunc("/api/v3/repos/acme/app/actions/jobs/101/logs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "2026-10-01T10:00:01.1234567Z ##[group]Run go test\n2026-10-01T10:00:02.0000000Z FAIL a_test.go:12\n")
	})
	f.HandleFunc("/api/v3/repos/acme/app/actions/jobs/102/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})

	docs, err := f.source(t, models.SourcePipelineRun, map[string]string{"Workflow": "ci.yml", "Days": "30"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 4 {
		t.Fatalf("got %d docs, want run+2 jobs for the failed run and 1 for the green run", len(docs))
	}
	run, build, lint := docs[0], docs[1], docs[2]
	for _, want := range []string{"CI #7 failure", "Duration: 5m0s", "[failure] build: expected 1, got 2 (a_test.go:12)", "- build (document s_run_11_job_101)"} {
		if !strings.Contains(run.Text, want) {
			t.Errorf("run text lacks %q:\n%s", want, run.Text)
		}
	}
	if !strings.Contains(build.Text, "Failed steps: Test") || !strings.Contains(build.Text, "##[group]Run go test\nFAIL a_test.go:12") {
		t.Errorf("build job text:\n%s", build.Text)
	}
	if build.Properties["parent_doc_id"] != "s_run_11" {
		t.Errorf("parent_doc_id = %q", build.Properties["parent_doc_id"])
	}
	if !strings.Contains(lint.Text, "log no longer available on GitHub: HTTP 410") {
		t.Errorf("lint job text:\n%s", lint.Text)
	}
	if docs[3].ID != "s_run_10" {
		t.Errorf("green run doc id = %q", docs[3].ID)
	}
}

func TestGitHubWorkflowRunsOverQueryLimit(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("/actions/workflows/7/runs", map[string]any{"total_count": 1500, "workflow_runs": []any{}})
	_, err := f.source(t, models.SourcePipelineRun, map[string]string{"Workflow": "7", "Days": "365"}).FetchDocuments(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "lower Days") {
		t.Errorf("err = %v, want the 1000-run limit error", err)
	}
}

func TestGitHubRetriesRateLimit(t *testing.T) {
	defer func(d time.Duration) { retryBaseDelay = d }(retryBaseDelay)
	retryBaseDelay = time.Millisecond
	f := newFakeGitHub(t)
	calls := 0
	f.HandleFunc("/api/v3/repos/acme/app", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "trunk"})
	})
	b, err := f.source(t, models.SourceCodeRepo, nil).branch(context.Background())
	if err != nil || b != "trunk" || calls != 2 {
		t.Errorf("branch = %q, err %v, calls %d; want a retry after the rate-limit 403", b, err, calls)
	}

	// A plain 403 (no access) is not retried and shows GitHub's message.
	f2 := newFakeGitHub(t)
	f2.HandleFunc("/api/v3/repos/acme/app", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Resource not accessible by personal access token"}`, http.StatusForbidden)
	})
	if _, err := f2.source(t, models.SourceCodeRepo, nil).branch(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "not accessible") {
		t.Errorf("err = %v, want GitHub's message", err)
	}
}

// A push during listing shifts GitHub's pages, so page 2 repeats the last
// commit of page 1; it must be indexed once.
func TestGitHubCommitsSkipPageShiftRepeats(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		start := map[string]int{"1": 0, "2": 99}[r.URL.Query().Get("page")]
		var page []map[string]any
		for i := start; i < min(start+100, 130); i++ {
			page = append(page, map[string]any{"sha": fmt.Sprintf("c%03d", i), "commit": map[string]any{"message": "m"}})
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	f.handleJSON("/commits/{sha}", map[string]any{"files": []any{}})
	docs, err := f.source(t, models.SourceGitCommits, map[string]string{"Branch": "main"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range docs {
		if ids[d.ID] {
			t.Fatalf("duplicate document %s", d.ID)
		}
		ids[d.ID] = true
	}
	if len(docs) != 130 {
		t.Errorf("got %d docs, want 130", len(docs))
	}
}

// A run finishing during listing raises total_count above what was listed;
// that is not GitHub's 1000-run cap and must not fail the sync.
func TestGitHubRunsToleratesNewRunDuringListing(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("/actions/workflows/ci.yml/runs", map[string]any{"total_count": 3, "workflow_runs": []any{
		map[string]any{"id": 2, "name": "CI", "run_number": 2, "conclusion": "success"},
		map[string]any{"id": 1, "name": "CI", "run_number": 1, "conclusion": "success"},
	}})
	docs, err := f.source(t, models.SourcePipelineRun, map[string]string{"Workflow": "ci.yml", "Days": "7"}).FetchDocuments(context.Background(), nil)
	if err != nil || len(docs) != 2 {
		t.Errorf("got %d docs, err %v; want both runs", len(docs), err)
	}
}
