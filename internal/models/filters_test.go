package models

import (
	"reflect"
	"testing"
)

func TestFilterValuesHierarchicalFieldsStoreAncestors(t *testing.T) {
	cases := []struct {
		field, raw string
		want       []string
	}{
		{FilterPath, "/src/App/Main.go", []string{"src", "src/app", "src/app/main.go"}},
		{FilterPath, `src\lib\`, []string{"src", "src/lib"}},
		{FilterIterationPath, "Payments/Sprint 3", []string{"payments", `payments\sprint 3`}},
		{FilterProject, "  Payments  ", []string{"payments"}},
		{FilterRepository, "", nil},
	}
	for _, c := range cases {
		if got := FilterValues(c.field, c.raw); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FilterValues(%q, %q) = %v, want %v", c.field, c.raw, got, c.want)
		}
	}
}

func TestNormalizeFilterValueMatchesStoredForm(t *testing.T) {
	// A query value must normalize to one of the stored values for the same
	// path, whatever slashes/case/edges the caller used.
	stored := FilterValues(FilterPath, "src/Auth/Login.cs")
	for _, q := range []string{"SRC/auth", `\src\auth\`, "src/auth/login.cs"} {
		n := NormalizeFilterValue(FilterPath, q)
		found := false
		for _, s := range stored {
			found = found || s == n
		}
		if !found {
			t.Errorf("query %q normalized to %q, not among stored %v", q, n, stored)
		}
	}
}

func TestDocumentFiltersAddDedupesAndSkipsEmpty(t *testing.T) {
	var f DocumentFilters
	f.Add(FilterPath, "src/a.go", "src/b.go", "")
	f.Add(FilterTag, " ui", "UI ", "")
	if got, want := f.Fields[FilterPath], []string{"src", "src/a.go", "src/b.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("path = %v, want %v", got, want)
	}
	if got, want := f.Fields[FilterTag], []string{"ui"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

func TestIDFilterAcceptsReferenceForms(t *testing.T) {
	for _, in := range []string{"1234", "#1234", "AB#1234", "ab#1234", " !1234 "} {
		if got := NormalizeFilterValue(FilterID, in); got != "1234" {
			t.Errorf("NormalizeFilterValue(id, %q) = %q, want 1234", in, got)
		}
	}
}

// A commit is findable by any abbreviation from git's 7 characters up.
func TestCommitFilterStoresPrefixes(t *testing.T) {
	sha := "ABC123DEF4567890ABC123DEF4567890ABCD1234"
	vals := FilterValues(FilterCommit, sha)
	for _, q := range []string{sha, "abc123d", "abc123def4", "abc123def4567890"} {
		if !containsString(vals, NormalizeFilterValue(FilterCommit, q)) {
			t.Errorf("query %q doesn't match the stored values", q)
		}
	}
	if containsString(vals, "abc123") {
		t.Error("prefixes shorter than 7 characters must not match")
	}
}
