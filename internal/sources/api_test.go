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

type fakeSecrets map[string]string

func (f fakeSecrets) GetValue(name string) string { return f[name] }

func src(cfg map[string]string) *models.SourceDefinition {
	return &models.SourceDefinition{ID: "src-1", Name: "Test Source", Type: "workitem", Config: cfg}
}

func TestFetchMapsItemsToDocuments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{
				{"id": 101, "title": "Login bug", "state": "Active", "severity": "High"},
				{"id": 102, "title": "Crash on save", "state": "New", "severity": "Critical"},
			},
		})
	}))
	defer srv.Close()

	s := &APISource{src: src(map[string]string{
		"Url":       srv.URL,
		"ItemsPath": "value",
		"IdField":   "id",
	}), secrets: nil}

	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs", len(docs))
	}
	if docs[0].ID != "src-1_capi_101" {
		t.Errorf("stable ID from IdField not used: %q", docs[0].ID)
	}
	if docs[0].Properties["title"] != "Login bug" {
		t.Errorf("title = %q", docs[0].Properties["title"])
	}
	if !strings.Contains(docs[0].Text, "severity: High") {
		t.Errorf("content fields missing: %q", docs[0].Text)
	}
	if docs[0].Tags["source_id"] != "src-1" || docs[0].Tags["source_name"] != "Test Source" {
		t.Errorf("tags = %v", docs[0].Tags)
	}
}

func TestContentFieldsSelection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"title": "T", "body": "keep me", "noise": "drop me"},
		})
	}))
	defer srv.Close()

	s := &APISource{src: src(map[string]string{
		"Url":           srv.URL,
		"ContentFields": "body",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docs[0].Text, "keep me") || strings.Contains(docs[0].Text, "drop me") {
		t.Errorf("ContentFields not honored: %q", docs[0].Text)
	}
}

func TestAuthSchemes(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	secrets := fakeSecrets{"my-pat": "pat-value", "my-key": "key-value"}

	// Basic with empty username (the ADO PAT pattern)
	s := &APISource{src: src(map[string]string{
		"Url": srv.URL, "AuthType": "basic", "Password": "my-pat",
	}), secrets: secrets}
	if _, err := s.FetchDocuments(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if auth := got.Get("Authorization"); !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("basic auth header = %q", auth)
	}

	// API key header
	s = &APISource{src: src(map[string]string{
		"Url": srv.URL, "AuthType": "apikey", "ApiKeyValue": "my-key",
	}), secrets: secrets}
	if _, err := s.FetchDocuments(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Api-Key") != "key-value" {
		t.Errorf("X-Api-Key = %q", got.Get("X-Api-Key"))
	}

	// Extra headers
	s = &APISource{src: src(map[string]string{
		"Url": srv.URL, "Headers": "X-Custom: hello\nX-Other: world",
	})}
	if _, err := s.FetchDocuments(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got.Get("X-Custom") != "hello" || got.Get("X-Other") != "world" {
		t.Errorf("extra headers not applied")
	}
}

func TestPaginationFollowsNextUrl(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"title": "B"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"title": "A"}},
			"next":  srv.URL + "?page=2",
		})
	}))
	defer srv.Close()

	s := &APISource{src: src(map[string]string{
		"Url": srv.URL, "ItemsPath": "items", "NextUrlPath": "next",
	})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[1].Properties["title"] != "B" {
		t.Errorf("pagination broken: %d docs", len(docs))
	}
}

func TestTopLimitsItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]any, 10)
		for i := range items {
			items[i] = map[string]any{"title": "x"}
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	s := &APISource{src: src(map[string]string{"Url": srv.URL, "Top": "3"})}
	docs, err := s.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Errorf("Top not enforced: %d docs", len(docs))
	}
}

func TestHTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	s := &APISource{src: src(map[string]string{"Url": srv.URL})}
	if _, err := s.FetchDocuments(context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "403") {
		t.Errorf("expected HTTP 403 error, got %v", err)
	}
}

func TestManualSource(t *testing.T) {
	m := &ManualSource{src: src(map[string]string{
		"Provider": "manual", "Title": "Spec", "Content": "The content.",
	})}
	docs, err := m.FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Text != "The content." {
		t.Errorf("docs = %+v", docs)
	}

	// Export placeholder must yield no documents.
	m = &ManualSource{src: src(map[string]string{
		"Provider": "manual", "Content": models.DocumentPlaceholder,
	})}
	docs, _ = m.FetchDocuments(context.Background(), nil)
	if len(docs) != 0 {
		t.Error("placeholder content produced documents")
	}
}

func TestFactorySelectsProvider(t *testing.T) {
	if s, _ := New(src(map[string]string{"Provider": "manual"}), nil); s == nil {
		t.Error("manual provider not created")
	} else if _, ok := s.(*ManualSource); !ok {
		t.Error("wrong type for manual provider")
	}
	if s, _ := New(src(map[string]string{}), nil); s == nil {
		t.Error("default provider not created")
	} else if _, ok := s.(*APISource); !ok {
		t.Error("wrong type for default provider")
	}
	// Legacy "custom" provider from Python-era configs maps to the API source.
	if s, _ := New(src(map[string]string{"Provider": "custom"}), nil); s == nil {
		t.Error("legacy custom provider not created")
	}
	if _, err := New(src(map[string]string{"Provider": "ado"}), nil); err == nil {
		t.Error("unknown provider should error")
	}
}
