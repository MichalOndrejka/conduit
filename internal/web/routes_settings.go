// Settings handlers: MCP info, embedding/Qdrant/preprocessing config
// (persisted to config.json), service verification, and the danger zone.
//
// Connection-affecting changes (embedding, Qdrant) are written to config.json
// and take effect immediately — VectorStore and EmbeddingService both read
// the shared *config.AppConfig live on every call rather than capturing
// values at construction time, so no restart is needed. The one exception:
// if the same field is also set via environment variable, config.Load()
// re-applies that env var on top of config.json on every process start, so
// the env var wins again after any restart until it's unset (see
// docs/configuration.md's "Environment variables" section).
package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/config"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// preprocSourceTypes drives the per-source-type toggles in the preprocessing
// form, matching source_type_labels in app/templates/settings.html.
var preprocSourceTypes = []struct{ Key, Label string }{
	{"work-item", "Work Items"},
	{"requirements", "Requirements"},
	{"test-case", "Test Cases"},
	{"commit-history", "Commit History"},
	{"code", "Source Code"},
	{"test-code", "Test Code"},
	{"documentation", "Documentation"},
}

// ── Page ────────────────────────────────────────────────────────────────────

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	type preprocTypeView struct {
		Label, FieldName string
		Checked          bool
	}
	types := make([]preprocTypeView, 0, len(preprocSourceTypes))
	for _, t := range preprocSourceTypes {
		checked, ok := s.cfg.Preprocessing.SourceTypes[t.Key]
		if !ok {
			checked = true // default-on when absent, like the Python template
		}
		types = append(types, preprocTypeView{
			Label:     t.Label,
			FieldName: "source_type_" + strings.ReplaceAll(t.Key, "-", "_"),
			Checked:   checked,
		})
	}
	s.render(w, s.settingsTmpl, "base", map[string]any{
		"Active":       "settings",
		"Cfg":          s.cfg,
		"ConfigPath":   config.Path(),
		"Credentials":  s.secrets.ListAll(),
		"PreprocTypes": types,
		"Notice":       r.URL.Query().Get("notice"),
	})
}

// ── Save: embedding ─────────────────────────────────────────────────────────

func (s *Server) handleSettingsEmbedding(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	dims, dimsOK := atoiStrict(r.FormValue("dimensions"))
	maxTokens, maxTokensOK := atoiStrict(r.FormValue("max_input_tokens"))
	if !dimsOK || dims <= 0 || !maxTokensOK || maxTokens <= 0 {
		http.Redirect(w, r, "/settings?notice=embedding_invalid", http.StatusSeeOther)
		return
	}
	old := s.cfg.Embedding
	ec := embeddingFromForm(r)
	ec.Concurrency = old.Concurrency // no form field — env-var only, preserve it

	// A change to anything that affects vector shape or the model itself means
	// existing vectors are stale — drop the collections and flag for reindex.
	changed := old.Provider != ec.Provider || old.Model != ec.Model ||
		old.Dimensions != ec.Dimensions || old.BaseURL != ec.BaseURL ||
		old.AzureEndpoint != ec.AzureEndpoint || old.AzureDeployment != ec.AzureDeployment

	s.cfg.Embedding = ec
	if err := config.Save(s.cfg); err != nil {
		httpError(w, err)
		return
	}

	notice := "embedding_saved"
	if changed {
		for _, col := range models.AllCollections {
			if s.vectors.CollectionExists(r.Context(), col) {
				_ = s.vectors.DeleteCollection(r.Context(), col)
			}
		}
		// Keep the experience collection usable (memory upserts don't create it).
		s.recreateExperience(r.Context())
		_ = s.sources.ResetAllSyncStatus("needs-reindex")
		notice = "embedding_saved_dropped"
	}
	http.Redirect(w, r, "/settings?notice="+notice, http.StatusSeeOther)
}

// ── Save: Qdrant ────────────────────────────────────────────────────────────

func (s *Server) handleSettingsQdrant(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	host := strings.TrimSpace(r.FormValue("qdrant_host"))
	port, portOK := atoiStrict(r.FormValue("qdrant_port"))
	if host == "" || !portOK || port < 1 || port > 65535 {
		http.Redirect(w, r, "/settings?notice=qdrant_invalid", http.StatusSeeOther)
		return
	}
	s.cfg.Qdrant = config.QdrantConfig{
		Host:   host,
		Port:   port,
		HTTPS:  r.FormValue("qdrant_https") == "on",
		APIKey: r.FormValue("qdrant_api_key"),
	}
	if err := config.Save(s.cfg); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/settings?notice=qdrant_saved", http.StatusSeeOther)
}

// ── Save: preprocessing ─────────────────────────────────────────────────────

func (s *Server) handleSettingsPreprocessing(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	pc := preprocessingFromForm(r)
	pc.Concurrency = s.cfg.Preprocessing.Concurrency // no form field — env-var only, preserve it
	s.cfg.Preprocessing = pc
	if err := config.Save(s.cfg); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/settings?notice=preprocessing_saved", http.StatusSeeOther)
}

// ── Verify ──────────────────────────────────────────────────────────────────

func (s *Server) handleSettingsVerify(w http.ResponseWriter, r *http.Request) {
	// The verify forms are POSTed as multipart/form-data (new FormData(form)).
	// ParseForm alone would mark r.Form as populated from the (empty) query
	// string and never read the multipart body, leaving every field blank —
	// use ParseMultipartForm so r.FormValue actually sees the submitted values.
	_ = r.ParseMultipartForm(32 << 20)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	switch r.PathValue("service") {
	case "embedding":
		tmp := &config.AppConfig{Embedding: embeddingFromForm(r)}
		svc := rag.NewEmbeddingService(tmp, s.secrets)
		vec, err := svc.Embed(ctx, "connection test")
		if err != nil {
			writeVerify(w, false, err.Error())
			return
		}
		writeVerify(w, true, fmt.Sprintf("OK — model returned %d-dim vector", len(vec)))
	case "qdrant":
		tmp := &config.AppConfig{Qdrant: config.QdrantConfig{
			Host:   r.FormValue("qdrant_host"),
			Port:   atoiOr(r.FormValue("qdrant_port"), 6333),
			HTTPS:  r.FormValue("qdrant_https") == "on",
			APIKey: r.FormValue("qdrant_api_key"),
		}}
		cols, err := rag.NewVectorStore(tmp).ListCollections(ctx)
		if err != nil {
			writeVerify(w, false, err.Error())
			return
		}
		writeVerify(w, true, fmt.Sprintf("Connected — %d collection(s)", len(cols)))
	case "preprocessing":
		tmp := &config.AppConfig{Preprocessing: preprocessingFromForm(r)}
		msg, err := rag.NewDocumentPreprocessor(tmp, s.secrets).Verify(ctx)
		if err != nil {
			writeVerify(w, false, err.Error())
			return
		}
		writeVerify(w, true, msg)
	default:
		http.NotFound(w, r)
	}
}

// ── Danger zone ─────────────────────────────────────────────────────────────

func (s *Server) handleDeleteAllSources(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.sources.ListAll()
	if err != nil {
		httpError(w, err)
		return
	}
	for i := range srcs {
		src := &srcs[i]
		collection := sources.CollectionFor(src)
		filter := &rag.Filter{Must: []rag.FieldCondition{{
			Key: models.TagKey("source_id"), Match: rag.Match{Value: src.ID},
		}}}
		_ = s.vectors.DeleteByFilter(r.Context(), collection, filter)
		_ = s.sources.Delete(src.ID)
	}
	http.Redirect(w, r, "/settings?notice=sources_deleted", http.StatusSeeOther)
}

func (s *Server) handleDeleteAllExperiences(w http.ResponseWriter, r *http.Request) {
	s.recreateExperience(r.Context())
	http.Redirect(w, r, "/settings?notice=experiences_deleted", http.StatusSeeOther)
}

func (s *Server) handleCleanSourceEmbeddings(w http.ResponseWriter, r *http.Request) {
	for _, col := range models.AllCollections {
		if col == models.CollectionExperience {
			continue
		}
		if s.vectors.CollectionExists(r.Context(), col) {
			_ = s.vectors.DeleteCollection(r.Context(), col)
		}
	}
	_ = s.sources.ResetAllSyncStatus("needs-reindex")
	http.Redirect(w, r, "/settings?notice=source_embeddings_cleaned", http.StatusSeeOther)
}

func (s *Server) handleCleanExperienceEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.recreateExperience(r.Context())
	http.Redirect(w, r, "/settings?notice=experience_embeddings_cleaned", http.StatusSeeOther)
}

// ── helpers ─────────────────────────────────────────────────────────────────

// recreateExperience drops and recreates the experience collection so the
// memory store stays usable (its upserts don't auto-create the collection).
func (s *Server) recreateExperience(ctx context.Context) {
	_ = s.vectors.DeleteCollection(ctx, models.CollectionExperience)
	_ = s.vectors.CreateCollection(ctx, models.CollectionExperience, s.cfg.Embedding.Dimensions)
}

func embeddingFromForm(r *http.Request) config.EmbeddingConfig {
	return config.EmbeddingConfig{
		Provider:              formOr(r, "provider", "openai-compatible"),
		Model:                 r.FormValue("model"),
		BaseURL:               r.FormValue("base_url"),
		Dimensions:            atoiOr(r.FormValue("dimensions"), 768),
		MaxInputTokens:        atoiOr(r.FormValue("max_input_tokens"), 8192),
		AzureEndpoint:         r.FormValue("azure_endpoint"),
		AzureDeployment:       r.FormValue("azure_deployment"),
		AzureAPIVersion:       formOr(r, "azure_api_version", "2024-02-01"),
		AzureAPIKeyCredential: r.FormValue("azure_api_key_credential"),
	}
}

func preprocessingFromForm(r *http.Request) config.PreprocessingConfig {
	return config.PreprocessingConfig{
		Enabled:               r.FormValue("enabled") == "on",
		Provider:              formOr(r, "provider", "openai-compatible"),
		BaseURL:               r.FormValue("base_url"),
		Model:                 r.FormValue("model"),
		SystemPrompt:          r.FormValue("system_prompt"),
		SourceTypes:           sourceTypesFromForm(r),
		AzureEndpoint:         r.FormValue("azure_endpoint"),
		AzureDeployment:       r.FormValue("azure_deployment"),
		AzureAPIVersion:       formOr(r, "azure_api_version", "2024-02-01"),
		AzureAPIKeyCredential: r.FormValue("azure_api_key_credential"),
	}
}

func sourceTypesFromForm(r *http.Request) map[string]bool {
	sourceTypes := make(map[string]bool, len(preprocSourceTypes))
	for _, t := range preprocSourceTypes {
		field := "source_type_" + strings.ReplaceAll(t.Key, "-", "_")
		sourceTypes[t.Key] = r.FormValue(field) == "on"
	}
	return sourceTypes
}

func writeVerify(w http.ResponseWriter, ok bool, msg string) {
	writeJSON(w, map[string]any{"ok": ok, "message": msg})
}

func formOr(r *http.Request, key, def string) string {
	if v := r.FormValue(key); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// atoiStrict parses s as a base-10 integer, returning ok=false for empty or
// non-numeric input (unlike atoiOr, which silently falls back to a default).
func atoiStrict(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil
}
