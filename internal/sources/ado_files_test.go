package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestFilterGitItemsDropsDirectoriesAndNonMatches(t *testing.T) {
	items := []any{
		map[string]any{"path": "/src", "gitObjectType": "tree"},
		map[string]any{"path": "/src/Foo.cs", "gitObjectType": "blob", "objectId": "sha1"},
		map[string]any{"path": "/README.md", "gitObjectType": "blob", "objectId": "sha2"},
	}
	kept := filterGitItems(items, parsePathFilter("*.cs"))
	if len(kept) != 1 {
		t.Fatalf("got %d kept items, want 1", len(kept))
	}
	m := kept[0].(map[string]any)
	if m["path"] != "/src/Foo.cs" {
		t.Errorf("kept item = %v", m)
	}
}

func TestFetchDocumentsFetchesRealFileContentForCodeSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/items"):
			if r.URL.Query().Get("recursionLevel") != "Full" {
				http.Error(w, "expected recursionLevel=Full to be auto-added", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"path": "/src", "gitObjectType": "tree"},
					{"path": "/src/Foo.cs", "gitObjectType": "blob", "objectId": "sha-foo"},
					{"path": "/README.md", "gitObjectType": "blob", "objectId": "sha-readme"},
				},
			})
		case strings.Contains(r.URL.Path, "/blobs/sha-foo"):
			_, _ = w.Write([]byte("public class Foo {}"))
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: codeSrc(map[string]string{
		"Url":        srv.URL + "/_apis/git/repositories/repo/items",
		"PathFilter": "*.cs",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1 (README.md and the directory should be filtered out)", len(docs))
	}
	if !strings.Contains(docs[0].Text, "public class Foo {}") {
		t.Errorf("expected real file content in doc text, got %q", docs[0].Text)
	}
	if !strings.HasPrefix(docs[0].Text, "/src/Foo.cs") {
		t.Errorf("expected doc text to start with the file path, got %q", docs[0].Text)
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
