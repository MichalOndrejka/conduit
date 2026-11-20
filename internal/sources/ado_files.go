// Azure DevOps repository files for APISource (Source Code, Test Code,
// Documentation, and Requirements in files mode): each configured branch's or
// tag's whole tree in a single zip download from the items endpoint
// (…/items?path=/&$format=zip), filtered locally by PathFilter. A sync costs
// one request per ref for the files, whatever the repository size, plus one
// to look up the default branch when no ref is configured.
package sources

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// maxRepoFileBytes skips indexing repo files larger than this (likely
// binary/generated).
const maxRepoFileBytes = 200 << 10

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

// adoRepoZipURL turns a source's items URL (already carrying its branch's
// versionDescriptor) into a download of the whole tree as one zip. A folder
// zip is always recursive; recursionLevel and scopePath are dropped because
// Azure DevOps Server rejects a recursionLevel other than None alongside a
// single item path (HTTP 400), and source URLs usually carry
// recursionLevel=Full from the items-list form.
func adoRepoZipURL(itemsURL string) (string, error) {
	u, err := url.Parse(itemsURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("recursionLevel")
	q.Del("scopePath")
	q.Set("path", "/")
	q.Set("$format", "zip")
	q.Set("download", "true")
	if q.Get("api-version") == "" {
		q.Set("api-version", "7.1")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// downloadRepoZip saves the tree's zip to a local file (a zip is read from
// its end, so it can't be consumed as it streams) — shared with the other
// sources of the sync batch that read the same zip with the same
// credential. The caller runs release when done with the file.
func (a *APISource) downloadRepoZip(ctx context.Context, zipURL string) (name string, release func(), err error) {
	return fetchArchive(ctx, "ado|"+zipURL+"|"+a.credentialKey(), func(ctx context.Context) (string, error) {
		client := a.httpClient()
		client.Timeout = archiveDownloadTimeout
		return downloadToFile(ctx, client, zipURL, "conduit-repo-*.zip", func(req *http.Request) {
			a.applyAuth(req)
			a.applyExtraHeaders(req)
			req.Header.Set("Accept", "application/zip")
		}, checkZip)
	})
}

// credentialKey identifies how the source authenticates (credential names,
// never secret values), so a download is shared only between sources that
// would be allowed to make it.
func (a *APISource) credentialKey() string {
	cfg := a.src
	return strings.Join([]string{cfg.GetConfig("AuthType"), cfg.GetConfig("Username"), cfg.GetConfig("Password"),
		cfg.GetConfig("Token"), cfg.GetConfig("ApiKeyHeader"), cfg.GetConfig("ApiKeyValue"), cfg.GetConfig("Headers")}, "\x00")
}

// readRepoZip returns every file of the zip matching patterns, sorted by path.
func readRepoZip(name string, patterns []string, progress ProgressCallback) ([]archiveFile, error) {
	zr, err := zip.OpenReader(name)
	if err != nil {
		return nil, fmt.Errorf("repository download is not a zip: %w", err)
	}
	defer zr.Close()
	var files []archiveFile
	for _, e := range zr.File {
		if e.FileInfo().IsDir() {
			continue
		}
		p := "/" + strings.TrimPrefix(e.Name, "/")
		if !matchesPathFilter(p, patterns) {
			continue
		}
		f := archiveFile{path: p}
		if e.UncompressedSize64 > maxRepoFileBytes {
			f.skipped = true
		} else {
			rc, err := e.Open()
			if err != nil {
				return nil, fmt.Errorf("reading %s from repository zip: %w", p, err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("reading %s from repository zip: %w", p, err)
			}
			f.text = string(data)
			f.skipped = isBinary(f.text)
		}
		files = append(files, f)
		if progress != nil && len(files)%200 == 0 {
			progress(models.SyncProgress{Phase: "fetching", Current: len(files),
				Message: fmt.Sprintf("Read %d files from repository zip", len(files))})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

// fetchAdoRepoFiles embeds every file matching PathFilter (at most top per
// ref) of each configured ref, one document per path and content holding its
// path, full content and the refs it is in.
func (a *APISource) fetchAdoRepoFiles(ctx context.Context, itemsURL string, top int, progress ProgressCallback) ([]models.SourceDocument, error) {
	refs, err := a.adoFileRefs(ctx, itemsURL)
	if err != nil {
		return nil, err
	}
	patterns := parsePathFilter(a.src.GetConfig("PathFilter"))
	refURLs := make(map[gitRef]string, len(refs))
	perRef := make([][]archiveFile, len(refs))
	for i, ref := range refs {
		refURL := withAdoItemsRef(itemsURL, ref)
		refURLs[ref] = refURL
		zipURL, err := adoRepoZipURL(refURL)
		if err != nil {
			return nil, err
		}
		if progress != nil {
			progress(models.SyncProgress{Phase: "fetching", Message: "Downloading repository zip of " + ref.Name})
		}
		name, release, err := a.downloadRepoZip(ctx, zipURL)
		if err != nil {
			return nil, fmt.Errorf("downloading the repository at %q: %w", ref.Name, err)
		}
		files, err := readRepoZip(name, patterns, progress)
		release()
		if err != nil {
			return nil, err
		}
		if len(files) > top {
			files = files[:top]
		}
		perRef[i] = files
	}

	merged := mergeRefFiles(refs, perRef)
	docs := make([]models.SourceDocument, len(merged))
	for i, f := range merged {
		// Linked to the first ref holding this content.
		d := a.itemsToDocuments([]any{map[string]any{"path": f.path}}, refURLs[f.refs[0]])[0]
		body := f.text
		if f.skipped {
			body = "(binary or large file skipped)"
		}
		d.Text = strings.TrimSpace(f.path + "\n\n" + body)
		// Keyed by path whatever IdField says: the zip carries no blob ids.
		d.ID = fmt.Sprintf("%s_capi_%s", a.src.ID, f.docKey())
		d.Properties["title"] = f.title()
		addVersionFilters(&d.Filters, d.Properties, f.refs)
		docs[i] = d
	}
	return docs, nil
}

// adoFileRefs is the refs a file source reads: the one its Url already names,
// else the configured branches and tags, else the repository's default
// branch.
func (a *APISource) adoFileRefs(ctx context.Context, itemsURL string) ([]gitRef, error) {
	if ref, ok := adoURLRef(itemsURL); ok {
		return []gitRef{ref}, nil
	}
	if refs := configuredRefs(a.src); len(refs) > 0 {
		return refs, nil
	}
	repoBase, _ := AdoItemsAPIBase(itemsURL)
	branch, err := a.adoDefaultBranch(ctx, a.httpClient(), repoBase, itemsURL)
	if err != nil {
		return nil, err
	}
	return []gitRef{{Name: branch}}, nil
}
