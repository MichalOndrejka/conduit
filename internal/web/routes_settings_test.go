package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
)

// ── Save: embedding ─────────────────────────────────────────────────────────

func TestHandleSettingsEmbeddingInvalidInputRedirectsWithNotice(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
	}{
		{"missing dimensions", url.Values{"max_input_tokens": {"8192"}}},
		{"zero dimensions", url.Values{"dimensions": {"0"}, "max_input_tokens": {"8192"}}},
		{"non-numeric dimensions", url.Values{"dimensions": {"abc"}, "max_input_tokens": {"8192"}}},
		{"missing max_input_tokens", url.Values{"dimensions": {"3"}}},
		{"zero max_input_tokens", url.Values{"dimensions": {"3"}, "max_input_tokens": {"0"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			resp := h.postForm("/settings/embedding", c.form)
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != "/settings?notice=embedding_invalid" {
				t.Errorf("Location = %q, want embedding_invalid notice", loc)
			}
		})
	}
}

func TestHandleSettingsEmbeddingSavesUnchangedConfigKeepsCollections(t *testing.T) {
	h := newHarness(t)
	h.qd.mu.Lock()
	h.qd.collections[models.CollectionWorkItems] = true
	h.qd.mu.Unlock()

	resp := h.postForm("/settings/embedding", url.Values{
		"model": {h.cfg.Embedding.Model}, "base_url": {h.cfg.Embedding.BaseURL},
		"dimensions": {"3"}, "max_input_tokens": {"8192"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/settings?notice=embedding_saved" {
		t.Errorf("Location = %q, want embedding_saved (config unchanged)", loc)
	}
	h.qd.mu.Lock()
	defer h.qd.mu.Unlock()
	if len(h.qd.deleteCollCalls) != 0 {
		t.Errorf("deleteCollCalls = %v, want none for an unchanged embedding config", h.qd.deleteCollCalls)
	}
}

func TestHandleSettingsEmbeddingModelChangeDropsCollectionsAndFlagsReindex(t *testing.T) {
	h := newHarness(t)
	mustSave(t, h, models.SourceDefinition{ID: "s1", Name: "A", SyncStatus: "completed"})
	h.qd.mu.Lock()
	h.qd.collections[models.CollectionWorkItems] = true
	h.qd.mu.Unlock()

	resp := h.postForm("/settings/embedding", url.Values{
		"model": {"a-different-model"}, "base_url": {h.cfg.Embedding.BaseURL},
		"dimensions": {"3"}, "max_input_tokens": {"8192"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/settings?notice=embedding_saved_dropped" {
		t.Errorf("Location = %q, want embedding_saved_dropped", loc)
	}
	if h.cfg.Embedding.Model != "a-different-model" {
		t.Errorf("cfg.Embedding.Model = %q, want updated to the new model", h.cfg.Embedding.Model)
	}
	got, err := h.sources.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncStatus != "needs-reindex" {
		t.Errorf("SyncStatus = %q, want needs-reindex after a model change", got.SyncStatus)
	}

	h.qd.mu.Lock()
	defer h.qd.mu.Unlock()
	found := false
	for _, c := range h.qd.deleteCollCalls {
		if c == models.CollectionWorkItems {
			found = true
		}
	}
	if !found {
		t.Errorf("deleteCollCalls = %v, want the stale work-items collection dropped", h.qd.deleteCollCalls)
	}
	if len(h.qd.createCalls) == 0 {
		t.Error("expected the experience collection to be recreated after a model change")
	}
}

func TestHandleSettingsEmbeddingSaveFailureReturns500(t *testing.T) {
	h := newHarness(t)
	// Point config.Save at a directory that doesn't exist, so os.WriteFile
	// fails and the handler's httpError path runs.
	t.Setenv("CONDUIT_CONFIG", filepath.Join(t.TempDir(), "no-such-dir", "config.json"))

	resp := h.postForm("/settings/embedding", url.Values{
		"model": {"m"}, "base_url": {"http://example.invalid"},
		"dimensions": {"3"}, "max_input_tokens": {"8192"},
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

// ── Save: Qdrant ────────────────────────────────────────────────────────────

func TestHandleSettingsQdrantInvalidInputRedirectsWithNotice(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
	}{
		{"missing url", url.Values{}},
		{"blank url", url.Values{"qdrant_url": {"   "}}},
		{"malformed url", url.Values{"qdrant_url": {"://not-a-url"}}},
		{"no host", url.Values{"qdrant_url": {"http://"}}},
		{"unsupported scheme", url.Values{"qdrant_url": {"ftp://localhost:6333"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			resp := h.postForm("/settings/qdrant", c.form)
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != "/settings?notice=qdrant_invalid" {
				t.Errorf("Location = %q, want qdrant_invalid notice", loc)
			}
		})
	}
}

func TestHandleSettingsQdrantSavesValidConfig(t *testing.T) {
	h := newHarness(t)
	resp := h.postForm("/settings/qdrant", url.Values{
		"qdrant_url": {"https://new-host:7000"}, "qdrant_api_key": {"secret-key"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", resp.StatusCode, bodyString(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/settings?notice=qdrant_saved" {
		t.Errorf("Location = %q, want qdrant_saved", loc)
	}
	want := config.QdrantConfig{URL: "https://new-host:7000", APIKey: "secret-key"}
	if h.cfg.Qdrant != want {
		t.Errorf("cfg.Qdrant = %+v, want %+v", h.cfg.Qdrant, want)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Qdrant != want {
		t.Errorf("reloaded cfg.Qdrant = %+v, want persisted %+v", reloaded.Qdrant, want)
	}
}

func TestHandleSettingsQdrantSaveFailureReturns500(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CONDUIT_CONFIG", filepath.Join(t.TempDir(), "no-such-dir", "config.json"))

	resp := h.postForm("/settings/qdrant", url.Values{
		"qdrant_url": {"http://localhost:6333"},
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

// ── Verify ──────────────────────────────────────────────────────────────────

func verifyResult(t *testing.T, resp *http.Response) (ok bool, message string) {
	t.Helper()
	var out struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(bodyString(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	return out.Ok, out.Message
}

func TestHandleSettingsVerifyEmbeddingSuccess(t *testing.T) {
	h := newHarness(t)
	resp := h.postMultipart("/settings/verify/embedding", url.Values{
		"model": {"test-model"}, "base_url": {h.cfg.Embedding.BaseURL},
		"dimensions": {"3"}, "max_input_tokens": {"8192"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ok, msg := verifyResult(t, resp)
	if !ok {
		t.Errorf("ok = false, message = %q, want a successful verify", msg)
	}
	if !strings.Contains(msg, "3-dim") {
		t.Errorf("message = %q, want it to mention the vector dimension", msg)
	}
}

func TestHandleSettingsVerifyEmbeddingFailureReportsError(t *testing.T) {
	h := newHarness(t)
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(badSrv.Close)

	resp := h.postMultipart("/settings/verify/embedding", url.Values{
		"model": {"test-model"}, "base_url": {badSrv.URL},
		"dimensions": {"3"}, "max_input_tokens": {"8192"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors reported in the JSON body)", resp.StatusCode)
	}
	ok, msg := verifyResult(t, resp)
	if ok {
		t.Errorf("ok = true, want false for an unreachable/erroring embedding endpoint")
	}
	if msg == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestHandleSettingsVerifyQdrantSuccess(t *testing.T) {
	h := newHarness(t)
	resp := h.postMultipart("/settings/verify/qdrant", url.Values{
		"qdrant_url": {h.cfg.Qdrant.URL},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ok, msg := verifyResult(t, resp)
	if !ok {
		t.Errorf("ok = false, message = %q, want a successful verify against the fake Qdrant", msg)
	}
	if !strings.Contains(msg, "Connected") {
		t.Errorf("message = %q, want a Connected message", msg)
	}
}

func TestHandleSettingsVerifyQdrantFailureReportsError(t *testing.T) {
	h := newHarness(t)
	resp := h.postMultipart("/settings/verify/qdrant", url.Values{
		"qdrant_url": {"http://127.0.0.1:1"}, // reserved port, connection refused
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors reported in the JSON body)", resp.StatusCode)
	}
	ok, msg := verifyResult(t, resp)
	if ok {
		t.Errorf("ok = true, want false for an unreachable Qdrant")
	}
	if msg == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestHandleSettingsVerifyPreprocessingSuccess(t *testing.T) {
	h := newHarness(t)
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	t.Cleanup(chatSrv.Close)

	resp := h.postMultipart("/settings/verify/preprocessing", url.Values{
		"model": {"chat-model"}, "base_url": {chatSrv.URL},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ok, msg := verifyResult(t, resp)
	if !ok {
		t.Errorf("ok = false, message = %q, want a successful verify", msg)
	}
	if !strings.Contains(msg, "reachable") {
		t.Errorf("message = %q, want a reachable message", msg)
	}
}

func TestHandleSettingsVerifyPreprocessingMissingModelFails(t *testing.T) {
	h := newHarness(t)
	resp := h.postMultipart("/settings/verify/preprocessing", url.Values{
		"base_url": {"http://example.invalid"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors reported in the JSON body)", resp.StatusCode)
	}
	ok, msg := verifyResult(t, resp)
	if ok {
		t.Errorf("ok = true, want false when no model is configured")
	}
	if !strings.Contains(msg, "model is required") {
		t.Errorf("message = %q, want a model-is-required error", msg)
	}
}

func TestHandleSettingsVerifyUnknownServiceReturns404(t *testing.T) {
	h := newHarness(t)
	resp := h.postMultipart("/settings/verify/not-a-real-service", url.Values{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
