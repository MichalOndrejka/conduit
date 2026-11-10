package sourcemgmt

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers/registry"
	"github.com/MichalOndrejka/conduit/internal/store"
)

// Export and import of the configuration: providers (never their tokens)
// and the sources that reference them. An import is planned first — what
// would be added, what already exists, what can't be used — so the UI can
// show it before anything is written; applying writes the plan.

// ExportDoc is the export file.
type ExportDoc struct {
	Providers []models.Provider         `json:"providers"`
	Sources   []models.SourceDefinition `json:"sources"`
}

// Export returns every provider and source.
func (m *Manager) Export() (*ExportDoc, error) {
	provs, err := m.ListProviders()
	if err != nil {
		return nil, err
	}
	srcs, err := m.sources.ListAll()
	if err != nil {
		return nil, err
	}
	return &ExportDoc{Providers: provs, Sources: srcs}, nil
}

// What an import does with each provider and source.
const (
	ImportAdd        = "add"         // added
	ImportRename     = "rename"      // added under a new name: the name is taken here
	ImportExists     = "exists"      // provider already here (same ID): kept as is
	ImportDuplicate  = "duplicate"   // source already selected here: skipped
	ImportNoProvider = "no_provider" // source whose provider is neither in the file nor here: skipped
	ImportInvalid    = "invalid"     // fails validation: skipped
)

// PlannedProvider is one provider of the file and what importing does with it.
type PlannedProvider struct {
	Name   string `json:"name"` // the name it gets (renamed, or the local one when it exists)
	Kind   string `json:"kind"` // display label
	URL    string `json:"url"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
	// NeedsToken: tokens are never exported, so an added provider needs one
	// before its sources can be read.
	NeedsToken bool `json:"needs_token"`
	prov       models.Provider
}

// PlannedSource is one source of the file and what importing does with it.
type PlannedSource struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // display label
	Provider string `json:"provider,omitempty"`
	Action   string `json:"action"`
	Note     string `json:"note,omitempty"`
	src      models.SourceDefinition
}

// ImportPlan is everything an import would do.
type ImportPlan struct {
	Providers    []PlannedProvider `json:"providers"`
	Sources      []PlannedSource   `json:"sources"`
	AddProviders int               `json:"add_providers"`
	AddSources   int               `json:"add_sources"`
}

// PlanImport reads an export file and works out what importing it would do,
// without writing anything. A file from before providers (a plain list of
// sources) plans every source as having no provider.
func (m *Manager) PlanImport(data []byte) (*ImportPlan, error) {
	var doc struct {
		Providers []models.Provider `json:"providers"`
	}
	_ = json.Unmarshal(data, &doc) // a plain source list has no providers
	srcs, err := store.ParseSources(data)
	if err != nil {
		return nil, fmt.Errorf("not a Conduit export: %w", err)
	}
	local, err := m.providers.ListAll()
	if err != nil {
		return nil, err
	}
	plan := &ImportPlan{Providers: []PlannedProvider{}, Sources: []PlannedSource{}}

	// Providers sources can use: the ones here, then the ones the file adds.
	usable := map[string]models.Provider{}
	names := map[string]bool{}
	for _, p := range local {
		usable[p.ID] = p
		names[strings.ToLower(p.Name)] = true
	}
	for _, p := range doc.Providers {
		pp := PlannedProvider{Name: strings.TrimSpace(p.Name), URL: p.BaseURL, Kind: p.Kind}
		in, known := registry.Get(p.Kind)
		if known {
			pp.Kind = in.Label()
		}
		if have, ok := usable[p.ID]; ok && p.ID != "" {
			pp.Action, pp.Name, pp.URL = ImportExists, have.Name, have.BaseURL
			pp.Note = "already here"
			plan.Providers = append(plan.Providers, pp)
			continue
		}
		if msg := validImportedProvider(p, known); msg != "" {
			pp.Action, pp.Note = ImportInvalid, msg
			plan.Providers = append(plan.Providers, pp)
			continue
		}
		p.Name = pp.Name
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		pp.Action = ImportAdd
		if names[strings.ToLower(p.Name)] {
			p.Name = uniqueName(p.Name, names)
			pp.Action, pp.Note = ImportRename, fmt.Sprintf("%q is taken here", pp.Name)
			pp.Name = p.Name
		}
		names[strings.ToLower(p.Name)] = true
		pp.NeedsToken, pp.prov = true, p
		usable[p.ID] = p
		plan.Providers = append(plan.Providers, pp)
		plan.AddProviders++
	}

	// Sources already selected here, by provider and selection.
	existing, err := m.sources.ListAll()
	if err != nil {
		return nil, err
	}
	selected := map[string]map[selectionKey]string{}
	mark := func(src *models.SourceDefinition) {
		if selected[src.ProviderID] == nil {
			selected[src.ProviderID] = map[selectionKey]string{}
		}
		selected[src.ProviderID][keyForSource(src)] = src.Name
	}
	for i := range existing {
		if existing[i].ProviderID != "" {
			mark(&existing[i])
		}
	}
	for _, src := range srcs {
		ps := PlannedSource{Name: src.Name, Type: Label(src.Type)}
		p, ok := usable[src.ProviderID]
		switch {
		case src.ProviderID == "":
			ps.Action, ps.Note = ImportNoProvider, "made before providers — tick it again in a provider's tree"
		case !ok:
			ps.Action, ps.Note = ImportNoProvider, "its provider is neither in the file nor here"
		}
		if ps.Action != "" {
			plan.Sources = append(plan.Sources, ps)
			continue
		}
		ps.Provider = p.Name
		if msg := validImportedSource(p, &src); msg != "" {
			ps.Action, ps.Note = ImportInvalid, msg
			plan.Sources = append(plan.Sources, ps)
			continue
		}
		if name, dup := selected[src.ProviderID][keyForSource(&src)]; dup {
			ps.Action, ps.Note = ImportDuplicate, fmt.Sprintf("already selected as %q", name)
			plan.Sources = append(plan.Sources, ps)
			continue
		}
		mark(&src)
		src.SyncStatus, src.SyncError, src.SyncErrorPhase, src.LastSyncedAt, src.Identity = "idle", nil, nil, nil, nil
		ps.Action, ps.src = ImportAdd, src
		plan.Sources = append(plan.Sources, ps)
		plan.AddSources++
	}
	return plan, nil
}

// ApplyImport writes what plan adds.
func (m *Manager) ApplyImport(plan *ImportPlan) error {
	m.selMu.Lock()
	defer m.selMu.Unlock()
	var provs []models.Provider
	for _, pp := range plan.Providers {
		if pp.Action == ImportAdd || pp.Action == ImportRename {
			provs = append(provs, pp.prov)
		}
	}
	if _, err := m.providers.Import(provs); err != nil {
		return err
	}
	var srcs []models.SourceDefinition
	for _, ps := range plan.Sources {
		if ps.Action == ImportAdd {
			srcs = append(srcs, ps.src)
		}
	}
	return m.sources.AddAll(srcs)
}

func validImportedProvider(p models.Provider, known bool) string {
	switch {
	case p.ID == "":
		return "no id"
	case !known:
		return fmt.Sprintf("unknown kind %q", p.Kind)
	case strings.TrimSpace(p.Name) == "":
		return "no name"
	}
	u, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "URL isn't a valid http(s) URL"
	}
	return ""
}

func validImportedSource(p models.Provider, src *models.SourceDefinition) string {
	if strings.TrimSpace(src.Name) == "" {
		return "no name"
	}
	if !ValidType(src.Type) {
		return fmt.Sprintf("unknown type %q", src.Type)
	}
	in, _ := registry.Get(p.Kind)
	if !slices.Contains(in.SourceTypes(), src.Type) {
		return fmt.Sprintf("%s has no %s sources", in.Label(), Label(src.Type))
	}
	return in.Validate(src.Type, src.Config)
}

// uniqueName returns "name (imported)", or "name (imported 2)", … — the
// first not in names.
func uniqueName(name string, names map[string]bool) string {
	for i := 1; ; i++ {
		candidate := name + " (imported)"
		if i > 1 {
			candidate = fmt.Sprintf("%s (imported %d)", name, i)
		}
		if !names[strings.ToLower(candidate)] {
			return candidate
		}
	}
}
