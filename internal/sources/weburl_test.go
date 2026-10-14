package sources

import (
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestAdoFileWebURL(t *testing.T) {
	cases := []struct {
		itemsURL, path, want string
	}{
		{
			"https://tfs.example.com/tfs/Coll/proj/_apis/git/repositories/repo/items?api-version=7.1",
			"/src/a b.cs",
			"https://tfs.example.com/tfs/Coll/proj/_git/repo?path=%2Fsrc%2Fa+b.cs",
		},
		{
			"https://dev.azure.com/org/proj/_apis/git/repositories/repo/items?versionDescriptor.version=dev&api-version=7.1",
			"/README.md",
			"https://dev.azure.com/org/proj/_git/repo?path=%2FREADME.md&version=GBdev",
		},
		{
			"https://dev.azure.com/org/proj/_apis/git/repositories/repo/items?versionDescriptor.version=v1&versionDescriptor.versionType=tag",
			"/a.go",
			"https://dev.azure.com/org/proj/_git/repo?path=%2Fa.go&version=GTv1",
		},
		{"https://example.com/api/items", "/a.go", ""},
	}
	for _, c := range cases {
		if got := adoFileWebURL(c.itemsURL, c.path); got != c.want {
			t.Errorf("adoFileWebURL(%q, %q) = %q, want %q", c.itemsURL, c.path, got, c.want)
		}
	}
}

func TestAdoCommitWebURL(t *testing.T) {
	got := adoCommitWebURL("https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits?api-version=7.1", "abc123")
	if want := "https://dev.azure.com/org/proj/_git/repo/commit/abc123"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestItemWebURLPrefersKnownFieldsAndRejectsAPIURLs(t *testing.T) {
	item := map[string]any{
		"url":       "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits/abc",
		"remoteUrl": "https://dev.azure.com/org/proj/_git/repo/commit/abc",
	}
	if got := itemWebURL(item, ""); got != "https://dev.azure.com/org/proj/_git/repo/commit/abc" {
		t.Errorf("got %q", got)
	}
	if got := itemWebURL(item, "url"); got != "" {
		t.Errorf("API URL accepted as web link: %q", got)
	}
	nested := map[string]any{"_links": map[string]any{"html": map[string]any{"href": "https://x/proj/_workitems/edit/1"}}}
	if got := itemWebURL(nested, ""); got != "https://x/proj/_workitems/edit/1" {
		t.Errorf("got %q", got)
	}
}

func TestItemsToDocumentsStoresWebURLNotAPIURL(t *testing.T) {
	const commitsURL = "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits?api-version=7.1"
	a := &APISource{src: &models.SourceDefinition{ID: "s", Type: models.SourceGitCommits, Config: map[string]string{}}}
	docs := a.itemsToDocuments([]any{map[string]any{"commitId": "abc", "comment": "fix"}}, commitsURL)
	if len(docs) != 1 {
		t.Fatalf("got %d docs", len(docs))
	}
	if _, ok := docs[0].Properties["url"]; ok {
		t.Errorf("API url property still stored: %v", docs[0].Properties)
	}
	if got, want := docs[0].Properties["web_url"], "https://dev.azure.com/org/proj/_git/repo/commit/abc"; got != want {
		t.Errorf("web_url = %q, want %q", got, want)
	}
}

func TestWorkItemWebURL(t *testing.T) {
	a := &APISource{src: workitemAdoSrc("https://tfs.example.com/tfs/Coll", nil)}
	item := workItemBatchItem{ID: 42, Fields: map[string]any{"System.TeamProject": "My Proj"}}
	if got, want := a.workItemWebURL(item), "https://tfs.example.com/tfs/Coll/My%20Proj/_workitems/edit/42"; got != want {
		t.Errorf("built link = %q, want %q", got, want)
	}
	item.Links = map[string]struct {
		Href string `json:"href"`
	}{"html": {Href: "https://tfs.example.com/tfs/Coll/web/wi.aspx?id=42"}}
	if got := a.workItemWebURL(item); got != "https://tfs.example.com/tfs/Coll/web/wi.aspx?id=42" {
		t.Errorf("_links.html not preferred: %q", got)
	}
}
