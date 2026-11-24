// Package models is the Go port of app/models.py: domain models and the
// payload-key / collection-name constants shared across the RAG pipeline.
package models

import "time"

const DocumentPlaceholder = "__DOCUMENT_REQUIRED__"

// ── Collection names ────────────────────────────────────────────────────────

const (
	CollectionBacklog       = "conduit_backlog"
	CollectionRequirements  = "conduit_requirements"
	CollectionSourceCode    = "conduit_code"
	CollectionTestCode      = "conduit_testcode"
	CollectionDocumentation = "conduit_documentation"
	CollectionCommits       = "conduit_commits"
	CollectionExperience    = "conduit_experience"
)

// AllCollections mirrors CollectionNames.ALL in app/models.py.
var AllCollections = []string{
	CollectionBacklog, CollectionRequirements, CollectionSourceCode,
	CollectionTestCode,
	CollectionDocumentation, CollectionCommits, CollectionExperience,
}

// RetiredCollections held data that is now read live (pipeline runs, pull
// requests), whose source type was merged into another (test cases, now
// Requirements sources) or renamed (work items, now Backlog); startup drops
// them if they still exist.
var RetiredCollections = []string{"conduit_pipeline_runs", "conduit_pull_requests", "conduit_testcases", "conduit_workitems"}

// ── Source types ─────────────────────────────────────────────────────────────

const (
	SourceBacklog       = "backlog"
	SourceRequirements  = "requirements"
	SourceCodeRepo      = "code"
	SourceTestCodeRepo  = "test-code"
	SourceDocumentation = "documentation"
	SourceGitCommits    = "commit-history"
	SourcePipelineRun   = "pipeline-run"
	SourcePullRequest   = "pull-request"
)

// RetiredSourceWorkItem was the Backlog type's key before it was renamed;
// the source store converts stored sources of it to Backlog as it loads
// them.
const RetiredSourceWorkItem = "work-item"

// RetiredSourceTestCase was the Test Cases source type; test cases are now
// Requirements sources. A stored source of this type isn't synced.
const RetiredSourceTestCase = "test-case"

// IsLiveType reports whether sources of type t are read live by their tool
// (search_pipeline_run, search_pull_request) instead of being synced into
// Qdrant: such a source only holds the scope and credentials.
func IsLiveType(t string) bool {
	return t == SourcePipelineRun || t == SourcePullRequest
}

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
	// Identity is the source token's owner (names, emails, logins), recorded
	// at sync so a "me" filter resolves without calling the source.
	Identity []string `json:"identity,omitempty"`
}

// GetConfig mirrors SourceDefinition.get_config in app/models.py.
func (s *SourceDefinition) GetConfig(key string) string {
	if s.Config == nil {
		return ""
	}
	return s.Config[key]
}

// StatsProvider is the source's provider as the Stats page groups API load:
// github or azure-devops.
func (s *SourceDefinition) StatsProvider() string {
	if s.GetConfig("Provider") == "github" {
		return "github"
	}
	return "azure-devops"
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
	// Sort holds numeric sort keys (dates as Unix seconds, durations,
	// counts), stored on every chunk as SortKey(name) for the list mode.
	Sort map[string]float64 `json:"-"`

	// FetchKey, when set, identifies the fetched inputs the document was
	// built from, other than content-addressed ones (a commit's listing entry
	// and refs, not its immutable diff). It is stored on every chunk so a
	// later sync can tell the document needs no refetch.
	FetchKey string `json:"-"`
	// Keep marks a document the source did not refetch because its stored
	// copy is provably current (see sources.IndexState): it carries only ID,
	// and the indexer leaves its stored chunks as they are.
	Keep bool `json:"-"`
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
	// Incremental sync fingerprints, stored on every chunk of a document:
	// the hash of what was embedded, of the rest of the payload, of the
	// settings the embedding depended on, the source's fetch key, and the
	// seal stamped on all of a source's points when a sync completes.
	PayloadContentHash = "content_hash"
	PayloadMetaHash    = "meta_hash"
	PayloadIndexFP     = "index_fp"
	PayloadFetchKey    = "fetch_key"
	PayloadSeal        = "seal"
	TagPrefix          = "tag_"
	PropPrefix         = "prop_"
)

// TagKey mirrors PayloadKeys.tag in app/models.py.
func TagKey(key string) string { return TagPrefix + key }

// PropKey mirrors PayloadKeys.prop in app/models.py.
func PropKey(key string) string { return PropPrefix + key }

// SortPrefix namespaces numeric sort keys in a chunk's payload.
const SortPrefix = "s_"

// SortKey is the payload key a sort key is stored under.
func SortKey(name string) string { return SortPrefix + name }
