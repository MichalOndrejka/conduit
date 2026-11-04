package sources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// repoTarball builds a gzipped tarball laid out as GitHub's: a pax global
// header, then every entry under one {owner}-{repo}-{sha}/ directory.
func repoTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(h *tar.Header, body string) {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "abc123"}}, "")
	write(&tar.Header{Typeflag: tar.TypeDir, Name: "acme-app-abc123/", Mode: 0o755}, "")
	write(&tar.Header{Typeflag: tar.TypeSymlink, Name: "acme-app-abc123/docs/link.md", Linkname: "guide.md", Mode: 0o777}, "")
	for name, body := range files {
		write(&tar.Header{Typeflag: tar.TypeReg, Name: "acme-app-abc123/" + name, Size: int64(len(body)), Mode: 0o644}, body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGitHubFetchFiles(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("", map[string]any{"default_branch": "main"})
	tarball := repoTarball(t, map[string]string{
		"docs/guide.md": "# Guide\nhello",
		"docs/logo.png": "\x89PN\x00",
		"docs/huge.md":  strings.Repeat("x", maxRepoFileBytes+1),
		"main.go":       "package main",
	})
	var requests atomic.Int32
	// Like github.com, the API redirects to a separate download URL.
	f.HandleFunc("/api/v3/repos/acme/app/tarball/main", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/codeload/acme/app/tar.gz/main", http.StatusFound)
	})
	f.HandleFunc("/codeload/acme/app/tar.gz/main", func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball)
	})

	docs, err := f.source(t, models.SourceDocumentation, map[string]string{"PathFilter": "docs/*"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 4 {
		t.Fatalf("got %d docs, want 4 (main.go filtered out)", len(docs))
	}
	if requests.Load() != 1 {
		t.Errorf("tarball requested %d times, want 1", requests.Load())
	}
	if f.auth != "token tok" { // handlers are done: FetchDocuments returned
		t.Errorf("Authorization = %q", f.auth)
	}
	for i := 1; i < len(docs); i++ {
		if docs[i-1].Properties["title"] > docs[i].Properties["title"] {
			t.Errorf("docs not sorted by path: %q before %q", docs[i-1].Properties["title"], docs[i].Properties["title"])
		}
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
	if got := byTitle["docs/link.md"].Text; got != "docs/link.md\n\nguide.md" {
		t.Errorf("symlink text = %q", got)
	}
}

// A download cut off part-way is restarted, and the retry's files replace
// the partial ones rather than adding to them.
func TestGitHubFetchFilesRetriesCutDownload(t *testing.T) {
	defer func(d time.Duration) { retryBaseDelay = d }(retryBaseDelay)
	retryBaseDelay = time.Millisecond

	f := newFakeGitHub(t)
	files := map[string]string{}
	for i := range 50 {
		files[fmt.Sprintf("src/f%02d.go", i)] = strings.Repeat(fmt.Sprintf("line %d\n", i), 200)
	}
	tarball := repoTarball(t, files)
	var requests atomic.Int32
	f.HandleFunc("/api/v3/repos/acme/app/tarball/dev", func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Write(tarball[:len(tarball)/2])
			return
		}
		w.Write(tarball)
	})

	docs, err := f.source(t, models.SourceCodeRepo, map[string]string{"Branch": "dev"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Errorf("tarball requested %d times, want 2", requests.Load())
	}
	if len(docs) != 51 { // 50 files + the symlink
		t.Errorf("got %d docs, want 51", len(docs))
	}
}

func TestGitHubFetchFilesReportsHTTPError(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/tarball/dev", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	_, err := f.source(t, models.SourceCodeRepo, map[string]string{"Branch": "dev"}).FetchDocuments(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %v, want an HTTP 404 error", err)
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

// Issue types are queried one per request and checked on each issue too: a
// server without issue types ignores the type parameter and returns every
// issue, which must not leak untyped or other-typed issues in.
func TestGitHubFetchIssuesByType(t *testing.T) {
	f := newFakeGitHub(t)
	issue := func(n int, typ string) map[string]any {
		m := map[string]any{"number": n, "title": fmt.Sprintf("Issue %d", n), "state": "open", "user": map[string]any{"login": "bob"}}
		if typ != "" {
			m["type"] = map[string]any{"name": typ}
		}
		return m
	}
	var gotTypes []string
	f.HandleFunc("/api/v3/repos/acme/app/issues", func(w http.ResponseWriter, r *http.Request) {
		gotTypes = append(gotTypes, r.URL.Query().Get("type"))
		// Behave like a server that ignores the type parameter.
		_ = json.NewEncoder(w).Encode([]any{issue(4, "Task"), issue(3, "bug"), issue(2, ""), issue(1, "Feature")})
	})

	docs, err := f.source(t, models.SourceWorkItemQuery, map[string]string{"WorkItemTypes": "Bug, Feature"}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotTypes, ",") != "Bug,Feature" {
		t.Errorf("type parameters = %v, want Bug then Feature", gotTypes)
	}
	var got []string
	for _, d := range docs {
		got = append(got, d.Properties["type"])
	}
	if strings.Join(got, ",") != "bug,Feature" {
		t.Errorf("doc types = %v, want issue 3 (bug) and issue 1 (Feature)", got)
	}
	if !strings.Contains(docs[0].Text, "Type: bug") {
		t.Errorf("text lacks the type:\n%s", docs[0].Text)
	}
}

func TestGitHubIssueTypes(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/orgs/acme/issue-types", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"name": "Task", "is_enabled": true},
			map[string]any{"name": "Bug"},
			map[string]any{"name": "Retired", "is_enabled": false},
		})
	})
	types, available, err := GitHubIssueTypes(context.Background(), f.source(t, models.SourceWorkItemQuery, nil).src, fakeSecrets{"gh": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if !available || strings.Join(types, ",") != "Bug,Task" {
		t.Errorf("types = %v, available = %v; want Bug,Task", types, available)
	}
	if f.auth != "token tok" {
		t.Errorf("Authorization = %q", f.auth)
	}
}

// A personal account (or a server without issue types) answers 404: no
// types, not an error.
func TestGitHubIssueTypesUnavailable(t *testing.T) {
	f := newFakeGitHub(t)
	types, available, err := GitHubIssueTypes(context.Background(), f.source(t, models.SourceWorkItemQuery, nil).src, nil)
	if err != nil || available || len(types) != 0 {
		t.Errorf("types = %v, available = %v, err = %v; want none, unavailable, no error", types, available, err)
	}
}
