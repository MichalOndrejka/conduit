// Settings handlers: MCP info, preprocessing config (persisted to
// config.json), and the danger zone.
//
// Embedding and Qdrant connection settings, and the preprocessing LLM
// connection (provider/base URL/model), are container-managed now — set via
// EMBEDDING_*/QDRANT_*/PREPROCESSING_* env vars (see internal/config/config.go)
// rather than edited here. This page only exposes preprocessing knobs with no
// env-var equivalent: which source types get summarized, and the prompt.
package web

import (
	"context"
	"net/http"
	"strings"

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
		"PreprocTypes": types,
		"Notice":       r.URL.Query().Get("notice"),
	})
}

// ── Save: preprocessing ─────────────────────────────────────────────────────
//
// Only SystemPrompt and SourceTypes come from this form — Provider/BaseURL/
// Model/Azure* have no form fields anymore (env-var only), so they're left
// untouched on the existing config rather than overwritten with zero values.

func (s *Server) handleSettingsPreprocessing(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.cfg.Preprocessing.SystemPrompt = r.FormValue("system_prompt")
	s.cfg.Preprocessing.SourceTypes = sourceTypesFromForm(r)
	if err := config.Save(s.cfg); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/settings?notice=preprocessing_saved", http.StatusSeeOther)
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

func sourceTypesFromForm(r *http.Request) map[string]bool {
	sourceTypes := make(map[string]bool, len(preprocSourceTypes))
	for _, t := range preprocSourceTypes {
		field := "source_type_" + strings.ReplaceAll(t.Key, "-", "_")
		sourceTypes[t.Key] = r.FormValue(field) == "on"
	}
	return sourceTypes
}
