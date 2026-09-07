package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// commitSrc builds a commit-history-typed source, since diff enrichment is now
// driven entirely by Type rather than a config flag.
func commitSrc(cfg map[string]string) *models.SourceDefinition {
	return &models.SourceDefinition{ID: "src-1", Name: "Test Source", Type: models.SourceGitCommits, Config: cfg}
}

func TestTruncateRuneSafeDoesNotSplitMultiByteRune(t *testing.T) {
	// "€" is 3 bytes (E2 82 AC); cutting at byte 4 of "ab€cd" (a=1,b=1,€=3)
	// would land inside the € rune without rune-aware trimming.
	s := "ab€cd"
	got := truncateRuneSafe(s, 4)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateRuneSafe produced invalid UTF-8: %q (bytes: %v)", got, []byte(got))
	}
	if got != "ab" {
		t.Errorf("truncateRuneSafe(%q, 4) = %q, want %q", s, got, "ab")
	}

	// A cut point that already lands on a boundary should be unaffected.
	if got := truncateRuneSafe("hello world", 5); got != "hello" {
		t.Errorf("truncateRuneSafe on ASCII = %q, want %q", got, "hello")
	}

	// A string shorter than the limit is returned unchanged.
	if got := truncateRuneSafe("hi", 10); got != "hi" {
		t.Errorf("truncateRuneSafe short string = %q, want %q", got, "hi")
	}
}

func TestAdoRepoAPIBase(t *testing.T) {
	cases := []struct {
		url    string
		base   string
		wantOK bool
	}{
		{
			url:    "https://dev.azure.com/org/proj/_apis/git/repositories/seefood-api/commits?api-version=7.1&searchCriteria.$top=50",
			base:   "https://dev.azure.com/org/proj/_apis/git/repositories/seefood-api",
			wantOK: true,
		},
		{
			url:    "https://dev.azure.com/org/proj/_apis/git/repositories/seefood-api/commits",
			base:   "https://dev.azure.com/org/proj/_apis/git/repositories/seefood-api",
			wantOK: true,
		},
		{url: "https://dev.azure.com/org/proj/_apis/wit/wiql", wantOK: false},
		{url: "https://api.example.com/items", wantOK: false},
		{url: "", wantOK: false},
	}
	for _, c := range cases {
		base, ok := AdoRepoAPIBase(c.url)
		if ok != c.wantOK {
			t.Errorf("AdoRepoAPIBase(%q) ok = %v, want %v", c.url, ok, c.wantOK)
			continue
		}
		if ok && base != c.base {
			t.Errorf("AdoRepoAPIBase(%q) = %q, want %q", c.url, base, c.base)
		}
	}
}

// adoStub serves a fake ADO repo: /commits/{id}/changes and /blobs/{sha}.
func adoStub(t *testing.T, changesByCommit map[string]adoChangesResponse, blobs map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/changes"):
			// path: /repo/commits/{id}/changes
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			id := parts[len(parts)-2]
			resp, ok := changesByCommit[id]
			if !ok {
				http.Error(w, "no such commit", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
		case strings.Contains(r.URL.Path, "/blobs/"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			sha := parts[len(parts)-1]
			text, ok := blobs[sha]
			if !ok {
				http.Error(w, "no such blob", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(text))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
}

func TestFetchCommitDiffEditedFile(t *testing.T) {
	changes := adoChangesResponse{Changes: []adoChange{
		{ChangeType: "edit", Item: adoChangeItem{Path: "main.go", ObjectID: "newsha", OriginalObjectID: "oldsha"}},
	}}
	srv := adoStub(t, map[string]adoChangesResponse{"abc123": changes}, map[string]string{
		"oldsha": "line1\nline2\nline3\n",
		"newsha": "line1\nCHANGED\nline3\n",
	})
	defer srv.Close()

	s := &APISource{src: src(map[string]string{"Url": srv.URL + "/_apis/git/repositories/repo/commits"})}
	diffText, filesChanged, err := s.fetchCommitDiff(context.Background(), s.httpClient(), srv.URL+"/_apis/git/repositories/repo", "abc123", defaultMaxFilesPerCommit, defaultMaxDiffChars)
	if err != nil {
		t.Fatal(err)
	}
	if filesChanged != 1 {
		t.Errorf("filesChanged = %d, want 1", filesChanged)
	}
	if !strings.Contains(diffText, "@@") {
		t.Errorf("diff missing hunk header: %q", diffText)
	}
	if !strings.Contains(diffText, "-line2") || !strings.Contains(diffText, "+CHANGED") {
		t.Errorf("diff missing changed lines: %q", diffText)
	}
	if !strings.Contains(diffText, "main.go") {
		t.Errorf("diff missing file path: %q", diffText)
	}
}

func TestFetchCommitDiffAddedAndDeletedFile(t *testing.T) {
	changes := adoChangesResponse{Changes: []adoChange{
		{ChangeType: "add", Item: adoChangeItem{Path: "new.go", ObjectID: "newsha"}},
		{ChangeType: "delete", Item: adoChangeItem{Path: "gone.go", OriginalObjectID: "oldsha"}},
	}}
	srv := adoStub(t, map[string]adoChangesResponse{"c1": changes}, map[string]string{
		"newsha": "package main\n",
		"oldsha": "package old\n",
	})
	defer srv.Close()

	s := &APISource{src: src(nil)}
	diffText, filesChanged, err := s.fetchCommitDiff(context.Background(), s.httpClient(), srv.URL+"/_apis/git/repositories/repo", "c1", defaultMaxFilesPerCommit, defaultMaxDiffChars)
	if err != nil {
		t.Fatal(err)
	}
	if filesChanged != 2 {
		t.Errorf("filesChanged = %d, want 2", filesChanged)
	}
	if !strings.Contains(diffText, "+package main") {
		t.Errorf("add diff missing new content: %q", diffText)
	}
	if !strings.Contains(diffText, "-package old") {
		t.Errorf("delete diff missing old content: %q", diffText)
	}
}

// A commit that adds a new folder reports that folder as a changed "item"
// alongside real file changes, but a folder's objectId points at a tree, not
// a blob — fetching it via the blobs endpoint as if it were a file must be
// skipped, since Azure DevOps rejects that with 400 Bad Request.
func TestFetchCommitDiffSkipsFolderChanges(t *testing.T) {
	changes := adoChangesResponse{Changes: []adoChange{
		{ChangeType: "add", Item: adoChangeItem{Path: "newdir", ObjectID: "treesha", IsFolder: true, GitObjectType: "tree"}},
		{ChangeType: "add", Item: adoChangeItem{Path: "newdir/main.go", ObjectID: "newsha", GitObjectType: "blob"}},
	}}
	srv := adoStub(t, map[string]adoChangesResponse{"c1": changes}, map[string]string{
		"newsha": "package main\n",
		// deliberately no "treesha" entry — a blob fetch for it must 404 the
		// stub, so if the fix regresses this test fails via the note below.
	})
	defer srv.Close()

	s := &APISource{src: src(nil)}
	diffText, filesChanged, err := s.fetchCommitDiff(context.Background(), s.httpClient(), srv.URL+"/_apis/git/repositories/repo", "c1", defaultMaxFilesPerCommit, defaultMaxDiffChars)
	if err != nil {
		t.Fatal(err)
	}
	if filesChanged != 2 {
		t.Errorf("filesChanged = %d, want 2", filesChanged)
	}
	if strings.Contains(diffText, "diff unavailable") {
		t.Errorf("folder change should have been skipped, not fetched as a blob: %q", diffText)
	}
	if !strings.Contains(diffText, "+package main") {
		t.Errorf("real file change missing from diff: %q", diffText)
	}
}

func TestFetchCommitDiffRespectsMaxFilesAndMaxChars(t *testing.T) {
	var changes adoChangesResponse
	blobs := map[string]string{}
	for i := 0; i < 5; i++ {
		path := "f" + strconv.Itoa(i) + ".go"
		oldSHA, newSHA := "old"+strconv.Itoa(i), "new"+strconv.Itoa(i)
		blobs[oldSHA] = "a\n"
		blobs[newSHA] = "b\n"
		changes.Changes = append(changes.Changes, adoChange{
			ChangeType: "edit",
			Item:       adoChangeItem{Path: path, ObjectID: newSHA, OriginalObjectID: oldSHA},
		})
	}
	srv := adoStub(t, map[string]adoChangesResponse{"c1": changes}, blobs)
	defer srv.Close()

	s := &APISource{src: src(nil)}

	// maxFiles=2 should skip the remaining 3 with a note, but still report the true count.
	diffText, filesChanged, err := s.fetchCommitDiff(context.Background(), s.httpClient(), srv.URL+"/_apis/git/repositories/repo", "c1", 2, defaultMaxDiffChars)
	if err != nil {
		t.Fatal(err)
	}
	if filesChanged != 5 {
		t.Errorf("filesChanged = %d, want 5", filesChanged)
	}
	if !strings.Contains(diffText, "more files changed (skipped)") {
		t.Errorf("expected skipped-files note: %q", diffText)
	}

	// maxChars=10 should truncate.
	diffText2, _, err := s.fetchCommitDiff(context.Background(), s.httpClient(), srv.URL+"/_apis/git/repositories/repo", "c1", defaultMaxFilesPerCommit, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diffText2, "(diff truncated)") {
		t.Errorf("expected truncation note: %q", diffText2)
	}
}

func TestFetchDocumentsWithFetchDiffsEnriches(t *testing.T) {
	changes := adoChangesResponse{Changes: []adoChange{
		{ChangeType: "edit", Item: adoChangeItem{Path: "main.go", ObjectID: "newsha", OriginalObjectID: "oldsha"}},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			if skip := r.URL.Query().Get("searchCriteria.$skip"); skip != "" && skip != "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"commitId": "abc123", "comment": "Fix login bug"},
				},
			})
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(changes)
		case strings.Contains(r.URL.Path, "/blobs/oldsha"):
			_, _ = w.Write([]byte("line1\nline2\n"))
		case strings.Contains(r.URL.Path, "/blobs/newsha"):
			_, _ = w.Write([]byte("line1\nFIXED\n"))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
		"ItemsPath":     "value",
		"IdField":       "commitId",
		"TitleField":    "comment",
		"ContentFields": "comment",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs", len(docs))
	}
	if !strings.Contains(docs[0].Text, "Fix login bug") {
		t.Errorf("commit message missing from text: %q", docs[0].Text)
	}
	if !strings.Contains(docs[0].Text, "@@") || !strings.Contains(docs[0].Text, "+FIXED") {
		t.Errorf("diff missing from text: %q", docs[0].Text)
	}
	if docs[0].Properties["files_changed"] != "1" {
		t.Errorf("files_changed = %q, want %q", docs[0].Properties["files_changed"], "1")
	}
}

// A source of any other type — even one pointed at an ADO commits URL that
// would otherwise qualify — must never get diff enrichment. Diff fetching is
// driven solely by Type == commit-history, not by config.
func TestFetchDocumentsNonCommitsTypeNeverGetsDiffs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"commitId": "abc123", "comment": "Fix login bug"},
				},
			})
		default:
			http.Error(w, "diff enrichment should never be attempted for a non-commits source type: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: src(map[string]string{
		"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
		"ItemsPath":     "value",
		"IdField":       "commitId",
		"TitleField":    "comment",
		"ContentFields": "comment",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs", len(docs))
	}
	if strings.Contains(docs[0].Text, "@@") {
		t.Errorf("non-commits source unexpectedly got a diff appended: %q", docs[0].Text)
	}
}

func TestFetchDocumentsFetchDiffsRequiresAdoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "1"}})
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url": srv.URL, "IdField": "id",
	})}
	if _, err := s.FetchDocuments(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "Azure DevOps") {
		t.Errorf("expected ADO URL error, got %v", err)
	}
}

func TestFetchCommitDiffSkippedCountAccurateWhenMaxCharsBreaksEarly(t *testing.T) {
	var changes adoChangesResponse
	blobs := map[string]string{}
	for i := 0; i < 5; i++ {
		path := "f" + strconv.Itoa(i) + ".go"
		oldSHA, newSHA := "old"+strconv.Itoa(i), "new"+strconv.Itoa(i)
		blobs[oldSHA] = "a\n"
		blobs[newSHA] = "b\n"
		changes.Changes = append(changes.Changes, adoChange{
			ChangeType: "edit",
			Item:       adoChangeItem{Path: path, ObjectID: newSHA, OriginalObjectID: oldSHA},
		})
	}
	srv := adoStub(t, map[string]adoChangesResponse{"c1": changes}, blobs)
	defer srv.Close()

	s := &APISource{src: src(nil)}

	// maxFiles left high (no file-count cap in play) but maxChars small
	// enough that the byte budget runs out partway through the 5 files.
	diffText, filesChanged, err := s.fetchCommitDiff(context.Background(), s.httpClient(), srv.URL+"/_apis/git/repositories/repo", "c1", defaultMaxFilesPerCommit, 50)
	if err != nil {
		t.Fatal(err)
	}
	if filesChanged != 5 {
		t.Fatalf("filesChanged = %d, want 5", filesChanged)
	}
	// Count "--- f" header starts rather than complete entries: a file whose
	// diff was fetched but then cut off by the maxChars hard-truncation still
	// counts as "processed" internally, even though its trailing "+++"/hunk
	// lines may not have survived the truncation.
	processed := strings.Count(diffText, "--- f")
	m := regexp.MustCompile(`… (\d+) more files changed \(skipped\)`).FindStringSubmatch(diffText)
	if m == nil {
		t.Fatalf("expected a skipped-files note, got %q", diffText)
	}
	skipped, _ := strconv.Atoi(m[1])
	if processed+skipped != filesChanged {
		t.Errorf("processed(%d) + skipped(%d) != filesChanged(%d); diffText=%q", processed, skipped, filesChanged, diffText)
	}
	if processed >= filesChanged {
		t.Errorf("expected maxChars to stop before processing all files, processed=%d", processed)
	}
}

func TestFetchDocumentsGracefullyDegradesOnPerCommitDiffFailure(t *testing.T) {
	changesOK := adoChangesResponse{Changes: []adoChange{
		{ChangeType: "edit", Item: adoChangeItem{Path: "main.go", ObjectID: "newsha", OriginalObjectID: "oldsha"}},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			if skip := r.URL.Query().Get("searchCriteria.$skip"); skip != "" && skip != "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"commitId": "ok1", "comment": "Good commit"},
					{"commitId": "bad1", "comment": "Bad commit"},
				},
			})
		case strings.Contains(r.URL.Path, "/commits/bad1/changes"):
			http.Error(w, "commit not found", http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(changesOK)
		case strings.Contains(r.URL.Path, "/blobs/oldsha"):
			_, _ = w.Write([]byte("line1\n"))
		case strings.Contains(r.URL.Path, "/blobs/newsha"):
			_, _ = w.Write([]byte("line1changed\n"))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
		"ItemsPath":     "value",
		"IdField":       "commitId",
		"TitleField":    "comment",
		"ContentFields": "comment",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected sync to succeed despite one commit's diff failing, got err: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}

	var good, bad *models.SourceDocument
	for i := range docs {
		switch {
		case strings.Contains(docs[i].Text, "Good commit"):
			good = &docs[i]
		case strings.Contains(docs[i].Text, "Bad commit"):
			bad = &docs[i]
		}
	}
	if good == nil || bad == nil {
		t.Fatalf("expected both commits' documents present: %+v", docs)
	}
	if !strings.Contains(good.Text, "@@") {
		t.Errorf("good commit missing diff: %q", good.Text)
	}
	if !strings.Contains(bad.Text, "diff fetch failed") {
		t.Errorf("bad commit missing failure note: %q", bad.Text)
	}
	if bad.Properties["files_changed"] != "0" {
		t.Errorf("bad commit files_changed = %q, want 0", bad.Properties["files_changed"])
	}
}

func TestFetchDocumentsWithFetchDiffsConcurrentOrderingIsCorrect(t *testing.T) {
	const n = 12
	changesFor := func(i int) adoChangesResponse {
		return adoChangesResponse{Changes: []adoChange{
			{ChangeType: "edit", Item: adoChangeItem{
				Path: fmt.Sprintf("f%d.go", i), ObjectID: fmt.Sprintf("new%d", i), OriginalObjectID: fmt.Sprintf("old%d", i),
			}},
		}}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			if skip := r.URL.Query().Get("searchCriteria.$skip"); skip != "" && skip != "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
				return
			}
			items := make([]map[string]any, n)
			for i := 0; i < n; i++ {
				items[i] = map[string]any{"commitId": fmt.Sprintf("c%d", i), "comment": fmt.Sprintf("commit %d", i)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": items})
		case strings.Contains(r.URL.Path, "/changes"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			id := parts[len(parts)-2] // "c{i}"
			var idx int
			fmt.Sscanf(id, "c%d", &idx)
			// Vary latency so completion order differs from submission order,
			// to actually exercise the concurrent workers rather than
			// happening to finish in submission order.
			time.Sleep(time.Duration(n-idx) * time.Millisecond)
			_ = json.NewEncoder(w).Encode(changesFor(idx))
		case strings.Contains(r.URL.Path, "/blobs/"):
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			sha := parts[len(parts)-1]
			if strings.HasPrefix(sha, "old") {
				_, _ = w.Write([]byte("old\n"))
			} else {
				_, _ = w.Write([]byte("new-" + strings.TrimPrefix(sha, "new") + "\n"))
			}
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
		"ItemsPath":     "value",
		"IdField":       "commitId",
		"TitleField":    "comment",
		"ContentFields": "comment",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != n {
		t.Fatalf("got %d docs, want %d", len(docs), n)
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("commit %d", i)
		if !strings.Contains(docs[i].Text, want) {
			t.Errorf("docs[%d] missing own commit message %q: %q", i, want, docs[i].Text)
		}
		wantFile := fmt.Sprintf("f%d.go", i)
		if !strings.Contains(docs[i].Text, wantFile) {
			t.Errorf("docs[%d] diff missing own file path %q (cross-talk between concurrent fetches?): %q", i, wantFile, docs[i].Text)
		}
	}
}

// TestFetchDocumentsPaginatesPastAdoCommitsPageSize verifies that a
// commit-history source keeps paging via searchCriteria.$top/$skip past
// Azure DevOps' single-page response size, rather than silently stopping at
// it (the "hard limit of 100" bug).
func TestFetchDocumentsPaginatesPastAdoCommitsPageSize(t *testing.T) {
	const total = adoCommitsPageSize + 30 // spans two pages
	var gotSkips []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			skip, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria.$skip"))
			top, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria.$top"))
			gotSkips = append(gotSkips, r.URL.Query().Get("searchCriteria.$skip"))
			end := skip + top
			if end > total {
				end = total
			}
			var items []map[string]any
			for i := skip; i < end; i++ {
				items = append(items, map[string]any{"commitId": fmt.Sprintf("c%d", i), "comment": fmt.Sprintf("commit %d", i)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": items})
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(adoChangesResponse{})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
		"ItemsPath":     "value",
		"IdField":       "commitId",
		"TitleField":    "comment",
		"ContentFields": "comment",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != total {
		t.Fatalf("got %d docs, want %d", len(docs), total)
	}
	if len(gotSkips) < 2 {
		t.Fatalf("expected at least 2 pages fetched, got %d requests: %v", len(gotSkips), gotSkips)
	}
	if gotSkips[0] != "0" {
		t.Errorf("first page skip = %q, want \"0\"", gotSkips[0])
	}
	if gotSkips[1] != strconv.Itoa(adoCommitsPageSize) {
		t.Errorf("second page skip = %q, want %q", gotSkips[1], strconv.Itoa(adoCommitsPageSize))
	}
}

// TestFetchDocumentsStopsPagingOnNullValueEnvelope reproduces a real-world
// bug: real ADO commit-history sources leave ItemsPath blank (it's not a
// visible ADO-tab field), so resolveItems auto-detects the "value" envelope
// instead of navigating an explicit path. If a trailing page's "value" comes
// back as JSON null instead of [] — which asList alone handles fine, but the
// pre-fix resolveItems fallback did not — the naive fallback wrapped that
// whole page's raw response as one bogus document, and since the pagination
// loop only stops once a page yields zero items, it kept "successfully"
// fetching (and appending) that same bogus document on every subsequent page
// up to maxPages, producing many near-identical giant-JSON documents.
func TestFetchDocumentsStopsPagingOnNullValueEnvelope(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			pages++
			skip, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria.$skip"))
			if skip == 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"count": 1, "value": []map[string]any{
					{"commitId": "c0", "comment": "only commit"},
				}})
				return
			}
			// Trailing empty page, serialized with "value": null rather than [].
			_ = json.NewEncoder(w).Encode(map[string]any{"count": 0, "value": nil})
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(adoChangesResponse{})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url": srv.URL + "/_apis/git/repositories/repo/commits",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1 (the null-value page must not become a bogus document)", len(docs))
	}
	if pages != 2 {
		t.Errorf("got %d page requests, want exactly 2 (one real page, one empty page that correctly stops pagination)", pages)
	}
}

// TestFetchDocumentsCommitHistoryRespectsTopAcrossPages verifies that a
// configured Top still caps the total fetched even once it spans more than
// one paginated request.
func TestFetchDocumentsCommitHistoryRespectsTopAcrossPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_apis/git/repositories/repo/commits":
			skip, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria.$skip"))
			top, _ := strconv.Atoi(r.URL.Query().Get("searchCriteria.$top"))
			var items []map[string]any
			for i := skip; i < skip+top; i++ {
				items = append(items, map[string]any{"commitId": fmt.Sprintf("c%d", i), "comment": fmt.Sprintf("commit %d", i)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": items})
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(adoChangesResponse{})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
		"ItemsPath":     "value",
		"IdField":       "commitId",
		"TitleField":    "comment",
		"ContentFields": "comment",
		"Top":           "130",
	})}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 130 {
		t.Fatalf("got %d docs, want 130", len(docs))
	}
}

// TestFetchDocumentsCommitHistoryFiltersByBranch verifies that a
// commit-history source scopes ADO's commits-list request to a single
// branch — defaulting to "main" when Branch is unset — rather than walking
// every branch in the repository.
func TestFetchDocumentsCommitHistoryFiltersByBranch(t *testing.T) {
	cases := []struct {
		name       string
		branchCfg  string
		wantBranch string
	}{
		{name: "default", branchCfg: "", wantBranch: "main"},
		{name: "explicit", branchCfg: "release/2.0", wantBranch: "release/2.0"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotBranch string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/_apis/git/repositories/repo/commits":
					gotBranch = r.URL.Query().Get("searchCriteria.itemVersion.version")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"value": []map[string]any{{"commitId": "c1", "comment": "hi"}},
					})
				case strings.Contains(r.URL.Path, "/changes"):
					_ = json.NewEncoder(w).Encode(adoChangesResponse{})
				default:
					http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
				}
			}))
			defer srv.Close()

			cfg := map[string]string{
				"Url":           srv.URL + "/_apis/git/repositories/repo/commits",
				"ItemsPath":     "value",
				"IdField":       "commitId",
				"TitleField":    "comment",
				"ContentFields": "comment",
			}
			if c.branchCfg != "" {
				cfg["Branch"] = c.branchCfg
			}
			s := &APISource{src: commitSrc(cfg)}

			if _, err := s.FetchDocuments(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if gotBranch != c.wantBranch {
				t.Errorf("searchCriteria.itemVersion.version = %q, want %q", gotBranch, c.wantBranch)
			}
		})
	}
}
