// Pull requests, read live from Azure DevOps or GitHub for review.
// Pull-request sources also index them (pullrequest_sync.go) so search can
// find them; diffs always come from the live reads here. A pull request is addressed by its web URL, which is
// matched to a configured source of the same server/organization so that
// source's credentials are used; listing takes a source directly.
//
// Diffs are merge-base → head (or between two iterations), one file at a
// time, so a large pull request is paged rather than truncated. Where a
// provider itself leaves something out (GitHub omits patches of binary or
// very large files and caps file and commit listings), the result says so in
// its Incomplete notes rather than hiding it.
package sources

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// PR statuses, in Azure DevOps' terms; GitHub's open / merged / closed
// unmerged map onto them.
const (
	PRStatusActive    = "active"
	PRStatusCompleted = "completed"
	PRStatusAbandoned = "abandoned"
	PRStatusAll       = "all"
)

// PRListFilter narrows a pull request listing. Author and Reviewer are "me"
// (the token's owner), an email (Azure DevOps) or a login (GitHub).
type PRListFilter struct {
	Status       string
	Author       string
	Reviewer     string
	TargetBranch string
	Page         int // 1-based, PRPageSize per page
}

// PRPageSize is the number of pull requests, files, threads or commits
// returned per page.
const PRPageSize = 20

type PRSummary struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Author       string `json:"author"`
	SourceBranch string `json:"source_branch,omitempty"`
	TargetBranch string `json:"target_branch,omitempty"`
	IsDraft      bool   `json:"is_draft"`
	CreatedAt    string `json:"created_at"`
	WebURL       string `json:"web_url"`
}

type PRReviewer struct {
	Name     string `json:"name"`
	ID       string `json:"id"` // email / unique name (ADO) or login (GitHub)
	Vote     string `json:"vote"`
	Required bool   `json:"required,omitempty"`
	CommitID string `json:"commit_id,omitempty"` // GitHub: commit the latest review was made on
}

type PRWorkItem struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Type   string `json:"type,omitempty"`
	State  string `json:"state,omitempty"`
	WebURL string `json:"web_url,omitempty"`
}

type PRCheck struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
	Blocking    bool   `json:"blocking,omitempty"`
	WebURL      string `json:"web_url,omitempty"`
}

type PullRequest struct {
	PRSummary
	Description string       `json:"description"`
	MergeStatus string       `json:"merge_status,omitempty"`
	HeadCommit  string       `json:"head_commit"`
	BaseCommit  string       `json:"base_commit"`
	Reviewers   []PRReviewer `json:"reviewers"`
	WorkItems   []PRWorkItem `json:"work_items"`
	Checks      []PRCheck    `json:"checks"`
	// Notes reports auxiliary data (work items, checks) that couldn't be
	// read; the rest of the pull request is still complete.
	Notes []string `json:"notes,omitempty"`
}

// PRRange selects what a diff compares: From 0 is the merge base with the
// target branch, To 0 the latest iteration.
type PRRange struct {
	From, To int
}

// PRChange is one changed file of a pull request, without its content.
type PRChange struct {
	Path         string
	OriginalPath string // a rename's old path
	ChangeType   string

	// provider-specific detail for rendering the diff
	oldObjectID, newObjectID string // ADO blobs
	baseCommit, headCommit   string
	githubFile               *githubCommitFile
}

// PRChanges is every changed file of a range, plus notes on any files the
// provider didn't list.
type PRChanges struct {
	Files      []PRChange
	HeadCommit string
	Incomplete []string
}

type PRFile struct {
	Path         string  `json:"path"`
	OriginalPath string  `json:"original_path,omitempty"`
	ChangeType   string  `json:"change_type"`
	Additions    int     `json:"additions"`
	Deletions    int     `json:"deletions"`
	Diff         string  `json:"diff,omitempty"`
	Content      *string `json:"content,omitempty"` // the file at head, when requested
	Incomplete   string  `json:"incomplete,omitempty"`
}

type PRComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	ReplyTo   string `json:"reply_to,omitempty"`
}

type PRThread struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`             // "file" (line-anchored), "general" or "review"
	Status   string      `json:"status,omitempty"` // ADO: active, fixed, closed, …; GitHub REST has none
	Path     string      `json:"path,omitempty"`
	Line     int         `json:"line,omitempty"`
	Outdated bool        `json:"outdated,omitempty"`
	Comments []PRComment `json:"comments"`
}

type PRIteration struct {
	Number      int    `json:"number"`
	HeadCommit  string `json:"head_commit"`
	BaseCommit  string `json:"base_commit,omitempty"`
	Author      string `json:"author,omitempty"`
	CreatedAt   string `json:"created_at"`
	Description string `json:"description,omitempty"`
}

type PRCommit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	WebURL  string `json:"web_url,omitempty"`
}

// PullRequestProvider reads one repository's pull requests.
type PullRequestProvider interface {
	List(ctx context.Context, f PRListFilter) (prs []PRSummary, hasMore bool, err error)
	Get(ctx context.Context, id int) (*PullRequest, error)
	Iterations(ctx context.Context, id int) ([]PRIteration, error)
	// Changes lists every file changed in r, without content.
	Changes(ctx context.Context, id int, r PRRange) (PRChanges, error)
	// Diff renders the given changes (from Changes); withContent adds each
	// file's content at head.
	Diff(ctx context.Context, changes []PRChange, withContent bool) ([]PRFile, error)
	Threads(ctx context.Context, id int) (threads []PRThread, notes []string, err error)
	Commits(ctx context.Context, id int) (commits []PRCommit, incomplete []string, err error)
	// WebURL is the pull request's browser link.
	WebURL(id int) string
}

// ResolvedPR is a pull request URL matched to the source whose credentials
// read it.
type ResolvedPR struct {
	Provider PullRequestProvider
	ID       int
	Source   *models.SourceDefinition
}

// ResolvePullRequestURL parses an Azure DevOps or GitHub pull request URL and
// picks the configured source whose credentials read it: the same
// repository if there is one, else the same project/owner, else the same
// organization/server. Between equally close sources, an enabled one wins
// over a disabled one.
func ResolvePullRequestURL(rawURL string, all []models.SourceDefinition, store secrets.Reader) (*ResolvedPR, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("pull request URL must be an http(s) URL (got %q)", rawURL)
	}

	type candidate struct {
		src   *models.SourceDefinition
		rank  int
		build func() (PullRequestProvider, error)
		id    int
	}
	var best *candidate
	for i := range all {
		src := &all[i]
		var c *candidate
		switch src.GetConfig("Provider") {
		case "github":
			if ref, rank, ok := matchGitHubPR(src, u); ok {
				c = &candidate{src: src, rank: rank, id: ref.id, build: func() (PullRequestProvider, error) {
					return newGitHubPRClient(src, store, ref.owner, ref.repo)
				}}
			}
		case "", "api", "custom":
			if ref, rank, ok := matchAdoPR(src, u); ok {
				c = &candidate{src: src, rank: rank, id: ref.id, build: func() (PullRequestProvider, error) {
					return newAdoPRClient(src, store, ref.org, ref.project, ref.repo), nil
				}}
			}
		}
		if c == nil {
			continue
		}
		c.rank *= 2
		if !src.Disabled {
			c.rank++
		}
		if best == nil || c.rank > best.rank {
			best = c
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no configured Azure DevOps or GitHub source matches %s — add a source for that "+
			"organization/server so its credentials can be used, or check the URL is a pull request link "+
			"(…/_git/{repo}/pullrequest/{id} or …/{owner}/{repo}/pull/{number})", rawURL)
	}
	p, err := best.build()
	if err != nil {
		return nil, err
	}
	return &ResolvedPR{Provider: p, ID: best.id, Source: best.src}, nil
}

// PullRequestProviderFor reads the pull requests of the repository src
// points at.
func PullRequestProviderFor(src *models.SourceDefinition, store secrets.Reader) (PullRequestProvider, error) {
	switch src.GetConfig("Provider") {
	case "github":
		g, err := newGitHubSource(src, store)
		if err != nil {
			return nil, err
		}
		return newGitHubPRClient(src, store, g.owner, g.repo)
	case "", "api", "custom":
		org, project, repo := adoSourceScope(src)
		if org == "" || project == "" || repo == "" {
			return nil, fmt.Errorf("source %q doesn't name an Azure DevOps repository — use a code, test code, "+
				"documentation, commit-history or pull-request source of the repository", src.Name)
		}
		return newAdoPRClient(src, store, org, project, repo), nil
	default:
		return nil, fmt.Errorf("source %q has provider %q, which has no pull requests", src.Name, src.GetConfig("Provider"))
	}
}

type adoPRRef struct {
	org, project, repo string
	id                 int
}

// matchAdoPR matches a pull request URL — {org}/{project}/_git/{repo}/
// pullrequest/{id}, or {org}/_git/{repo}/pullrequest/{id} for a repository
// named after its project — against an Azure DevOps source's organization.
func matchAdoPR(src *models.SourceDefinition, u *url.URL) (adoPRRef, int, bool) {
	org, project, repo := adoSourceScope(src)
	if org == "" {
		return adoPRRef{}, 0, false
	}
	ou, err := url.Parse(org)
	if err != nil || !strings.EqualFold(ou.Host, u.Host) {
		return adoPRRef{}, 0, false
	}
	orgPath := strings.Trim(ou.Path, "/")
	path := strings.Trim(u.Path, "/")
	if orgPath != "" {
		if !strings.HasPrefix(strings.ToLower(path), strings.ToLower(orgPath)+"/") {
			return adoPRRef{}, 0, false
		}
		path = path[len(orgPath)+1:]
	}
	segs := strings.Split(path, "/")
	if len(segs) >= 4 && segs[0] == "_git" {
		segs = append([]string{segs[1]}, segs...) // project named like the repo
	}
	if len(segs) < 5 || segs[1] != "_git" || !strings.EqualFold(segs[3], "pullrequest") {
		return adoPRRef{}, 0, false
	}
	id, err := strconv.Atoi(segs[4])
	if err != nil || id <= 0 {
		return adoPRRef{}, 0, false
	}
	ref := adoPRRef{org: org, project: segs[0], repo: segs[2], id: id}
	rank := 1
	if strings.EqualFold(project, ref.project) {
		rank = 2
		if strings.EqualFold(repo, ref.repo) {
			rank = 3
		}
	}
	return ref, rank, true
}

// adoSourceScope reads an Azure DevOps source's organization (collection)
// URL, project and repository: from the form's AdoOrg/AdoProject/AdoRepo
// fields, else from its {org}/{project}/_apis/git/repositories/{repo}/… Url.
func adoSourceScope(src *models.SourceDefinition) (org, project, repo string) {
	org = strings.TrimRight(strings.TrimSpace(src.GetConfig("AdoOrg")), "/")
	project = strings.TrimSpace(src.GetConfig("AdoProject"))
	repo = strings.TrimSpace(src.GetConfig("AdoRepo"))

	raw := src.GetConfig("Url")
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		before, after, found := strings.Cut(u.Path, "/_apis/")
		if found {
			if i := strings.LastIndexByte(before, '/'); i >= 0 {
				if org == "" {
					org = u.Scheme + "://" + u.Host + before[:i]
				}
				if project == "" {
					project = before[i+1:]
				}
			}
			if rest, ok := strings.CutPrefix(after, "git/repositories/"); ok && repo == "" {
				repo, _, _ = strings.Cut(rest, "/")
			}
		}
	}
	if repo == "" {
		// AdoResource is the form's "git/repositories/{repo}/{commits|items}".
		if rest, ok := strings.CutPrefix(src.GetConfig("AdoResource"), "git/repositories/"); ok {
			repo, _, _ = strings.Cut(rest, "/")
		}
	}
	if p, err := url.PathUnescape(project); err == nil {
		project = p
	}
	if r, err := url.PathUnescape(repo); err == nil {
		repo = r
	}
	return org, project, repo
}

type githubPRRef struct {
	owner, repo string
	id          int
}

// matchGitHubPR matches {server}/{owner}/{repo}/pull/{number} against a
// GitHub source's server.
func matchGitHubPR(src *models.SourceDefinition, u *url.URL) (githubPRRef, int, bool) {
	_, webBase, err := GitHubBases(src.GetConfig("GitHubUrl"))
	if err != nil {
		return githubPRRef{}, 0, false
	}
	wu, err := url.Parse(webBase)
	if err != nil || !strings.EqualFold(wu.Host, u.Host) {
		return githubPRRef{}, 0, false
	}
	path := strings.Trim(u.Path, "/")
	if base := strings.Trim(wu.Path, "/"); base != "" {
		rest, ok := strings.CutPrefix(path, base+"/")
		if !ok {
			return githubPRRef{}, 0, false
		}
		path = rest
	}
	segs := strings.Split(path, "/")
	if len(segs) < 4 || segs[2] != "pull" {
		return githubPRRef{}, 0, false
	}
	id, err := strconv.Atoi(segs[3])
	if err != nil || id <= 0 {
		return githubPRRef{}, 0, false
	}
	ref := githubPRRef{owner: segs[0], repo: segs[1], id: id}
	rank := 1
	if strings.EqualFold(src.GetConfig("GitHubOwner"), ref.owner) {
		rank = 2
		if strings.EqualFold(src.GetConfig("GitHubRepo"), ref.repo) {
			rank = 3
		}
	}
	return ref, rank, true
}

// SelectChanges picks the changes Diff should render: the one at path (a
// rename's old path matches too, a leading "/" is ignored), or else page
// (1-based) of PRPageSize files. hasMore reports further pages.
func SelectChanges(all []PRChange, path string, page int) (selected []PRChange, hasMore bool, err error) {
	if path != "" {
		want := strings.TrimPrefix(path, "/")
		i := slices.IndexFunc(all, func(c PRChange) bool {
			return strings.TrimPrefix(c.Path, "/") == want ||
				(c.OriginalPath != "" && strings.TrimPrefix(c.OriginalPath, "/") == want)
		})
		if i < 0 {
			return nil, false, fmt.Errorf("%s is not changed in this pull request (or iteration range) — "+
				"use files mode for the list of changed paths", path)
		}
		return all[i : i+1], false, nil
	}
	items, more := PageOf(all, page)
	return items, more, nil
}

// PageOf returns page (1-based) of PRPageSize items and whether more follow.
func PageOf[T any](all []T, page int) ([]T, bool) {
	if page < 1 {
		page = 1
	}
	start := (page - 1) * PRPageSize
	if start >= len(all) {
		return []T{}, false
	}
	end := min(start+PRPageSize, len(all))
	return all[start:end], end < len(all)
}

// diffLineCounts counts a unified diff's added and removed lines. Only hunk
// lines count, so the ---/+++ file header (or a removed line that itself
// starts with "--") isn't mistaken for one another.
func diffLineCounts(diff string) (additions, deletions int) {
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// checkRange validates r against n iterations and fills in a latest To.
func checkRange(r PRRange, n int) (PRRange, error) {
	if n == 0 {
		return r, fmt.Errorf("pull request has no iterations")
	}
	if r.To == 0 {
		r.To = n
	}
	if r.From < 0 || r.To < 1 || r.To > n || r.From >= r.To {
		return r, fmt.Errorf("iteration range %d..%d is invalid: this pull request has iterations 1..%d, "+
			"and from_iteration (0 = merge base) must be less than to_iteration", r.From, r.To, n)
	}
	return r, nil
}
