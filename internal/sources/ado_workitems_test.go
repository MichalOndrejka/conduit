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
