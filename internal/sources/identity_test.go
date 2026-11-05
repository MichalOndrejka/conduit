package sources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// The token owner comes from connectionData, so a "me" filter can resolve
// from the cache later.
func TestAdoFetchIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/_apis/connectionData" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticatedUser": map[string]any{
			"providerDisplayName": "Ann Lee",
			"properties":          map[string]any{"Account": map[string]any{"$value": "ann@corp"}},
		}})
	}))
	defer srv.Close()
	a := &APISource{src: &models.SourceDefinition{Config: map[string]string{"AdoOrg": srv.URL + "/org/"}}}
	ids, err := a.FetchIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"Ann Lee", "ann@corp"}) {
		t.Errorf("identity = %v", ids)
	}
	// A generic API source has no organization and no identity.
	if ids, err := (&APISource{src: &models.SourceDefinition{}}).FetchIdentity(context.Background()); err != nil || ids != nil {
		t.Errorf("generic source identity = %v, %v", ids, err)
	}
}

func TestGitHubFetchIdentity(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "ann", "name": "Ann Lee", "email": nil})
	})
	ids, err := f.source(t, models.SourceGitCommits, nil).FetchIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"ann", "Ann Lee"}) {
		t.Errorf("identity = %v", ids)
	}
}
