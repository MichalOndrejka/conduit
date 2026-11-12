package sources

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// codeSrc builds a Source Code-typed source, since file-content enrichment is
// driven entirely by Type, mirroring commitSrc for commit-history.
func codeSrc(cfg map[string]string) *models.SourceDefinition {
	return &models.SourceDefinition{ID: "src-1", Name: "Test Source", Type: models.SourceCodeRepo, Config: cfg}
}

func TestAdoItemsAPIBase(t *testing.T) {
	cases := []struct {
		url    string
		base   string
		wantOK bool
	}{
		{
			url:    "https://dev.azure.com/org/proj/_apis/git/repositories/repo/items?recursionLevel=Full&api-version=7.1",
			base:   "https://dev.azure.com/org/proj/_apis/git/repositories/repo",
			wantOK: true,
		},
		{
			url:    "https://dev.azure.com/org/proj/_apis/git/repositories/repo/items",
			base:   "https://dev.azure.com/org/proj/_apis/git/repositories/repo",
			wantOK: true,
		},
		{url: "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits", wantOK: false},
		{url: "https://api.example.com/items", wantOK: false},
	}
	for _, c := range cases {
		base, ok := AdoItemsAPIBase(c.url)
		if ok != c.wantOK {
			t.Errorf("AdoItemsAPIBase(%q) ok = %v, want %v", c.url, ok, c.wantOK)
			continue
		}
		if ok && base != c.base {
			t.Errorf("AdoItemsAPIBase(%q) = %q, want %q", c.url, base, c.base)
		}
	}
}

func TestMatchesPathFilter(t *testing.T) {
	patterns := parsePathFilter("*.cs, *.ts")
	cases := []struct {
		path string
		want bool
	}{
		{"src/Foo.cs", true},
		{"src/nested/Bar.ts", true},
		{"README.md", false},
		{"Foo.CS", false}, // matching is case-sensitive, matches ADO's own casing
	}
	for _, c := range cases {
		if got := matchesPathFilter(c.path, patterns); got != c.want {
			t.Errorf("matchesPathFilter(%q, %v) = %v, want %v", c.path, patterns, got, c.want)
		}
	}

	// An empty filter accepts everything.
	if !matchesPathFilter("anything.bin", parsePathFilter("")) {
		t.Error("empty PathFilter should match every path")
	}
}

func TestMatchesPathFilterRecursiveGlob(t *testing.T) {
	patterns := parsePathFilter("**/*.md")
	cases := []struct {
		path string
		want bool
	}{
		{"README.md", true},          // top level
		{"/README.md", true},         // ADO-style leading slash
		{"docs/README.md", true},     // one level deep
		{"docs/sub/README.md", true}, // multiple levels deep
		{"README.txt", false},
	}
	for _, c := range cases {
		if got := matchesPathFilter(c.path, patterns); got != c.want {
			t.Errorf("matchesPathFilter(%q, %v) = %v, want %v", c.path, patterns, got, c.want)
		}
	}
}

// repoZip builds the zip Azure DevOps serves for …/items?path=/&$format=zip.
func repoZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("src/"); err != nil { // a directory entry
		t.Fatal(err)
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A code source downloads the branch as one zip and filters it locally:
// one request, whatever the number of files.
func TestFetchDocumentsDownloadsRepositoryAsOneZip(t *testing.T) {
	zipBody := repoZip(t, map[string]string{
		"src/Foo.cs":   "public class Foo {}",
		"README.md":    "# Readme",
		"src/Big.cs":   strings.Repeat("x", maxRepoFileBytes+1),
		"src/Logo.cs":  "\x00\x01binary",
		"src/Other.cs": "class Other {}",
	})
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		q := r.URL.Query()
		if !strings.HasSuffix(r.URL.Path, "/items") || q.Get("$format") != "zip" || q.Get("path") != "/" ||
			q.Get("versionDescriptor.version") != "dev" || q.Get("api-version") != "7.1" ||
			// Azure DevOps Server: HTTP 400 for a recursionLevel other than
			// None with a single item path.
			q.Has("recursionLevel") || q.Has("scopePath") {
			http.Error(w, "unexpected request: "+r.URL.String(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBody)
	}))
	defer srv.Close()

	s := &APISource{src: codeSrc(map[string]string{
		"Url":        srv.URL + "/_apis/git/repositories/repo/items?recursionLevel=Full&scopePath=/&api-version=7.1",
		"Branch":     "dev",
		"PathFilter": "*.cs",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Errorf("made %d requests, want 1: %v", len(requests), requests)
	}
	var got []string
	for _, d := range docs {
		got = append(got, d.ID+" | "+d.Text)
	}
	want := []string{
		"src-1_capi_/src/Big.cs | /src/Big.cs\n\n(binary or large file skipped)",
		"src-1_capi_/src/Foo.cs | /src/Foo.cs\n\npublic class Foo {}",
		"src-1_capi_/src/Logo.cs | /src/Logo.cs\n\n(binary or large file skipped)",
		"src-1_capi_/src/Other.cs | /src/Other.cs\n\nclass Other {}",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("docs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(docs[1].Properties["web_url"], "path=%2Fsrc%2FFoo.cs") && !strings.Contains(docs[1].Properties["web_url"], "path=/src/Foo.cs") {
		t.Errorf("web_url = %q", docs[1].Properties["web_url"])
	}
}

// TestFetchDocumentsAppliesPathFilterBeforeTop guards against a regression
// where Top truncated the file list before PathFilter ran: a small Top (as
// the source preview uses) could slice off every matching file.
func TestFetchDocumentsAppliesPathFilterBeforeTop(t *testing.T) {
	zipBody := repoZip(t, map[string]string{
		"src/A.cs": "a", "src/B.cs": "b", "doc/guide/intro.md": "# Intro",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zipBody)
	}))
	defer srv.Close()

	s := &APISource{src: codeSrc(map[string]string{
		"Url":        srv.URL + "/_apis/git/repositories/repo/items",
		"PathFilter": "doc/**/*.md",
		"Top":        "1",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || !strings.Contains(docs[0].Text, "# Intro") {
		t.Fatalf("docs = %+v", docs)
	}
}

// A download cut off part-way is restarted; a response that isn't a zip
// fails the sync.
func TestFetchDocumentsRepositoryZipErrors(t *testing.T) {
	zipBody := repoZip(t, map[string]string{"src/A.cs": "a"})
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(zipBody)))
			_, _ = w.Write(zipBody[:10]) // connection drops part-way
			return
		}
		_, _ = w.Write(zipBody)
	}))
	defer srv.Close()
	s := &APISource{src: codeSrc(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/items"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil || len(docs) != 1 || calls != 2 {
		t.Fatalf("docs %d, calls %d, err %v", len(docs), calls, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer bad.Close()
	s = &APISource{src: codeSrc(map[string]string{"Url": bad.URL + "/_apis/git/repositories/repo/items"})}
	if _, err := s.FetchDocuments(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "not a zip") {
		t.Errorf("err = %v, want a not-a-zip error", err)
	}
}

func TestFetchDocumentsCodeSourceRequiresAdoItemsURL(t *testing.T) {
	s := &APISource{src: codeSrc(map[string]string{
		"Url": "https://api.example.com/items",
	})}
	if _, err := s.FetchDocuments(context.Background(), nil); err == nil {
		t.Error("expected an error for a code source on a non-ADO-items URL")
	}
}

func TestFetchDocumentsDocumentationFallsBackToPlainMetadataForNonAdoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": "1", "title": "Wiki page"}},
		})
	}))
	defer srv.Close()

	s := &APISource{src: &models.SourceDefinition{
		ID: "src-1", Name: "Test Source", Type: models.SourceDocumentation,
		Config: map[string]string{"Url": srv.URL},
	}}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
}
