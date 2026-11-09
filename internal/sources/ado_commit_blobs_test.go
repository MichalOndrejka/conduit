package sources

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// commitRepoStub serves n commits, each modifying two files, counting the
// per-blob GETs and batched POSTs. zipName names a zip entry for a sha.
func commitRepoStub(t *testing.T, n int, zipName func(sha string) string) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var gets, posts atomic.Int32
	blob := func(sha string) string { return "content of " + sha + "\n" }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/commits"):
			if skip := r.URL.Query().Get("searchCriteria.$skip"); skip != "" && skip != "0" {
				writeJSON(w, map[string]any{"value": []any{}})
				return
			}
			var items []any
			for i := range n {
				items = append(items, map[string]any{"commitId": fmt.Sprintf("c%d", i), "comment": "change"})
			}
			writeJSON(w, map[string]any{"value": items})
		case strings.HasSuffix(p, "/changes"):
			id := strings.Split(p, "/")[len(strings.Split(p, "/"))-2]
			writeJSON(w, adoChangesResponse{Changes: []adoChange{
				{ChangeType: "edit", Item: adoChangeItem{Path: "/a.txt", OriginalObjectID: id + "a0", ObjectID: id + "a1"}},
				{ChangeType: "edit", Item: adoChangeItem{Path: "/b.txt", OriginalObjectID: id + "b0", ObjectID: id + "b1"}},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/blobs"):
			posts.Add(1)
			var shas []string
			_ = json.NewDecoder(r.Body).Decode(&shas)
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			for _, sha := range shas {
				fw, _ := zw.Create(zipName(sha))
				_, _ = fw.Write([]byte(blob(sha)))
			}
			_ = zw.Close()
			_, _ = w.Write(buf.Bytes())
		case strings.Contains(p, "/blobs/"):
			gets.Add(1)
			_, _ = w.Write([]byte(blob(p[strings.LastIndex(p, "/")+1:])))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gets, &posts
}

// 30 commits × 2 edited files need 120 file versions: one zip, not 120 GETs.
func TestCommitHistoryDownloadsBlobsInOneZip(t *testing.T) {
	srv, gets, posts := commitRepoStub(t, 30, func(sha string) string { return sha })
	s := &APISource{src: commitSrc(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/commits"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 30 || gets.Load() != 0 || posts.Load() != 1 {
		t.Fatalf("docs %d, blob GETs %d, zip POSTs %d; want 30, 0, 1", len(docs), gets.Load(), posts.Load())
	}
	for _, want := range []string{"-content of c7a0", "+content of c7a1", "-content of c7b0", "+content of c7b1"} {
		if !strings.Contains(docs[7].Text, want) {
			t.Errorf("commit c7 diff lacks %q:\n%s", want, docs[7].Text)
		}
	}
}

// Over adoBlobsPerZip versions, the commits are split over several zips.
func TestCommitHistorySplitsLargeBlobBatches(t *testing.T) {
	n := adoBlobsPerZip/4 + 10 // 4 versions per commit
	srv, gets, posts := commitRepoStub(t, n, func(sha string) string { return sha })
	s := &APISource{src: commitSrc(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/commits"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != n || gets.Load() != 0 || posts.Load() != 2 {
		t.Fatalf("docs %d, blob GETs %d, zip POSTs %d; want %d, 0, 2", len(docs), gets.Load(), posts.Load(), n)
	}
}

// A server that names zip entries differently still yields complete diffs:
// the blobs the zip lacks under their ids are read one by one.
func TestCommitHistoryFallsBackWhenZipEntriesAreNamedDifferently(t *testing.T) {
	srv, gets, _ := commitRepoStub(t, 2, func(sha string) string { return "blob-" + sha + ".bin.x" })
	s := &APISource{src: commitSrc(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/commits"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 8 || !strings.Contains(docs[1].Text, "+content of c1b1") {
		t.Fatalf("blob GETs %d, diff:\n%s", gets.Load(), docs[1].Text)
	}
}
