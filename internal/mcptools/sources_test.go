package mcptools

import (
	"context"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// fakeManager is an in-memory SourceManager that records Save/Delete calls.
type fakeManager struct {
	byID      map[string]models.SourceDefinition
	order     []string
	creds     []string
	savedFrom string // oldCollection passed to the last Save
	deleted   []string
}

func newFakeManager(creds ...string) *fakeManager {
	m := &fakeManager{byID: map[string]models.SourceDefinition{}, creds: creds}
	m.put(models.SourceDefinition{ID: "wi", Name: "Work items", Type: models.SourceWorkItemQuery, SyncStatus: "completed",
		Config: map[string]string{"Provider": "api", "Url": "https://ado.example/_apis/wit/wiql", "AuthType": "basic", "Password": "ado-pat"}})
	m.put(models.SourceDefinition{ID: "notes", Name: "Notes", Type: models.SourceDocumentation,
		Config: map[string]string{"Provider": "manual", "Title": "Notes", "Content": "long pasted text"}})
	return m
}

func (m *fakeManager) put(src models.SourceDefinition) {
	if _, ok := m.byID[src.ID]; !ok {
		m.order = append(m.order, src.ID)
	}
	m.byID[src.ID] = src
}

func (m *fakeManager) ListAll() ([]models.SourceDefinition, error) {
	out := []models.SourceDefinition{}
	for _, id := range m.order {
		if src, ok := m.byID[id]; ok {
			out = append(out, src)
		}
	}
	return out, nil
}

func (m *fakeManager) Get(id string) (*models.SourceDefinition, error) {
	src, ok := m.byID[id]
	if !ok {
		return nil, nil
	}
	src.Config = copyConfig(src.Config)
	return &src, nil
}

func (m *fakeManager) Save(_ context.Context, oldCollection string, src *models.SourceDefinition) error {
	m.savedFrom = oldCollection
	m.put(*src)
	return nil
}

func (m *fakeManager) Delete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	delete(m.byID, id)
	return nil
}

func (m *fakeManager) CredentialNames() []string { return m.creds }

func (m *fakeManager) IndexedChunks(context.Context, *models.SourceDefinition) int { return 42 }

func manage(t *testing.T, m *fakeManager, runner *fakeRunner, req map[string]any) (map[string]any, error) {
	t.Helper()
	return handleManageRequest(context.Background(), map[string]any{"request": req}, m, runner)
}

func validCreate() map[string]any {
	return map[string]any{
		"mode": "create", "name": " Bugs ", "type": models.SourceWorkItemQuery,
		"config": map[string]any{"Url": "https://ado.example/_apis/wit/wiql", "AuthType": "basic", "Password": "ado-pat", "Top": float64(200)},
	}
}

func TestManageListIncludesCredentialNamesAndTypes(t *testing.T) {
	out, err := manage(t, newFakeManager("ado-pat", "jira"), newFakeRunner(), map[string]any{"mode": "list"})
	if err != nil {
		t.Fatal(err)
	}
	srcs := out["sources"].([]sourceSummary)
	if len(srcs) != 2 || srcs[0].ID != "wi" || srcs[1].Provider != "manual" || srcs[1].SyncStatus != "idle" {
		t.Errorf("sources = %+v", srcs)
	}
	if creds := out["credentials"].([]string); strings.Join(creds, ",") != "ado-pat,jira" {
		t.Errorf("credentials = %v", creds)
	}
	if types := out["types"].([]map[string]string); len(types) == 0 || types[0]["type"] == "" {
		t.Errorf("types = %v", types)
	}
}

func TestManageListCredentialsNeverNil(t *testing.T) {
	out, _ := manage(t, newFakeManager(), newFakeRunner(), map[string]any{"mode": "list"})
	if creds := out["credentials"].([]string); creds == nil {
		t.Error("credentials must be [] not null when none are stored")
	}
}

func TestManageGetStripsManualContent(t *testing.T) {
	m := newFakeManager()
	out, err := manage(t, m, newFakeRunner(), map[string]any{"mode": "get", "source_id": "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if got := out["source"].(sourceView).Config["Content"]; got != models.DocumentPlaceholder {
		t.Errorf("Content = %q, want placeholder", got)
	}
	if _, err := manage(t, m, newFakeRunner(), map[string]any{"mode": "get", "source_id": "nope"}); err == nil {
		t.Error("expected an error for an unknown id")
	}
}

func TestManageCreateSavesAPISource(t *testing.T) {
	m := newFakeManager("ado-pat")
	out, err := manage(t, m, newFakeRunner(), validCreate())
	if err != nil {
		t.Fatal(err)
	}
	v := out["source"].(sourceView)
	saved := m.byID[v.ID]
	if v.ID == "" || saved.Name != "Bugs" || saved.SyncStatus != "idle" || m.savedFrom != "" {
		t.Errorf("saved = %+v (oldCollection %q)", saved, m.savedFrom)
	}
	if saved.Config["Provider"] != "api" || saved.Config["Top"] != "200" || saved.Config["Password"] != "ado-pat" {
		t.Errorf("config = %v", saved.Config)
	}
}

func TestManageCreateRejectsSecretValueInCredentialField(t *testing.T) {
	m := newFakeManager("ado-pat")
	req := validCreate()
	req["config"].(map[string]any)["Password"] = "s3cr3t-token-value"
	_, err := manage(t, m, newFakeRunner(), req)
	if err == nil {
		t.Fatal("expected a credential that isn't stored to be rejected")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error echoes the value back: %v", err)
	}
	if !strings.Contains(err.Error(), "ado-pat") {
		t.Errorf("error should list stored credential names: %v", err)
	}
	if len(m.byID) != 2 {
		t.Error("nothing must be saved")
	}
}

func TestManageCreateRejectsBadInput(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unknown key":  func(r map[string]any) { r["config"].(map[string]any)["Provider"] = "manual" },
		"bad url":      func(r map[string]any) { r["config"].(map[string]any)["Url"] = "ftp://x" },
		"bad type":     func(r map[string]any) { r["type"] = "jira" },
		"missing name": func(r map[string]any) { delete(r, "name") },
		"non-string":   func(r map[string]any) { r["config"].(map[string]any)["Headers"] = []any{"a"} },
	}
	for name, mutate := range cases {
		m := newFakeManager("ado-pat")
		req := validCreate()
		mutate(req)
		if _, err := manage(t, m, newFakeRunner(), req); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if len(m.byID) != 2 {
			t.Errorf("%s: nothing must be saved", name)
		}
	}
}

func TestManageUpdateMergesConfigAndPassesOldCollection(t *testing.T) {
	m := newFakeManager("ado-pat")
	_, err := manage(t, m, newFakeRunner(), map[string]any{
		"mode": "update", "source_id": "wi", "type": models.SourceRequirements,
		"config": map[string]any{"Top": "50", "AuthType": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := m.byID["wi"]
	if got.Type != models.SourceRequirements || got.Config["Top"] != "50" || got.Config["Url"] == "" {
		t.Errorf("updated = %+v", got)
	}
	if _, ok := got.Config["AuthType"]; ok {
		t.Error(`"" should remove the key`)
	}
	if want := sources.CollectionFor(&models.SourceDefinition{Type: models.SourceWorkItemQuery}); m.savedFrom != want {
		t.Errorf("oldCollection = %q, want %q", m.savedFrom, want)
	}
}

// A source whose credential was deleted from the library can still be
// renamed: only credential fields the edit changes are checked.
func TestManageUpdateKeepsUnchangedMissingCredential(t *testing.T) {
	m := newFakeManager() // "ado-pat" not stored
	if _, err := manage(t, m, newFakeRunner(), map[string]any{"mode": "update", "source_id": "wi", "name": "WI"}); err != nil {
		t.Fatalf("rename rejected: %v", err)
	}
	if m.byID["wi"].Name != "WI" {
		t.Error("rename not saved")
	}
}

func TestManageUpdateRefusesManualAndSyncing(t *testing.T) {
	m := newFakeManager("ado-pat")
	if _, err := manage(t, m, newFakeRunner(), map[string]any{"mode": "update", "source_id": "notes", "name": "x"}); err == nil {
		t.Error("expected manual sources to be refused")
	}
	runner := newFakeRunner()
	runner.syncing["wi"] = true
	if _, err := manage(t, m, runner, map[string]any{"mode": "update", "source_id": "wi", "name": "x"}); err == nil {
		t.Error("expected a syncing source to be refused")
	}
	if m.byID["wi"].Name != "Work items" {
		t.Error("nothing must be saved")
	}
}

func TestManageDeleteNeedsConfirm(t *testing.T) {
	m := newFakeManager()
	out, err := manage(t, m, newFakeRunner(), map[string]any{"mode": "delete", "source_id": "wi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.deleted) != 0 {
		t.Fatal("deleted without confirm")
	}
	if w := out["would_delete"].(map[string]any); w["indexed_chunks"] != 42 || w["name"] != "Work items" {
		t.Errorf("would_delete = %v", w)
	}
	if _, err := manage(t, m, newFakeRunner(), map[string]any{"mode": "delete", "source_id": "wi", "confirm": true}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.deleted, ",") != "wi" {
		t.Errorf("deleted = %v", m.deleted)
	}
}

func TestManageDeleteRefusesSyncing(t *testing.T) {
	m := newFakeManager()
	runner := newFakeRunner()
	runner.syncing["wi"] = true
	if _, err := manage(t, m, runner, map[string]any{"mode": "delete", "source_id": "wi", "confirm": true}); err == nil {
		t.Error("expected a syncing source to be refused")
	}
	if len(m.deleted) != 0 {
		t.Error("nothing must be deleted")
	}
}

func TestManageRejectsUnknownMode(t *testing.T) {
	if _, err := manage(t, newFakeManager(), newFakeRunner(), map[string]any{"mode": "drop"}); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}
