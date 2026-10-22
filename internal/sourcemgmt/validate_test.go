package sourcemgmt

import (
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestValidateGitCommitsRequiresAdoURL(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://api.example.com/items",
		"IdField":  "id",
	}
	if msg := Validate(models.SourceGitCommits, cfg); msg == "" {
		t.Error("expected an error for a commit-history source on a non-ADO-commits URL")
	}
}

// IdField is no longer a visible ADO-tab input — a commit-history source
// without one must be accepted and auto-default to "commitId" at fetch time
// (see enrichWithDiffs), not fail validation.
func TestValidateGitCommitsAcceptsMissingIdField(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits",
	}
	if msg := Validate(models.SourceGitCommits, cfg); msg != "" {
		t.Errorf("expected a commit-history config without IdField to pass, got %q", msg)
	}
}

func TestValidateGitCommitsAccepted(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://dev.azure.com/org/proj/_apis/git/repositories/repo/commits",
		"IdField":  "commitId",
	}
	if msg := Validate(models.SourceGitCommits, cfg); msg != "" {
		t.Errorf("expected a valid commit-history config to pass, got %q", msg)
	}
}

// A source type with no special URL-shape requirement (e.g. Work Items) must
// not be forced through the commit-history or code/test-code ADO-URL checks.
func TestValidateNonCommitsTypeSkipsCommitsRequirements(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://api.example.com/items",
	}
	if msg := Validate(models.SourceWorkItemQuery, cfg); msg != "" {
		t.Errorf("expected a non-commits, non-code source to skip URL-shape requirements, got %q", msg)
	}
}

// Source Code / Test Code sources always fetch real file content, so they
// require an ADO items-list URL, just as Git Commits sources require a
// commits-list URL.
func TestValidateCodeRepoRequiresAdoItemsURL(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://api.example.com/items",
	}
	if msg := Validate(models.SourceCodeRepo, cfg); msg == "" {
		t.Error("expected an error for a code source on a non-ADO-items URL")
	}
}

func TestValidateCodeRepoAcceptsAdoItemsURL(t *testing.T) {
	cfg := map[string]string{
		"Provider": "api",
		"Url":      "https://dev.azure.com/org/proj/_apis/git/repositories/repo/items",
	}
	if msg := Validate(models.SourceCodeRepo, cfg); msg != "" {
		t.Errorf("expected a valid code-repo config to pass, got %q", msg)
	}
}

func TestValidatePipelineRun(t *testing.T) {
	valid := func() map[string]string {
		return map[string]string{
			"Provider": "api", "Platform": "ado", "AdoOrg": "https://ado.example", "AdoProject": "proj",
			"Url": "https://ado.example/proj", "DefinitionId": "12", "Days": "30",
		}
	}
	if msg := Validate(models.SourcePipelineRun, valid()); msg != "" {
		t.Errorf("valid pipeline-run config rejected: %q", msg)
	}
	for key, bad := range map[string]string{"DefinitionId": "abc", "Days": "0", "Platform": ""} {
		cfg := valid()
		cfg[key] = bad
		if msg := Validate(models.SourcePipelineRun, cfg); msg == "" {
			t.Errorf("%s=%q: expected a validation error", key, bad)
		}
	}
	cfg := valid()
	delete(cfg, "Days")
	if msg := Validate(models.SourcePipelineRun, cfg); msg == "" {
		t.Error("missing Days: expected a validation error")
	}
}

func TestValidateAuthType(t *testing.T) {
	cfg := map[string]string{"Provider": "api", "Url": "https://api.example.com/items"}
	for _, ok := range []string{"", "none", "bearer", "Basic", "apikey"} {
		cfg["AuthType"] = ok
		if msg := Validate(models.SourceWorkItemQuery, cfg); msg != "" {
			t.Errorf("AuthType=%q rejected: %q", ok, msg)
		}
	}
	cfg["AuthType"] = "oauth"
	if msg := Validate(models.SourceWorkItemQuery, cfg); msg == "" {
		t.Error("expected an unknown AuthType to be rejected")
	}
}
