package sourcemgmt

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
)

// Selections turn ticks in a provider's tree into sources. A tick selects a
// part of one source:
//
//   - repository files: one source per repository × type; its ticked
//     folders and files are its PathFilter ("" = the whole repository)
//   - commit history: one source per repository
//   - Azure DevOps work items: one source per project × type; ticked work
//     item types are its WorkItemTypes
//   - GitHub issues: one source per repository × type; ticked issue types
//     narrow it ("" = every issue)
//   - Azure DevOps pull requests: one source per project; ticked
//     repositories narrow it ("" = every repository)
//   - Azure DevOps pipeline runs: one source per project; ticked pipeline
//     folders narrow it ("" = every pipeline)
//   - GitHub pull requests and pipeline runs: one source per repository
//
// Ticking only edits configuration; Sync fetches and embeds.

// Selection states of a node for one source type.
const (
	StateOff       = ""
	StateOn        = "on"        // selected on this node
	StateInherited = "inherited" // covered by a selection further up
)

// selectionKey identifies the source a tick belongs to.
type selectionKey struct {
	org, project, repo, srcType string
	workItems                   bool // a work-item / issue source (vs. repository files)
}

// selection is what a tick on a node means.
type selection struct {
	key selectionKey
	// listKey is the config key listing the source's selected parts, ""
	// when the node is the whole source.
	listKey string
	// part is the node's entry in listKey; "" = the whole source.
	part string
	// wholeAllowed is false when the source must list at least one part
	// (Azure DevOps work items: by type).
	wholeAllowed bool
	// covers reports whether entry includes part (a folder its contents).
	covers func(entry, part string) bool
}

func noCover(string, string) bool { return false }

// coversPath: "/dir/**" covers every path below /dir.
func coversPath(entry, part string) bool {
	dir, ok := strings.CutSuffix(entry, "/**")
	if !ok {
		return false
	}
	p := strings.TrimSuffix(part, "/**")
	return strings.HasPrefix(p, dir+"/")
}

// coversFolder: an Azure DevOps pipeline folder covers its subfolders.
func coversFolder(entry, part string) bool {
	return len(part) > len(entry) && strings.EqualFold(part[:len(entry)+1], entry+`\`)
}

// pathEntry is the PathFilter entry a file-tree node stands for: "" for the
// whole repository, "/dir/**" for a folder, "/path" for a file.
func pathEntry(ref providers.NodeRef) string {
	p := strings.Trim(ref.Path, "/")
	switch {
	case ref.Kind == providers.NodeRepo || p == "":
		return ""
	case ref.Kind == providers.NodeFolder:
		return "/" + p + "/**"
	default:
		return "/" + p
	}
}

func selectionFor(ref providers.NodeRef, srcType string) (selection, error) {
	ado := ref.Project != ""
	k := selectionKey{org: ref.Org, project: ref.Project, srcType: srcType}
	bad := func() (selection, error) {
		return selection{}, fmt.Errorf("%s can't be selected on a %q node", Label(srcType), ref.Kind)
	}
	switch srcType {
	case models.SourceGitCommits:
		if ref.Kind != providers.NodeRepo {
			return bad()
		}
		k.repo = ref.Repo
		return selection{key: k, covers: noCover}, nil

	case models.SourcePullRequest:
		switch {
		case ado && ref.Kind == providers.NodeRepos:
			return selection{key: k, listKey: providers.KeyRepositories, wholeAllowed: true, covers: noCover}, nil
		case ado && ref.Kind == providers.NodeRepo:
			return selection{key: k, listKey: providers.KeyRepositories, part: ref.Repo, wholeAllowed: true, covers: noCover}, nil
		case !ado && ref.Kind == providers.NodeRepo:
			k.repo = ref.Repo
			return selection{key: k, covers: noCover}, nil
		}
		return bad()

	case models.SourcePipelineRun:
		switch {
		case ado && ref.Kind == providers.NodePipelines:
			return selection{key: k, listKey: providers.KeyPipelineFolder, wholeAllowed: true, covers: coversFolder}, nil
		case ado && ref.Kind == providers.NodePipelineFolder:
			return selection{key: k, listKey: providers.KeyPipelineFolder, part: strings.Trim(ref.Path, `\`), wholeAllowed: true, covers: coversFolder}, nil
		case !ado && ref.Kind == providers.NodePipelines:
			k.repo = ref.Repo
			return selection{key: k, covers: noCover}, nil
		}
		return bad()
	}

	switch ref.Kind {
	case providers.NodeRepo, providers.NodeFolder, providers.NodeFile:
		if !slices.Contains(providers.FileOffers, srcType) {
			return bad()
		}
		k.repo = ref.Repo
		return selection{key: k, listKey: providers.KeyPathFilter, part: pathEntry(ref), wholeAllowed: true, covers: coversPath}, nil
	case providers.NodeWorkItemType:
		if !slices.Contains(providers.WorkItemOffers, srcType) {
			return bad()
		}
		k.workItems = true
		return selection{key: k, listKey: providers.KeyWorkItemTypes, part: ref.ID, covers: noCover}, nil
	case providers.NodeIssues, providers.NodeIssueType:
		if !slices.Contains(providers.WorkItemOffers, srcType) {
			return bad()
		}
		k.repo, k.workItems = ref.Repo, true
		part := ""
		if ref.Kind == providers.NodeIssueType {
			part = ref.ID
		}
		return selection{key: k, listKey: providers.KeyWorkItemTypes, part: part, wholeAllowed: true, covers: noCover}, nil
	}
	return bad()
}

func keyForSource(src *models.SourceDefinition) selectionKey {
	return selectionKey{
		org:       src.GetConfig(providers.KeyOrg),
		project:   src.GetConfig(providers.KeyProject),
		repo:      src.GetConfig(providers.KeyRepo),
		srcType:   src.Type,
		workItems: providers.IsWorkItemType(src.Type, src.Config),
	}
}

// providerSources indexes a provider's sources by selection key.
func (m *Manager) providerSources(providerID string) (map[selectionKey]*models.SourceDefinition, error) {
	all, err := m.SourcesOf(providerID)
	if err != nil {
		return nil, err
	}
	out := make(map[selectionKey]*models.SourceDefinition, len(all))
	for i := range all {
		out[keyForSource(&all[i])] = &all[i]
	}
	return out, nil
}

// NodeState is a node's selection state per offered source type.
type NodeState map[string]string

// States returns the selection state of each node, by index.
func (m *Manager) States(providerID string, nodes []providers.Node) ([]NodeState, error) {
	byKey, err := m.providerSources(providerID)
	if err != nil {
		return nil, err
	}
	out := make([]NodeState, len(nodes))
	for i, n := range nodes {
		st := NodeState{}
		for _, t := range n.Offers {
			st[t] = nodeState(byKey, n.Ref, t)
		}
		out[i] = st
	}
	return out, nil
}

func nodeState(byKey map[selectionKey]*models.SourceDefinition, ref providers.NodeRef, srcType string) string {
	sel, err := selectionFor(ref, srcType)
	if err != nil {
		return StateOff
	}
	src := byKey[sel.key]
	if src == nil {
		return StateOff
	}
	if sel.listKey == "" {
		return StateOn
	}
	list := providers.ParseList(src.GetConfig(sel.listKey))
	switch {
	case len(list) == 0 && sel.wholeAllowed:
		if sel.part == "" {
			return StateOn
		}
		return StateInherited
	case sel.part == "":
		return StateOff // parts are selected, not the whole
	case slices.ContainsFunc(list, func(e string) bool { return strings.EqualFold(e, sel.part) }):
		return StateOn
	case slices.ContainsFunc(list, func(e string) bool { return sel.covers(e, sel.part) }):
		return StateInherited
	}
	return StateOff
}

// Toggle selects (on) or deselects srcType on node in provider providerID,
// creating, updating or deleting the source it belongs to, and returns that
// source (nil once deleted). name labels the node (it names a wiki's
// source). isSyncing guards sources mid-sync, whose finishing sync would
// overwrite the edit. Deselecting the last part of a source deletes it
// together with its indexed data.
func (m *Manager) Toggle(ctx context.Context, providerID string, node providers.NodeRef, name, srcType string, on bool, isSyncing func(id string) bool) (*models.SourceDefinition, error) {
	m.selMu.Lock()
	defer m.selMu.Unlock()

	prov, in, err := m.ProviderFor(providerID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(in.SourceTypes(), srcType) {
		return nil, fmt.Errorf("%s has no %s sources", in.Label(), Label(srcType))
	}
	sel, err := selectionFor(node, srcType)
	if err != nil {
		return nil, err
	}
	byKey, err := m.providerSources(providerID)
	if err != nil {
		return nil, err
	}
	src := byKey[sel.key]
	if src != nil && isSyncing != nil && isSyncing(src.ID) {
		return nil, fmt.Errorf("%q is syncing — wait for it to finish or cancel it, then change the selection", src.Name)
	}

	if src == nil {
		if !on {
			return nil, nil
		}
		src = &models.SourceDefinition{
			ID: uuid.NewString(), ProviderID: prov.ID, Type: srcType, SyncStatus: "idle",
			Name: sourceName(sel.key, name), Config: map[string]string{},
		}
		setIf(src.Config, providers.KeyOrg, sel.key.org)
		setIf(src.Config, providers.KeyProject, sel.key.project)
		setIf(src.Config, providers.KeyRepo, sel.key.repo)
		if sel.key.workItems && srcType != models.SourceWorkItemQuery {
			src.Config[providers.KeyFetchMode] = providers.FetchModeWorkItems
		}
		if sel.listKey != "" {
			setIf(src.Config, sel.listKey, sel.part)
		}
		if msg := in.Validate(src.Type, src.Config); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return src, m.Save(ctx, src)
	}

	if sel.listKey == "" { // the node is the whole source
		if !on {
			return nil, m.Delete(ctx, src.ID)
		}
		return src, nil
	}
	list, keep := toggleList(providers.ParseList(src.GetConfig(sel.listKey)), sel, on)
	if !keep {
		return nil, m.Delete(ctx, src.ID)
	}
	if len(list) == 0 {
		delete(src.Config, sel.listKey)
	} else {
		src.Config[sel.listKey] = strings.Join(list, ",")
	}
	return src, m.Save(ctx, src)
}

// toggleList applies a tick to a source's list of parts (empty = the whole
// source, where allowed). keep is false when nothing stays selected.
func toggleList(list []string, sel selection, on bool) (out []string, keep bool) {
	whole := len(list) == 0 && sel.wholeAllowed
	same := func(e string) bool { return strings.EqualFold(e, sel.part) }
	switch {
	case on && sel.part == "":
		return nil, true // the whole source
	case on && whole:
		return nil, true // already covered
	case on:
		if slices.ContainsFunc(list, same) || slices.ContainsFunc(list, func(e string) bool { return sel.covers(e, sel.part) }) {
			return list, true // already selected
		}
		// A folder replaces the entries beneath it.
		kept := slices.DeleteFunc(list, func(e string) bool { return sel.covers(sel.part, e) })
		return append(kept, sel.part), true
	case sel.part == "":
		return nil, false // the whole source deselected
	case whole:
		return nil, true // a part of a wholly selected source stays selected
	default:
		kept := slices.DeleteFunc(list, same)
		return kept, len(kept) > 0
	}
}

var guidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func sourceName(k selectionKey, nodeName string) string {
	scope := k.project
	switch {
	case k.repo != "" && guidRE.MatchString(k.repo):
		// A wiki, selected by its backing repository's ID.
		scope = strings.TrimPrefix(k.project+"/"+nodeName, "/")
	case k.repo != "" && k.project != "":
		scope = k.project + "/" + k.repo
	case k.repo != "":
		scope = k.org + "/" + k.repo
	}
	return scope + " — " + Label(k.srcType)
}

func setIf(cfg map[string]string, key, value string) {
	if value != "" {
		cfg[key] = value
	}
}
