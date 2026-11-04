package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func TestAdoWorkItemTypes(t *testing.T) {
	var gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotAuth, _ = r.BasicAuth()
		gotVersion = r.URL.Query().Get("api-version")
		switch r.URL.Path {
		case "/org/My Project/_apis/wit/workitemtypes":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"name": "User Story"}, {"name": "Bug"}, {"name": "Test Plan"},
				{"name": "Shared Steps"}, {"name": "Old Type", "isDisabled": true}, {"name": "Risk"},
			}})
		case "/org/My Project/_apis/wit/workitemtypecategories":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{
				{"referenceName": "Microsoft.RequirementCategory", "workItemTypes": []map[string]any{{"name": "User Story"}}},
				{"referenceName": "Microsoft.HiddenCategory", "workItemTypes": []map[string]any{{"name": "Test Plan"}, {"name": "Shared Steps"}}},
			}})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	src := &models.SourceDefinition{Config: map[string]string{
		"AdoOrg": srv.URL + "/org/", "AdoProject": "My Project", "AdoApiVersion": "6.0",
		"AuthType": "basic", "Password": "pat",
	}}
	got, err := AdoWorkItemTypes(context.Background(), src, fakeSecrets{"pat": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Bug", "Risk", "User Story"}; !reflect.DeepEqual(got, want) {
		t.Errorf("types = %v, want %v", got, want)
	}
	if gotAuth != "secret" || gotVersion != "6.0" {
		t.Errorf("auth = %q, api-version = %q", gotAuth, gotVersion)
	}
}

func TestAdoWorkItemTypesRequiresOrgAndProject(t *testing.T) {
	_, err := AdoWorkItemTypes(context.Background(), &models.SourceDefinition{Config: map[string]string{"AdoOrg": "https://dev.azure.com/x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Errorf("err = %v, want a missing-project error", err)
	}
}
