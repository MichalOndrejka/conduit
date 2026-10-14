// Azure DevOps commit enrichment for APISource. ADO's Git REST API has no
// single "get this commit as a patch" endpoint (unlike GitHub's
// /commit/{sha}.diff), so a real diff has to be assembled client-side: list
// the changed files for a commit, fetch each file's old/new blob text, and
// diff them locally.
//
// Enrichment is lossless by design: every changed file of every commit is
// diffed in full, and anything that can't be fetched fails the sync rather
// than being silently dropped or truncated.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	// maxRawResponseBytes bounds a single blob/changes/commit response. A
	// response over it is an error, never silently truncated.
	maxRawResponseBytes = 64 << 20

	// adoCommitsPageSize is the page size requested per call when paging
	// through Azure DevOps' commits-list endpoint (see withAdoCommitsPaging).
	// It also matches ADO's own default page size when no $top is given,
	// which is the "hard limit of 100" a commit-history source would
	// otherwise silently stop at without manual pagination.
	adoCommitsPageSize = 100

	// adoChangesPageSize is the page size requested per call when paging
	// through a single commit's changed-files list.
	adoChangesPageSize = 100

	// defaultCommitsBranch is the branch a commit-history source reads from
	// when Branch is left unset.
	defaultCommitsBranch = "main"

	maxFetchAttempts = 4
	maxRetryAfter    = 30 * time.Second
)

// retryBaseDelay is the first retry's backoff, doubled per attempt. A var so
// tests can shrink it.
var retryBaseDelay = time.Second

// withAdoCommitsPaging appends Azure DevOps' commits-list pagination
// parameters (searchCriteria.$top/$skip) to a commits API URL. The endpoint
// returns no next-page link, so a commit-history source has to page through
// it manually to fetch more than one page's worth of commits.
func withAdoCommitsPaging(commitsURL string, top, skip int) string {
	sep := "?"
	if strings.Contains(commitsURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%ssearchCriteria.$top=%d&searchCriteria.$skip=%d", commitsURL, sep, top, skip)
}

// withAdoBranchFilter appends Azure DevOps' commits-list branch filter
// (searchCriteria.itemVersion.version) to a commits API URL, scoping the
// listed commits to a single branch's history instead of every branch in the
// repository.
func withAdoBranchFilter(commitsURL, branch string) string {
	sep := "?"
	if strings.Contains(commitsURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%ssearchCriteria.itemVersion.version=%s", commitsURL, sep, url.QueryEscape(branch))
}

// AdoRepoAPIBase derives the ADO git-repository API base
// (".../_apis/git/repositories/{repo}") from a commits-list endpoint URL such
// as ".../_apis/git/repositories/{repo}/commits?api-version=7.1".
func AdoRepoAPIBase(commitsURL string) (string, bool) {
	base := commitsURL
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(base, "/")
	const suffix = "/commits"
	if !strings.HasSuffix(base, suffix) || !strings.Contains(base, "/_apis/git/repositories/") {
		return "", false
	}
	return strings.TrimSuffix(base, suffix), true
}

type adoChangeItem struct {
	Path             string `json:"path"`
	ObjectID         string `json:"objectId"`
	OriginalObjectID string `json:"originalObjectId"`
	IsFolder         bool   `json:"isFolder"`
	GitObjectType    string `json:"gitObjectType"`
}

type adoChange struct {
	ChangeType       string        `json:"changeType"`
	Item             adoChangeItem `json:"item"`
	SourceServerItem string        `json:"sourceServerItem"` // a rename's original path
}

type adoChangesResponse struct {
	Changes []adoChange `json:"changes"`
}

// fetchCommitComment fetches a single commit's full message. ADO's
// commits-list endpoint truncates long messages (flagging commentTruncated);
// the single-commit endpoint returns them whole.
func (a *APISource) fetchCommitComment(ctx context.Context, client *http.Client, repoBase, commitID string) (string, error) {
	data, err := a.getRaw(ctx, client, fmt.Sprintf("%s/commits/%s?api-version=7.1", repoBase, commitID))
	if err != nil {
		return "", err
	}
	var commit struct {
		Comment string `json:"comment"`
	}
	if err := json.Unmarshal(data, &commit); err != nil {
		return "", fmt.Errorf("commit response is not valid JSON: %w", err)
	}
	return commit.Comment, nil
}

// fetchCommitChanges lists every change in commitID, paging through ADO's
// changes endpoint so large commits aren't cut off at one page.
func (a *APISource) fetchCommitChanges(ctx context.Context, client *http.Client, repoBase, commitID string) ([]adoChange, error) {
	var all []adoChange
	for skip := 0; ; {
		changesURL := fmt.Sprintf("%s/commits/%s/changes?top=%d&skip=%d&api-version=7.1", repoBase, commitID, adoChangesPageSize, skip)
		data, err := a.getRaw(ctx, client, changesURL)
		if err != nil {
			return nil, err
		}
		var page adoChangesResponse
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("changes response is not valid JSON: %w", err)
		}
		all = append(all, page.Changes...)
		if len(page.Changes) < adoChangesPageSize {
			return all, nil
		}
		skip += len(page.Changes)
	}
}

// fetchCommitDiff builds a unified diff of every file changed by commitID,
// and lists the changed file paths (a rename's original path included). Any
// file that can't be fetched fails the whole commit.
func (a *APISource) fetchCommitDiff(ctx context.Context, client *http.Client, repoBase, commitID string) (diffText string, filesChanged int, paths []string, err error) {
	changes, err := a.fetchCommitChanges(ctx, client, repoBase, commitID)
	if err != nil {
		return "", 0, nil, err
	}

	var b strings.Builder
	for _, c := range changes {
		// Folder/tree entries show up in a commit's changes alongside real
		// file changes (e.g. a commit that adds a new directory) but have no
		// blob to diff — fetching one by its tree objectId as if it were a
		// blob gets rejected by Azure DevOps with 400 Bad Request.
		if c.Item.Path == "" || strings.EqualFold(c.ChangeType, "none") || c.Item.IsFolder || strings.EqualFold(c.Item.GitObjectType, "tree") {
			continue
		}
		paths = append(paths, c.Item.Path)
		if c.SourceServerItem != "" && c.SourceServerItem != c.Item.Path {
			paths = append(paths, c.SourceServerItem)
		}
		fileDiff, err := a.fileDiff(ctx, client, repoBase, c)
		if err != nil {
			return "", 0, nil, fmt.Errorf("diffing %s: %w", c.Item.Path, err)
		}
		b.WriteString(fileDiff)
	}
	return b.String(), len(changes), paths, nil
}

// fileDiff fetches old/new blob content for one changed file and returns its
// unified diff. Either sha may be empty (add or delete). Binary files can't be
// embedded as text, so they're recorded by path and change type instead.
func (a *APISource) fileDiff(ctx context.Context, client *http.Client, repoBase string, c adoChange) (string, error) {
	path := c.Item.Path
	oldText, err := a.blobText(ctx, client, repoBase, c.Item.OriginalObjectID)
	if err != nil {
		return "", err
	}
	newText, err := a.blobText(ctx, client, repoBase, c.Item.ObjectID)
	if err != nil {
		return "", err
	}
	if isBinary(oldText) || isBinary(newText) {
		return fmt.Sprintf("--- %s\n(binary file, %s)\n\n", path, c.ChangeType), nil
	}

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldText),
		B:        difflib.SplitLines(newText),
		FromFile: path,
		ToFile:   path,
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", nil
	}
	return text + "\n", nil
}

// isBinary reports whether blob content isn't diffable text.
func isBinary(s string) bool {
	return strings.IndexByte(s, 0) >= 0 || !utf8.ValidString(s)
}

// blobText fetches a blob's content by SHA. An empty sha (add/delete side)
// returns an empty string.
func (a *APISource) blobText(ctx context.Context, client *http.Client, repoBase, sha string) (string, error) {
	if sha == "" {
		return "", nil
	}
	data, err := a.getRaw(ctx, client, fmt.Sprintf("%s/blobs/%s?api-version=7.1&$format=text", repoBase, sha))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// errRetryable marks a getRaw failure worth retrying (network error, 429,
// 5xx); anything else (404, 401, …) fails immediately.
var errRetryable = errors.New("retryable")

// getRaw performs an authenticated GET and returns the raw response body,
// reusing the source's existing auth/header/TLS configuration. Transient
// failures are retried with backoff, honouring Retry-After.
func (a *APISource) getRaw(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		data, retryAfter, err := a.getRawOnce(ctx, client, url)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, errRetryable) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
		if attempt == maxFetchAttempts-1 {
			break
		}
		delay := retryBaseDelay << attempt
		if retryAfter > 0 {
			delay = retryAfter
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", maxFetchAttempts, lastErr)
}

func (a *APISource) getRawOnce(ctx context.Context, client *http.Client, url string) (data []byte, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	a.applyAuth(req)
	a.applyExtraHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, fmt.Errorf("%w: %v", errRetryable, err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxRawResponseBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: reading %s: %v", errRetryable, url, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			retryAfter = min(time.Duration(s)*time.Second, maxRetryAfter)
		}
		return nil, retryAfter, fmt.Errorf("%w: HTTP %d from %s", errRetryable, resp.StatusCode, url)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	if len(data) > maxRawResponseBytes {
		return nil, 0, fmt.Errorf("response from %s exceeds %d MB", url, maxRawResponseBytes>>20)
	}
	return data, 0, nil
}
