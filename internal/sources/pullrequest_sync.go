// Pull request indexing for pull-request sources: every pull request of the
// repository created in the last Days days, of any status, becomes one
// document so an agent can find it by meaning instead of by URL. The
// document holds what a reviewer would search by: title, description,
// status, branches, author, reviewers and votes, linked work items, every
// commit message, the changed-file list and every comment thread. Diffs are
// not embedded; the search_pull_request tool reads them live from the
// document's web_url.
//
// Fetching follows the other sources: anything that can't be read fails the
// sync (the previous index stays intact), and what a provider itself leaves
// out (GitHub's file and commit listing caps, an unreadable linked issue)
// is written into the document's Notes rather than dropped.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// adoPRListPageSize is the $top requested per page of the pull request list.
const adoPRListPageSize = 100

// prIndexer is the part of a provider only indexing needs.
type prIndexer interface {
	PullRequestProvider
	// listCreatedSince lists the IDs of every pull request created at or
	// after since, of any status, newest first; top > 0 caps the list.
	listCreatedSince(ctx context.Context, since time.Time, top int) ([]int, error)
	// indexDetails is Get without the checks, which go stale in an index.
	indexDetails(ctx context.Context, id int) (*PullRequest, error)
}

// prScope is the project / repository a pull-request source covers, for its
// documents' filters.
type prScope struct {
	project, repository string
	idPrefix            string // "!" (Azure DevOps) or "#" (GitHub)
}

// prRecord is everything fetched for one pull request.
type prRecord struct {
	pr         *PullRequest
	commits    []PRCommit
	changes    PRChanges
	threads    []PRThread
	incomplete []string
}

// pullRequestDays reads the source's Days window.
func pullRequestDays(src *models.SourceDefinition) (int, error) {
	days, err := strconv.Atoi(strings.TrimSpace(src.GetConfig("Days")))
	if err != nil || days <= 0 {
		return 0, fmt.Errorf("pull-request sources require a positive number of Days (got %q)", src.GetConfig("Days"))
	}
	return days, nil
}

// fetchPullRequestDocuments lists the source's pull requests and builds one
// document per pull request.
func fetchPullRequestDocuments(ctx context.Context, src *models.SourceDefinition, p prIndexer, scope prScope,
	top int, progress ProgressCallback) ([]models.SourceDocument, error) {
	days, err := pullRequestDays(src)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Listing pull requests"})
	}
	ids, err := p.listCreatedSince(ctx, time.Now().UTC().AddDate(0, 0, -days), top)
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}

	n := len(ids)
	records := make([]*prRecord, n)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	for i := 0; i < n && workCtx.Err() == nil; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, err := fetchPullRequestRecord(workCtx, p, ids[i])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("pull request %s%d: %w", scope.idPrefix, ids[i], err)
				}
				cancel()
				return
			}
			records[i] = rec
			completed++
			if progress != nil {
				progress(models.SyncProgress{
					Phase: "fetching", Current: completed, Total: n,
					Message: fmt.Sprintf("Fetching pull request %d/%d", completed, n),
				})
			}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	docs := make([]models.SourceDocument, 0, n)
	for _, rec := range records {
		docs = append(docs, pullRequestDocument(src, scope, rec))
	}
	return docs, nil
}

func fetchPullRequestRecord(ctx context.Context, p prIndexer, id int) (*prRecord, error) {
	pr, err := p.indexDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	commits, incomplete, err := p.Commits(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading commits: %w", err)
	}
	changes, err := p.Changes(ctx, id, PRRange{})
	if err != nil {
		return nil, fmt.Errorf("reading changed files: %w", err)
	}
	threads, _, err := p.Threads(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading comment threads: %w", err)
	}
	return &prRecord{pr: pr, commits: commits, changes: changes, threads: threads, incomplete: incomplete}, nil
}

// pullRequestDocument renders one pull request as a document.
func pullRequestDocument(src *models.SourceDefinition, scope prScope, rec *prRecord) models.SourceDocument {
	pr := rec.pr
	label := fmt.Sprintf("Pull request %s%d", scope.idPrefix, pr.ID)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: %s\n", label, pr.Title)
	writeField(&sb, "Status", pr.Status)
	if pr.IsDraft {
		writeField(&sb, "Draft", "yes")
	}
	writeField(&sb, "Author", pr.Author)
	writeField(&sb, "Created", pr.CreatedAt)
	writeField(&sb, "Source branch", pr.SourceBranch)
	writeField(&sb, "Target branch", pr.TargetBranch)
	writeField(&sb, "Repository", scope.project+"/"+scope.repository)
	writeField(&sb, "Merge status", pr.MergeStatus)
	writeField(&sb, "Web URL", pr.WebURL)

	if len(pr.Reviewers) > 0 {
		sb.WriteString("\nReviewers:\n")
		for _, r := range pr.Reviewers {
			who := r.Name
			if r.ID != "" && r.ID != r.Name {
				who += " <" + r.ID + ">"
			}
			req := ""
			if r.Required {
				req = " (required)"
			}
			fmt.Fprintf(&sb, "- %s: %s%s\n", who, r.Vote, req)
		}
	}

	if len(pr.WorkItems) > 0 {
		sb.WriteString("\nLinked work items:\n")
		for _, w := range pr.WorkItems {
			line := "- #" + w.ID
			if w.Type != "" {
				line += " [" + w.Type + "]"
			}
			if w.Title != "" {
				line += " " + w.Title
			}
			if w.State != "" {
				line += " (" + w.State + ")"
			}
			sb.WriteString(line + "\n")
		}
	}

	if d := strings.TrimSpace(pr.Description); d != "" {
		sb.WriteString("\nDescription:\n" + d + "\n")
	}

	if len(rec.commits) > 0 {
		fmt.Fprintf(&sb, "\nCommits (%d):\n", len(rec.commits))
		for _, c := range rec.commits {
			fmt.Fprintf(&sb, "- %s %s, %s:\n", shortSHA(c.SHA), c.Author, c.Date)
			sb.WriteString(indent(strings.TrimSpace(c.Message), "  ") + "\n")
		}
	}

	if len(rec.changes.Files) > 0 {
		fmt.Fprintf(&sb, "\nChanged files (%d):\n", len(rec.changes.Files))
		for _, f := range rec.changes.Files {
			if f.OriginalPath != "" {
				fmt.Fprintf(&sb, "- %s %s → %s\n", f.ChangeType, f.OriginalPath, f.Path)
			} else {
				fmt.Fprintf(&sb, "- %s %s\n", f.ChangeType, f.Path)
			}
		}
	}

	if len(rec.threads) > 0 {
		fmt.Fprintf(&sb, "\nDiscussion (%d threads):\n", len(rec.threads))
		for _, th := range rec.threads {
			head := "Thread"
			switch {
			case th.Path != "" && th.Line > 0:
				head += fmt.Sprintf(" on %s:%d", th.Path, th.Line)
			case th.Path != "":
				head += " on " + th.Path
			case th.Kind == "review":
				head = "Review"
			}
			if th.Status != "" {
				head += " [" + th.Status + "]"
			}
			if th.Outdated {
				head += " (outdated)"
			}
			sb.WriteString("\n" + head + "\n")
			for _, c := range th.Comments {
				prefix := "- "
				if c.ReplyTo != "" {
					prefix = "  ↳ "
				}
				fmt.Fprintf(&sb, "%s%s, %s:\n", prefix, c.Author, c.CreatedAt)
				sb.WriteString(indent(strings.TrimSpace(c.Body), "    ") + "\n")
			}
		}
	}

	notes := append(append(append([]string{}, pr.Notes...), rec.incomplete...), rec.changes.Incomplete...)
	if len(notes) > 0 {
		sb.WriteString("\nNotes:\n")
		for _, n := range notes {
			sb.WriteString("- " + n + "\n")
		}
	}

	props := map[string]string{
		"title":         pr.Title,
		"type":          "pull_request",
		"pr_id":         strconv.Itoa(pr.ID),
		"status":        pr.Status,
		"author":        pr.Author,
		"source_branch": pr.SourceBranch,
		"target_branch": pr.TargetBranch,
		"created_at":    pr.CreatedAt,
		"web_url":       pr.WebURL,
		"project":       scope.project,
		"repository":    scope.repository,
	}
	var f models.DocumentFilters
	f.Add(models.FilterProject, scope.project)
	f.Add(models.FilterRepository, scope.repository)
	f.Add(models.FilterState, pr.Status)
	f.Add(models.FilterBranch, pr.TargetBranch)
	f.Add(models.FilterAuthor, splitIdentity(pr.Author)...)
	for _, r := range pr.Reviewers {
		f.Add(models.FilterReviewer, r.Name, r.ID)
	}
	f.Date = parseTime(pr.CreatedAt)

	return models.SourceDocument{
		ID:          fmt.Sprintf("%s_pr_%d", src.ID, pr.ID),
		Text:        strings.TrimSpace(sb.String()),
		Tags:        map[string]string{"source_id": src.ID, "source_name": src.Name},
		Properties:  props,
		Filters:     f,
		ChunkHeader: fmt.Sprintf("%s: %s", label, pr.Title),
	}
}

// splitIdentity splits "Name <unique>" into both parts, so either matches
// a filter.
func splitIdentity(who string) []string {
	name, rest, ok := strings.Cut(who, " <")
	if !ok {
		return []string{who}
	}
	return []string{name, strings.TrimSuffix(rest, ">")}
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func indent(s, prefix string) string {
	if s == "" {
		return prefix + "(empty)"
	}
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// ── Azure DevOps ─────────────────────────────────────────────────────────────

// listCreatedSince pages the repository's pull requests of every status.
// Azure DevOps lists the newest first; listing stops at the first page that
// holds nothing created on or after since.
func (c *adoPRClient) listCreatedSince(ctx context.Context, since time.Time, top int) ([]int, error) {
	var ids []int
	seen := map[int]bool{}
	for skip := 0; ; skip += adoPRListPageSize {
		q := url.Values{
			"searchCriteria.status": {PRStatusAll},
			"$top":                  {strconv.Itoa(adoPRListPageSize)},
			"$skip":                 {strconv.Itoa(skip)},
		}
		var resp struct {
			Value []adoPullRequest `json:"value"`
		}
		if _, err := c.getJSON(ctx, c.repoAPI()+"/pullrequests"+c.query(q), &resp); err != nil {
			return nil, err
		}
		inWindow := 0
		for _, pr := range resp.Value {
			created, err := time.Parse(time.RFC3339, pr.CreationDate)
			if err != nil {
				return nil, fmt.Errorf("pull request !%d has an unreadable creation date %q", pr.PullRequestID, pr.CreationDate)
			}
			if created.Before(since) {
				continue
			}
			inWindow++
			// A pull request created during listing shifts the pages.
			if !seen[pr.PullRequestID] {
				seen[pr.PullRequestID] = true
				ids = append(ids, pr.PullRequestID)
			}
			if top > 0 && len(ids) >= top {
				return ids, nil
			}
		}
		if len(resp.Value) < adoPRListPageSize || inWindow == 0 {
			return ids, nil
		}
	}
}

func (c *adoPRClient) indexDetails(ctx context.Context, id int) (*PullRequest, error) {
	return c.get(ctx, id, false)
}

// fetchAdoPullRequests serves an Azure DevOps pull-request source.
func (a *APISource) fetchAdoPullRequests(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	org, project, repo := adoSourceScope(a.src)
	if org == "" || project == "" || repo == "" {
		return nil, fmt.Errorf("Azure DevOps pull-request sources require an organization, a project and a repository")
	}
	c := newAdoPRClient(a.src, a.secrets, org, project, repo)
	top := 0
	if n, err := strconv.Atoi(a.src.GetConfig("Top")); err == nil && n > 0 {
		top = n
	}
	return fetchPullRequestDocuments(ctx, a.src, c, prScope{project: project, repository: repo, idPrefix: "!"}, top, progress)
}

// ── GitHub ───────────────────────────────────────────────────────────────────

// listCreatedSince pages /pulls of every state, newest first, stopping at
// the first pull request created before since.
func (c *githubPRClient) listCreatedSince(ctx context.Context, since time.Time, top int) ([]int, error) {
	var ids []int
	seen := map[int]bool{}
	done := false
	err := c.g.pages(ctx, c.g.repoAPI()+"/pulls?state=all&sort=created&direction=desc", func(data []byte) (int, bool, error) {
		var page []githubPR
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		for _, pr := range page {
			created, err := time.Parse(time.RFC3339, pr.CreatedAt)
			if err != nil {
				return 0, false, fmt.Errorf("pull request #%d has an unreadable creation date %q", pr.Number, pr.CreatedAt)
			}
			if created.Before(since) {
				done = true
				break
			}
			if !seen[pr.Number] {
				seen[pr.Number] = true
				ids = append(ids, pr.Number)
			}
			if top > 0 && len(ids) >= top {
				done = true
				break
			}
		}
		return len(page), !done, nil
	})
	return ids, err
}

func (c *githubPRClient) indexDetails(ctx context.Context, id int) (*PullRequest, error) {
	pr, err := c.pr(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.details(ctx, pr, false)
}

// fetchPullRequests serves a GitHub pull-request source.
func (g *GitHubSource) fetchPullRequests(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	return fetchPullRequestDocuments(ctx, g.src, &githubPRClient{g: g},
		prScope{project: g.owner, repository: g.repo, idPrefix: "#"}, g.top(), progress)
}
