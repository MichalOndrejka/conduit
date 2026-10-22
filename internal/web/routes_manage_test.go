package web

import "testing"

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
