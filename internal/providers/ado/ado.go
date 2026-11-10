// Package ado is the Azure DevOps integration. A provider's base URL is
// https://dev.azure.com (every organization the token sees), one
// organization or collection, or an Azure DevOps Server (its collections);
// sources add the organization/collection when the URL holds several, the
// project and, where relevant, the repository, pipeline folder or work item
// types.
package ado

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// Kind is the identifier stored on providers.
const Kind = "ado"

// defaultAPIVersion is the REST API version requests ask for unless the
// provider overrides it — supported by Azure DevOps Services and Azure DevOps
// Server 2022+. Older servers need e.g. 6.0 (Server 2020) or 5.1 (2019).
const defaultAPIVersion = "7.1"

func apiVersionOf(p models.Provider) string {
	if v := strings.TrimSpace(p.APIVersion); v != "" {
		return v
	}
	return defaultAPIVersion
}

type Integration struct{}

func (Integration) Kind() string           { return Kind }
func (Integration) Label() string          { return "Azure DevOps" }
func (Integration) DefaultBaseURL() string { return "https://dev.azure.com" }
func (Integration) BaseURLHint() string {
	return "https://dev.azure.com lists every organization the token can access (create the PAT for \"All accessible organizations\"). " +
		"For Azure DevOps Server, the server URL (https://tfs.example.com/tfs) lists its collections. " +
		"A single organization or collection URL works too."
}

func (Integration) SourceTypes() []string {
	return []string{
		models.SourceWorkItemQuery, models.SourceRequirements, models.SourceTestCase,
		models.SourceGitCommits, models.SourcePipelineRun, models.SourcePullRequest,
		models.SourceCodeRepo, models.SourceTestCodeRepo, models.SourceDocumentation,
	}
}

// Test lists the root of the provider's tree, which needs a valid token.
func (i Integration) Test(ctx context.Context, conn providers.Conn) error {
	_, err := i.Discover(ctx, conn, providers.NodeRef{})
	return err
}

func (i Integration) Validate(srcType string, cfg map[string]string) string {
	if !slices.Contains(i.SourceTypes(), srcType) {
		return fmt.Sprintf("Azure DevOps has no %q sources.", srcType)
	}
	if strings.TrimSpace(cfg[providers.KeyProject]) == "" {
		return "Project is required."
	}
	if msg := validatePositive(cfg, providers.KeyTop, "Max items"); msg != "" {
		return msg
	}
	switch {
	case srcType == models.SourcePipelineRun:
		return validateRuns(cfg)
	case srcType == models.SourcePullRequest:
		return ""
	case providers.IsWorkItemType(srcType, cfg):
		if len(providers.ParseList(cfg[providers.KeyWorkItemTypes])) == 0 {
			return "Select at least one work item type."
		}
	default: // repository files and commits
		if strings.TrimSpace(cfg[providers.KeyRepo]) == "" {
			return "Repository is required."
		}
	}
	return ""
}

func validatePositive(cfg map[string]string, key, label string) string {
	if v := strings.TrimSpace(cfg[key]); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			return label + " must be a positive number."
		}
	}
	return ""
}

func validateRuns(cfg map[string]string) string {
	v := strings.TrimSpace(cfg[providers.KeyRuns])
	if v == "" {
		return ""
	}
	if n, err := strconv.Atoi(v); err != nil || n <= 0 || n > sources.MaxPipelineRuns {
		return fmt.Sprintf("Runs must be a number from 1 to %d.", sources.MaxPipelineRuns)
	}
	return ""
}

// Compile builds the Azure DevOps configuration of the fetchers in
// internal/sources: the generic API source with the ADO preset, authenticated
// with the provider's token as a basic-auth password.
func (Integration) Compile(prov models.Provider, src *models.SourceDefinition) map[string]string {
	cfg := src.Config
	org := OrgURL(prov.BaseURL, cfg[providers.KeyOrg])
	project := strings.TrimSpace(cfg[providers.KeyProject])
	repo := strings.TrimSpace(cfg[providers.KeyRepo])
	version := apiVersionOf(prov)

	out := map[string]string{
		"Provider":      "api",
		"Platform":      "ado",
		"AuthType":      "basic",
		"Username":      "",
		"Password":      models.ProviderTokenName(prov.ID),
		"AdoOrg":        org,
		"AdoProject":    project,
		"AdoRepo":       repo,
		"AdoApiVersion": version,
	}
	if prov.SkipTLSVerify {
		out["VerifySSL"] = "false"
	}
	providers.CopyKeys(out, cfg,
		providers.KeyBranch, providers.KeyPathFilter, providers.KeyFetchMode,
		providers.KeyWorkItemTypes, providers.KeyAreaPaths, providers.KeyTop,
		providers.KeyRuns, providers.KeyPipelineFolder, providers.KeyRepositories,
	)

	projectBase := org + "/" + url.PathEscape(project)
	switch {
	case src.Type == models.SourcePipelineRun, src.Type == models.SourcePullRequest,
		providers.IsWorkItemType(src.Type, cfg):
		out["Url"] = projectBase
	default:
		resource := "items"
		if src.Type == models.SourceGitCommits {
			resource = "commits"
		}
		out["Url"] = fmt.Sprintf("%s/_apis/git/repositories/%s/%s?api-version=%s",
			projectBase, url.PathEscape(repo), resource, url.QueryEscape(version))
	}
	return out
}

// OrgURL is the URL of organization/collection org under the provider's base
// URL, or the base URL itself when org is empty (a provider made for one
// organization or collection).
func OrgURL(base, org string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if org = strings.TrimSpace(org); org == "" {
		return base
	}
	return base + "/" + url.PathEscape(org)
}

// newClient authenticates with the PAT as a basic-auth password and an empty
// username, as Azure DevOps expects.
func newClient(conn providers.Conn) *providers.Client {
	token := conn.Token
	return &providers.Client{
		HTTP:      conn.HTTPClient(),
		Authorize: func(r *http.Request) { r.SetBasicAuth("", token) },
	}
}
