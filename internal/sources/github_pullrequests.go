// GitHub pull requests (see pullrequests.go), read from the REST API with the
// matched GitHub source's token. GitHub's own limits are reported, not
// worked around: a pull request lists at most 3000 files and 250 commits, a
// comparison at most 300 files, and patches of binary or very large files are
// omitted. Iterations are the pull request's commits, in order — GitHub has
// no push history in REST.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

const (
	githubMaxPRFiles      = 3000
	githubMaxPRCommits    = 250
	githubMaxCompareFiles = 300
)

type githubPRClient struct {
	g *GitHubSource
}

func newGitHubPRClient(src *models.SourceDefinition, store secrets.Reader, owner, repo string) (PullRequestProvider, error) {
	g, err := newGitHubSource(src, store)
	if err != nil {
		return nil, err
	}
	g.owner, g.repo = owner, repo
	return &githubPRClient{g: g}, nil
}

func (c *githubPRClient) WebURL(id int) string {
	return fmt.Sprintf("%s/pull/%d", c.g.repoWeb(), id)
}

func (c *githubPRClient) prAPI(id int) string {
	return fmt.Sprintf("%s/pulls/%d", c.g.repoAPI(), id)
}

type githubRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type githubPR struct {
	Number             int          `json:"number"`
	Title              string       `json:"title"`
	Body               string       `json:"body"`
	State              string       `json:"state"`
	Draft              bool         `json:"draft"`
	MergedAt           *string      `json:"merged_at"`
	User               githubUser   `json:"user"`
	CreatedAt          string       `json:"created_at"`
	HTMLURL            string       `json:"html_url"`
	Head               githubRef    `json:"head"`
	Base               githubRef    `json:"base"`
	MergeableState     string       `json:"mergeable_state"`
	RequestedReviewers []githubUser `json:"requested_reviewers"`
}

// githubPRStatus maps open / merged / closed onto Azure DevOps' statuses.
func githubPRStatus(state string, mergedAt *string) string {
	switch {
	case state == "open":
		return PRStatusActive
	case mergedAt != nil && *mergedAt != "":
		return PRStatusCompleted
	default:
		return PRStatusAbandoned
	}
}

func (c *githubPRClient) pr(ctx context.Context, id int) (githubPR, error) {
	var pr githubPR
	_, err := c.g.getJSON(ctx, c.prAPI(id), &pr)
	return pr, err
}

// githubWho is a list filter's user: "me" becomes @me, emails are refused
// (GitHub search filters by login only).
func githubWho(who string) (string, error) {
	if strings.EqualFold(who, "me") {
		return "@me", nil
	}
	if strings.Contains(who, "@") {
		return "", fmt.Errorf("GitHub filters by login, not email (got %q) — pass the user's login or \"me\"", who)
	}
	return who, nil
}

// List uses issue search, the only GitHub listing that filters by author,
// requested reviewer and merged state. Reviewer matches pull requests whose
// review is still requested from that user.
func (c *githubPRClient) List(ctx context.Context, f PRListFilter) ([]PRSummary, bool, error) {
	if want := strings.TrimSpace(f.Repository); want != "" && !strings.EqualFold(want, c.g.repo) &&
		!strings.EqualFold(want, c.g.owner+"/"+c.g.repo) {
		return nil, false, fmt.Errorf("this source covers %s/%s, not %q", c.g.owner, c.g.repo, want)
	}
	terms := []string{"repo:" + c.g.owner + "/" + c.g.repo, "is:pr"}
	switch f.Status {
	case "", PRStatusActive:
		terms = append(terms, "is:open")
	case PRStatusCompleted:
		terms = append(terms, "is:merged")
	case PRStatusAbandoned:
		terms = append(terms, "is:closed", "is:unmerged")
	}
	for _, who := range []struct{ qualifier, value string }{{"author", f.Author}, {"review-requested", f.Reviewer}} {
		if who.value == "" {
			continue
		}
		login, err := githubWho(who.value)
		if err != nil {
			return nil, false, err
		}
		terms = append(terms, who.qualifier+":"+login)
	}
	if f.TargetBranch != "" {
		terms = append(terms, "base:"+strings.TrimPrefix(f.TargetBranch, "refs/heads/"))
	}
	page := max(f.Page, 1)
	q := url.Values{"q": {strings.Join(terms, " ")}, "sort": {"created"}, "order": {"desc"},
		"per_page": {strconv.Itoa(PRPageSize)}, "page": {strconv.Itoa(page)}}

	var resp struct {
		TotalCount int `json:"total_count"`
		Items      []struct {
			Number      int        `json:"number"`
			Title       string     `json:"title"`
			State       string     `json:"state"`
			Draft       bool       `json:"draft"`
			User        githubUser `json:"user"`
			CreatedAt   string     `json:"created_at"`
			HTMLURL     string     `json:"html_url"`
			PullRequest struct {
				MergedAt *string `json:"merged_at"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	if _, err := c.g.getJSON(ctx, c.g.apiBase+"/search/issues?"+q.Encode(), &resp); err != nil {
		return nil, false, err
	}
	out := []PRSummary{}
	for _, it := range resp.Items {
		out = append(out, PRSummary{
			ID: it.Number, Title: it.Title, Status: githubPRStatus(it.State, it.PullRequest.MergedAt),
			Author: it.User.Login, IsDraft: it.Draft, CreatedAt: it.CreatedAt, WebURL: it.HTMLURL,
		})
	}
	return out, resp.TotalCount > page*PRPageSize, nil
}

type githubReview struct {
	ID          int64      `json:"id"`
	User        githubUser `json:"user"`
	State       string     `json:"state"`
	Body        string     `json:"body"`
	CommitID    string     `json:"commit_id"`
	SubmittedAt string     `json:"submitted_at"`
}

func (c *githubPRClient) reviews(ctx context.Context, id int) ([]githubReview, error) {
	var all []githubReview
	err := c.g.pages(ctx, c.prAPI(id)+"/reviews", func(data []byte) (int, bool, error) {
		var page []githubReview
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		all = append(all, page...)
		return len(page), true, nil
	})
	return all, err
}

// githubClosingRef finds the issues a pull request description closes with
// GitHub's keywords ("fixes #12"). The REST API doesn't expose linked issues,
// so this is what GitHub itself links on merge for same-repository issues.
var githubClosingRef = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)\b`)

func (c *githubPRClient) Get(ctx context.Context, id int) (*PullRequest, error) {
	pr, err := c.pr(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.details(ctx, pr, true)
}

// details completes a pull request with its reviewers and linked issues;
// withChecks adds the head commit's checks, which only the live tool needs.
func (c *githubPRClient) details(ctx context.Context, pr githubPR, withChecks bool) (*PullRequest, error) {
	id := pr.Number
	out := &PullRequest{
		PRSummary: PRSummary{
			ID: pr.Number, Title: pr.Title, Status: githubPRStatus(pr.State, pr.MergedAt), Author: pr.User.Login,
			SourceBranch: pr.Head.Ref, TargetBranch: pr.Base.Ref, IsDraft: pr.Draft, CreatedAt: pr.CreatedAt, WebURL: pr.HTMLURL,
		},
		Description: pr.Body, MergeStatus: pr.MergeableState, HeadCommit: pr.Head.SHA, BaseCommit: pr.Base.SHA,
		Reviewers: []PRReviewer{}, WorkItems: []PRWorkItem{}, Checks: []PRCheck{},
	}

	reviews, err := c.reviews(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading reviews: %w", err)
	}
	// A reviewer's vote is their latest approval, change request or
	// dismissal; a comment-only review counts only when there is nothing else.
	byLogin := map[string]int{}
	for _, r := range reviews {
		state := strings.ToLower(r.State)
		if state == "pending" {
			continue
		}
		i, seen := byLogin[r.User.Login]
		if !seen {
			byLogin[r.User.Login] = len(out.Reviewers)
			out.Reviewers = append(out.Reviewers, PRReviewer{Name: r.User.Login, ID: r.User.Login, Vote: state, CommitID: r.CommitID})
			continue
		}
		if state != "commented" || out.Reviewers[i].Vote == "commented" {
			out.Reviewers[i].Vote, out.Reviewers[i].CommitID = state, r.CommitID
		}
	}
	for _, u := range pr.RequestedReviewers {
		if i, seen := byLogin[u.Login]; seen {
			out.Reviewers[i].Vote = "pending" // re-requested after reviewing
			continue
		}
		out.Reviewers = append(out.Reviewers, PRReviewer{Name: u.Login, ID: u.Login, Vote: "pending"})
	}

	seen := map[string]bool{}
	for _, m := range githubClosingRef.FindAllStringSubmatch(pr.Body, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		item := PRWorkItem{ID: m[1], Type: "issue", WebURL: c.g.repoWeb() + "/issues/" + m[1]}
		var issue struct {
			Title string `json:"title"`
			State string `json:"state"`
		}
		if _, err := c.g.getJSON(ctx, c.g.repoAPI()+"/issues/"+m[1], &issue); err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("linked issue #%s could not be read: %v", m[1], err))
		} else {
			item.Title, item.State = issue.Title, issue.State
		}
		out.WorkItems = append(out.WorkItems, item)
	}

	if !withChecks {
		return out, nil
	}
	checks, errs := c.checks(ctx, pr.Head.SHA)
	out.Checks = append(out.Checks, checks...)
	for _, err := range errs {
		out.Notes = append(out.Notes, "checks could not be read fully: "+err.Error())
	}
	return out, nil
}

// checks reads the head commit's check runs (GitHub Actions and other apps)
// and its commit statuses (older CI integrations).
func (c *githubPRClient) checks(ctx context.Context, sha string) ([]PRCheck, []error) {
	var out []PRCheck
	var errs []error
	commitAPI := c.g.repoAPI() + "/commits/" + url.PathEscape(sha)

	err := c.g.pages(ctx, commitAPI+"/check-runs", func(data []byte) (int, bool, error) {
		var page struct {
			CheckRuns []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				Output     struct {
					Title string `json:"title"`
				} `json:"output"`
			} `json:"check_runs"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		for _, r := range page.CheckRuns {
			state := r.Status
			if r.Status == "completed" && r.Conclusion != "" {
				state = r.Conclusion
			}
			out = append(out, PRCheck{Name: r.Name, State: state, Description: r.Output.Title, WebURL: r.HTMLURL})
		}
		return len(page.CheckRuns), true, nil
	})
	if err != nil {
		errs = append(errs, err)
	}

	var combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	if _, err := c.g.getJSON(ctx, commitAPI+"/status", &combined); err != nil {
		errs = append(errs, err)
	} else {
		for _, s := range combined.Statuses {
			out = append(out, PRCheck{Name: s.Context, State: s.State, Description: s.Description, WebURL: s.TargetURL})
		}
	}
	return out, errs
}

// commits lists the pull request's commits, oldest first; incomplete is set
// when GitHub's 250-commit listing limit was reached.
func (c *githubPRClient) commits(ctx context.Context, id int) ([]githubCommit, []string, error) {
	var all []githubCommit
	err := c.g.pages(ctx, c.prAPI(id)+"/commits", func(data []byte) (int, bool, error) {
		var page []githubCommit
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		all = append(all, page...)
		return len(page), len(all) < githubMaxPRCommits, nil
	})
	if err != nil {
		return nil, nil, err
	}
	var incomplete []string
	if len(all) >= githubMaxPRCommits {
		incomplete = append(incomplete, fmt.Sprintf("GitHub lists at most %d commits of a pull request; "+
			"any later commits (and iterations) are not included", githubMaxPRCommits))
	}
	return all, incomplete, nil
}

func (c *githubPRClient) Commits(ctx context.Context, id int) ([]PRCommit, []string, error) {
	commits, incomplete, err := c.commits(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	out := make([]PRCommit, 0, len(commits))
	for _, cm := range commits {
		a := cm.Commit.Author
		author := a.Name
		if a.Email != "" {
			author += " <" + a.Email + ">"
		}
		out = append(out, PRCommit{SHA: cm.SHA, Message: cm.Commit.Message, Author: author, Date: a.Date, WebURL: cm.HTMLURL})
	}
	return out, incomplete, nil
}

func (c *githubPRClient) Iterations(ctx context.Context, id int) ([]PRIteration, error) {
	commits, _, err := c.commits(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]PRIteration, 0, len(commits))
	for i, cm := range commits {
		subject, _, _ := strings.Cut(cm.Commit.Message, "\n")
		out = append(out, PRIteration{
			Number: i + 1, HeadCommit: cm.SHA, Author: cm.Commit.Author.Name,
			CreatedAt: cm.Commit.Committer.Date, Description: subject,
		})
	}
	return out, nil
}

func (c *githubPRClient) Changes(ctx context.Context, id int, r PRRange) (PRChanges, error) {
	pr, err := c.pr(ctx, id)
	if err != nil {
		return PRChanges{}, err
	}
	if r == (PRRange{}) {
		return c.prFiles(ctx, id, pr.Head.SHA)
	}

	commits, incomplete, err := c.commits(ctx, id)
	if err != nil {
		return PRChanges{}, err
	}
	if r, err = checkRange(r, len(commits)); err != nil {
		return PRChanges{}, err
	}
	if r.From == 0 && r.To == len(commits) && len(incomplete) == 0 {
		return c.prFiles(ctx, id, pr.Head.SHA)
	}
	from := pr.Base.SHA
	if r.From > 0 {
		from = commits[r.From-1].SHA
	}
	head := commits[r.To-1].SHA

	var cmp struct {
		Files []githubCommitFile `json:"files"`
	}
	if _, err := c.g.getJSON(ctx, c.g.repoAPI()+"/compare/"+url.PathEscape(from)+"..."+url.PathEscape(head), &cmp); err != nil {
		return PRChanges{}, err
	}
	out := c.changes(cmp.Files, head)
	out.Incomplete = incomplete
	if len(cmp.Files) >= githubMaxCompareFiles {
		out.Incomplete = append(out.Incomplete, fmt.Sprintf("GitHub compares at most %d files between "+
			"iterations; any further changed files are not listed", githubMaxCompareFiles))
	}
	return out, nil
}

func (c *githubPRClient) prFiles(ctx context.Context, id int, head string) (PRChanges, error) {
	var files []githubCommitFile
	err := c.g.pages(ctx, c.prAPI(id)+"/files", func(data []byte) (int, bool, error) {
		var page []githubCommitFile
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		files = append(files, page...)
		return len(page), len(files) < githubMaxPRFiles, nil
	})
	if err != nil {
		return PRChanges{}, err
	}
	out := c.changes(files, head)
	if len(files) >= githubMaxPRFiles {
		out.Incomplete = append(out.Incomplete, fmt.Sprintf("GitHub lists at most %d changed files of a "+
			"pull request; any further files are not listed", githubMaxPRFiles))
	}
	return out, nil
}

func (c *githubPRClient) changes(files []githubCommitFile, head string) PRChanges {
	out := PRChanges{Files: make([]PRChange, 0, len(files)), HeadCommit: head}
	for i := range files {
		f := &files[i]
		ch := PRChange{Path: f.Filename, ChangeType: f.Status, headCommit: head, githubFile: f}
		if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
			ch.OriginalPath = f.PreviousFilename
		}
		out.Files = append(out.Files, ch)
	}
	return out
}

func (c *githubPRClient) Diff(ctx context.Context, changes []PRChange, withContent bool) ([]PRFile, error) {
	out := make([]PRFile, len(changes))
	errs := make([]error, len(changes))
	sem := make(chan struct{}, diffFetchConcurrency)
	var wg sync.WaitGroup
	for i, ch := range changes {
		f := ch.githubFile
		patch, missing := githubFilePatch(*f, firstNonEmpty(ch.OriginalPath, ch.Path))
		out[i] = PRFile{
			Path: ch.Path, OriginalPath: ch.OriginalPath, ChangeType: ch.ChangeType,
			Additions: f.Additions, Deletions: f.Deletions, Diff: patch,
		}
		if missing {
			out[i].Incomplete = "GitHub returned no patch for this file (binary or very large)"
		}
		if !withContent || f.Status == "removed" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			text, err := c.content(ctx, ch.Path, ch.headCommit)
			if err != nil {
				errs[i] = fmt.Errorf("reading %s: %w", ch.Path, err)
				return
			}
			if !isBinary(text) {
				out[i].Content = &text
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// content reads a file's raw text at ref.
func (c *githubPRClient) content(ctx context.Context, path, ref string) (string, error) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	data, _, err := c.g.get(ctx, c.g.repoAPI()+"/contents/"+strings.Join(segs, "/")+"?ref="+url.QueryEscape(ref),
		"application/vnd.github.raw")
	return string(data), err
}

func (c *githubPRClient) Threads(ctx context.Context, id int) ([]PRThread, []string, error) {
	type reviewComment struct {
		ID           int64      `json:"id"`
		InReplyToID  int64      `json:"in_reply_to_id"`
		Path         string     `json:"path"`
		Line         *int       `json:"line"`
		OriginalLine *int       `json:"original_line"`
		User         githubUser `json:"user"`
		Body         string     `json:"body"`
		CreatedAt    string     `json:"created_at"`
	}
	var comments []reviewComment
	err := c.g.pages(ctx, c.prAPI(id)+"/comments", func(data []byte) (int, bool, error) {
		var page []reviewComment
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		comments = append(comments, page...)
		return len(page), true, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reading review comments: %w", err)
	}

	var threads []PRThread
	byRoot := map[int64]int{}
	for _, cm := range comments {
		pc := PRComment{ID: strconv.FormatInt(cm.ID, 10), Author: cm.User.Login, Body: cm.Body, CreatedAt: cm.CreatedAt}
		if cm.InReplyToID != 0 {
			pc.ReplyTo = strconv.FormatInt(cm.InReplyToID, 10)
			if i, ok := byRoot[cm.InReplyToID]; ok {
				threads[i].Comments = append(threads[i].Comments, pc)
				continue
			}
		}
		th := PRThread{ID: pc.ID, Kind: "file", Path: cm.Path, Outdated: cm.Line == nil, Comments: []PRComment{pc}}
		switch {
		case cm.Line != nil:
			th.Line = *cm.Line
		case cm.OriginalLine != nil:
			th.Line = *cm.OriginalLine
		}
		byRoot[cm.ID] = len(threads)
		threads = append(threads, th)
	}

	reviews, err := c.reviews(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("reading reviews: %w", err)
	}
	for _, r := range reviews {
		if strings.TrimSpace(r.Body) == "" {
			continue
		}
		threads = append(threads, PRThread{
			ID: "review-" + strconv.FormatInt(r.ID, 10), Kind: "review", Status: strings.ToLower(r.State),
			Comments: []PRComment{{ID: strconv.FormatInt(r.ID, 10), Author: r.User.Login, Body: r.Body, CreatedAt: r.SubmittedAt}},
		})
	}

	err = c.g.pages(ctx, c.g.repoAPI()+"/issues/"+strconv.Itoa(id)+"/comments", func(data []byte) (int, bool, error) {
		var page []struct {
			ID        int64      `json:"id"`
			User      githubUser `json:"user"`
			Body      string     `json:"body"`
			CreatedAt string     `json:"created_at"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		for _, cm := range page {
			id := strconv.FormatInt(cm.ID, 10)
			threads = append(threads, PRThread{ID: "comment-" + id, Kind: "general",
				Comments: []PRComment{{ID: id, Author: cm.User.Login, Body: cm.Body, CreatedAt: cm.CreatedAt}}})
		}
		return len(page), true, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reading comments: %w", err)
	}

	sort.SliceStable(threads, func(i, j int) bool {
		return threads[i].Comments[0].CreatedAt < threads[j].Comments[0].CreatedAt
	})
	if threads == nil {
		threads = []PRThread{}
	}
	notes := []string{"GitHub's REST API doesn't report whether a review thread is resolved, so file threads have no status; " +
		"outdated marks comments on lines the latest changes replaced."}
	return threads, notes, nil
}
