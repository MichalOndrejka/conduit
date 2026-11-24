package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
)

// indexedAs returns the index state a completed sync of docs would leave
// (one chunk each), sealed with seal.
func indexedAs(docs []models.SourceDocument, seal string) *IndexState {
	st := &rag.SourceState{Docs: map[string]*rag.StoredDoc{}}
	if seal != "" {
		st.Seal = rag.SealValue(seal, len(docs))
	}
	for _, d := range docs {
		st.Docs[d.ID] = &rag.StoredDoc{Complete: true, Chunks: 1, IndexFP: "fp", FetchKey: d.FetchKey, ContentHash: "h"}
	}
	return &IndexState{IndexFP: "fp", State: st}
}

func kept(docs []models.SourceDocument) (keep, fetched []string) {
	for _, d := range docs {
		if d.Keep {
			keep = append(keep, d.ID)
		} else {
			fetched = append(fetched, d.ID)
		}
	}
	return keep, fetched
}

// Commits already indexed, current and listed from the same refs, are not
// diffed again; one whose refs changed is.
func TestAdoCommitsSkipIndexedSHAs(t *testing.T) {
	history := map[string][]string{"branch/main": {"c3", "c2", "c1"}, "tag/v1.0": {"c1"}}
	var changes atomic.Int32
	srv := httptest.NewServer(blobsZipShim(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			q := r.URL.Query()
			var items []map[string]any
			if skip := q.Get("searchCriteria.$skip"); skip == "" || skip == "0" {
				for _, id := range history[q.Get("searchCriteria.itemVersion.versionType")+"/"+q.Get("searchCriteria.itemVersion.version")] {
					items = append(items, map[string]any{"commitId": id, "comment": "commit " + id})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": items})
		case strings.Contains(r.URL.Path, "/changes"):
			changes.Add(1)
			_ = json.NewEncoder(w).Encode(adoChangesResponse{})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	cfg := map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/commits", "Branch": "main"}
	s := &APISource{src: commitSrc(cfg)}

	first, err := s.FetchDocuments(WithIndexState(context.Background(), &IndexState{IndexFP: "fp", State: &rag.SourceState{}}), nil)
	if err != nil || changes.Load() != 3 {
		t.Fatalf("first fetch: %v, %d change listings", err, changes.Load())
	}
	for _, d := range first {
		if d.FetchKey == "" || d.Keep {
			t.Fatalf("first fetch doc %+v", d)
		}
	}

	again, err := s.FetchDocuments(WithIndexState(context.Background(), indexedAs(first, "")), nil)
	if keep, fetched := kept(again); err != nil || len(keep) != 3 || len(fetched) != 0 || changes.Load() != 3 {
		t.Fatalf("second fetch: keep %v, fetched %v, err %v, %d change listings", keep, fetched, err, changes.Load())
	}

	// Ticking a tag changes c1's refs only.
	cfg["Tags"] = "v1.0"
	again, err = s.FetchDocuments(WithIndexState(context.Background(), indexedAs(first, "")), nil)
	if keep, fetched := kept(again); err != nil || len(keep) != 2 || len(fetched) != 1 || fetched[0] != "src-1_capi_c1" {
		t.Fatalf("after ticking a tag: keep %v, fetched %v, err %v", keep, fetched, err)
	}
	for _, d := range again {
		if d.ID == "src-1_capi_c1" && d.Properties["version"] != "main, v1.0" {
			t.Errorf("c1 version = %q", d.Properties["version"])
		}
	}

	// Any other setting may shape the documents: everything is refetched.
	delete(cfg, "Tags")
	cfg["ContentFields"] = "comment"
	again, _ = s.FetchDocuments(WithIndexState(context.Background(), indexedAs(first, "")), nil)
	if keep, _ := kept(again); len(keep) != 0 {
		t.Errorf("after a settings change kept %v, want none", keep)
	}

	// A document embedded under another index fingerprint is refetched.
	stale := indexedAs(first, "")
	stale.IndexFP = "fp2"
	delete(cfg, "ContentFields")
	again, _ = s.FetchDocuments(WithIndexState(context.Background(), stale), nil)
	if keep, _ := kept(again); len(keep) != 0 {
		t.Errorf("under a new index fingerprint kept %v, want none", keep)
	}
}

func TestGitHubCommitsSkipIndexedSHAs(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("", map[string]any{"default_branch": "main"})
	f.HandleFunc("/api/v3/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		var page []map[string]any
		if r.URL.Query().Get("page") == "1" {
			for _, sha := range []string{"bbb2", "aaa1"} {
				page = append(page, map[string]any{"sha": sha, "commit": map[string]any{"message": "m " + sha}})
			}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	var diffs atomic.Int32
	f.HandleFunc("/api/v3/repos/acme/app/commits/", func(w http.ResponseWriter, r *http.Request) {
		diffs.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
	})
	s := f.source(t, models.SourceGitCommits, nil)
	first, err := s.FetchDocuments(WithIndexState(context.Background(), &IndexState{IndexFP: "fp", State: &rag.SourceState{}}), nil)
	if err != nil || diffs.Load() != 2 {
		t.Fatalf("first fetch: %v, %d diffs", err, diffs.Load())
	}
	again, err := s.FetchDocuments(WithIndexState(context.Background(), indexedAs(first, "")), nil)
	if keep, _ := kept(again); err != nil || len(keep) != 2 || diffs.Load() != 2 {
		t.Errorf("second fetch: keep %v, err %v, %d diffs", keep, err, diffs.Load())
	}
}

// A repository tree is downloaded at the commit its ref points to; a source
// sealed at the same commits downloads nothing, and a moved ref downloads
// again.
func TestAdoFilesSkipSealedTree(t *testing.T) {
	head := "sha-1"
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_apis/git/repositories/repo"):
			_, _ = w.Write([]byte(`{"defaultBranch":"refs/heads/main"}`))
		case strings.HasSuffix(r.URL.Path, "/refs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]string{{"name": "refs/heads/main", "objectId": head}}})
		case r.URL.Query().Get("versionDescriptor.version") != head:
			http.Error(w, "not at the resolved commit: "+r.URL.String(), http.StatusBadRequest)
		default:
			downloads.Add(1)
			_, _ = w.Write(repoZip(t, map[string]string{"src/A.cs": "a", "src/B.cs": "b"}))
		}
	}))
	defer srv.Close()
	s := &APISource{src: codeSrc(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/items"})}

	st := &IndexState{IndexFP: "fp", State: &rag.SourceState{}}
	first, err := s.FetchDocuments(WithIndexState(context.Background(), st), nil)
	if err != nil || len(first) != 2 || downloads.Load() != 1 || st.Seal == "" {
		t.Fatalf("first fetch: %d docs, %v, %d downloads, seal %q", len(first), err, downloads.Load(), st.Seal)
	}
	seal := st.Seal

	// Sealed at the same commit: nothing downloaded, everything kept.
	st = indexedAs(first, seal)
	again, err := s.FetchDocuments(WithIndexState(context.Background(), st), nil)
	if keep, _ := kept(again); err != nil || len(keep) != 2 || downloads.Load() != 1 || st.Seal != seal {
		t.Fatalf("sealed fetch: keep %v, %v, %d downloads, seal %q", keep, err, downloads.Load(), st.Seal)
	}

	// Not sealed (the last sync failed part-way): downloaded again.
	if _, err := s.FetchDocuments(WithIndexState(context.Background(), indexedAs(first, "")), nil); err != nil || downloads.Load() != 2 {
		t.Fatalf("unsealed fetch: %v, %d downloads", err, downloads.Load())
	}

	// The branch moved: downloaded again, under a new seal.
	head = "sha-2"
	st = indexedAs(first, seal)
	again, err = s.FetchDocuments(WithIndexState(context.Background(), st), nil)
	if keep, _ := kept(again); err != nil || len(keep) != 0 || downloads.Load() != 3 || st.Seal == seal {
		t.Fatalf("moved fetch: keep %v, %v, %d downloads", keep, err, downloads.Load())
	}

	// A whole document lost since the sealed sync: downloaded again.
	st = indexedAs(first, seal)
	delete(st.State.Docs, first[1].ID)
	if _, err := s.FetchDocuments(WithIndexState(context.Background(), st), nil); err != nil || downloads.Load() != 4 {
		t.Fatalf("lost document: %v, %d downloads", err, downloads.Load())
	}

	// One document not current: downloaded again.
	head = "sha-1"
	st = indexedAs(first, seal)
	st.State.Docs[first[0].ID].Complete = false
	if again, _ := s.FetchDocuments(WithIndexState(context.Background(), st), nil); downloads.Load() != 5 || again[0].Keep {
		t.Errorf("incomplete doc: %d downloads", downloads.Load())
	}
}

func TestGitHubFilesSkipSealedTree(t *testing.T) {
	f := newFakeGitHub(t)
	var downloads atomic.Int32
	f.HandleFunc("/api/v3/repos/acme/app/tarball/main", func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(repoTarball(t, map[string]string{"a.go": "a"}))
	})
	s := f.source(t, models.SourceCodeRepo, map[string]string{"Branch": "main"})
	st := &IndexState{IndexFP: "fp", State: &rag.SourceState{}}
	first, err := s.FetchDocuments(WithIndexState(context.Background(), st), nil)
	if err != nil || downloads.Load() != 1 || st.Seal == "" {
		t.Fatalf("first fetch: %v, %d downloads, seal %q", err, downloads.Load(), st.Seal)
	}
	sealed := indexedAs(first, st.Seal)
	again, err := s.FetchDocuments(WithIndexState(context.Background(), sealed), nil)
	if keep, _ := kept(again); err != nil || len(keep) != len(first) || downloads.Load() != 1 {
		t.Errorf("sealed fetch: keep %v, %v, %d downloads", keep, err, downloads.Load())
	}
}
