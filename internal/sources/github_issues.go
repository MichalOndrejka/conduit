// Issues for GitHubSource: open and closed issues (pull requests excluded),
// each with its body and every comment, for Work Items sources and the
// "workitems" mode of Requirements and Test Cases.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

type githubIssue struct {
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	State       string       `json:"state"`
	StateReason string       `json:"state_reason"`
	Body        string       `json:"body"`
	User        githubUser   `json:"user"`
	Assignees   []githubUser `json:"assignees"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	Comments    int    `json:"comments"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ClosedAt    string `json:"closed_at"`
	HTMLURL     string `json:"html_url"`
	PullRequest any    `json:"pull_request"` // set on pull requests, which the issues API lists too
}

type githubComment struct {
	User      githubUser `json:"user"`
	CreatedAt string     `json:"created_at"`
	Body      string     `json:"body"`
}

func (g *GitHubSource) fetchIssues(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Listing issues"})
	}
	issues, err := g.listIssues(ctx)
	if err != nil {
		return nil, err
	}

	n := len(issues)
	comments := make([][]githubComment, n)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	report := func() {
		mu.Lock()
		completed++
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: completed, Total: n,
				Message: fmt.Sprintf("Fetching issue %d/%d", completed, n),
			})
		}
		mu.Unlock()
	}
	for i := 0; i < n && workCtx.Err() == nil; i++ {
		if issues[i].Comments == 0 {
			report()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			c, err := g.listComments(workCtx, issues[i].Number)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("issue #%d: fetching comments: %w", issues[i].Number, err)
				}
				mu.Unlock()
				cancel()
				return
			}
			comments[i] = c
			report()
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	docs := make([]models.SourceDocument, n)
	for i, is := range issues {
		docs[i] = g.issueDocument(is, comments[i])
	}
	return docs, nil
}

// listIssues lists the repository's issues, newest first, at most Top.
// Pages are ordered by creation date: ordered by update date, an issue edited
// during listing would jump onto a page already read and be missed, while a
// new issue only repeats one already listed. GitHub's labels parameter
// requires all given labels, so each configured label is queried separately
// and the results merged: an issue with any of them is included.
func (g *GitHubSource) listIssues(ctx context.Context) ([]githubIssue, error) {
	labels := parsePathFilter(g.src.GetConfig("Labels"))
	if len(labels) == 0 {
		labels = []string{""}
	}
	top := g.top()
	byNumber := map[int]githubIssue{}
	for _, label := range labels {
		listURL := g.repoAPI() + "/issues?state=all&sort=created&direction=desc"
		if label != "" {
			listURL += "&labels=" + url.QueryEscape(label)
		}
		found := 0
		err := g.pages(ctx, listURL, func(data []byte) (int, bool, error) {
			var page []githubIssue
			if err := json.Unmarshal(data, &page); err != nil {
				return 0, false, err
			}
			for _, is := range page {
				if is.PullRequest != nil {
					continue
				}
				byNumber[is.Number] = is
				found++
			}
			return len(page), top == 0 || found < top, nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing issues: %w", err)
		}
	}
	issues := make([]githubIssue, 0, len(byNumber))
	for _, is := range byNumber {
		issues = append(issues, is)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Number > issues[j].Number })
	if top > 0 && len(issues) > top {
		issues = issues[:top]
	}
	return issues, nil
}

func (g *GitHubSource) listComments(ctx context.Context, number int) ([]githubComment, error) {
	var all []githubComment
	err := g.pages(ctx, fmt.Sprintf("%s/issues/%d/comments", g.repoAPI(), number), func(data []byte) (int, bool, error) {
		var page []githubComment
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		all = append(all, page...)
		return len(page), true, nil
	})
	return all, err
}

func (g *GitHubSource) issueDocument(is githubIssue, comments []githubComment) models.SourceDocument {
	labels := make([]string, len(is.Labels))
	for i, l := range is.Labels {
		labels[i] = l.Name
	}
	assignees := make([]string, len(is.Assignees))
	for i, a := range is.Assignees {
		assignees[i] = a.Login
	}
	state := is.State
	if is.StateReason != "" {
		state += " (" + strings.ReplaceAll(is.StateReason, "_", " ") + ")"
	}

	var sb strings.Builder
	sb.WriteString(is.Title + "\n")
	writeField(&sb, "Issue", "#"+strconv.Itoa(is.Number))
	writeField(&sb, "State", state)
	writeField(&sb, "Author", is.User.Login)
	writeField(&sb, "Assignees", strings.Join(assignees, ", "))
	writeField(&sb, "Labels", strings.Join(labels, ", "))
	if is.Milestone != nil {
		writeField(&sb, "Milestone", is.Milestone.Title)
	}
	writeField(&sb, "Created", is.CreatedAt)
	writeField(&sb, "Updated", is.UpdatedAt)
	writeField(&sb, "Closed", is.ClosedAt)
	if body := strings.TrimSpace(is.Body); body != "" {
		sb.WriteString("\n" + body + "\n")
	}
	for _, c := range comments {
		fmt.Fprintf(&sb, "\nComment by %s on %s:\n%s\n", c.User.Login, c.CreatedAt, strings.TrimSpace(c.Body))
	}

	webURL := is.HTMLURL
	if !isWebURL(webURL) {
		webURL = fmt.Sprintf("%s/issues/%d", g.repoWeb(), is.Number)
	}
	props := map[string]string{
		"title":   is.Title,
		"type":    "Issue",
		"state":   is.State,
		"web_url": webURL,
	}
	filters := g.scopeFilters(props)
	filters.Add(models.FilterWorkItemType, "Issue")
	filters.Add(models.FilterState, is.State)
	filters.Add(models.FilterAssignedTo, assignees...)
	filters.Add(models.FilterTag, labels...)
	return models.SourceDocument{
		ID:          fmt.Sprintf("%s_gh_issue_%d", g.src.ID, is.Number),
		Text:        strings.TrimSpace(sb.String()),
		Tags:        g.tags(),
		Properties:  props,
		Filters:     filters,
		ChunkHeader: fmt.Sprintf("Issue #%d", is.Number),
	}
}
