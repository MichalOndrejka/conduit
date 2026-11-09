// Repository files for GitHubSource: the branch's whole tree in a single
// gzipped tarball download (GET /repos/{owner}/{repo}/tarball/{ref}), read as
// it streams in. A sync costs one API request for the files, plus one to look
// up the default branch when no Branch is configured, whatever the repository
// size, so syncing stays far below GitHub's rate limits.
package sources

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// archiveDownloadTimeout bounds one repository archive download. It replaces
// the client's per-request timeout, which would cut off a large archive
// still streaming in.
const archiveDownloadTimeout = 30 * time.Minute

// refPath escapes a branch name for a URL path, keeping its slashes.
func refPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// archiveFile is one file read from the repository archive.
type archiveFile struct {
	path    string
	text    string
	skipped bool // binary or over maxRepoFileBytes
}

// readArchive downloads the tarball of ref — shared with the other sources
// of the sync batch that read the same tarball with the same token — and
// returns every file matching patterns. A download that fails part-way is
// restarted from the beginning.
func (g *GitHubSource) readArchive(ctx context.Context, ref string, patterns []string, progress ProgressCallback) ([]archiveFile, error) {
	tarURL := g.repoAPI() + "/tarball/" + refPath(ref)
	name, release, err := fetchArchive(ctx, "github|"+tarURL+"|"+g.src.GetConfig("Token"), func(ctx context.Context) (string, error) {
		client := *g.client
		client.Timeout = archiveDownloadTimeout
		return downloadToFile(ctx, &client, tarURL, "conduit-repo-*.tar.gz", g.prepare(""), checkTarGz)
	})
	if err != nil {
		return nil, fmt.Errorf("downloading %s/%s at %q: %w", g.owner, g.repo, ref, err)
	}
	defer release()
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var files []archiveFile
	err = func(body io.Reader) error {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return fmt.Errorf("repository archive is not gzip: %w", err)
		}
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("reading repository archive: %w", err)
			}
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeSymlink {
				continue // directories, the pax header holding the commit SHA
			}
			// Entries sit under a single {owner}-{repo}-{sha}/ directory.
			_, path, ok := strings.Cut(hdr.Name, "/")
			if !ok || path == "" || !matchesPathFilter(path, patterns) {
				continue
			}
			f := archiveFile{path: path}
			switch {
			case hdr.Typeflag == tar.TypeSymlink:
				f.text = hdr.Linkname // as GitHub stores a symlink's blob
			case hdr.Size > maxRepoFileBytes:
				f.skipped = true
			default:
				data, err := io.ReadAll(tr)
				if err != nil {
					return fmt.Errorf("reading %s from repository archive: %w", path, err)
				}
				f.text = string(data)
				f.skipped = isBinary(f.text)
			}
			files = append(files, f)
			if progress != nil {
				progress(models.SyncProgress{Phase: "fetching", Current: len(files),
					Message: fmt.Sprintf("Read %d files from repository archive", len(files))})
			}
		}
	}(f)
	if err != nil {
		return nil, fmt.Errorf("reading the archive of %s/%s at %q: %w", g.owner, g.repo, ref, err)
	}
	return files, nil
}

// fetchFiles embeds every file matching PathFilter, one document per file
// holding its path and full content.
func (g *GitHubSource) fetchFiles(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	branch, err := g.branch(ctx)
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Downloading repository archive"})
	}
	files, err := g.readArchive(ctx, branch, parsePathFilter(g.src.GetConfig("PathFilter")), progress)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	if top := g.top(); top > 0 && len(files) > top {
		files = files[:top]
	}

	docs := make([]models.SourceDocument, 0, len(files))
	for _, f := range files {
		body := f.text
		if f.skipped {
			body = "(binary or large file skipped)"
		}
		props := map[string]string{
			"title":   f.path,
			"web_url": g.repoWeb() + "/blob/" + refPath(branch) + "/" + refPath(f.path),
			"branch":  branch,
		}
		filters := g.scopeFilters(props)
		addFileFilters(&filters, f.path)
		docs = append(docs, models.SourceDocument{
			ID:         fmt.Sprintf("%s_gh_file_%s", g.src.ID, f.path),
			Text:       strings.TrimSpace(f.path + "\n\n" + body),
			Tags:       g.tags(),
			Properties: props,
			Filters:    filters,
		})
	}
	return docs, nil
}

// prepare returns the request setup for this source: the Accept media type
// (JSON by default) and the token, if one is configured.
func (g *GitHubSource) prepare(accept string) func(*http.Request) {
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	token := ""
	if name := g.src.GetConfig("Token"); name != "" && g.secrets != nil {
		token = g.secrets.GetValue(name)
	}
	return func(req *http.Request) {
		req.Header.Set("Accept", accept)
		if token != "" {
			// "token" rather than "Bearer": every GitHub Enterprise Server
			// version accepts it for personal access tokens.
			req.Header.Set("Authorization", "token "+token)
		}
	}
}
