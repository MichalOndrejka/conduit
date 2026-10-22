// Package sourcemgmt holds the source-config rules shared by the web UI and
// the manage_sources MCP tool: which source types and config keys exist, how
// a config is validated, and how saving or deleting a source keeps its
// indexed vectors consistent.
package sourcemgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
	"github.com/MichalOndrejka/conduit/internal/store"
)

// TypeInfo describes a source type: a generic domain category that routes to
// a Qdrant collection (not provider-specific).
type TypeInfo struct{ Value, Label, Description string }

// Types drives the UI's type-picker tiles and the MCP tool's type list.
var Types = []TypeInfo{
	{models.SourceWorkItemQuery, "Work Items", "Epics, features, stories, bugs and tasks from a work-tracking system."},
	{models.SourceRequirements, "Requirements", "Product and software requirements, risks and specifications."},
	{models.SourceTestCase, "Test Cases", "Manual and automated test-case definitions."},
	{models.SourceGitCommits, "Commit History", "Commit history and messages from a repository."},
	{models.SourcePipelineRun, "Pipeline Runs", "Build pipeline runs with failed-step logs and failed test results."},
	{models.SourceCodeRepo, "Source Code", "Application source files from a repository."},
	{models.SourceTestCodeRepo, "Test Code", "Automated test and spec files from a repository."},
	{models.SourceDocumentation, "Documentation", "Wiki pages or documentation files from a repository."},
}

// Label returns a type's display name, or the type itself if unknown.
func Label(t string) string {
	for _, st := range Types {
		if st.Value == t {
			return st.Label
		}
	}
	return t
}

// ValidType reports whether t is one of Types.
func ValidType(t string) bool {
	for _, st := range Types {
		if st.Value == t {
			return true
		}
	}
	return false
}

// APIConfigKeys are the generic API source's config fields. Credential
// fields (CredentialKeys) store credential *names*, never secret values.
var APIConfigKeys = []string{
	"Url", "HttpMethod", "Body", "Headers", "AuthType",
	"Token", "Username", "Password", "ApiKeyHeader", "ApiKeyValue",
	"ItemsPath", "IdField", "TitleField", "ContentFields", "WebUrlField",
	"NextUrlPath", "Top", "VerifySSL",
	"Branch", "PathFilter", "WorkItemTypes", "AreaPaths",
	"FetchMode", "DefinitionId", "Days",
}

// PresetConfigKeys are UI-only fields the friendly platform forms (e.g. Azure
// DevOps) store so an existing source re-opens in the right tab, pre-filled.
// The backend ignores them — execution is driven entirely by APIConfigKeys,
// so any platform is "just a generic HTTP source" (see internal/sources/source.go).
var PresetConfigKeys = []string{
	"Platform", "AdoOrg", "AdoProject", "AdoApiVersion", "AdoResource", "AdoRepo", "AdoQuery",
}

// CredentialKeys are the config fields that name a stored credential.
var CredentialKeys = []string{"Token", "Password", "ApiKeyValue"}

var authTypes = []string{"none", "bearer", "basic", "apikey"}

// Validate checks provider-specific fields, returning a user-facing error
// message, or "" if the config is valid.
func Validate(srcType string, cfg map[string]string) string {
	switch cfg["Provider"] {
	case "manual":
		if strings.TrimSpace(cfg["Content"]) == "" {
			return "Content is required."
		}
	case "api":
		if cfg["Platform"] == "ado" && cfg["AdoProject"] == "" {
			return "Project is required for Azure DevOps sources."
		}
		if a := cfg["AuthType"]; a != "" && !slices.Contains(authTypes, strings.ToLower(a)) {
			return "Auth type must be none, bearer, basic or apikey."
		}
		if srcType == models.SourcePipelineRun {
			if cfg["Platform"] != "ado" {
				return "Pipeline Runs sources require the Azure DevOps provider."
			}
			if n, err := strconv.Atoi(strings.TrimSpace(cfg["DefinitionId"])); err != nil || n <= 0 {
				return "Pipeline definition ID must be a positive number."
			}
			if n, err := strconv.Atoi(strings.TrimSpace(cfg["Days"])); err != nil || n <= 0 {
				return "Days must be a positive number."
			}
		}
		u, err := url.Parse(cfg["Url"])
		if cfg["Url"] == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "URL must be a valid http(s) URL."
		}
		if top := cfg["Top"]; top != "" {
			if n, err := strconv.Atoi(top); err != nil || n <= 0 {
				return "Max items must be a positive number."
			}
		}
		if srcType == models.SourceGitCommits {
			if _, ok := sources.AdoRepoAPIBase(cfg["Url"]); !ok {
				return "Commit History sources require an Azure DevOps commits API URL (…/_apis/git/repositories/{repo}/commits) — diffs are always fetched for this source type."
			}
		}
		if srcType == models.SourceCodeRepo || srcType == models.SourceTestCodeRepo {
			if _, ok := sources.AdoItemsAPIBase(cfg["Url"]); !ok {
				return fmt.Sprintf("%s sources require an Azure DevOps items API URL (…/_apis/git/repositories/{repo}/items) — file content is always fetched for this source type.", Label(srcType))
			}
		}
		if body := cfg["Body"]; body != "" && !json.Valid([]byte(body)) {
			return "POST body must be valid JSON."
		}
		for _, line := range strings.Split(cfg["Headers"], "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.Contains(line, ":") {
				return `Headers must be one "Name: value" pair per line.`
			}
		}
	}
	return ""
}

// Manager saves and deletes sources while keeping their indexed vectors
// consistent.
type Manager struct {
	sources *store.SourceConfigStore
	vectors *rag.VectorStore
	secrets *secrets.Store
}

func New(sourceStore *store.SourceConfigStore, vectors *rag.VectorStore, secretsStore *secrets.Store) *Manager {
	return &Manager{sources: sourceStore, vectors: vectors, secrets: secretsStore}
}

func (m *Manager) ListAll() ([]models.SourceDefinition, error) { return m.sources.ListAll() }

func (m *Manager) Get(id string) (*models.SourceDefinition, error) { return m.sources.Get(id) }

// CredentialNames lists stored credential names (never their values).
func (m *Manager) CredentialNames() []string {
	creds := m.secrets.ListAll()
	names := make([]string, len(creds))
	for i, c := range creds {
		names[i] = c.Name
	}
	return names
}

// Save stores src. oldCollection is the collection src was indexed into
// before this edit ("" for a new source). A type/provider change moves the
// source to a different collection, so its vectors in the old one are
// removed — otherwise they'd keep surfacing in the old search tool forever
// (a re-sync only replaces within the new collection) — and it's reset to
// idle for a fresh sync.
func (m *Manager) Save(ctx context.Context, oldCollection string, src *models.SourceDefinition) error {
	if oldCollection != "" && sources.CollectionFor(src) != oldCollection {
		if err := m.vectors.DeleteByFilter(ctx, oldCollection, sourceFilter(src.ID)); err != nil {
			log.Printf("warning: could not clean up vectors in %s after collection change: %v", oldCollection, err)
		}
		src.SyncStatus = "idle"
		src.LastSyncedAt = nil
	}
	return m.sources.Save(*src)
}

// Delete removes a source's indexed vectors (best-effort) and the source
// record itself. A missing source is not an error.
func (m *Manager) Delete(ctx context.Context, id string) error {
	src, err := m.sources.Get(id)
	if err != nil {
		return err
	}
	if src == nil {
		return nil
	}
	// Best-effort vector cleanup — the source record is removed either way.
	_ = m.vectors.DeleteByFilter(ctx, sources.CollectionFor(src), sourceFilter(src.ID))
	return m.sources.Delete(src.ID)
}

// IndexedChunks counts the source's indexed chunks (0 on error).
func (m *Manager) IndexedChunks(ctx context.Context, src *models.SourceDefinition) int {
	return m.vectors.Count(ctx, sources.CollectionFor(src), sourceFilter(src.ID))
}

func sourceFilter(id string) *rag.Filter {
	return &rag.Filter{Must: []rag.FieldCondition{{
		Key: models.TagKey("source_id"), Match: rag.Match{Value: id},
	}}}
}
