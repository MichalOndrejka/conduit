// Batched blob downloads for Azure DevOps commit history: the file contents
// a sync's diffs need come in zips of many blobs (POST …/blobs with the blob
// ids) instead of one request per file version.
package sources

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
)

// adoBlobsPerZip caps the blobs asked for in one zip, keeping each request
// and its download a reasonable size.
const adoBlobsPerZip = 500

// blobsZip downloads shas in one zip (Azure DevOps' Get Blobs Zip: POST
// {repo}/blobs with the ids as a JSON array) and returns each blob's
// content by sha. Entries are matched to their sha by file name; a blob the
// zip doesn't hold under its sha is read on its own, so nothing is lost if a
// server names entries differently — only the batching is.
func (a *APISource) blobsZip(ctx context.Context, client *http.Client, repoBase string, shas []string) (map[string]string, error) {
	body, err := json.Marshal(shas)
	if err != nil {
		return nil, err
	}
	dl := *client
	dl.Timeout = archiveDownloadTimeout
	name, err := requestToFile(ctx, &dl, http.MethodPost, repoBase+"/blobs?api-version=7.1", body, "conduit-blobs-*.zip",
		func(req *http.Request) {
			a.applyAuth(req)
			a.applyExtraHeaders(req)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/zip")
		}, checkZip)
	if err != nil {
		return nil, fmt.Errorf("downloading %d blobs: %w", len(shas), err)
	}
	defer os.Remove(name)

	want := make(map[string]bool, len(shas))
	for _, s := range shas {
		want[strings.ToLower(s)] = true
	}
	out := make(map[string]string, len(shas))
	zr, err := zip.OpenReader(name)
	if err != nil {
		return nil, fmt.Errorf("blobs download is not a zip: %w", err)
	}
	defer zr.Close()
	for _, e := range zr.File {
		sha := strings.ToLower(strings.TrimSuffix(path.Base(e.Name), path.Ext(e.Name)))
		if e.FileInfo().IsDir() || !want[sha] {
			continue
		}
		rc, err := e.Open()
		if err != nil {
			return nil, fmt.Errorf("reading blob %s from zip: %w", sha, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading blob %s from zip: %w", sha, err)
		}
		out[sha] = string(data)
	}

	var missing []string
	for _, s := range shas {
		if _, ok := out[strings.ToLower(s)]; !ok {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		log.Printf("warning: the blobs zip of %s lacked %d of %d blobs under their ids — reading those one by one",
			repoBase, len(missing), len(shas))
		for _, s := range missing {
			text, err := a.blobText(ctx, client, repoBase, s)
			if err != nil {
				return nil, fmt.Errorf("reading blob %s: %w", s, err)
			}
			out[strings.ToLower(s)] = text
		}
	}
	return out, nil
}

// commitBlobs lists the blob ids a commit's diff needs.
func commitBlobs(changes []adoChange) []string {
	var out []string
	for _, c := range changes {
		if !diffableChange(c) {
			continue
		}
		for _, sha := range []string{c.Item.OriginalObjectID, c.Item.ObjectID} {
			if sha != "" {
				out = append(out, sha)
			}
		}
	}
	return out
}

// batchCommitDiffs renders the diff of every commit from its changes,
// downloading the blobs of consecutive commits together in zips of up to
// adoBlobsPerZip (a commit with more gets a zip of its own, or several).
// Each batch's blobs are released before the next is downloaded. report is
// called after each commit's diff.
func (a *APISource) batchCommitDiffs(ctx context.Context, client *http.Client, repoBase string, changes [][]adoChange, out []commitOutcome, report func()) error {
	flush := func(commits []int) error {
		seen := map[string]bool{}
		var shas []string
		for _, i := range commits {
			for _, sha := range commitBlobs(changes[i]) {
				if !seen[strings.ToLower(sha)] {
					seen[strings.ToLower(sha)] = true
					shas = append(shas, sha)
				}
			}
		}
		blobs := map[string]string{}
		for start := 0; start < len(shas); start += adoBlobsPerZip {
			got, err := a.blobsZip(ctx, client, repoBase, shas[start:min(start+adoBlobsPerZip, len(shas))])
			if err != nil {
				return err
			}
			for k, v := range got {
				blobs[k] = v
			}
		}
		for _, i := range commits {
			diff, files, paths, err := commitDiff(changes[i], func(sha string) (string, error) {
				text, ok := blobs[strings.ToLower(sha)]
				if !ok {
					return "", fmt.Errorf("blob %s was not downloaded", sha)
				}
				return text, nil
			})
			if err != nil {
				return fmt.Errorf("commit %s: %w", out[i].id, err)
			}
			out[i].diff, out[i].filesChanged, out[i].paths = diff, files, paths
			report()
		}
		return nil
	}

	var batch []int
	size := 0
	for i := range changes {
		n := len(commitBlobs(changes[i]))
		if len(batch) > 0 && size+n > adoBlobsPerZip {
			if err := flush(batch); err != nil {
				return err
			}
			batch, size = nil, 0
		}
		batch = append(batch, i)
		size += n
	}
	if len(batch) > 0 {
		return flush(batch)
	}
	return nil
}
