package web

import (
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestValidateSourceConfigGitCommitsRequiresAdoURL(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://api.example.com/items",
		"IdField":  "id",
	}
	if msg := validateSourceConfig(models.SourceGitCommits, cfg); msg == "" {
		t.Error("expected an error for a commit-history source on a non-ADO-commits URL")
	}
}

// IdField is no longer a visible ADO-tab input — a commit-history source
// without one must be accepted and auto-default to "commitId" at fetch time
// (see enrichWithDiffs), not fail validation.
func TestValidateSourceConfigGitCommitsAcceptsMissingIdField(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits",
	}
	if msg := validateSourceConfig(models.SourceGitCommits, cfg); msg != "" {
		t.Errorf("expected a commit-history config without IdField to pass, got %q", msg)
	}
}

func TestValidateSourceConfigGitCommitsAccepted(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits",
		"IdField":  "commitId",
	}
	if msg := validateSourceConfig(models.SourceGitCommits, cfg); msg != "" {
		t.Errorf("expected a valid commit-history config to pass, got %q", msg)
	}
}

// A source type with no special URL-shape requirement (e.g. Work Items) must
// not be forced through the commit-history or code/test-code ADO-URL checks.
func TestValidateSourceConfigNonCommitsTypeSkipsCommitsRequirements(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://api.example.com/items",
	}
	if msg := validateSourceConfig(models.SourceWorkItemQuery, cfg); msg != "" {
		t.Errorf("expected a non-commits, non-code source to skip URL-shape requirements, got %q", msg)
	}
}

// Source Code / Test Code sources always fetch real file content, so they
// require an ADO items-list URL, just as Git Commits sources require a
// commits-list URL.
func TestValidateSourceConfigCodeRepoRequiresAdoItemsURL(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://api.example.com/items",
	}
	if msg := validateSourceConfig(models.SourceCodeRepo, cfg); msg == "" {
		t.Error("expected an error for a code source on a non-ADO-items URL")
	}
}

func TestValidateSourceConfigCodeRepoAcceptsAdoItemsURL(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://dev.azure.com/org/proj/_apis/git/repositories/repo/items",
	}
	if msg := validateSourceConfig(models.SourceCodeRepo, cfg); msg != "" {
		t.Errorf("expected a valid code-repo config to pass, got %q", msg)
	}
}

func TestValidateSourceConfigMaxFilesPerCommitMustBePositive(t *testing.T) {
	cfg := map[string]string{
		"Provider":          "api",
		"Url":               "https://api.example.com/items",
		"MaxFilesPerCommit": "-1",
	}
	if msg := validateSourceConfig(models.SourceCodeRepo, cfg); msg == "" {
		t.Error("expected an error for a non-positive MaxFilesPerCommit")
	}
}

func TestValidateSourceConfigMaxDiffCharsMustBePositive(t *testing.T) {
	cfg := map[string]string{
		"Provider":     "api",
		"Url":          "https://api.example.com/items",
		"MaxDiffChars": "not-a-number",
	}
	if msg := validateSourceConfig(models.SourceCodeRepo, cfg); msg == "" {
		t.Error("expected an error for a non-numeric MaxDiffChars")
	}
}
