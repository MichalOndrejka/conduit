// Commit history for GitHubSource: the branch's commits, newest first, each
// with its full message and the patch of every changed file. Unlike Azure
// DevOps, GitHub returns the patches itself, so nothing is diffed locally.
//
// A commit that can't be fetched fails the sync rather than being indexed
// partially. Where GitHub itself leaves something out (patches of binary or
// very large files, files beyond its 3000-per-commit listing limit), the
// document says so.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// githubMaxCommitFiles is the most changed files GitHub lists for one
// commit.
const githubMaxCommitFiles = 3000

type githubGitUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

type githubCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Author    githubGitUser `json:"author"`
		Committer githubGitUser `json:"committer"`
		Message   string        `json:"message"`
	} `json:"commit"`
	HTMLURL string      `json:"html_url"`
	Author  *githubUser `json:"author"`
}

type githubCommitFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
}

// githubCommitDiff is one commit's fetched diff.
type githubCommitDiff struct {
	diff         string
	filesChanged int
	paths        []string
}

func (g *GitHubSource) fetchCommits(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	branch, err := g.branch(ctx)
	if err != nil {
		return nil, err
	}
	commits, err := g.listCommits(ctx, branch, progress)
	if err != nil {
		return nil, err
	}

	n := len(commits)
	diffs := make([]githubCommitDiff, n)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	for i := 0; i < n && workCtx.Err() == nil; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			d, err := g.fetchCommitDiff(workCtx, commits[i].SHA)
			if err != nil {
				fail(fmt.Errorf("commit %s: %w", commits[i].SHA, err))
				return
			}
			diffs[i] = d

			mu.Lock()
			completed++
			if progress != nil {
				progress(models.SyncProgress{
					Phase: "fetching", Current: completed, Total: n,
					Message: fmt.Sprintf("Fetching commit %d/%d", completed, n),
				})
			}
			mu.Unlock()
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
	for i, c := range commits {
		docs[i] = g.commitDocument(c, diffs[i], branch)
	}
	return docs, nil
}

// listCommits pages through the branch's history until it (or Top) runs
// out.
func (g *GitHubSource) listCommits(ctx context.Context, branch string, progress ProgressCallback) ([]githubCommit, error) {
	top := g.top()
	var all []githubCommit
	seen := map[string]bool{}
	err := g.pages(ctx, g.repoAPI()+"/commits?sha="+url.QueryEscape(branch), func(data []byte) (int, bool, error) {
		var page []githubCommit
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, fmt.Errorf("listing commits: %w", err)
		}
		// A push during listing shifts the pages; skip the repeats.
		for _, c := range page {
			if !seen[c.SHA] {
				seen[c.SHA] = true
				all = append(all, c)
			}
		}
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: len(all), Total: top,
				Message: fmt.Sprintf("Listing commits (%d so far)", len(all)),
			})
		}
		return len(page), top == 0 || len(all) < top, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing commits: %w", err)
	}
	if top > 0 && len(all) > top {
		all = all[:top]
	}
	return all, nil
}

// fetchCommitDiff fetches every changed file of a commit (the single-commit
// endpoint pages its file list) and renders them as one unified diff.
func (g *GitHubSource) fetchCommitDiff(ctx context.Context, sha string) (githubCommitDiff, error) {
	var files []githubCommitFile
	err := g.pages(ctx, g.repoAPI()+"/commits/"+url.PathEscape(sha), func(data []byte) (int, bool, error) {
		var page struct {
			Files []githubCommitFile `json:"files"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		files = append(files, page.Files...)
		return len(page.Files), len(files) < githubMaxCommitFiles, nil
	})
	if err != nil {
		return githubCommitDiff{}, err
	}

	var d githubCommitDiff
	var b strings.Builder
	for _, f := range files {
		d.filesChanged++
		d.paths = append(d.paths, f.Filename)
		oldPath := f.Filename
		if f.PreviousFilename != "" && f.PreviousFilename != f.Filename {
			d.paths = append(d.paths, f.PreviousFilename)
			oldPath = f.PreviousFilename
		}
		fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldPath, f.Filename)
		switch {
		case f.Patch != "":
			b.WriteString(strings.TrimRight(f.Patch, "\n") + "\n\n")
		case f.Additions+f.Deletions == 0:
			fmt.Fprintf(&b, "(%s, no content changes)\n\n", f.Status)
		default:
			fmt.Fprintf(&b, "(%s, +%d -%d; GitHub returns no patch for binary or very large files)\n\n", f.Status, f.Additions, f.Deletions)
		}
	}
	if len(files) >= githubMaxCommitFiles {
		fmt.Fprintf(&b, "(GitHub lists at most %d changed files per commit; any further files are not included)\n", githubMaxCommitFiles)
	}
	d.diff = b.String()
	return d, nil
}

func (g *GitHubSource) commitDocument(c githubCommit, d githubCommitDiff, branch string) models.SourceDocument {
	a, cm := c.Commit.Author, c.Commit.Committer
	var sb strings.Builder
	sb.WriteString("Commit " + c.SHA + "\n")
	writeField(&sb, "Author", strings.TrimSpace(a.Name+" "+angle(a.Email)))
	writeField(&sb, "Date", a.Date)
	if cm.Name != "" && (cm.Name != a.Name || cm.Email != a.Email) {
		writeField(&sb, "Committer", strings.TrimSpace(cm.Name+" "+angle(cm.Email)))
	}
	writeField(&sb, "Branch", branch)
	writeField(&sb, "Files changed", strconv.Itoa(d.filesChanged))
	sb.WriteString("\n" + strings.TrimSpace(c.Commit.Message) + "\n")
	if d.diff != "" {
		sb.WriteString("\n" + d.diff)
	}

	webURL := c.HTMLURL
	if !isWebURL(webURL) {
		webURL = g.repoWeb() + "/commit/" + c.SHA
	}
	subject := strings.TrimSpace(c.Commit.Message)
	if i := strings.IndexByte(subject, '\n'); i >= 0 {
		subject = strings.TrimSpace(subject[:i])
	}
	short := c.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	props := map[string]string{
		"title":         fmt.Sprintf("%s (%s)", subject, short),
		"web_url":       webURL,
		"commit":        c.SHA,
		"author":        a.Name,
		"branch":        branch,
		"files_changed": strconv.Itoa(d.filesChanged),
	}
	filters := g.scopeFilters(props)
	filters.Add(models.FilterPath, d.paths...)
	filters.Add(models.FilterAuthor, a.Name, a.Email)
	if c.Author != nil {
		filters.Add(models.FilterAuthor, c.Author.Login)
	}
	filters.Date = parseTime(a.Date)
	return models.SourceDocument{
		ID:          fmt.Sprintf("%s_commit_%s", g.src.ID, c.SHA),
		Text:        strings.TrimSpace(sb.String()),
		Tags:        g.tags(),
		Properties:  props,
		Filters:     filters,
		ChunkHeader: "Commit " + short,
	}
}
