// Package models is the Go port of app/models.py: domain models and the
// payload-key / collection-name constants shared across the RAG pipeline.
package models

import "time"

const DocumentPlaceholder = "__DOCUMENT_REQUIRED__"

// ── Collection names ────────────────────────────────────────────────────────

// Collections are organized by shape — where the data lives — not by any
// company's vocabulary: whatever a team calls its requirements or test cases,
// they are either work items or files, and are narrowed by the team's own
// field values (work item type, path, code_kind).
const (
	CollectionWorkItems     = "conduit_workitems"
	CollectionCode          = "conduit_code" // production code and tests, narrowed by code_kind
	CollectionDocumentation = "conduit_documentation"
	CollectionCommits       = "conduit_commits"
	CollectionPipelineRuns  = "conduit_pipeline_runs"
	CollectionExperience    = "conduit_experience"
)

// AllCollections mirrors CollectionNames.ALL in app/models.py.
var AllCollections = []string{
	CollectionWorkItems, CollectionCode, CollectionDocumentation,
	CollectionCommits, CollectionPipelineRuns, CollectionExperience,
}

// LegacyCollections held the company-specific source types merged into the
// shape collections (see MigrateSourceType). A source's points are removed
// from them once it has re-synced into its new collection, and an emptied
// legacy collection is dropped.
var LegacyCollections = []string{
	"conduit_requirements", "conduit_testcases", "conduit_testcode",
}

// ── Source types ─────────────────────────────────────────────────────────────

const (
	SourceWorkItemQuery = "work-item"
	SourceCodeRepo      = "code"
	SourceDocumentation = "documentation"
	SourceGitCommits    = "commit-history"
	SourcePipelineRun   = "pipeline-run"
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

	// SourceDocID, ChunkIndex and TotalChunks locate this result within its
	// source document — pass SourceDocID and ChunkIndex±1 back to the
	// retrieve_chunk request mode to fetch the adjacent chunk deterministically
	// (no embedding/semantic search involved) when HasPrevious/HasNext is true.
	SourceDocID string `json:"source_doc_id"`
	ChunkIndex  int    `json:"chunk_index"`
	TotalChunks int    `json:"total_chunks"`
	HasPrevious bool   `json:"has_previous"`
	HasNext     bool   `json:"has_next"`
}

type SourceDocument struct {
	ID         string            `json:"id"`
	Text       string            `json:"text"`
	Tags       map[string]string `json:"tags"`
	Properties map[string]string `json:"properties"`
	// Filters holds the document's filterable categories (see filters.go),
	// copied onto every chunk at index time.
	Filters DocumentFilters `json:"-"`
	// ChunkHeader, when set, is prepended to every chunk of the document as
	// "[<ChunkHeader> | chunk k/N]" so a chunk deep inside a long text still
	// names what it belongs to (see rag.WithChunkHeaders).
	ChunkHeader string `json:"-"`
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
