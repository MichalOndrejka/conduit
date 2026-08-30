// Azure DevOps file-content enrichment for APISource. For Source Code / Test
// Code / Documentation sources, the items listing endpoint only returns file
// metadata (path, blob SHA, …) — the actual file content has to be fetched
// per-item from the same blobs endpoint used for commit diffs.
package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// AdoItemsAPIBase derives the ADO git-repository API base
// (".../_apis/git/repositories/{repo}") from an items-list endpoint URL such
// as ".../_apis/git/repositories/{repo}/items?recursionLevel=Full&api-version=7.1".
func AdoItemsAPIBase(itemsURL string) (string, bool) {
	base := itemsURL
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(base, "/")
	const suffix = "/items"
	if !strings.HasSuffix(base, suffix) || !strings.Contains(base, "/_apis/git/repositories/") {
		return "", false
	}
	return strings.TrimSuffix(base, suffix), true
}

// withRecursionFull ensures an ADO items-list URL requests the full recursive
// file tree — without it, ADO returns only the repository root entry.
func withRecursionFull(itemsURL string) string {
	u, err := url.Parse(itemsURL)
	if err != nil {
		return itemsURL
	}
	q := u.Query()
	if q.Get("recursionLevel") == "" {
		q.Set("recursionLevel", "Full")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// parsePathFilter splits a comma-separated wildcard list (e.g. "*.cs, *.ts")
// into trimmed, non-empty patterns.
func parsePathFilter(raw string) []string {
	var patterns []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// matchesPathFilter reports whether p matches at least one wildcard pattern —
// or true if patterns is empty (no filter configured). Patterns are matched
// against both the full path and the filename alone, so "*.cs", "src/*.cs"
// and "**/*.cs" (any depth) all work as expected. Matching uses doublestar
// rather than the standard library's path.Match, which treats "**" as a
// plain "*" and can't cross directory boundaries. ADO paths are always
// POSIX-style regardless of host OS, hence "path" (not "path/filepath").
func matchesPathFilter(p string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	name := path.Base(p)
	trimmed := strings.TrimPrefix(p, "/")
	for _, pat := range patterns {
		if ok, _ := doublestar.Match(pat, name); ok {
			return true
		}
		if ok, _ := doublestar.Match(pat, trimmed); ok {
			return true
		}
	}
	return false
}

// filterGitItems drops directory entries (gitObjectType == "tree") and any
// file whose path doesn't match patterns.
func filterGitItems(items []any, patterns []string) []any {
	kept := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := m["gitObjectType"].(string); strings.EqualFold(s, "tree") {
			continue
		}
		p, _ := m["path"].(string)
		if p == "" || !matchesPathFilter(p, patterns) {
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

// fileContentOutcome is one file's fetched content, computed off the main
// goroutine and applied to its document afterwards.
type fileContentOutcome struct {
	text    string
	skipped bool
	err     error
}

// enrichWithFileContent replaces each document's text with its file's real
// content (path + blob text), fetched from Azure DevOps. items and docs must
// be the same length and order, as produced by itemsToDocuments. Content is
// fetched with bounded concurrency; a single file's fetch failing is recorded
// as a note on that document rather than failing the whole sync.
func (a *APISource) enrichWithFileContent(ctx context.Context, client *http.Client, repoBase string, items []any, docs []models.SourceDocument, progress ProgressCallback) error {
	n := len(items)
	if len(docs) < n {
		n = len(docs)
	}
	outcomes := make([]fileContentOutcome, n)

	var wg sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	sem := make(chan struct{}, diffFetchConcurrency)

	for i := 0; i < n; i++ {
		m, ok := items[i].(map[string]any)
		if !ok {
			continue
		}
		sha, _ := m["objectId"].(string)
		if sha == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, sha string) {
			defer wg.Done()
			defer func() { <-sem }()
			text, skipped, err := a.blobText(ctx, client, repoBase, sha)
			outcomes[i] = fileContentOutcome{text: text, skipped: skipped, err: err}

			progressMu.Lock()
			completed++
			if progress != nil {
				progress(models.SyncProgress{
					Phase: "fetching", Current: completed, Total: n,
					Message: fmt.Sprintf("Fetching file %d/%d", completed, n),
				})
			}
			progressMu.Unlock()
		}(i, sha)
	}
	wg.Wait()

	for i, out := range outcomes {
		m, _ := items[i].(map[string]any)
		p, _ := m["path"].(string)
		switch {
		case out.err != nil:
			docs[i].Text = strings.TrimSpace(docs[i].Text + fmt.Sprintf("\n\n(file content fetch failed: %v)", out.err))
		case out.skipped:
			docs[i].Text = strings.TrimSpace(p + "\n\n(binary or large file skipped)")
		case out.text != "":
			// Replace rather than append — the item's raw JSON metadata (sha,
			// url, …) dumped by itemsToDocuments is noise once we have the
			// real file content.
			docs[i].Text = strings.TrimSpace(p + "\n\n" + out.text)
		}
	}
	return nil
}
