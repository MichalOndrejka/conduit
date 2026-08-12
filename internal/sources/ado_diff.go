// Azure DevOps commit-diff enrichment for APISource. ADO's Git REST API has
// no single "get this commit as a patch" endpoint (unlike GitHub's
// /commit/{sha}.diff), so a real diff has to be assembled client-side:
// list the changed files for a commit, fetch each file's old/new blob text,
// and diff them locally.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	defaultMaxDiffChars      = 20000
	defaultMaxFilesPerCommit = 20
	maxBlobFetchBytes        = 200 << 10 // skip diffing anything larger (likely binary/generated)
)

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
}

type adoChange struct {
	ChangeType string        `json:"changeType"`
	Item       adoChangeItem `json:"item"`
}

type adoChangesResponse struct {
	Changes []adoChange `json:"changes"`
}

// fetchCommitDiff builds a unified diff of every file changed by commitID,
// bounded by maxFiles and maxChars so a single pathological commit can't blow
// up the embedded document.
func (a *APISource) fetchCommitDiff(ctx context.Context, client *http.Client, repoBase, commitID string, maxFiles, maxChars int) (diffText string, filesChanged int, err error) {
	changesURL := fmt.Sprintf("%s/commits/%s/changes?api-version=7.1", repoBase, commitID)
	data, err := a.getRaw(ctx, client, changesURL)
	if err != nil {
		return "", 0, err
	}
	var changes adoChangesResponse
	if err := json.Unmarshal(data, &changes); err != nil {
		return "", 0, fmt.Errorf("changes response is not valid JSON: %w", err)
	}

	filesChanged = len(changes.Changes)
	limit := filesChanged
	if maxFiles > 0 && limit > maxFiles {
		limit = maxFiles
	}

	var b strings.Builder
	processed := 0 // files actually accounted for (diffed, errored, or deliberately no-op)
	for _, c := range changes.Changes[:limit] {
		if c.Item.Path == "" || strings.EqualFold(c.ChangeType, "none") {
			processed++
			continue
		}
		fileDiff, err := a.fileDiff(ctx, client, repoBase, c.Item.Path, c.Item.OriginalObjectID, c.Item.ObjectID)
		processed++
		if err != nil {
			fmt.Fprintf(&b, "--- %s\n(diff unavailable: %v)\n\n", c.Item.Path, err)
			continue
		}
		b.WriteString(fileDiff)
		if maxChars > 0 && b.Len() > maxChars {
			break // maxChars cap hit — remaining files (if any) are reported as skipped below
		}
	}
	diffText = b.String()
	// Hard-truncate the diffed content itself first, then append the
	// skipped/truncated trailer notes afterwards so a large diff body can
	// never eat into (or cut off) the notes that follow it.
	truncatedMidContent := false
	if maxChars > 0 && len(diffText) > maxChars {
		diffText = truncateRuneSafe(diffText, maxChars)
		truncatedMidContent = true
	}
	if skipped := filesChanged - processed; skipped > 0 {
		diffText += fmt.Sprintf("\n… %d more files changed (skipped)\n", skipped)
	}
	if truncatedMidContent {
		diffText += "\n… (diff truncated)\n"
	}
	return diffText, filesChanged, nil
}

// truncateRuneSafe cuts s to at most maxBytes bytes without splitting a
// multi-byte UTF-8 rune in half.
func truncateRuneSafe(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}

// fileDiff fetches old/new blob content for one changed file and returns its
// unified diff. Either sha may be empty (add or delete).
func (a *APISource) fileDiff(ctx context.Context, client *http.Client, repoBase, path, oldSHA, newSHA string) (string, error) {
	oldText, oldSkipped, err := a.blobText(ctx, client, repoBase, oldSHA)
	if err != nil {
		return "", err
	}
	newText, newSkipped, err := a.blobText(ctx, client, repoBase, newSHA)
	if err != nil {
		return "", err
	}
	if oldSkipped || newSkipped {
		return fmt.Sprintf("--- %s\n(binary or large file skipped)\n\n", path), nil
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

// blobText fetches a blob's text content by SHA. An empty sha (add/delete
// side) returns an empty string. skipped reports content too large to diff.
func (a *APISource) blobText(ctx context.Context, client *http.Client, repoBase, sha string) (text string, skipped bool, err error) {
	if sha == "" {
		return "", false, nil
	}
	blobURL := fmt.Sprintf("%s/blobs/%s?api-version=7.1&$format=text", repoBase, sha)
	data, err := a.getRaw(ctx, client, blobURL)
	if err != nil {
		return "", false, err
	}
	if len(data) > maxBlobFetchBytes {
		return "", true, nil
	}
	return string(data), false, nil
}

// getRaw performs an authenticated GET and returns the raw response body,
// reusing the source's existing auth/header/TLS configuration.
func (a *APISource) getRaw(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	a.applyAuth(req)
	a.applyExtraHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return data, nil
}
