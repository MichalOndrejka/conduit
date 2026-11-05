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

// workitemAdoSrc builds a Work Items source on the Azure DevOps preset —
// distinct from src() in api_test.go, which builds a "work-item"-typed source
// without Platform=ado and so keeps using the generic single-URL fetch.
func workitemAdoSrc(orgURL string, cfg map[string]string) *models.SourceDefinition {
	merged := map[string]string{
		"Provider":   "api",
		"Platform":   "ado",
		"AdoOrg":     orgURL,
		"AdoProject": "proj",
		"Url":        orgURL + "/proj",
	}
	for k, v := range cfg {
		merged[k] = v
	}
	return &models.SourceDefinition{ID: "src-1", Name: "Test Source", Type: models.SourceWorkItemQuery, Config: merged}
}

func TestFetchAdoWorkItemsQueriesAndBatches(t *testing.T) {
	var gotWiqlQuery string
	var gotBatchIDs []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			var body struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotWiqlQuery = body.Query
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workItems": []map[string]any{{"id": 27}, {"id": 28}},
			})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			var body struct {
				IDs []int `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotBatchIDs = body.IDs
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": 27, "fields": map[string]any{"System.Title": "Classifier bug", "System.WorkItemType": "Bug"}},
					{"id": 28, "fields": map[string]any{"System.Title": "Add hotdog scan", "System.WorkItemType": "Task"}},
				},
			})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: workitemAdoSrc(srv.URL, map[string]string{"WorkItemTypes": "Bug,Task"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(gotWiqlQuery, "[System.WorkItemType] IN ('Bug', 'Task')") {
		t.Errorf("WIQL query missing type filter: %q", gotWiqlQuery)
	}
	if !strings.Contains(gotWiqlQuery, "[System.TeamProject] = 'proj'") {
		t.Errorf("WIQL query missing project filter: %q", gotWiqlQuery)
	}
	if len(gotBatchIDs) != 2 || gotBatchIDs[0] != 27 || gotBatchIDs[1] != 28 {
		t.Errorf("batch IDs = %v, want [27 28]", gotBatchIDs)
	}

	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	if docs[0].ID != "src-1_capi_27" {
		t.Errorf("doc ID = %q", docs[0].ID)
	}
	if docs[0].Properties["title"] != "Classifier bug" || docs[0].Properties["type"] != "Bug" {
		t.Errorf("doc properties = %v", docs[0].Properties)
	}
}

// TestFetchAdoWorkItemsRequestsAllFields asserts the batch request uses ADO's
// $expand (which returns every field, incl. custom ones) rather than a fixed
// "fields" allowlist that would drop custom project/org fields.
func TestFetchAdoWorkItemsRequestsAllFields(t *testing.T) {
	var gotBatchBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{{"id": 9}}})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			_ = json.NewDecoder(r.Body).Decode(&gotBatchBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{{"id": 9, "fields": map[string]any{"System.Title": "T"}}},
			})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: workitemAdoSrc(srv.URL, nil)}
	if _, err := s.FetchDocuments(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	if _, ok := gotBatchBody["$expand"]; !ok {
		t.Errorf("batch request missing $expand, body = %v", gotBatchBody)
	}
	if _, ok := gotBatchBody["fields"]; ok {
		t.Errorf("batch request should not send a fields allowlist, body = %v", gotBatchBody)
	}
}

// TestFetchAdoWorkItemsEmbedsAllFieldsAndStripsHTML asserts every fetched field
// (including custom ones) is embedded and that HTML rich-text values are reduced
// to plain text.
func TestFetchAdoWorkItemsEmbedsAllFieldsAndStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{{"id": 42}}})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{{"id": 42, "fields": map[string]any{
					"System.Title":        "Custom field item",
					"Custom.BusinessArea":  "Payments",
					"System.Description":   "<div>Alpha&nbsp;Beta</div><br>Gamma",
				}}},
			})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// ContentFields is deliberately set — work items must ignore it and embed all.
	s := &APISource{src: workitemAdoSrc(srv.URL, map[string]string{"ContentFields": "System.Title"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	text := docs[0].Text
	if !strings.Contains(text, "Custom.BusinessArea: Payments") {
		t.Errorf("custom field not embedded: %q", text)
	}
	if strings.Contains(text, "<div>") || strings.Contains(text, "&nbsp;") {
		t.Errorf("HTML not stripped from field value: %q", text)
	}
	if !strings.Contains(text, "Alpha Beta") || !strings.Contains(text, "Gamma") {
		t.Errorf("stripped description text missing: %q", text)
	}
}

func TestStripHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<div>Alpha&nbsp;Beta</div>", "Alpha Beta"},
		{"line1<br>line2", "line1\nline2"},
		{"<p>a</p><p>b</p>", "a\nb"},
		{"plain text", "plain text"},
		{"a &amp; b &lt; c", "a & b < c"},
		{"<ul><li>one</li><li>two</li></ul>", "one\ntwo"},
	}
	for _, c := range cases {
		if got := stripHTML(c.in); got != c.want {
			t.Errorf("stripHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFetchAdoWorkItemsNoTypeFilterWhenUnconfigured(t *testing.T) {
	var gotWiqlQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			var body struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotWiqlQuery = body.Query
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{}})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: workitemAdoSrc(srv.URL, nil)}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotWiqlQuery, "WorkItemType") {
		t.Errorf("unexpected type filter with no WorkItemTypes configured: %q", gotWiqlQuery)
	}
	if len(docs) != 0 {
		t.Errorf("got %d docs, want 0", len(docs))
	}
}

func TestFetchAdoWorkItemsFiltersByAreaPath(t *testing.T) {
	var gotWiqlQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			var body struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotWiqlQuery = body.Query
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{{"id": 5}}})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": 5, "fields": map[string]any{"System.Title": "Fix scan", "System.AreaPath": `proj\Team A`}},
				},
			})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: workitemAdoSrc(srv.URL, map[string]string{"AreaPaths": `proj\Team A, proj\Team B`})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(gotWiqlQuery, `[System.AreaPath] UNDER 'proj\Team A' OR [System.AreaPath] UNDER 'proj\Team B'`) {
		t.Errorf("WIQL query missing area path filter: %q", gotWiqlQuery)
	}
	if len(docs) != 1 || docs[0].Properties["area"] != `proj\Team A` {
		t.Errorf("docs = %+v", docs)
	}
}

func TestFetchAdoWorkItemsRequiresProject(t *testing.T) {
	s := &APISource{src: workitemAdoSrc("https://dev.azure.com/org", map[string]string{"AdoProject": ""})}
	if _, err := s.FetchDocuments(context.Background(), nil); err == nil {
		t.Error("expected an error when AdoProject is missing")
	}
}

func TestNonAdoWorkItemSourceUsesGenericFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": 1, "title": "Custom API work item"}},
		})
	}))
	defer srv.Close()

	s := &APISource{src: &models.SourceDefinition{
		ID: "src-1", Name: "Test Source", Type: models.SourceWorkItemQuery,
		Config: map[string]string{"Url": srv.URL, "ItemsPath": "value", "IdField": "id"},
	}}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Properties["title"] != "Custom API work item" {
		t.Errorf("docs = %+v", docs)
	}
}

// A work item's links and whole discussion are embedded, so the agent can
// follow a parent, a pull request, a commit or a build by id, and sees every
// comment rather than System.History's latest one. Discussions are fetched
// only for items that have comments, across continuation pages.
func TestFetchAdoWorkItemsEmbedsLinksAndDiscussion(t *testing.T) {
	var commentCalls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{{"id": 42}, {"id": 43}}})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": 42, "fields": map[string]any{
					"System.Title": "Login fails", "System.WorkItemType": "Bug", "System.TeamProject": "Payments",
					"System.CommentCount": 3, "System.History": "<div>only the latest</div>",
				}, "relations": []map[string]any{
					{"rel": "System.LinkTypes.Hierarchy-Reverse", "url": "https://ado/_apis/wit/workItems/7", "attributes": map[string]any{"name": "Parent"}},
					{"rel": "System.LinkTypes.Related", "url": "https://ado/_apis/wit/workItems/9", "attributes": map[string]any{"name": "Related", "comment": "same root cause"}},
					{"rel": "ArtifactLink", "url": "vstfs:///Git/PullRequestId/proj%2Frepo%2F45", "attributes": map[string]any{"name": "Pull Request"}},
					{"rel": "ArtifactLink", "url": "vstfs:///Git/Commit/proj%2Frepo%2Fabc123def4567890", "attributes": map[string]any{"name": "Fixed in Commit"}},
					{"rel": "ArtifactLink", "url": "vstfs:///Build/Build/1011", "attributes": map[string]any{"name": "Found in build"}},
					{"rel": "ArtifactLink", "url": "vstfs:///Git/Ref/proj%2Frepo%2FGBfeature%2Flogin", "attributes": map[string]any{"name": "Branch"}},
					{"rel": "Hyperlink", "url": "https://wiki/login"},
				}},
				{"id": 43, "fields": map[string]any{"System.Title": "No comments", "System.CommentCount": 0}},
			}})
		case strings.Contains(r.URL.Path, "/wit/workItems/") && strings.HasSuffix(r.URL.Path, "/comments"):
			commentCalls = append(commentCalls, r.URL.Path)
			if !strings.Contains(r.URL.Path, "/Payments/") || !strings.HasSuffix(r.URL.Query().Get("api-version"), "-preview") {
				http.Error(w, "bad comments request "+r.URL.String(), http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("continuationToken") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"continuationToken": "p2", "comments": []map[string]any{
					{"id": 2, "text": "<p>Repro on Safari</p>", "createdBy": map[string]any{"displayName": "Bo"}, "createdDate": "2026-11-02T10:00:00Z"},
					{"id": 1, "text": "First look", "createdBy": map[string]any{"displayName": "Al"}, "createdDate": "2026-11-01T10:00:00Z"},
				}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": []map[string]any{
				{"id": 3, "text": "Fixed by !45", "createdBy": map[string]any{"displayName": "Cy"}, "createdDate": "2026-11-03T10:00:00Z"},
			}})
		default:
			http.Error(w, "unexpected request: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: workitemAdoSrc(srv.URL, nil)}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(commentCalls) != 2 {
		t.Errorf("comment requests = %v, want two pages for item 42 only", commentCalls)
	}
	text := docs[0].Text
	for _, want := range []string{
		"Links:", "- Parent: #7", "- Related: #9 — same root cause", "- Pull Request: !45",
		"- Fixed in Commit: abc123def4567890", "- Found in build: build 1011", "- Branch: feature/login",
		"- Hyperlink: https://wiki/login",
		"Discussion (3 comments):", "- Al (2026-11-01T10:00:00Z): First look",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("work item text lacks %q:\n%s", want, text)
		}
	}
	// Oldest first, across pages.
	if a, b, c := strings.Index(text, "First look"), strings.Index(text, "Repro on Safari"), strings.Index(text, "Fixed by !45"); !(a < b && b < c) {
		t.Errorf("discussion out of order:\n%s", text)
	}
	if strings.Contains(text, "only the latest") {
		t.Error("System.History is superseded by the discussion")
	}
	if got := docs[0].Filters.Fields[models.FilterID]; len(got) != 1 || got[0] != "42" {
		t.Errorf("id filter = %v", got)
	}
	if strings.Contains(docs[1].Text, "Discussion") || strings.Contains(docs[1].Text, "Links:") {
		t.Errorf("item without comments or links:\n%s", docs[1].Text)
	}
}

// A discussion that can't be fetched fails the sync rather than embedding
// the item without it.
func TestFetchAdoWorkItemsFailsWhenDiscussionUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/wit/wiql"):
			_ = json.NewEncoder(w).Encode(map[string]any{"workItems": []map[string]any{{"id": 42}}})
		case strings.HasSuffix(r.URL.Path, "/wit/workitemsbatch"):
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"id": 42, "fields": map[string]any{"System.Title": "T", "System.CommentCount": 1}},
			}})
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s := &APISource{src: workitemAdoSrc(srv.URL, nil)}
	if _, err := s.FetchDocuments(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "work item 42: fetching discussion") {
		t.Fatalf("err = %v, want the discussion failure", err)
	}
}
