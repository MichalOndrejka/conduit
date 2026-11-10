// Package sourcemgmt holds the provider and source rules shared by the web
// UI and the MCP tools: which source types exist, how providers are saved
// with their tokens, how ticks in a provider's tree become sources
// (selection.go), how a source's settings are validated, and how saving or
// deleting a source keeps its indexed vectors consistent.
package sourcemgmt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/providers/registry"
	"github.com/MichalOndrejka/conduit/internal/rag"
	"github.com/MichalOndrejka/conduit/internal/secrets"
	"github.com/MichalOndrejka/conduit/internal/sources"
	"github.com/MichalOndrejka/conduit/internal/store"
)

// TypeInfo describes a source type: a generic domain category that routes to
// a Qdrant collection (not provider-specific).
type TypeInfo struct{ Value, Label, Description string }

// Types lists the source types, in the order the UI shows them.
var Types = []TypeInfo{
	{models.SourceWorkItemQuery, "Work Items", "Epics, features, stories, bugs and tasks — Azure DevOps work items or GitHub issues."},
	{models.SourceRequirements, "Requirements", "Product and software requirements, risks and specifications."},
	{models.SourceTestCase, "Test Cases", "Manual and automated test-case definitions."},
	{models.SourceGitCommits, "Commit History", "Commit history and messages from a repository."},
	{models.SourcePipelineRun, "Pipeline Runs", "The pipelines of a project or pipeline folder (GitHub: a repository's workflows), whose runs, failed-step logs and failed tests are read live. Not synced."},
	{models.SourcePullRequest, "Pull Requests", "The repositories of a project, or some of them (GitHub: one repository), whose pull requests, diffs and comment threads are read live. Not synced."},
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
	return slices.ContainsFunc(Types, func(st TypeInfo) bool { return st.Value == t })
}

// Manager saves providers and sources while keeping tokens, selections and
// indexed vectors consistent.
type Manager struct {
	sources   *store.SourceConfigStore
	vectors   *rag.VectorStore
	providers *store.ProviderStore
	secrets   *secrets.Store
	resolver  *registry.Resolver
	selMu     sync.Mutex // serializes selection edits (read-modify-write of a source)
}

func New(sourceStore *store.SourceConfigStore, vectors *rag.VectorStore, providerStore *store.ProviderStore, secretsStore *secrets.Store) *Manager {
	return &Manager{
		sources: sourceStore, vectors: vectors, providers: providerStore, secrets: secretsStore,
		resolver: &registry.Resolver{Providers: providerStore, Secrets: secretsStore},
	}
}

// Resolver compiles sources for their fetchers and resolves provider tokens.
func (m *Manager) Resolver() *registry.Resolver { return m.resolver }

// ── Providers ───────────────────────────────────────────────────────────────

// ListProviders returns the saved providers, by name.
func (m *Manager) ListProviders() ([]models.Provider, error) {
	all, err := m.providers.ListAll()
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
	return all, nil
}

// ProviderFor returns a saved provider and its integration.
func (m *Manager) ProviderFor(id string) (*models.Provider, providers.Integration, error) {
	return m.resolver.Provider(id)
}

// Conn resolves a provider's token for a provider request.
func (m *Manager) Conn(p *models.Provider) providers.Conn { return m.resolver.Conn(p) }

// HasToken reports whether a token is stored for the provider.
func (m *Manager) HasToken(id string) bool {
	return m.secrets != nil && m.secrets.Has(models.ProviderTokenName(id))
}

// SaveProvider validates and stores p (a new one when p.ID is ""), and its
// token unless token is "" (an edit keeping the stored token).
func (m *Manager) SaveProvider(p *models.Provider, token string) error {
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	p.APIVersion = strings.TrimSpace(p.APIVersion)
	token = strings.TrimSpace(token)
	in, ok := registry.Get(p.Kind)
	if !ok {
		return fmt.Errorf("unknown provider kind %q", p.Kind)
	}
	if p.Name == "" {
		return errors.New("name is required")
	}
	if u, err := url.Parse(p.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("URL must be a valid http(s) URL")
	}
	all, err := m.providers.ListAll()
	if err != nil {
		return err
	}
	for _, o := range all {
		if o.ID != p.ID && strings.EqualFold(o.Name, p.Name) {
			return fmt.Errorf("a provider named %q already exists", p.Name)
		}
	}
	isNew := p.ID == ""
	if isNew {
		p.ID = uuid.NewString()
	}
	if token == "" && in.Kind() == "ado" && !m.HasToken(p.ID) {
		return errors.New("a personal access token is required")
	}
	if token != "" {
		if err := m.secrets.Set(models.ProviderTokenName(p.ID), token); err != nil {
			return err
		}
	}
	return m.providers.Save(*p)
}

// DeleteProvider removes a provider and its token. A provider still used by
// sources is kept: delete them first.
func (m *Manager) DeleteProvider(id string) error {
	used, err := m.SourcesOf(id)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		names := make([]string, len(used))
		for i, s := range used {
			names[i] = s.Name
		}
		return fmt.Errorf("%d source(s) use this provider (%s) — delete them first", len(used), strings.Join(names, ", "))
	}
	if m.secrets != nil && m.secrets.Has(models.ProviderTokenName(id)) {
		if err := m.secrets.Delete(models.ProviderTokenName(id)); err != nil {
			return err
		}
	}
	return m.providers.Delete(id)
}

// SourcesOf returns the sources of a provider.
func (m *Manager) SourcesOf(providerID string) ([]models.SourceDefinition, error) {
	all, err := m.sources.ListAll()
	if err != nil {
		return nil, err
	}
	var out []models.SourceDefinition
	for _, s := range all {
		if s.ProviderID == providerID {
			out = append(out, s)
		}
	}
	return out, nil
}

// ── Sources ─────────────────────────────────────────────────────────────────

func (m *Manager) ListAll() ([]models.SourceDefinition, error) { return m.sources.ListAll() }

func (m *Manager) Get(id string) (*models.SourceDefinition, error) { return m.sources.Get(id) }

// Validate checks a source's config against its provider's rules,
// returning a user-facing message, or "" if valid.
func (m *Manager) Validate(src *models.SourceDefinition) string {
	if !ValidType(src.Type) {
		return fmt.Sprintf("Unknown source type %q.", src.Type)
	}
	p, in, err := m.resolver.Provider(src.ProviderID)
	if err != nil {
		return "Provider: " + err.Error()
	}
	if !slices.Contains(in.SourceTypes(), src.Type) {
		return fmt.Sprintf("%s has no %s sources.", p.Name, Label(src.Type))
	}
	return in.Validate(src.Type, src.Config)
}

// UpdateSettings applies a source's editable settings: its name, enabled
// state and the keys of providers.SettingKeys ("" removes a key). Selection
// keys (project, repository, folder, …) are changed by ticking instead.
func (m *Manager) UpdateSettings(ctx context.Context, id, name string, disabled *bool, settings map[string]string) (*models.SourceDefinition, error) {
	m.selMu.Lock()
	defer m.selMu.Unlock()
	src, err := m.sources.Get(id)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, fmt.Errorf("no source has id %q", id)
	}
	if name = strings.TrimSpace(name); name != "" {
		src.Name = name
	}
	if disabled != nil {
		src.Disabled = *disabled
	}
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	for k, v := range settings {
		if !slices.Contains(providers.SettingKeys, k) {
			return nil, fmt.Errorf("%q is not a setting (settings: %s)", k, strings.Join(providers.SettingKeys, ", "))
		}
		if v = strings.TrimSpace(v); v == "" {
			delete(src.Config, k)
		} else {
			src.Config[k] = v
		}
	}
	if msg := m.Validate(src); msg != "" {
		return nil, errors.New(msg)
	}
	return src, m.Save(ctx, src)
}

// Save stores src. A live source has no sync status to keep.
func (m *Manager) Save(_ context.Context, src *models.SourceDefinition) error {
	if models.IsLiveType(src.Type) {
		src.SyncStatus, src.LastSyncedAt, src.SyncError, src.SyncErrorPhase = "idle", nil, nil, nil
	}
	if src.SyncStatus == "" {
		src.SyncStatus = "idle"
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
	if coll := sources.CollectionFor(src); coll != "" {
		if err := m.vectors.DeleteByFilter(ctx, coll, sourceFilter(src.ID)); err != nil {
			log.Printf("warning: could not delete the indexed data of source %s: %v", src.ID, err)
		}
	}
	return m.sources.Delete(src.ID)
}

// IndexedChunks counts the source's indexed chunks (0 on error, and for a
// live source, which has none).
func (m *Manager) IndexedChunks(ctx context.Context, src *models.SourceDefinition) int {
	coll := sources.CollectionFor(src)
	if coll == "" {
		return 0
	}
	return m.vectors.Count(ctx, coll, sourceFilter(src.ID))
}

func sourceFilter(id string) *rag.Filter {
	return &rag.Filter{Must: []rag.FieldCondition{{
		Key: models.TagKey("source_id"), Match: rag.Match{Value: id},
	}}}
}
