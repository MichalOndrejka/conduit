// Management handlers: source delete/enable, sync control, export/import,
// and the PCA vector map. Providers, the browse tree and source settings are
// in routes_providers.go.
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/memory"
	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// handleSourceDelete removes the source and its vectors, mirroring
// sources_delete_post in app/web/routes.py.
func (s *Server) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Delete(r.Context(), r.PathValue("id")); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

// handleSourceToggle flips a source's disabled flag. A disabled source is
// skipped by Sync (see syncsvc.Service.Sync) and greyed out in the list.
func (s *Server) handleSourceToggle(w http.ResponseWriter, r *http.Request) {
	src, err := s.sources.Get(r.PathValue("id"))
	if err != nil {
		httpError(w, err)
		return
	}
	if src == nil {
		http.Redirect(w, r, "/sources", http.StatusFound)
		return
	}
	src.Disabled = !src.Disabled
	if err := s.sources.Save(*src); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

// handleDisableSelected marks each checked source as disabled, for the
// Sources page's bulk "Disable selected" action.
func (s *Server) handleDisableSelected(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	for _, id := range r.Form["ids"] {
		src, err := s.sources.Get(id)
		if err != nil {
			httpError(w, err)
			return
		}
		if src == nil || src.Disabled {
			continue
		}
		src.Disabled = true
		if err := s.sources.Save(*src); err != nil {
			httpError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

// handleDeleteSelected removes each checked source (and its vectors), for the
// Sources page's bulk "Delete selected" action.
func (s *Server) handleDeleteSelected(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	for _, id := range r.Form["ids"] {
		if err := s.mgr.Delete(r.Context(), id); err != nil {
			httpError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
}

// ── Sync control ────────────────────────────────────────────────────────────

// handleSyncSelected syncs each checked source, for the Sources page's bulk
// "Sync selected" action.
func (s *Server) handleSyncSelected(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	ids := r.Form["ids"]
	go s.sync.SyncSelected(context.Background(), ids)
	http.Redirect(w, r, "/sources", http.StatusSeeOther)
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

// ── Vector map (PCA) ────────────────────────────────────────────────────────

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Active":    "sources",
		"Kind":      "sources",
		"BackHref":  "/sources",
		"BackLabel": "Sources",
		"EmptyText": "No completed sources with vectors yet.",
	}
	if r.URL.Query().Get("kind") == "experience" {
		data["Active"] = "experience"
		data["Kind"] = "experience"
		data["BackHref"] = "/experience"
		data["BackLabel"] = "Experience"
		data["EmptyText"] = "No experience entries with vectors yet."
	}
	s.render(w, s.mapTmpl, "base", data)
}

// handleMapData ports /api/map-data: samples vectors per completed source (or,
// with ?kind=experience, the experience collection instead), and projects
// them to 2D with PCA (UMAP intentionally dropped — no Go impl).
func (s *Server) handleMapData(w http.ResponseWriter, r *http.Request) {
	type mapPoint struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Source string  `json:"source"`
		Title  string  `json:"title"`
	}

	if qdrant := s.health.Qdrant(); qdrant.Status != "ready" {
		msg := "unreachable"
		if qdrant.Message != "" {
			msg = qdrant.Message
		}
		writeJSON(w, map[string]any{"points": []mapPoint{}, "qdrant_error": msg})
		return
	}

	var vectors [][]float32
	var meta []mapPoint

	if r.URL.Query().Get("kind") == "experience" {
		const maxSamples = 500
		points, _, err := s.vectors.Scroll(r.Context(), memory.Collection, nil, maxSamples, nil, true)
		if err != nil {
			httpError(w, err)
			return
		}
		for _, p := range points {
			vec := decodeVector(p.Vector)
			if vec == nil {
				continue
			}
			situation, _ := p.Payload[models.PayloadText].(string)
			vectors = append(vectors, vec)
			meta = append(meta, mapPoint{Source: "Experience", Title: firstLine(situation, 80)})
		}
	} else {
		srcs, err := s.sources.ListAll()
		if err != nil {
			httpError(w, err)
			return
		}

		const samplesPerSource = 200
		for i := range srcs {
			src := &srcs[i]
			if src.SyncStatus != "completed" || models.IsLiveType(src.Type) {
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
