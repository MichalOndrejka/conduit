// Package models is the Go port of app/models.py: domain models and the
// payload-key / collection-name constants shared across the RAG pipeline.
package models

import "time"

const DocumentPlaceholder = "__DOCUMENT_REQUIRED__"

// ── Collection names ────────────────────────────────────────────────────────

const (
	CollectionWorkItems     = "conduit_workitems"
	CollectionRequirements  = "conduit_requirements"
	CollectionSourceCode    = "conduit_code"
	CollectionTestCode      = "conduit_testcode"
	CollectionTestCases     = "conduit_testcases"
	CollectionDocumentation = "conduit_documentation"
	CollectionCommits       = "conduit_commits"
	CollectionExperience    = "conduit_experience"
)

// AllCollections mirrors CollectionNames.ALL in app/models.py.
var AllCollections = []string{
	CollectionWorkItems, CollectionRequirements, CollectionSourceCode,
	CollectionTestCode, CollectionTestCases,
	CollectionDocumentation, CollectionCommits,
	CollectionExperience,
	// Legacy collections from the removed Build Results / Test Results
	// source types: no longer written to, but kept here so "Clean source
	// embeddings" can still delete any left over from before the removal.
	"conduit_builds", "conduit_testresults",
}

// ── Source types ─────────────────────────────────────────────────────────────

const (
	SourceWorkItemQuery = "work-item"
	SourceRequirements  = "requirements"
	SourceTestCase      = "test-case"
	SourceCodeRepo      = "code"
	SourceTestCodeRepo  = "test-code"
	SourceDocumentation = "documentation"
	SourceGitCommits    = "commit-history"
)

// ── Core domain models ────────────────────────────────────────────────────────

type CredentialInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SourceDefinition struct {
	ID             string            `json:"id"`
	Type           string            `json:"type"`
	Name           string            `json:"name"`
	Disabled       bool              `json:"disabled"` // zero value (false) keeps existing sources enabled
	LastSyncedAt   *time.Time        `json:"last_synced_at"`
	SyncStatus     string            `json:"sync_status"` // idle | syncing | completed | failed
	SyncError      *string           `json:"sync_error"`
	SyncErrorPhase *string           `json:"sync_error_phase"` // fetch | embed
	Config         map[string]string `json:"config"`
}

// GetConfig mirrors SourceDefinition.get_config in app/models.py.
func (s *SourceDefinition) GetConfig(key string) string {
	if s.Config == nil {
		return ""
	}
	return s.Config[key]
}

type SearchResult struct {
	ID         string            `json:"id"`
	Score      float64           `json:"score"`
	Text       string            `json:"text"`
	Tags       map[string]string `json:"tags"`
	Properties map[string]string `json:"properties"`
}

type SourceDocument struct {
	ID         string            `json:"id"`
	Text       string            `json:"text"`
	Tags       map[string]string `json:"tags"`
	Properties map[string]string `json:"properties"`
}

type TextChunk struct {
	Text        string `json:"text"`
	Index       int    `json:"index"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
}

type SyncProgress struct {
	Phase   string `json:"phase"` // fetching | embedding
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Message string `json:"message,omitempty"`
}

// ── Payload key constants ─────────────────────────────────────────────────────

const (
	PayloadText        = "text"
	PayloadIndexedAtMs = "indexed_at_ms"
	PayloadSourceDocID = "source_doc_id"
	PayloadChunkIndex  = "chunk_index"
	PayloadTotalChunks = "total_chunks"
	TagPrefix          = "tag_"
	PropPrefix         = "prop_"
)

// TagKey mirrors PayloadKeys.tag in app/models.py.
func TagKey(key string) string { return TagPrefix + key }

// PropKey mirrors PayloadKeys.prop in app/models.py.
func PropKey(key string) string { return PropPrefix + key }
