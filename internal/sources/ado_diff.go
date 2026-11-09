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
	"bytes"
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

	"github.com/MichalOndrejka/conduit/internal/rag"
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

// adoDefaultBranch looks up the default branch (without refs/heads/) of the
// repository a commits-list URL points at, using that URL's api-version.
func (a *APISource) adoDefaultBranch(ctx context.Context, client *http.Client, commitsURL string) (string, error) {
	repoBase, ok := AdoRepoAPIBase(commitsURL)
	if !ok {
		return "", fmt.Errorf("not an Azure DevOps commits URL: %q", commitsURL)
	}
	version := "7.1"
	if u, err := url.Parse(commitsURL); err == nil && u.Query().Get("api-version") != "" {
		version = u.Query().Get("api-version")
	}
	data, err := a.getRaw(ctx, client, repoBase+"?api-version="+url.QueryEscape(version))
	if err != nil {
		return "", fmt.Errorf("reading the repository's default branch: %w", err)
	}
	var repo struct {
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := json.Unmarshal(data, &repo); err != nil {
		return "", fmt.Errorf("repository response is not valid JSON: %w", err)
	}
	if repo.DefaultBranch == "" {
		return "", fmt.Errorf("repository has no default branch (is it empty?); set Branch")
	}
	return strings.TrimPrefix(repo.DefaultBranch, "refs/heads/"), nil
}

// withAdoItemsBranch scopes an ADO items-list URL to branch via
// versionDescriptor. An empty branch, or a URL that already names a version,
// is left as is.
func withAdoItemsBranch(itemsURL, branch string) string {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if branch == "" {
		return itemsURL
	}
	u, err := url.Parse(itemsURL)
	if err != nil {
		return itemsURL
	}
	q := u.Query()
	if q.Get("versionDescriptor.version") != "" {
		return itemsURL
	}
	q.Set("versionDescriptor.version", branch)
	q.Set("versionDescriptor.versionType", "branch")
	u.RawQuery = q.Encode()
	return u.String()
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
// reading each blob on its own (the commit-history sync batches them
// instead, see commitDocuments).
func (a *APISource) fetchCommitDiff(ctx context.Context, client *http.Client, repoBase, commitID string) (diffText string, filesChanged int, paths []string, err error) {
	changes, err := a.fetchCommitChanges(ctx, client, repoBase, commitID)
	if err != nil {
		return "", 0, nil, err
	}
	return commitDiff(changes, func(sha string) (string, error) { return a.blobText(ctx, client, repoBase, sha) })
}

// diffableChange reports whether c is a file change with blobs to diff.
// Folder/tree entries show up in a commit's changes alongside real file
// changes (e.g. a commit that adds a new directory) but have no blob — asking
// for one by its tree objectId gets a 400 Bad Request from Azure DevOps.
func diffableChange(c adoChange) bool {
	return c.Item.Path != "" && !strings.EqualFold(c.ChangeType, "none") && !c.Item.IsFolder &&
		!strings.EqualFold(c.Item.GitObjectType, "tree")
}

// commitDiff renders the unified diff of every file in changes, reading
// blob contents through blob (an empty sha, the missing side of an add or
// delete, reads as ""), and lists the changed paths (a rename's original
// path included). Any blob that can't be read fails the whole commit.
func commitDiff(changes []adoChange, blob func(sha string) (string, error)) (diffText string, filesChanged int, paths []string, err error) {
	var b strings.Builder
	for _, c := range changes {
		if !diffableChange(c) {
			continue
		}
		paths = append(paths, c.Item.Path)
		if c.SourceServerItem != "" && c.SourceServerItem != c.Item.Path {
			paths = append(paths, c.SourceServerItem)
		}
		read := func(sha string) (string, error) {
			if sha == "" {
				return "", nil
			}
			return blob(sha)
		}
		oldText, err := read(c.Item.OriginalObjectID)
		if err != nil {
			return "", 0, nil, fmt.Errorf("diffing %s: %w", c.Item.Path, err)
		}
		newText, err := read(c.Item.ObjectID)
		if err != nil {
			return "", 0, nil, fmt.Errorf("diffing %s: %w", c.Item.Path, err)
		}
		fileDiff, err := blobDiff(c.Item.Path, c.Item.Path, oldText, newText, c.ChangeType)
		if err != nil {
			return "", 0, nil, fmt.Errorf("diffing %s: %w", c.Item.Path, err)
		}
		b.WriteString(fileDiff)
	}
	return b.String(), len(changes), paths, nil
}

// blobDiff renders the unified diff between two versions of a file, with 3
// lines of context. Binary content can't be shown as text, so it is recorded
// by path and change type instead. Identical content renders as "".
func blobDiff(fromPath, toPath, oldText, newText, changeType string) (string, error) {
	if isBinary(oldText) || isBinary(newText) {
		return fmt.Sprintf("--- %s\n(binary file, %s)\n\n", toPath, changeType), nil
	}

	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldText),
		B:        difflib.SplitLines(newText),
		FromFile: fromPath,
		ToFile:   toPath,
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
	data, _, err := a.getRawWithHeader(ctx, client, url)
	return data, err
}

// getRawWithHeader is getRaw that also returns the successful response's
// headers, for APIs that page via a response header (e.g. ADO's
// x-ms-continuationtoken).
func (a *APISource) getRawWithHeader(ctx context.Context, client *http.Client, url string) ([]byte, http.Header, error) {
	return getWithRetry(ctx, client, url, func(req *http.Request) {
		a.applyAuth(req)
		a.applyExtraHeaders(req)
	})
}

// getWithRetry performs a GET, letting prepare set auth and headers, and
// returns the raw body and headers. Transient failures (network errors, 429,
// 5xx, rate-limit 403s) are retried with backoff, honouring Retry-After.
func getWithRetry(ctx context.Context, client *http.Client, url string, prepare func(*http.Request)) ([]byte, http.Header, error) {
	var (
		data   []byte
		header http.Header
	)
	err := withRetry(ctx, func() (time.Duration, error) {
		d, h, retryAfter, err := getOnce(ctx, client, url, prepare)
		data, header = d, h
		return retryAfter, err
	})
	if err != nil {
		return nil, nil, err
	}
	return data, header, nil
}

// streamWithRetry performs a GET and hands the successful response body to
// consume as it arrives, without buffering it. A transient failure — of the
// request or of reading the body part-way — restarts the whole download with
// the same backoff as getWithRetry, so consume must reset any state it builds.
// consume's own errors fail immediately unless they wrap errRetryable.
func streamWithRetry(ctx context.Context, client *http.Client, url string, prepare func(*http.Request), consume func(io.Reader) error) error {
	return streamRequestWithRetry(ctx, client, http.MethodGet, url, nil, prepare, consume)
}

// streamRequestWithRetry is streamWithRetry for any method; body (nil for
// none) is sent again on every attempt.
func streamRequestWithRetry(ctx context.Context, client *http.Client, method, url string, body []byte, prepare func(*http.Request), consume func(io.Reader) error) error {
	return withRetry(ctx, func() (time.Duration, error) {
		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return 0, err
		}
		if prepare != nil {
			prepare(req)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, fmt.Errorf("%w: %v", errRetryable, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			return statusError(resp, url, body)
		}
		err = consume(retryableReader{resp.Body})
		if err != nil && ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, errRetryable) {
			err = fmt.Errorf("%w: download of %s cut short: %v", errRetryable, url, err)
		}
		return 0, err
	})
}

// retryableReader marks a failed read of a response body (connection reset,
// timeout) as retryable, so a download interrupted part-way is restarted.
type retryableReader struct{ r io.Reader }

func (r retryableReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		err = fmt.Errorf("%w: %w", errRetryable, err)
	}
	return n, err
}

// withRetry runs attempt up to maxFetchAttempts times while it fails with an
// errRetryable error, waiting retryAfter (when given) or an exponential
// backoff between attempts.
func withRetry(ctx context.Context, attempt func() (retryAfter time.Duration, err error)) error {
	var lastErr error
	for n := 0; n < maxFetchAttempts; n++ {
		retryAfter, err := attempt()
		if err == nil {
			return nil
		}
		if !errors.Is(err, errRetryable) || ctx.Err() != nil {
			return err
		}
		lastErr = err
		if n == maxFetchAttempts-1 {
			break
		}
		delay := retryBaseDelay << n
		if retryAfter > 0 {
			delay = retryAfter
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("giving up after %d attempts: %w", maxFetchAttempts, lastErr)
}

// httpStatusError is a non-retryable HTTP error response.
type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string { return e.msg }

// statusError classifies a response by status: nil for 2xx; an errRetryable
// error (with the server's Retry-After, if any) for 429, 5xx and GitHub
// rate-limit 403s; otherwise a final httpStatusError quoting body.
func statusError(resp *http.Response, url string, body []byte) (retryAfter time.Duration, err error) {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0, nil
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		retryAfter = min(time.Duration(s)*time.Second, maxRetryAfter)
	}
	// GitHub refuses requests over its rate limit with 403 (primary limit:
	// X-RateLimit-Remaining is 0; secondary limit: Retry-After is set).
	rateLimited := resp.StatusCode == http.StatusForbidden &&
		(resp.Header.Get("X-RateLimit-Remaining") == "0" || retryAfter > 0)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || rateLimited {
		return retryAfter, fmt.Errorf("%w: HTTP %d from %s", errRetryable, resp.StatusCode, url)
	}
	return 0, &httpStatusError{status: resp.StatusCode,
		msg: fmt.Sprintf("HTTP %d from %s: %s", resp.StatusCode, url, rag.Truncate(string(body), 300))}
}

func getOnce(ctx context.Context, client *http.Client, url string, prepare func(*http.Request)) (data []byte, header http.Header, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	if prepare != nil {
		prepare(req)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, 0, ctx.Err()
		}
		return nil, nil, 0, fmt.Errorf("%w: %v", errRetryable, err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxRawResponseBytes+1))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%w: reading %s: %v", errRetryable, url, err)
	}
	if retryAfter, err := statusError(resp, url, data); err != nil {
		return nil, nil, retryAfter, err
	}
	if len(data) > maxRawResponseBytes {
		return nil, nil, 0, fmt.Errorf("response from %s exceeds %d MB", url, maxRawResponseBytes>>20)
	}
	return data, resp.Header, 0, nil
}
