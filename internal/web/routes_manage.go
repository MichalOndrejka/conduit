// Management handlers: source CRUD, sync control, credentials, export, and
// the PCA vector map. Write-side port of app/web/routes.py for the generic
// source model.
package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// sourceTypes drives the type <select> in the source form: generic domain
// categories that route to Qdrant collections (not provider-specific).
var sourceTypes = []struct{ Value, Label string }{
	{models.SourceWorkItemQuery, "Work Items"},
	{models.SourceRequirements, "Requirements"},
	{models.SourceTestCase, "Test Cases"},
	{models.SourceCodeRepo, "Source Code"},
	{models.SourceTestCodeRepo, "Test Code"},
	{models.SourcePipelineBuild, "Build Results"},
	{models.SourceDocumentation, "Documentation"},
	{models.SourceTestResults, "Test Results"},
	{models.SourceGitCommits, "Git Commits"},
}

// apiConfigKeys are the generic API source's config fields, read 1:1 from the
// form. Credential fields (Token, Password, ApiKeyValue) store credential
// *names*, never secret values.
var apiConfigKeys = []string{
	"Url", "HttpMethod", "Body", "Headers", "AuthType",
	"Token", "Username", "Password", "ApiKeyHeader", "ApiKeyValue",
	"ItemsPath", "IdField", "TitleField", "ContentFields",
	"NextUrlPath", "Top", "VerifySSL",
}

// ── Source CRUD ─────────────────────────────────────────────────────────────

func (s *Server) renderSourceForm(w http.ResponseWriter, src *models.SourceDefinition, errMsg string) {
	creds := s.secrets.ListAll()
	s.render(w, s.sourceFormTmpl, "base", map[string]any{
		"Active":      "sources",
		"Source":      src,
		"IsNew":       src.ID == "",
		"Error":       errMsg,
		"SourceTypes": sourceTypes,
		"Credentials": creds,
	})
}

func (s *Server) handleSourceCreateGet(w http.ResponseWriter, _ *http.Request) {
	s.renderSourceForm(w, &models.SourceDefinition{Config: map[string]string{}}, "")
}

func sourceFromForm(r *http.Request, existing *models.SourceDefinition) *models.SourceDefinition {
	src := existing
	if src == nil {
		src = &models.SourceDefinition{
			ID:         uuid.NewString(),
			SyncStatus: "idle",
		}
	}
	src.Name = strings.TrimSpace(r.FormValue("name"))
	src.Type = r.FormValue("type")
	cfg := map[string]string{"Provider": r.FormValue("provider")}
	if cfg["Provider"] == "manual" {
		cfg["Title"] = strings.TrimSpace(r.FormValue("Title"))
		content := r.FormValue("Content")
		// Keep existing uploaded content if the edit form leaves it blank.
		if content == "" && existing != nil {
			content = existing.GetConfig("Content")
		}
		cfg["Content"] = content
	} else {
		cfg["Provider"] = "api"
		for _, key := range apiConfigKeys {
			if v := strings.TrimSpace(r.FormValue(key)); v != "" {
				cfg[key] = v
			}
		}
	}
	src.Config = cfg
	return src
}

func (s *Server) handleSourceCreatePost(w http.ResponseWriter, r *http.Request) {
	src := sourceFromForm(r, nil)
	if src.Name == "" {
		s.renderSourceForm(w, src, "Name is required.")
		return
	}
	if err := s.sources.Save(*src); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSourceEditGet(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	if src == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.renderSourceForm(w, src, "")
}

func (s *Server) handleSourceEditPost(w http.ResponseWriter, r *http.Request) {
	existing, err := s.sources.Get(r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	if existing == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	// Capture before sourceFromForm mutates existing in place.
	oldCollection := sources.CollectionFor(existing)
	src := sourceFromForm(r, existing)
	if src.Name == "" {
		s.renderSourceForm(w, src, "Name is required.")
		return
	}
	// A type/provider change moves the source to a different collection —
	// clean up the old one, or its vectors would keep surfacing in the old
	// search tool forever (re-sync only replaces within the new collection).
	if newCollection := sources.CollectionFor(src); newCollection != oldCollection {
		filter := &rag.Filter{Must: []rag.FieldCondition{{
			Key: models.TagKey("source_id"), Match: rag.Match{Value: src.ID},
		}}}
		if err := s.vectors.DeleteByFilter(r.Context(), oldCollection, filter); err != nil {
			log.Printf("warning: could not clean up vectors in %s after collection change: %v", oldCollection, err)
		}
		src.SyncStatus = "idle" // old data gone; needs a fresh sync
		src.LastSyncedAt = nil
	}
	if err := s.sources.Save(*src); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleSourceDelete removes the source and its vectors, mirroring
// sources_delete_post in app/web/routes.py.
func (s *Server) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	if src != nil {
		collection := sources.CollectionFor(src)
		filter := &rag.Filter{Must: []rag.FieldCondition{{
			Key: models.TagKey("source_id"), Match: rag.Match{Value: src.ID},
		}}}
		// Best-effort vector cleanup — the source record is removed either way.
		_ = s.vectors.DeleteByFilter(r.Context(), collection, filter)
		if err := s.sources.Delete(src.ID); err != nil {
			httpError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ── Sync control ────────────────────────────────────────────────────────────

func (s *Server) handleSyncOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	go s.sync.Sync(context.Background(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	go s.sync.SyncAll(context.Background())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSyncPause(w http.ResponseWriter, r *http.Request) {
	s.sync.Control().Pause(r.PathValue("id"))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSyncResume(w http.ResponseWriter, r *http.Request) {
	s.sync.Control().Resume(r.PathValue("id"))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSyncCancel(w http.ResponseWriter, r *http.Request) {
	s.sync.Control().RequestCancel(r.PathValue("id"))
	writeJSON(w, map[string]bool{"ok": true})
}

// ── Export ──────────────────────────────────────────────────────────────────

func (s *Server) handleExport(w http.ResponseWriter, _ *http.Request) {
	stripped, err := s.sources.ExportStripped()
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="conduit-sources.json"`)
	writeJSON(w, stripped)
}

// ── Credentials ─────────────────────────────────────────────────────────────

func (s *Server) renderCredentials(w http.ResponseWriter, errMsg string) {
	creds := s.secrets.ListAll()
	srcs, _ := s.sources.ListAll()
	type credView struct {
		models.CredentialInfo
		UsedBy []string
	}
	views := make([]credView, 0, len(creds))
	for _, c := range creds {
		views = append(views, credView{
			CredentialInfo: c,
			UsedBy:         s.secrets.SourcesUsing(c.Name, srcs),
		})
	}
	s.render(w, s.credsTmpl, "base", map[string]any{
		"Active":      "credentials",
		"Credentials": views,
		"Error":       errMsg,
	})
}

func (s *Server) handleCredentials(w http.ResponseWriter, _ *http.Request) {
	s.renderCredentials(w, "")
}

func (s *Server) handleCredentialCreate(w http.ResponseWriter, r *http.Request) {
	err := s.secrets.Create(r.FormValue("name"), r.FormValue("note"), r.FormValue("value"))
	if err != nil {
		s.renderCredentials(w, err.Error())
		return
	}
	http.Redirect(w, r, "/credentials", http.StatusSeeOther)
}

func (s *Server) handleCredentialEdit(w http.ResponseWriter, r *http.Request) {
	oldName := r.PathValue("name")
	newName := r.FormValue("name")
	renamed, err := s.secrets.Update(oldName, newName, r.FormValue("note"), r.FormValue("value"))
	if err != nil {
		s.renderCredentials(w, err.Error())
		return
	}
	if renamed != "" && renamed != newName {
		// Cascade the rename into source configs, like the Python app.
		if err := s.sources.RenameCredentialReferences(renamed, strings.TrimSpace(newName)); err != nil {
			httpError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/credentials", http.StatusSeeOther)
}

func (s *Server) handleCredentialDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.secrets.Delete(r.PathValue("name")); err != nil {
		s.renderCredentials(w, err.Error())
		return
	}
	http.Redirect(w, r, "/credentials", http.StatusSeeOther)
}

// ── Vector map (PCA) ────────────────────────────────────────────────────────

func (s *Server) handleMap(w http.ResponseWriter, _ *http.Request) {
	s.render(w, s.mapTmpl, "base", map[string]any{"Active": "map"})
}

// handleMapData ports /api/map-data: samples vectors per completed source,
// projects them to 2D with PCA (UMAP intentionally dropped — no Go impl).
func (s *Server) handleMapData(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.sources.ListAll()
	if err != nil {
		httpError(w, err)
		return
	}

	type mapPoint struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Source string  `json:"source"`
		Title  string  `json:"title"`
	}
	var vectors [][]float32
	var meta []mapPoint

	const samplesPerSource = 200
	for i := range srcs {
		src := &srcs[i]
		if src.SyncStatus != "completed" {
			continue
		}
		collection := sources.CollectionFor(src)
		filter := &rag.Filter{Must: []rag.FieldCondition{{
			Key: models.TagKey("source_id"), Match: rag.Match{Value: src.ID},
		}}}
		points, _, err := s.vectors.Scroll(r.Context(), collection, filter, samplesPerSource, nil, true)
		if err != nil {
			continue
		}
		for _, p := range points {
			vec := decodeVector(p.Vector)
			if vec == nil {
				continue
			}
			title, _ := p.Payload[models.PropKey("title")].(string)
			if title == "" {
				if text, ok := p.Payload[models.PayloadText].(string); ok {
					title = firstLine(text, 80)
				}
			}
			vectors = append(vectors, vec)
			meta = append(meta, mapPoint{Source: src.Name, Title: title})
		}
	}

	coords := rag.PCA2D(vectors)
	for i := range meta {
		meta[i].X = coords[i][0]
		meta[i].Y = coords[i][1]
	}
	writeJSON(w, map[string]any{"points": meta, "method": "pca"})
}

func firstLine(text string, maxLen int) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > maxLen {
		text = text[:maxLen] + "…"
	}
	return text
}

// decodeVector handles both Qdrant vector encodings: a plain array, or a
// named-vector object (take the first entry), mirroring get_all_with_vectors
// in app/memory/service.py.
func decodeVector(raw json.RawMessage) []float32 {
	if len(raw) == 0 {
		return nil
	}
	var vec []float32
	if err := json.Unmarshal(raw, &vec); err == nil {
		return vec
	}
	var named map[string][]float32
	if err := json.Unmarshal(raw, &named); err == nil {
		for _, v := range named {
			return v
		}
	}
	return nil
}
