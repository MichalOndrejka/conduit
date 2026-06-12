// Manual document source — Go port of app/sources/manual.py.
package sources

import (
	"context"

	"github.com/MichalOndrejka/conduit/internal/models"
)

type ManualSource struct {
	src *models.SourceDefinition
}

func (m *ManualSource) FetchDocuments(_ context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	content := m.src.GetConfig("Content")
	if content == models.DocumentPlaceholder {
		return nil, nil
	}
	title := m.src.GetConfig("Title")
	if title == "" {
		title = m.src.Name
	}
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Loading " + title})
	}
	return []models.SourceDocument{{
		ID:   m.src.ID,
		Text: content,
		Tags: map[string]string{
			"source_id":   m.src.ID,
			"source_name": m.src.Name,
		},
		Properties: map[string]string{"title": title},
	}}, nil
}
