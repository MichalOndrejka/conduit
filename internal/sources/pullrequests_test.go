package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func adoSrc(id string, disabled bool, cfg map[string]string) models.SourceDefinition {
	full := map[string]string{"Provider": "api", "AuthType": "basic", "Password": "pat"}
	for k, v := range cfg {
		full[k] = v
	}
	return models.SourceDefinition{ID: id, Name: id, Type: models.SourceGitCommits, Disabled: disabled, Config: full}
}

func TestResolvePullRequestURL_AdoPrefersSameRepo(t *testing.T) {
	all := []models.SourceDefinition{
		adoSrc("org-only", false, map[string]string{"AdoOrg": "https://dev.azure.com/acme", "AdoProject": "Other"}),
		adoSrc("same-repo-disabled", true, map[string]string{
			"Url": "https://dev.azure.com/acme/Shop/_apis/git/repositories/web/commits?api-version=7.1"}),
		adoSrc("same-repo", false, map[string]string{
			"Url": "https://dev.azure.com/acme/Shop/_apis/git/repositories/web/commits?api-version=7.1"}),
		adoSrc("other-org", false, map[string]string{"AdoOrg": "https://dev.azure.com/zzz", "AdoProject": "Shop"}),
	}
	pr, err := ResolvePullRequestURL("https://dev.azure.com/acme/Shop/_git/web/pullrequest/12?_a=files", all, fakeSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if pr.ID != 12 || pr.Source.ID != "same-repo" {
		t.Fatalf("got id %d from %s, want 12 from same-repo", pr.ID, pr.Source.ID)
	}
	if got := pr.Provider.WebURL(12); got != "https://dev.azure.com/acme/Shop/_git/web/pullrequest/12" {
		t.Errorf("web url %s", got)
	}

	// A disabled source of the same repository is still closer than an
	// enabled one of another project.
	pr, err = ResolvePullRequestURL("https://dev.azure.com/acme/Shop/_git/web/pullrequest/12", all[:2], fakeSecrets{})
	if err != nil || pr.Source.ID != "same-repo-disabled" {
		t.Fatalf("got %v, %v; want same-repo-disabled", pr, err)
	}
}

func TestAdoPR_IdentityByEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tfs/Coll/_apis/identities" || r.URL.Query().Get("filterValue") != "ann@corp" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "ann-guid"}}})
	}))
	t.Cleanup(srv.Close)
	s := adoSrc("ado", false, nil)
	c := newAdoPRClient(&s, fakeSecrets{}, srv.URL+"/tfs/Coll", "P", "r")
	if id, err := c.identityID(context.Background(), "ann@corp"); err != nil || id != "ann-guid" {
		t.Fatalf("got %q, %v", id, err)
	}
	if _, err := c.identityID(context.Background(), "nobody@corp"); err == nil {
		t.Fatal("want an error for an unknown identity")
	}
}

func TestResolvePullRequestURL_AdoServerCollectionAndProjectNamedRepo(t *testing.T) {
	all := []models.SourceDefinition{
		adoSrc("tfs", false, map[string]string{"AdoOrg": "https://tfs.corp/tfs/DefaultCollection", "AdoProject": "Billing"}),
	}
	pr, err := ResolvePullRequestURL("https://tfs.corp/tfs/DefaultCollection/_git/Billing/pullrequest/3", all, fakeSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	c := pr.Provider.(*adoPRClient)
	if c.org != "https://tfs.corp/tfs/DefaultCollection" || c.project != "Billing" || c.repo != "Billing" || pr.ID != 3 {
		t.Fatalf("got %s / %s / %s #%d", c.org, c.project, c.repo, pr.ID)
	}
}

func TestResolvePullRequestURL_GitHub(t *testing.T) {
	all := []models.SourceDefinition{
		{ID: "gh-other", Config: map[string]string{"Provider": "github", "GitHubOwner": "acme", "GitHubRepo": "lib"}},
		{ID: "gh", Config: map[string]string{"Provider": "github", "GitHubOwner": "acme", "GitHubRepo": "app"}},
		{ID: "ghe", Config: map[string]string{"Provider": "github", "GitHubUrl": "https://git.corp", "GitHubOwner": "acme", "GitHubRepo": "app"}},
	}
	pr, err := ResolvePullRequestURL("https://github.com/acme/app/pull/45/files", all, fakeSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if pr.ID != 45 || pr.Source.ID != "gh" {
		t.Fatalf("got #%d from %s", pr.ID, pr.Source.ID)
	}
	if _, err := ResolvePullRequestURL("https://gitlab.com/acme/app/-/merge_requests/1", all, fakeSecrets{}); err == nil {
		t.Fatal("want an error for a URL no source matches")
	}
}

func TestPullRequestProviderFor_AdoNeedsRepo(t *testing.T) {
	s := adoSrc("wi", false, map[string]string{"AdoOrg": "https://dev.azure.com/acme", "AdoProject": "Shop"})
	if _, err := PullRequestProviderFor(&s, fakeSecrets{}); err == nil {
		t.Fatal("want an error for a source without a repository")
	}
	s.Config["AdoResource"] = "git/repositories/web/items"
	p, err := PullRequestProviderFor(&s, fakeSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if c := p.(*adoPRClient); c.repo != "web" {
		t.Fatalf("repo %q", c.repo)
	}
}

func TestDiffLineCounts(t *testing.T) {
	diff := "--- a.txt\n+++ a.txt\n@@ -1,3 +1,3 @@\n keep\n---removed dashes\n+added\n+++added pluses\n"
	if a, d := diffLineCounts(diff); a != 2 || d != 1 {
		t.Fatalf("got +%d -%d, want +2 -1", a, d)
	}
}

func TestSelectChanges(t *testing.T) {
	var all []PRChange
	for i := 0; i < PRPageSize+5; i++ {
		all = append(all, PRChange{Path: "/f" + string(rune('a'+i))})
	}
	all[3].OriginalPath = "/old.go"

	page2, more, err := SelectChanges(all, "", 2)
	if err != nil || len(page2) != 5 || more {
		t.Fatalf("page 2: %d files, more=%v, err=%v", len(page2), more, err)
	}
	if _, more, _ := SelectChanges(all, "", 1); !more {
		t.Fatal("page 1 should report more")
	}
	one, _, err := SelectChanges(all, "old.go", 1)
	if err != nil || len(one) != 1 || one[0].Path != all[3].Path {
		t.Fatalf("by original path: %+v, %v", one, err)
	}
	if _, _, err := SelectChanges(all, "missing.go", 1); err == nil {
		t.Fatal("want an error for an unchanged path")
	}
}

// fakeAdoPR serves Azure DevOps at {srv.URL}/org for project Proj, repo
// repo, pull request 7.
func fakeAdoPR(t *testing.T) (*httptest.Server, *adoPRClient) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); !ok || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	js := func(path string, v any) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(v) })
	}
	repo := "/org/Proj/_apis/git/repositories/repo"
	pr := repo + "/pullRequests/7"

	js("GET "+pr, map[string]any{
		"pullRequestId": 7, "title": "Add cart", "description": "Adds the cart", "status": "active",
		"createdBy":     map[string]any{"displayName": "Ann", "uniqueName": "ann@corp"},
		"sourceRefName": "refs/heads/feature/cart", "targetRefName": "refs/heads/main", "mergeStatus": "succeeded",
		"reviewers": []any{
			map[string]any{"displayName": "Bob", "uniqueName": "bob@corp", "vote": 10, "isRequired": true},
			map[string]any{"displayName": "Cy", "uniqueName": "cy@corp", "vote": -5},
		},
		"lastMergeSourceCommit": map[string]any{"commitId": "h2"},
		"lastMergeTargetCommit": map[string]any{"commitId": "t1"},
		"repository":            map[string]any{"project": map[string]any{"id": "pid"}},
	})
	js("GET "+pr+"/workitems", map[string]any{"value": []any{map[string]any{"id": "42"}}})
	js("GET /org/_apis/wit/workitems", map[string]any{"value": []any{map[string]any{"id": 42, "fields": map[string]string{
		"System.Title": "Cart", "System.State": "Active", "System.WorkItemType": "User Story"}}}})
	js("GET "+pr+"/statuses", map[string]any{"value": []any{
		map[string]any{"state": "pending", "context": map[string]any{"name": "lint", "genre": "ci"}},
		map[string]any{"state": "succeeded", "context": map[string]any{"name": "lint", "genre": "ci"}},
	}})
	mux.HandleFunc("GET /org/Proj/_apis/policy/evaluations", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("artifactId"); got != "vstfs:///CodeReview/CodeReviewId/pid/7" {
			t.Errorf("artifactId %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{
			map[string]any{"status": "rejected", "context": map[string]any{"buildId": 99}, "configuration": map[string]any{
				"isEnabled": true, "isBlocking": true, "type": map[string]any{"displayName": "Build"},
				"settings": map[string]any{"displayName": "PR build"}}},
			map[string]any{"status": "approved", "configuration": map[string]any{"isEnabled": false}},
		}})
	})
	js("GET "+pr+"/iterations", map[string]any{"value": []any{
		map[string]any{"id": 1, "sourceRefCommit": map[string]any{"commitId": "h1"}, "commonRefCommit": map[string]any{"commitId": "b1"}},
		map[string]any{"id": 2, "sourceRefCommit": map[string]any{"commitId": "h2"}, "commonRefCommit": map[string]any{"commitId": "b1"},
			"author": map[string]any{"displayName": "Ann"}, "description": "fixups"},
	}})
	mux.HandleFunc("GET "+pr+"/iterations/2/changes", func(w http.ResponseWriter, r *http.Request) {
		entries := []any{
			map[string]any{"changeType": "edit", "item": map[string]any{"path": "/cart.go", "objectId": "new1", "originalObjectId": "old1"}},
			map[string]any{"changeType": "add", "item": map[string]any{"path": "/add.go"}},
			map[string]any{"changeType": "edit", "item": map[string]any{"path": "/dir", "isFolder": true}},
			map[string]any{"changeType": "delete", "item": map[string]any{"path": "/gone.go", "originalObjectId": "old2"}},
		}
		resp := map[string]any{"changeEntries": entries}
		if r.URL.Query().Get("$compareTo") == "1" {
			resp["changeEntries"] = entries[:1]
		}
		if r.URL.Query().Get("$skip") == "0" {
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"changeEntries": []any{}})
	})
	blobs := map[string]string{"old1": "a\nb\nc\n", "new1": "a\nB\nc\nd\n", "old2": "bye\n"}
	mux.HandleFunc("GET "+repo+"/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		text, ok := blobs[r.PathValue("sha")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(text))
	})
	mux.HandleFunc("GET "+repo+"/items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("path") != "/add.go" || q.Get("versionDescriptor.version") != "h2" {
			t.Errorf("items lookup %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": "package add\n"})
	})
	js("GET "+pr+"/threads", map[string]any{"value": []any{
		map[string]any{"id": 1, "status": "active",
			"threadContext": map[string]any{"filePath": "/cart.go", "rightFileStart": map[string]any{"line": 2}},
			"comments": []any{
				map[string]any{"id": 1, "author": map[string]any{"displayName": "Bob"}, "content": "Why B?", "commentType": "text"},
				map[string]any{"id": 2, "parentCommentId": 1, "author": map[string]any{"displayName": "Ann"}, "content": "Typo fix"},
			}},
		map[string]any{"id": 2, "comments": []any{map[string]any{"id": 1, "content": "Bob voted 10", "commentType": "system"}}},
		map[string]any{"id": 3, "isDeleted": true, "comments": []any{map[string]any{"id": 1, "content": "x"}}},
	}})
	mux.HandleFunc("GET "+pr+"/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("continuationToken") == "" {
			w.Header().Set("x-ms-continuationtoken", "next")
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{
				map[string]any{"commitId": "c2", "comment": "Long…", "commentTruncated": true, "author": map[string]any{"name": "Ann", "email": "ann@corp"}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"commitId": "c1", "comment": "Start"}}})
	})
	js("GET "+repo+"/commits/c2", map[string]any{"comment": "Long message, in full"})
	js("GET /org/_apis/connectionData", map[string]any{"authenticatedUser": map[string]any{"id": "me-guid"}})
	mux.HandleFunc("GET "+repo+"/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("searchCriteria.reviewerId") != "me-guid" || q.Get("searchCriteria.status") != "active" ||
			q.Get("searchCriteria.targetRefName") != "refs/heads/main" || q.Get("$top") != "21" || q.Get("$skip") != "20" {
			t.Errorf("list query %s", r.URL.RawQuery)
		}
		var value []any
		for i := 0; i < PRPageSize+1; i++ {
			value = append(value, map[string]any{"pullRequestId": 100 + i, "title": "t"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": value})
	})

	s := adoSrc("ado", false, map[string]string{"AdoOrg": srv.URL + "/org", "AdoProject": "Proj", "AdoRepo": "repo"})
	pr7, err := ResolvePullRequestURL(srv.URL+"/org/Proj/_git/repo/pullrequest/7", []models.SourceDefinition{s}, fakeSecrets{"pat": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, pr7.Provider.(*adoPRClient)
}

func TestAdoPR_Get(t *testing.T) {
	_, c := fakeAdoPR(t)
	pr, err := c.Get(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Author != "Ann <ann@corp>" || pr.SourceBranch != "feature/cart" || pr.TargetBranch != "main" || pr.HeadCommit != "h2" {
		t.Errorf("summary %+v", pr.PRSummary)
	}
	if len(pr.Reviewers) != 2 || pr.Reviewers[0].Vote != "approved" || !pr.Reviewers[0].Required || pr.Reviewers[1].Vote != "waiting_for_author" {
		t.Errorf("reviewers %+v", pr.Reviewers)
	}
	if len(pr.WorkItems) != 1 || pr.WorkItems[0].Title != "Cart" || !strings.HasSuffix(pr.WorkItems[0].WebURL, "/org/Proj/_workitems/edit/42") {
		t.Errorf("work items %+v", pr.WorkItems)
	}
	if len(pr.Checks) != 2 || pr.Checks[0].Name != "ci/lint" || pr.Checks[0].State != "succeeded" ||
		pr.Checks[1].Name != "Build: PR build" || !pr.Checks[1].Blocking || !strings.HasSuffix(pr.Checks[1].WebURL, "buildId=99") {
		t.Errorf("checks %+v", pr.Checks)
	}
	if len(pr.Notes) != 0 {
		t.Errorf("notes %v", pr.Notes)
	}
}

func TestAdoPR_ChangesAndDiff(t *testing.T) {
	_, c := fakeAdoPR(t)
	ctx := context.Background()
	changes, err := c.Changes(ctx, 7, PRRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 3 || changes.HeadCommit != "h2" {
		t.Fatalf("changes %+v", changes)
	}
	files, err := c.Diff(ctx, changes.Files, true)
	if err != nil {
		t.Fatal(err)
	}
	edit, add, del := files[0], files[1], files[2]
	if edit.Additions != 2 || edit.Deletions != 1 || !strings.Contains(edit.Diff, "+B\n") || edit.Content == nil || *edit.Content != "a\nB\nc\nd\n" {
		t.Errorf("edit %+v", edit)
	}
	if add.Additions != 1 || add.Deletions != 0 || !strings.Contains(add.Diff, "+package add") {
		t.Errorf("add %+v", add)
	}
	if del.Deletions != 1 || del.Content != nil {
		t.Errorf("delete %+v", del)
	}

	since, err := c.Changes(ctx, 7, PRRange{From: 1})
	if err != nil || len(since.Files) != 1 || since.Files[0].baseCommit != "h1" {
		t.Fatalf("since iteration 1: %+v, %v", since, err)
	}
	if _, err := c.Changes(ctx, 7, PRRange{From: 2, To: 2}); err == nil {
		t.Fatal("want an error for an empty iteration range")
	}
}

func TestAdoPR_ThreadsCommitsList(t *testing.T) {
	_, c := fakeAdoPR(t)
	ctx := context.Background()

	threads, _, err := c.Threads(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].Kind != "file" || threads[0].Line != 2 || len(threads[0].Comments) != 2 ||
		threads[0].Comments[1].ReplyTo != "1" {
		t.Errorf("threads %+v", threads)
	}

	commits, _, err := c.Commits(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0].Message != "Long message, in full" || commits[0].Author != "Ann <ann@corp>" ||
		!strings.HasSuffix(commits[0].WebURL, "/org/Proj/_git/repo/commit/c2") {
		t.Errorf("commits %+v", commits)
	}

	prs, more, err := c.List(ctx, PRListFilter{Reviewer: "me", TargetBranch: "main", Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != PRPageSize || !more || !strings.HasSuffix(prs[0].WebURL, "/pullrequest/100") {
		t.Errorf("list: %d prs, more=%v", len(prs), more)
	}
}

func TestAdoPR_IdentitiesAPI(t *testing.T) {
	for org, want := range map[string]string{
		"https://dev.azure.com/acme":             "https://vssps.dev.azure.com/acme/_apis/identities",
		"https://acme.visualstudio.com":          "https://acme.vssps.visualstudio.com/_apis/identities",
		"https://tfs.corp/tfs/DefaultCollection": "https://tfs.corp/tfs/DefaultCollection/_apis/identities",
	} {
		if got := (&adoPRClient{org: org}).identitiesAPI(); got != want {
			t.Errorf("%s: got %s, want %s", org, got, want)
		}
	}
}

func githubPR7(t *testing.T, f *fakeGitHub) PullRequestProvider {
	t.Helper()
	s := models.SourceDefinition{ID: "gh", Config: map[string]string{
		"Provider": "github", "GitHubUrl": f.srv.URL, "GitHubOwner": "acme", "GitHubRepo": "app", "Token": "gh"}}
	pr, err := ResolvePullRequestURL(f.srv.URL+"/acme/app/pull/7", []models.SourceDefinition{s}, fakeSecrets{"gh": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return pr.Provider
}

func TestGitHubPR_Get(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("/pulls/7", map[string]any{
		"number": 7, "title": "Cart", "body": "Fixes #3 and closes #3, resolves: #4", "state": "closed", "merged_at": "2026-01-02T00:00:00Z",
		"user": map[string]any{"login": "ann"}, "head": map[string]any{"ref": "cart", "sha": "h2"}, "base": map[string]any{"ref": "main", "sha": "b1"},
		"html_url": f.srv.URL + "/acme/app/pull/7", "requested_reviewers": []any{map[string]any{"login": "dee"}, map[string]any{"login": "cy"}},
	})
	f.handleJSON("/pulls/7/reviews", []any{
		map[string]any{"user": map[string]any{"login": "bob"}, "state": "CHANGES_REQUESTED", "commit_id": "h1"},
		map[string]any{"user": map[string]any{"login": "bob"}, "state": "COMMENTED", "commit_id": "h2"},
		map[string]any{"user": map[string]any{"login": "cy"}, "state": "APPROVED", "commit_id": "h1"},
	})
	f.handleJSON("/issues/3", map[string]any{"title": "Cart missing", "state": "closed"})
	f.HandleFunc("/api/v3/repos/acme/app/issues/4", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	f.handleJSON("/commits/h2/check-runs", map[string]any{"check_runs": []any{
		map[string]any{"name": "test", "status": "completed", "conclusion": "failure"},
		map[string]any{"name": "lint", "status": "in_progress"},
	}})
	f.handleJSON("/commits/h2/status", map[string]any{"statuses": []any{map[string]any{"context": "ci/legacy", "state": "success"}}})

	pr, err := githubPR7(t, f).Get(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Status != PRStatusCompleted || pr.SourceBranch != "cart" || pr.HeadCommit != "h2" {
		t.Errorf("summary %+v", pr.PRSummary)
	}
	votes := map[string]string{}
	for _, r := range pr.Reviewers {
		votes[r.ID] = r.Vote
	}
	if votes["bob"] != "changes_requested" || votes["cy"] != "pending" || votes["dee"] != "pending" || len(votes) != 3 {
		t.Errorf("reviewers %+v", pr.Reviewers)
	}
	if len(pr.WorkItems) != 2 || pr.WorkItems[0].Title != "Cart missing" || pr.WorkItems[1].ID != "4" || len(pr.Notes) != 1 {
		t.Errorf("work items %+v, notes %v", pr.WorkItems, pr.Notes)
	}
	if len(pr.Checks) != 3 || pr.Checks[0].State != "failure" || pr.Checks[1].State != "in_progress" {
		t.Errorf("checks %+v", pr.Checks)
	}
}

func TestGitHubPR_FilesDiffAndIterations(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("/pulls/7", map[string]any{"number": 7, "head": map[string]any{"sha": "h2"}, "base": map[string]any{"sha": "b1"}})
	f.handleJSON("/pulls/7/files", []any{
		map[string]any{"filename": "cart.go", "status": "modified", "additions": 1, "deletions": 1, "patch": "@@ -1 +1 @@\n-a\n+b"},
		map[string]any{"filename": "logo.png", "status": "added", "additions": 0, "deletions": 0},
		map[string]any{"filename": "big.json", "previous_filename": "old.json", "status": "renamed", "additions": 9000, "deletions": 3},
	})
	f.handleJSON("/pulls/7/commits", []any{
		map[string]any{"sha": "h1", "commit": map[string]any{"message": "first\n\nbody", "author": map[string]any{"name": "Ann"}}},
		map[string]any{"sha": "h2", "commit": map[string]any{"message": "second"}},
	})
	f.HandleFunc("/api/v3/repos/acme/app/compare/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/compare/h1...h2") {
			t.Errorf("compare %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{
			map[string]any{"filename": "cart.go", "status": "modified", "additions": 1, "deletions": 0, "patch": "@@ -1 +1,2 @@\n b\n+c"}}})
	})
	f.HandleFunc("/api/v3/repos/acme/app/contents/cart.go", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "h2" || r.Header.Get("Accept") != "application/vnd.github.raw" {
			t.Errorf("contents request %s %s", r.URL.RawQuery, r.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte("b\n"))
	})

	p := githubPR7(t, f)
	ctx := context.Background()
	changes, err := p.Changes(ctx, 7, PRRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 3 || changes.Files[2].OriginalPath != "old.json" || len(changes.Incomplete) != 0 {
		t.Fatalf("changes %+v", changes)
	}
	files, err := p.Diff(ctx, changes.Files[:1], true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(files[0].Diff, "--- cart.go\n+++ cart.go\n@@") || files[0].Content == nil || *files[0].Content != "b\n" {
		t.Errorf("cart.go %+v", files[0])
	}
	files, err = p.Diff(ctx, changes.Files[1:], false)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Incomplete != "" || files[1].Incomplete == "" || !strings.HasPrefix(files[1].Diff, "--- old.json\n+++ big.json") {
		t.Errorf("no-patch files %+v", files)
	}

	since, err := p.Changes(ctx, 7, PRRange{From: 1})
	if err != nil || len(since.Files) != 1 || since.HeadCommit != "h2" {
		t.Fatalf("since iteration 1: %+v, %v", since, err)
	}
	iters, err := p.Iterations(ctx, 7)
	if err != nil || len(iters) != 2 || iters[0].Description != "first" || iters[1].HeadCommit != "h2" {
		t.Fatalf("iterations %+v, %v", iters, err)
	}
}

func TestGitHubPR_ThreadsAndList(t *testing.T) {
	f := newFakeGitHub(t)
	line := 5
	f.handleJSON("/pulls/7/comments", []any{
		map[string]any{"id": 10, "path": "cart.go", "line": line, "user": map[string]any{"login": "bob"}, "body": "nit", "created_at": "2026-01-01T10:00:00Z"},
		map[string]any{"id": 11, "in_reply_to_id": 10, "path": "cart.go", "line": line, "user": map[string]any{"login": "ann"}, "body": "done", "created_at": "2026-01-01T11:00:00Z"},
		map[string]any{"id": 12, "path": "old.go", "original_line": 3, "user": map[string]any{"login": "bob"}, "body": "stale", "created_at": "2026-01-01T09:00:00Z"},
	})
	f.handleJSON("/pulls/7/reviews", []any{
		map[string]any{"id": 20, "user": map[string]any{"login": "bob"}, "state": "CHANGES_REQUESTED", "body": "See nits", "submitted_at": "2026-01-01T12:00:00Z"},
		map[string]any{"id": 21, "user": map[string]any{"login": "cy"}, "state": "APPROVED", "body": ""},
	})
	f.handleJSON("/issues/7/comments", []any{
		map[string]any{"id": 30, "user": map[string]any{"login": "dee"}, "body": "Ship it", "created_at": "2026-01-01T08:00:00Z"},
	})
	f.HandleFunc("/api/v3/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "repo:acme/app is:pr is:closed is:unmerged author:@me review-requested:bob base:main" || q.Get("page") != "1" {
			t.Errorf("search %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 21, "items": []any{
			map[string]any{"number": 5, "title": "Old", "state": "closed", "user": map[string]any{"login": "me"}, "pull_request": map[string]any{}}}})
	})

	p := githubPR7(t, f)
	ctx := context.Background()
	threads, notes, err := p.Threads(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, th := range threads {
		kinds = append(kinds, th.Kind+":"+th.ID)
	}
	if strings.Join(kinds, ",") != "general:comment-30,file:12,file:10,review:review-20" {
		t.Errorf("threads %v", kinds)
	}
	if !threads[1].Outdated || threads[1].Line != 3 || len(threads[2].Comments) != 2 || threads[3].Status != "changes_requested" || len(notes) != 1 {
		t.Errorf("thread details %+v", threads)
	}

	prs, more, err := p.List(ctx, PRListFilter{Status: PRStatusAbandoned, Author: "me", Reviewer: "bob", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Status != PRStatusAbandoned || !more {
		t.Errorf("list %+v more=%v", prs, more)
	}
	if _, _, err := p.List(ctx, PRListFilter{Author: "ann@corp"}); err == nil {
		t.Error("want an error for an email filter on GitHub")
	}
}

// A pull-request source covers its project — every repository through the
// project endpoint — or the repositories it lists, merged newest first; a
// named repository must be in scope.
func TestAdoPR_ProjectScopedListing(t *testing.T) {
	var paths []string
	pr := func(id int, repo, created string) map[string]any {
		return map[string]any{"pullRequestId": id, "title": "PR " + repo, "status": "active", "creationDate": created,
			"repository": map[string]any{"name": repo}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var value []any
		switch r.URL.Path {
		case "/acme/Shop/_apis/git/pullrequests":
			value = []any{pr(3, "web", "2026-11-03T00:00:00Z"), pr(2, "api", "2026-11-02T00:00:00Z")}
		case "/acme/Shop/_apis/git/repositories/api/pullrequests":
			value = []any{pr(5, "api", "2026-11-05T00:00:00Z"), pr(2, "api", "2026-11-02T00:00:00Z")}
		case "/acme/Shop/_apis/git/repositories/web/pullrequests":
			value = []any{pr(3, "web", "2026-11-03T00:00:00Z")}
		default:
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"value": value})
	}))
	defer srv.Close()
	src := models.SourceDefinition{ID: "prs", Name: "PRs", Type: models.SourcePullRequest, Config: map[string]string{
		"Provider": "api", "AdoOrg": srv.URL + "/acme", "AdoProject": "Shop", "Url": srv.URL + "/acme/Shop"}}
	list := func(f PRListFilter) []PRSummary {
		t.Helper()
		p, err := PullRequestProviderFor(&src, fakeSecrets{})
		if err != nil {
			t.Fatal(err)
		}
		prs, _, err := p.List(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		return prs
	}

	prs := list(PRListFilter{})
	if len(prs) != 2 || paths[0] != "/acme/Shop/_apis/git/pullrequests" || prs[0].Repository != "web" ||
		prs[0].WebURL != srv.URL+"/acme/Shop/_git/web/pullrequest/3" {
		t.Fatalf("project-wide = %+v via %v", prs, paths)
	}
	paths = nil
	if prs := list(PRListFilter{Repository: "api"}); len(prs) != 2 || paths[0] != "/acme/Shop/_apis/git/repositories/api/pullrequests" {
		t.Errorf("one repository = %+v via %v", prs, paths)
	}

	src.Config["Repositories"] = "api,web"
	if prs := list(PRListFilter{}); len(prs) != 3 || prs[0].ID != 5 || prs[1].ID != 3 || prs[2].ID != 2 {
		t.Errorf("merged = %+v, want 5, 3, 2 (newest first)", prs)
	}
	p, _ := PullRequestProviderFor(&src, fakeSecrets{})
	if _, _, err := p.List(context.Background(), PRListFilter{Repository: "infra"}); err == nil || !strings.Contains(err.Error(), "not in this source's scope") {
		t.Errorf("out-of-scope repository: err = %v", err)
	}
}
