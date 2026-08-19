package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// ── Static ───────────────────────────────────────────────────────────────────

func TestHandleFaviconSVG(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/favicon.svg")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "<svg") {
		t.Error("favicon body does not look like SVG")
	}
}

// ── Source items page ────────────────────────────────────────────────────────

func TestHandleSourceItemsRedirectsWhenMissing(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/sources/does-not-exist/items")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
}

func TestHandleSourceItemsRendersPage(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Items Source", Type: "work-item"})

	h.qd.mu.Lock()
	h.qd.scroll[models.CollectionWorkItems] = []map[string]any{
		{"id": "p1", "payload": map[string]any{
			"text":          "chunk text",
			"source_doc_id": "doc-1",
			"prop_title":    "My Document",
			"total_chunks":  "1",
		}},
	}
	h.qd.count[models.CollectionWorkItems] = 1
	h.qd.mu.Unlock()

	resp := h.get("/sources/s1/items")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "chunk text") {
		t.Error("items page does not show the chunk text")
	}
	if !strings.Contains(body, "My Document") {
		t.Error("items page does not show the document title in the structure panel")
	}
}

// ── Experience page ──────────────────────────────────────────────────────────

func TestHandleExperiencePageRenders(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/experience")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHandleExperienceCreateGetShowsForm(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/experience/create")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHandleExperienceDeleteRedirects(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/experience/some-id/delete", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/experience" {
		t.Errorf("Location = %q, want /experience", loc)
	}
}

// ── Credentials page ─────────────────────────────────────────────────────────

func TestHandleCredentialsPageListsCreatedAndMissing(t *testing.T) {
	h := newHarness(t)
	if err := h.secrets.Create("stored-cred", "value"); err != nil {
		t.Fatal(err)
	}
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "Uses missing cred", Config: map[string]string{
		"Provider": "api", "Password": "missing-cred",
	}})

	resp := h.get("/credentials")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "stored-cred") {
		t.Error("credentials page does not list the stored credential")
	}
	if !strings.Contains(body, "missing-cred") {
		t.Error("credentials page does not flag the missing credential reference")
	}
}

func TestHandleCredentialCreateGetShowsForm(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/credentials/create")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// ── Sync selected (no-op with empty selection) ──────────────────────────────

func TestHandleSyncSelectedRedirects(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/sources/sync-selected", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
}

// ── Settings danger zone: experience embeddings ─────────────────────────────

func TestHandleCleanExperienceEmbeddingsRecreatesCollection(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/settings/clean-experience-embeddings", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.qd.mu.Lock()
	defer h.qd.mu.Unlock()
	if len(h.qd.createCalls) == 0 {
		t.Error("expected the experience collection to be recreated")
	}
}

// ── Map page, experience kind ────────────────────────────────────────────────

func TestHandleMapPageRendersExperienceKind(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/map?kind=experience")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHandleMapDataExperienceKindDecodesNamedVectors(t *testing.T) {
	h := newHarness(t)
	h.qd.mu.Lock()
	h.qd.scroll[models.CollectionExperience] = []map[string]any{
		{"id": "e1", "vector": map[string]any{"default": []float32{0.1, 0.2, 0.3}}, "payload": map[string]any{"text": "a situation"}},
	}
	h.qd.mu.Unlock()

	resp := h.get("/api/map-data?kind=experience")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body := bodyString(t, resp); !strings.Contains(body, "Experience") {
		t.Errorf("expected an Experience point in the response, got %s", body)
	}
}
