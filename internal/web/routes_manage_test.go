package web

import "testing"

func TestValidateSourceConfigFetchDiffsRequiresAdoURL(t *testing.T) {
	cfg := map[string]string{
		"Provider":   "api",
		"Url":        "https://api.example.com/items",
		"IdField":    "id",
		"FetchDiffs": "true",
	}
	if msg := validateSourceConfig(cfg); msg == "" {
		t.Error("expected an error for FetchDiffs on a non-ADO-commits URL")
	}
}

func TestValidateSourceConfigFetchDiffsRequiresIdField(t *testing.T) {
	cfg := map[string]string{
		"Provider":   "api",
		"Url":        "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits",
		"FetchDiffs": "true",
	}
	if msg := validateSourceConfig(cfg); msg == "" {
		t.Error("expected an error for FetchDiffs without IdField")
	}
}

func TestValidateSourceConfigFetchDiffsAccepted(t *testing.T) {
	cfg := map[string]string{
		"Provider":   "api",
		"Url":        "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits",
		"IdField":    "commitId",
		"FetchDiffs": "true",
	}
	if msg := validateSourceConfig(cfg); msg != "" {
		t.Errorf("expected valid FetchDiffs config to pass, got %q", msg)
	}
}

func TestValidateSourceConfigMaxFilesPerCommitMustBePositive(t *testing.T) {
	cfg := map[string]string{
		"Provider":          "api",
		"Url":               "https://api.example.com/items",
		"MaxFilesPerCommit": "-1",
	}
	if msg := validateSourceConfig(cfg); msg == "" {
		t.Error("expected an error for a non-positive MaxFilesPerCommit")
	}
}

func TestValidateSourceConfigMaxDiffCharsMustBePositive(t *testing.T) {
	cfg := map[string]string{
		"Provider":     "api",
		"Url":          "https://api.example.com/items",
		"MaxDiffChars": "not-a-number",
	}
	if msg := validateSourceConfig(cfg); msg == "" {
		t.Error("expected an error for a non-numeric MaxDiffChars")
	}
}
