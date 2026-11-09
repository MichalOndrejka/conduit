// GitHub source: repository files, commit history with diffs and issues
// with their comments, read from the GitHub REST API of github.com or a
// GitHub Enterprise Server. Pull requests and GitHub Actions workflow runs
// are read live by their tools (pullrequests.go, pipelines.go).
//
//	GitHubUrl    server URL: https://github.com (default) or a GitHub
//	             Enterprise Server such as https://github.example.com
//	GitHubOwner  user or organization owning the repository
//	GitHubRepo   repository name
//	Token        credential name of a personal access token (empty: public
//	             repositories only, at a low rate limit)
//	Branch       files and commits: branch to read (default: the
//	             repository's default branch)
//	PathFilter   files: comma-separated wildcards, as for Azure DevOps
//	Top          commits and issues: max items, most recent first
//	FetchMode    requirements / test cases: "files" (default) or "workitems"
//	             (issues)
//	Labels       issues: comma-separated labels; an issue matching any of
//	             them is fetched (default: every issue)
//	WorkItemTypes issues: comma-separated organization issue types (Bug,
//	             Feature, …); an issue of any of them is fetched (default:
//	             every issue, typed or not)
//	Workflow     pipeline runs: workflow file name (ci.yml) or numeric ID
//	Runs         pipeline runs: how many runs a listing returns by default
//	VerifySSL    "false" to skip TLS verification (self-hosted servers)
//
// GitHub's own limits apply and are noted on the affected documents rather
// than hidden: a commit lists at most 3000 changed files, and patches of
// binary or very large files are not returned.
package sources

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/stats"
)

// githubPageSize is the per_page requested from GitHub's paged listings (its
// maximum).
const githubPageSize = 100

type GitHubSource struct {
	src     *models.SourceDefinition
	secrets secrets.Reader
	client  *http.Client

	apiBase string // https://api.github.com or {server}/api/v3
	webBase string // https://github.com or {server}
	owner   string
	repo    string
}

func newGitHubSource(src *models.SourceDefinition, store secrets.Reader) (*GitHubSource, error) {
	apiBase, webBase, err := GitHubBases(src.GetConfig("GitHubUrl"))
	if err != nil {
		return nil, err
	}
	g := &GitHubSource{
		src: src, secrets: store,
		apiBase: apiBase, webBase: webBase,
		owner: strings.TrimSpace(src.GetConfig("GitHubOwner")),
		repo:  strings.TrimSpace(src.GetConfig("GitHubRepo")),
	}
	if g.owner == "" || g.repo == "" {
		return nil, fmt.Errorf("GitHub sources require an owner and a repository")
	}
	g.client = &http.Client{Timeout: 60 * time.Second}
	if strings.EqualFold(src.GetConfig("VerifySSL"), "false") {
		g.client.Transport = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-hosted servers with private CAs
		}
	}
	// Counts requests (and rate-limit refusals) for the sync's statistics.
	g.client.Transport = stats.CountingTransport(g.client.Transport)
	return g, nil
}

// GitHubBases derives the REST API base and the web base from a server URL:
// github.com (or empty) → https://api.github.com; a GitHub Enterprise Cloud
// tenant with data residency ({tenant}.ghe.com) → https://api.{tenant}.ghe.com;
// a GitHub Enterprise Server → {server}/api/v3. An API URL given directly is
// accepted too.
func GitHubBases(server string) (apiBase, webBase string, err error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		server = "https://github.com"
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", fmt.Errorf("GitHub server URL must be a valid http(s) URL (got %q)", server)
	}
	switch host := strings.ToLower(u.Host); {
	case host == "github.com" || host == "www.github.com" || host == "api.github.com":
		return "https://api.github.com", "https://github.com", nil
	case strings.HasSuffix(host, ".ghe.com"):
		tenant := strings.TrimPrefix(host, "api.")
		return u.Scheme + "://api." + tenant, u.Scheme + "://" + tenant, nil
	case strings.HasSuffix(u.Path, "/api/v3"):
		return server, strings.TrimSuffix(server, "/api/v3"), nil
	default:
		return server + "/api/v3", server, nil
	}
}

func (g *GitHubSource) FetchDocuments(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	switch {
	case models.IsLiveType(g.src.Type):
		return nil, errLiveSource(g.src)
	case isWorkItemQuery(g.src):
		return g.fetchIssues(ctx, progress)
	case g.src.Type == models.SourceGitCommits:
		return g.fetchCommits(ctx, progress)
	default:
		return g.fetchFiles(ctx, progress)
	}
}

// repoAPI is the repository's REST base, /repos/{owner}/{repo}.
func (g *GitHubSource) repoAPI() string {
	return g.apiBase + "/repos/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo)
}

// repoWeb is the repository's web page.
func (g *GitHubSource) repoWeb() string {
	return g.webBase + "/" + url.PathEscape(g.owner) + "/" + url.PathEscape(g.repo)
}

// top is the configured item cap, or 0 for unlimited.
func (g *GitHubSource) top() int {
	n, err := strconv.Atoi(g.src.GetConfig("Top"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// get performs an authenticated GET; accept overrides the JSON media type
// (e.g. raw blob content).
func (g *GitHubSource) get(ctx context.Context, reqURL, accept string) ([]byte, http.Header, error) {
	return getWithRetry(ctx, g.client, reqURL, g.prepare(accept))
}

func (g *GitHubSource) getJSON(ctx context.Context, reqURL string, v any) (http.Header, error) {
	data, header, err := g.get(ctx, reqURL, "")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return nil, fmt.Errorf("response from %s is not valid JSON: %w", reqURL, err)
	}
	return header, nil
}

// pages follows a listing through every page via per_page/page, calling
// each with the raw page until it returns false or a page comes back short.
func (g *GitHubSource) pages(ctx context.Context, listURL string, each func(data []byte) (count int, more bool, err error)) error {
	sep := "?"
	if strings.Contains(listURL, "?") {
		sep = "&"
	}
	for page := 1; ; page++ {
		data, _, err := g.get(ctx, fmt.Sprintf("%s%sper_page=%d&page=%d", listURL, sep, githubPageSize, page), "")
		if err != nil {
			return err
		}
		n, more, err := each(data)
		if err != nil {
			return err
		}
		if !more || n < githubPageSize {
			return nil
		}
	}
}

// branch returns the configured branch, or the repository's default branch.
func (g *GitHubSource) branch(ctx context.Context) (string, error) {
	if b := strings.TrimSpace(g.src.GetConfig("Branch")); b != "" {
		return b, nil
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := g.getJSON(ctx, g.repoAPI(), &repo); err != nil {
		return "", fmt.Errorf("reading repository %s/%s: %w", g.owner, g.repo, err)
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("repository %s/%s has no default branch (is it empty?)", g.owner, g.repo)
	}
	return repo.DefaultBranch, nil
}

// scopeFilters returns a document's owner/repository filters and mirrors
// them into props. The owner fills the project filter, so search narrows
// GitHub and Azure DevOps sources the same way.
func (g *GitHubSource) scopeFilters(props map[string]string) models.DocumentFilters {
	var f models.DocumentFilters
	f.Add(models.FilterProject, g.owner)
	f.Add(models.FilterRepository, g.repo)
	props["project"] = g.owner
	props["repository"] = g.repo
	return f
}

func (g *GitHubSource) tags() map[string]string {
	return map[string]string{"source_id": g.src.ID, "source_name": g.src.Name}
}

// githubUser is the user object GitHub embeds in issues, comments and runs.
type githubUser struct {
	Login string `json:"login"`
}
