// Repository files for GitHubSource: the branch's whole tree in one
// recursive listing, then each matching file's blob.
package sources

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/MichalOndrejka/conduit/internal/models"
)

type githubTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob, tree or commit (submodule)
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

// refPath escapes a branch name for a URL path, keeping its slashes.
func refPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// commitSHA resolves a branch to its head commit.
func (g *GitHubSource) commitSHA(ctx context.Context, branch string) (string, error) {
	data, _, err := g.get(ctx, g.repoAPI()+"/commits/"+refPath(branch), "application/vnd.github.sha")
	if err != nil {
		return "", fmt.Errorf("resolving branch %q: %w", branch, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// listTree lists every file (blob) of the tree at commit sha. A tree too big
// for one recursive listing fails the sync rather than indexing part of it.
func (g *GitHubSource) listTree(ctx context.Context, sha string) ([]githubTreeEntry, error) {
	var resp struct {
		Tree      []githubTreeEntry `json:"tree"`
		Truncated bool              `json:"truncated"`
	}
	if _, err := g.getJSON(ctx, g.repoAPI()+"/git/trees/"+url.PathEscape(sha)+"?recursive=1", &resp); err != nil {
		return nil, fmt.Errorf("listing files of %s/%s: %w", g.owner, g.repo, err)
	}
	if resp.Truncated {
		return nil, fmt.Errorf("repository %s/%s has more files than GitHub returns in one tree listing", g.owner, g.repo)
	}
	files := make([]githubTreeEntry, 0, len(resp.Tree))
	for _, e := range resp.Tree {
		if e.Type == "blob" {
			files = append(files, e)
		}
	}
	return files, nil
}

// fetchFiles embeds every file matching PathFilter, one document per file
// holding its path and full content. A single file that can't be fetched is
// recorded as a note on its document rather than failing the whole sync,
// as for Azure DevOps.
func (g *GitHubSource) fetchFiles(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	branch, err := g.branch(ctx)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Listing files"})
	}
	sha, err := g.commitSHA(ctx, branch)
	if err != nil {
		return nil, err
	}
	all, err := g.listTree(ctx, sha)
	if err != nil {
		return nil, err
	}
	patterns := parsePathFilter(g.src.GetConfig("PathFilter"))
	var files []githubTreeEntry
	for _, f := range all {
		if matchesPathFilter(f.Path, patterns) {
			files = append(files, f)
		}
	}
	if top := g.top(); top > 0 && len(files) > top {
		files = files[:top]
	}

	n := len(files)
	outcomes := make([]fileContentOutcome, n)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	for i := range files {
		if files[i].Size > maxRepoFileBytes {
			outcomes[i] = fileContentOutcome{skipped: true}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			data, _, err := g.get(ctx, g.repoAPI()+"/git/blobs/"+url.PathEscape(files[i].SHA), "application/vnd.github.raw")
			text := string(data)
			out := fileContentOutcome{text: text, err: err}
			if err == nil && (len(text) > maxRepoFileBytes || isBinary(text)) {
				out = fileContentOutcome{skipped: true}
			}
			outcomes[i] = out

			mu.Lock()
			completed++
			if progress != nil {
				progress(models.SyncProgress{
					Phase: "fetching", Current: completed, Total: n,
					Message: fmt.Sprintf("Fetching file %d/%d", completed, n),
				})
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	docs := make([]models.SourceDocument, 0, n)
	for i, f := range files {
		var body string
		switch out := outcomes[i]; {
		case out.err != nil:
			body = fmt.Sprintf("(file content fetch failed: %v)", out.err)
		case out.skipped:
			body = "(binary or large file skipped)"
		default:
			body = out.text
		}
		props := map[string]string{
			"title":   f.Path,
			"web_url": g.repoWeb() + "/blob/" + refPath(branch) + "/" + refPath(f.Path),
			"branch":  branch,
		}
		filters := g.scopeFilters(props)
		addFileFilters(&filters, f.Path)
		docs = append(docs, models.SourceDocument{
			ID:         fmt.Sprintf("%s_gh_file_%s", g.src.ID, f.Path),
			Text:       strings.TrimSpace(f.Path + "\n\n" + body),
			Tags:       g.tags(),
			Properties: props,
			Filters:    filters,
		})
	}
	return docs, nil
}
