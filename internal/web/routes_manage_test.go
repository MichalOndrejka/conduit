package web

import (
	"regexp"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/sourcemgmt"
)

func TestAdoRepoNameFromResource(t *testing.T) {
	cases := []struct {
		resource string
		want     string
	}{
		{"git/repositories/MyRepo/items", "MyRepo"},
		{"git/repositories/MyRepo/commits", "MyRepo"},
		{"git/repositories/My%20Repo/items", "My Repo"},
		{"", ""},
		{"git/repositories/MyRepo/items/extra", ""},
		{"some/other/path", ""},
	}
	for _, c := range cases {
		if got := adoRepoNameFromResource(c.resource); got != c.want {
			t.Errorf("adoRepoNameFromResource(%q) = %q, want %q", c.resource, got, c.want)
		}
	}
}

// Every named field of the source form must be a config key the server keeps
// (sourceFromForm saves only sourcemgmt.ConfigKeys) or one of the form's own
// controls; otherwise whatever is typed into it is silently dropped on save.
func TestSourceFormFieldsAreAllSaved(t *testing.T) {
	data, err := templateFS.ReadFile("templates/source_form.html")
	if err != nil {
		t.Fatal(err)
	}
	formControls := map[string]bool{
		"id": true, "name": true, "type": true, "provider": true, "action": true, // read by the handler
		"ProviderTab": true, "RefKind": true, // tab switches
		"AdoFetchMode": true, "GitHubFetchMode": true, "AdoPipelineScope": true, // submitted via FetchMode / which field is enabled
	}
	saved := map[string]bool{"Provider": true}
	for _, p := range sourcemgmt.Providers {
		for _, k := range sourcemgmt.ConfigKeys(p) {
			saved[k] = true
		}
	}
	matches := regexp.MustCompile(`name="([A-Za-z_-]+)"`).FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatal("found no named fields in source_form.html")
	}
	for _, m := range matches {
		if !saved[m[1]] && !formControls[m[1]] {
			t.Errorf("form field %q is neither a saved config key nor a known form control", m[1])
		}
	}
}
