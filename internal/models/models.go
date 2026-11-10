// Package models is the Go port of app/models.py: domain models and the
// payload-key / collection-name constants shared across the RAG pipeline.
package models

import "time"

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
	CollectionDocumentation, CollectionCommits, CollectionExperience,
}

// RetiredCollections held data that is now read live (pipeline runs, pull
// requests); startup drops them if they still exist.
var RetiredCollections = []string{"conduit_pipeline_runs", "conduit_pull_requests"}

// ── Source types ─────────────────────────────────────────────────────────────

const (
	SourceWorkItemQuery = "work-item"
	SourceRequirements  = "requirements"
	SourceTestCase      = "test-case"
	SourceCodeRepo      = "code"
	SourceTestCodeRepo  = "test-code"
	SourceDocumentation = "documentation"
	SourceGitCommits    = "commit-history"
	SourcePipelineRun   = "pipeline-run"
	SourcePullRequest   = "pull-request"
)

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

// Provider is one account on a source system — an Azure DevOps organization,
// collection or server, or a GitHub instance — with the token Conduit
// authenticates with. Sources reference a provider by ID and add only what
// they select inside it (project, repository, folder, …).
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is a providers registry kind: "ado" or "github".
	Kind string `json:"kind"`
	// BaseURL is the provider's root: https://dev.azure.com (every
	// organization the token sees), an organization or collection URL
	// (https://dev.azure.com/org, https://tfs.corp/DefaultCollection), an
	// Azure DevOps Server (https://tfs.corp/tfs), or a GitHub server
	// (https://github.com, https://github.corp).
	BaseURL string `json:"base_url"`
	// SkipTLSVerify disables certificate checks, for self-hosted instances
	// with a private CA that isn't mounted into the container.
	SkipTLSVerify bool `json:"skip_tls_verify,omitempty"`
	// APIVersion overrides the Azure DevOps REST API version, for older
	// servers ("" = 7.1).
	APIVersion string `json:"api_version,omitempty"`
	// The token is never stored here: it lives encrypted in the secrets
	// store under ProviderTokenName(ID).
}

// ProviderTokenName is the secrets-store entry holding a provider's token.
func ProviderTokenName(providerID string) string { return "provider-token:" + providerID }

type SourceDefinition struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	// ProviderID is the provider whose token reads this source.
	ProviderID     string            `json:"provider_id"`
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

// SortPrefix namespaces numeric sort keys in a chunk's payload.
const SortPrefix = "s_"

// SortKey is the payload key a sort key is stored under.
func SortKey(name string) string { return SortPrefix + name }
