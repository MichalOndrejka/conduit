package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func daysAgo(n int) string { return time.Now().UTC().AddDate(0, 0, -n).Format(time.RFC3339) }

func TestFetchAdoPullRequests(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	repo := "/acme/Shop/_apis/git/repositories/web"
	pr := func(id int, created string) map[string]any {
		return map[string]any{"pullRequestId": id, "title": "Add retry to checkout", "status": "completed",
			"description": "Retries payment calls.", "creationDate": created,
			"createdBy":     map[string]any{"displayName": "Ann", "uniqueName": "ann@corp"},
			"sourceRefName": "refs/heads/feature/retry", "targetRefName": "refs/heads/main",
			"reviewers": []any{map[string]any{"displayName": "Bob", "uniqueName": "bob@corp", "vote": 10, "isRequired": true}}}
	}
	mux.HandleFunc(repo+"/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("searchCriteria.status"); got != "all" {
			t.Errorf("status %q, want all", got)
		}
		// PR 5 is outside the 30-day window and must not be fetched.
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{pr(9, daysAgo(2)), pr(5, daysAgo(60))}})
	})
	mux.HandleFunc(repo+"/pullRequests/9", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pr(9, daysAgo(2)))
	})
	mux.HandleFunc(repo+"/pullRequests/9/workitems", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": "77"}}})
	})
	mux.HandleFunc("/acme/_apis/wit/workitems", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": 77,
			"fields": map[string]string{"System.Title": "Checkout fails on timeout", "System.State": "Done", "System.WorkItemType": "Bug"}}}})
	})
	mux.HandleFunc(repo+"/pullRequests/9/commits", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"commitId": "abcdef1234567890",
			"comment": "Retry payment client", "author": map[string]any{"name": "Ann", "email": "ann@corp", "date": daysAgo(2)}}}})
	})
	mux.HandleFunc(repo+"/pullRequests/9/iterations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": 1,
			"sourceRefCommit": map[string]any{"commitId": "h"}, "commonRefCommit": map[string]any{"commitId": "b"}}}})
	})
	mux.HandleFunc(repo+"/pullRequests/9/iterations/1/changes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"changeEntries": []any{map[string]any{"changeType": "edit",
			"item": map[string]any{"path": "/src/payment.go", "objectId": "n", "originalObjectId": "o"}}}})
	})
	mux.HandleFunc(repo+"/pullRequests/9/threads", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"id": 1, "status": "fixed",
			"threadContext": map[string]any{"filePath": "/src/payment.go", "rightFileStart": map[string]any{"line": 12}},
			"comments": []any{
				map[string]any{"id": 1, "author": map[string]any{"displayName": "Bob"}, "content": "Use exponential backoff here."},
				map[string]any{"id": 2, "parentCommentId": 1, "author": map[string]any{"displayName": "Ann"}, "content": "Done."},
				map[string]any{"id": 3, "commentType": "system", "content": "Ann voted"},
			}}}})
	})

	src := &models.SourceDefinition{ID: "prs", Name: "Web PRs", Type: models.SourcePullRequest, Config: map[string]string{
		"Provider": "api", "Platform": "ado", "AuthType": "basic", "Password": "pat",
		"AdoOrg": srv.URL + "/acme", "AdoProject": "Shop", "AdoRepo": "web", "Days": "30"}}
	s, err := New(src, fakeSecrets{"pat": "x"})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d documents, want 1 (PR 5 is outside the window)", len(docs))
	}
	d := docs[0]
	for _, want := range []string{
		"Pull request !9: Add retry to checkout", "Status: completed", "Target branch: main",
		"Bob <bob@corp>: approved (required)", "#77 [Bug] Checkout fails on timeout (Done)",
		"Retries payment calls.", "Retry payment client", "edit /src/payment.go",
		"Thread on /src/payment.go:12 [fixed]", "Use exponential backoff here.", "↳ Ann",
	} {
		if !strings.Contains(d.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, d.Text)
		}
	}
	if strings.Contains(d.Text, "Ann voted") {
		t.Error("system comments must not be embedded")
	}
	if d.ID != "prs_pr_9" || d.Properties["web_url"] != srv.URL+"/acme/Shop/_git/web/pullrequest/9" {
		t.Errorf("id %s, web_url %s", d.ID, d.Properties["web_url"])
	}
	f := d.Filters.Fields
	for field, want := range map[string]string{
		models.FilterRepository: "web", models.FilterState: "completed", models.FilterBranch: "main",
		models.FilterAuthor: "ann@corp", models.FilterReviewer: "bob@corp",
	} {
		if !slices.Contains(f[field], want) {
			t.Errorf("filter %s = %v, want %q", field, f[field], want)
		}
	}
	if d.Filters.Date.IsZero() {
		t.Error("creation date filter missing")
	}
	if CollectionFor(src) != models.CollectionPullRequests {
		t.Errorf("collection %s", CollectionFor(src))
	}
}

func TestFetchGitHubPullRequests(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	api := "/api/v3/repos/acme/app"
	pr := func(n int, created string) map[string]any {
		return map[string]any{"number": n, "title": "Fix login redirect", "body": "Fixes #3", "state": "closed",
			"merged_at": daysAgo(1), "user": map[string]any{"login": "ann"}, "created_at": created,
			"html_url": srv.URL + "/acme/app/pull/4",
			"head":     map[string]any{"ref": "fix/login", "sha": "h"}, "base": map[string]any{"ref": "main", "sha": "b"}}
	}
	listed := 0
	mux.HandleFunc(api+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		listed++
		if q := r.URL.Query(); q.Get("state") != "all" || q.Get("sort") != "created" || q.Get("direction") != "desc" {
			t.Errorf("list query %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]any{pr(4, daysAgo(1)), pr(2, daysAgo(90))})
	})
	mux.HandleFunc(api+"/pulls/4", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pr(4, daysAgo(1)))
	})
	mux.HandleFunc(api+"/pulls/4/reviews", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"id": 1, "user": map[string]any{"login": "bob"}, "state": "APPROVED", "body": "LGTM"}})
	})
	mux.HandleFunc(api+"/issues/3", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "Login loops", "state": "closed"})
	})
	mux.HandleFunc(api+"/pulls/4/commits", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"sha": "c1", "commit": map[string]any{"message": "Keep return URL",
			"author": map[string]any{"name": "Ann", "date": daysAgo(1)}}}})
	})
	mux.HandleFunc(api+"/pulls/4/files", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"filename": "auth/login.go", "status": "modified"}})
	})
	mux.HandleFunc(api+"/pulls/4/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"id": 10, "path": "auth/login.go", "line": 5,
			"user": map[string]any{"login": "bob"}, "body": "Validate the return host."}})
	})
	mux.HandleFunc(api+"/issues/4/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	// Checks go stale in an index, so they must not be read.
	mux.HandleFunc(api+"/commits/h/check-runs", func(w http.ResponseWriter, r *http.Request) { t.Error("checks were fetched") })

	src := &models.SourceDefinition{ID: "gh", Name: "App PRs", Type: models.SourcePullRequest, Config: map[string]string{
		"Provider": "github", "GitHubUrl": srv.URL, "GitHubOwner": "acme", "GitHubRepo": "app", "Days": "30"}}
	s, err := New(src, fakeSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || listed != 1 {
		t.Fatalf("got %d documents from %d list pages, want 1 from 1 (listing stops at the window)", len(docs), listed)
	}
	for _, want := range []string{
		"Pull request #4: Fix login redirect", "Status: completed", "bob: approved", "#3 [issue] Login loops (closed)",
		"Keep return URL", "modified auth/login.go", "Thread on auth/login.go:5", "Validate the return host.", "LGTM",
	} {
		if !strings.Contains(docs[0].Text, want) {
			t.Errorf("text lacks %q:\n%s", want, docs[0].Text)
		}
	}
	if f := docs[0].Filters.Fields; !slices.Contains(f[models.FilterProject], "acme") || !slices.Contains(f[models.FilterReviewer], "bob") {
		t.Errorf("filters %v", f)
	}
}
