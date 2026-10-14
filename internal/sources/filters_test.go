package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestAdoScope(t *testing.T) {
	cases := []struct {
		name         string
		cfg          map[string]string
		url          string
		project, rep string
	}{
		{
			name: "cloud url", url: "https://dev.azure.com/org/Payments/_apis/git/repositories/api/commits",
			project: "Payments", rep: "api",
		},
		{
			name: "on-prem collection url", url: "https://tfs.example.com/tfs/DefaultCollection/My%20Project/_apis/git/repositories/web%20app/items",
			project: "My Project", rep: "web app",
		},
		{
			name:    "preset fields win over a GUID in the url",
			cfg:     map[string]string{"AdoProject": "Payments", "AdoRepo": "api"},
			url:     "https://dev.azure.com/org/Payments/_apis/git/repositories/0f1e2d3c/commits",
			project: "Payments", rep: "api",
		},
		{
			name: "org-level url has no project", url: "https://dev.azure.com/org/_apis/git/repositories/api/commits",
			project: "", rep: "api",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &APISource{src: commitSrc(c.cfg)}
			project, repo := a.adoScope(c.url)
			if project != c.project || repo != c.rep {
				t.Errorf("adoScope = (%q, %q), want (%q, %q)", project, repo, c.project, c.rep)
			}
		})
	}
}

func TestCommitDocumentsCarryFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_apis/git/repositories/api/commits"):
			if skip := r.URL.Query().Get("searchCriteria.$skip"); skip != "" && skip != "0" {
				_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{
				"commitId": "c1", "comment": "Rename login",
				"author": map[string]any{"name": "Jane Doe", "email": "Jane@Example.com", "date": "2024-03-05T10:00:00Z"},
			}}})
		case strings.Contains(r.URL.Path, "/changes"):
			_ = json.NewEncoder(w).Encode(adoChangesResponse{Changes: []adoChange{
				{ChangeType: "rename", Item: adoChangeItem{Path: "/src/Auth/SignIn.cs", ObjectID: "b1", OriginalObjectID: "b1"}, SourceServerItem: "/src/Auth/Login.cs"},
				{ChangeType: "add", Item: adoChangeItem{Path: "/src/Auth", IsFolder: true}},
			}})
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write([]byte("x\n"))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &APISource{src: commitSrc(map[string]string{
		"Url":    srv.URL + "/org/Payments/_apis/git/repositories/api/commits",
		"Branch": "release/2.0",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	f := docs[0].Filters
	want := map[string][]string{
		models.FilterProject:    {"payments"},
		models.FilterRepository: {"api"},
		models.FilterAuthor:     {"jane doe", "jane@example.com"},
		models.FilterPath:       {"src", "src/auth", "src/auth/signin.cs", "src/auth/login.cs"},
	}
	for field, w := range want {
		if got := f.Fields[field]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %v, want %v", field, got, w)
		}
	}
	if !f.Date.Equal(time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v, want 2024-03-05T10:00:00Z", f.Date)
	}
	if docs[0].Properties["project"] != "Payments" || docs[0].Properties["repository"] != "api" {
		t.Errorf("project/repository not shown in properties: %v", docs[0].Properties)
	}
}

func TestRepoFileDocumentsCarryFilters(t *testing.T) {
	a := &APISource{src: codeSrc(map[string]string{"AdoProject": "Payments", "AdoRepo": "api"})}
	url := "https://dev.azure.com/org/Payments/_apis/git/repositories/api/items?versionDescriptor.version=develop"
	docs := a.itemsToDocuments([]any{map[string]any{"path": "/src/Auth/Login.CS", "objectId": "b1"}}, url)
	f := docs[0].Filters
	want := map[string][]string{
		models.FilterProject:    {"payments"},
		models.FilterRepository: {"api"},
		models.FilterPath:       {"src", "src/auth", "src/auth/login.cs"},
		models.FilterExtension:  {"cs"},
	}
	for field, w := range want {
		if got := f.Fields[field]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %v, want %v", field, got, w)
		}
	}
}

func TestWorkItemDocumentsCarryFilters(t *testing.T) {
	a := &APISource{src: src(map[string]string{"AdoOrg": "https://dev.azure.com/org", "AdoProject": "Payments"})}
	docs := a.workItemsToDocuments([]workItemBatchItem{{ID: 7, Fields: map[string]any{
		"System.Title":         "Login fails",
		"System.TeamProject":   "Payments",
		"System.WorkItemType":  "Bug",
		"System.State":         "Active",
		"System.AreaPath":      `Payments\Team A`,
		"System.IterationPath": `Payments\Sprint 3`,
		"System.AssignedTo":    map[string]any{"displayName": "Jane Doe", "uniqueName": "jane@example.com"},
		"System.Tags":          "auth; Regression",
		"System.ChangedDate":   "2024-03-05T10:00:00.123Z",
	}}})
	f := docs[0].Filters
	want := map[string][]string{
		models.FilterProject:       {"payments"},
		models.FilterWorkItemType:  {"bug"},
		models.FilterState:         {"active"},
		models.FilterIterationPath: {"payments", `payments\sprint 3`},
		models.FilterAssignedTo:    {"jane doe", "jane@example.com"},
		models.FilterTag:           {"auth", "regression"},
	}
	for field, w := range want {
		if got := f.Fields[field]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %v, want %v", field, got, w)
		}
	}
	if _, ok := f.Fields["area_path"]; ok {
		t.Error("work item area path must not be stored as a filter (the source is already scoped to it)")
	}
	if !f.Date.IsZero() {
		t.Errorf("work item date must not be stored as a filter, got %v", f.Date)
	}
}
