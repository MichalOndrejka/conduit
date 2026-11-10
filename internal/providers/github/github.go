// Package github is the GitHub integration (github.com, GitHub Enterprise
// Cloud with data residency, GitHub Enterprise Server). A provider's base URL
// is the server; sources add the owner (Organization) and repository.
package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// Kind is the identifier stored on providers.
const Kind = "github"

type Integration struct{}

func (Integration) Kind() string           { return Kind }
func (Integration) Label() string          { return "GitHub" }
func (Integration) DefaultBaseURL() string { return "https://github.com" }
func (Integration) BaseURLHint() string {
	return "https://github.com, a GitHub Enterprise Cloud tenant (https://acme.ghe.com) or a GitHub Enterprise Server (https://github.example.com)."
}

func (Integration) SourceTypes() []string {
	return []string{
		models.SourceWorkItemQuery, models.SourceRequirements, models.SourceTestCase,
		models.SourceGitCommits, models.SourcePipelineRun, models.SourcePullRequest,
		models.SourceCodeRepo, models.SourceTestCodeRepo, models.SourceDocumentation,
	}
}

// Test reads the token's user, which needs a valid token.
func (Integration) Test(ctx context.Context, conn providers.Conn) error {
	d, err := newDiscoverer(conn)
	if err != nil {
		return err
	}
	_, err = d.login(ctx)
	return err
}

func (i Integration) Validate(srcType string, cfg map[string]string) string {
	if !slices.Contains(i.SourceTypes(), srcType) {
		return fmt.Sprintf("GitHub has no %q sources.", srcType)
	}
	if strings.TrimSpace(cfg[providers.KeyOrg]) == "" {
		return "Owner is required."
	}
	if strings.TrimSpace(cfg[providers.KeyRepo]) == "" {
		return "Repository is required."
	}
	if v := strings.TrimSpace(cfg[providers.KeyTop]); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 {
			return "Max items must be a positive number."
		}
	}
	if v := strings.TrimSpace(cfg[providers.KeyRuns]); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n <= 0 || n > sources.MaxPipelineRuns {
			return fmt.Sprintf("Runs must be a number from 1 to %d.", sources.MaxPipelineRuns)
		}
	}
	return ""
}

// Compile builds the configuration of the GitHub fetcher in internal/sources.
func (Integration) Compile(prov models.Provider, src *models.SourceDefinition) map[string]string {
	cfg := src.Config
	out := map[string]string{
		"Provider":    "github",
		"GitHubUrl":   strings.TrimSpace(prov.BaseURL),
		"GitHubOwner": strings.TrimSpace(cfg[providers.KeyOrg]),
		"GitHubRepo":  strings.TrimSpace(cfg[providers.KeyRepo]),
		"Token":       models.ProviderTokenName(prov.ID),
	}
	if prov.SkipTLSVerify {
		out["VerifySSL"] = "false"
	}
	providers.CopyKeys(out, cfg,
		providers.KeyBranch, providers.KeyPathFilter, providers.KeyFetchMode,
		providers.KeyWorkItemTypes, providers.KeyLabels, providers.KeyTop, providers.KeyRuns,
	)
	return out
}

// ── Discovery ───────────────────────────────────────────────────────────────
// Owners (the token's user and its organizations) → repositories →
// Issues (with the organization's issue types), Workflows, then the
// default branch's folders and files.

func (Integration) Discover(ctx context.Context, conn providers.Conn, parent providers.NodeRef) ([]providers.Node, error) {
	d, err := newDiscoverer(conn)
	if err != nil {
		return nil, err
	}
	switch parent.Kind {
	case providers.NodeRoot:
		return d.owners(ctx)
	case providers.NodeOrg:
		return d.repos(ctx, parent.Org)
	case providers.NodeRepo:
		groups := []providers.Node{
			{Ref: providers.NodeRef{Kind: providers.NodeIssues, Org: parent.Org, Repo: parent.Repo}, Name: "Issues",
				HasChildren: true, Offers: providers.WorkItemOffers},
			{Ref: providers.NodeRef{Kind: providers.NodePipelines, Org: parent.Org, Repo: parent.Repo}, Name: "Workflows",
				HasChildren: true, Offers: []string{models.SourcePipelineRun}},
		}
		files, err := d.contents(ctx, parent)
		if err != nil {
			return nil, err
		}
		return append(groups, files...), nil
	case providers.NodeFolder:
		return d.contents(ctx, parent)
	case providers.NodeIssues:
		return d.issueTypes(ctx, parent)
	case providers.NodePipelines:
		return d.workflows(ctx, parent)
	default:
		return nil, fmt.Errorf("nothing to list under a %q node", parent.Kind)
	}
}

type discoverer struct {
	client  *providers.Client
	apiBase string
}

func newDiscoverer(conn providers.Conn) (*discoverer, error) {
	apiBase, _, err := sources.GitHubBases(conn.BaseURL)
	if err != nil {
		return nil, err
	}
	token := conn.Token
	return &discoverer{
		apiBase: apiBase,
		client: &providers.Client{
			HTTP:   conn.HTTPClient(),
			Header: http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {"2022-11-28"}},
			Authorize: func(r *http.Request) {
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
			},
		},
	}, nil
}

func (d *discoverer) login(ctx context.Context) (string, error) {
	var me struct {
		Login string `json:"login"`
	}
	if _, err := d.client.GetJSON(ctx, d.apiBase+"/user", &me); err != nil {
		return "", fmt.Errorf("reading the token's user: %w", err)
	}
	return me.Login, nil
}

// owners lists the token's user and the organizations it belongs to.
func (d *discoverer) owners(ctx context.Context) ([]providers.Node, error) {
	me, err := d.login(ctx)
	if err != nil {
		return nil, err
	}
	nodes := []providers.Node{{Ref: providers.NodeRef{Kind: providers.NodeOrg, Org: me}, Name: me, Detail: "you", HasChildren: true}}
	var orgs []providers.Node
	err = d.pages(ctx, d.apiBase+"/user/orgs", func(page []map[string]any) {
		for _, o := range page {
			login, _ := o["login"].(string)
			if login != "" {
				orgs = append(orgs, providers.Node{Ref: providers.NodeRef{Kind: providers.NodeOrg, Org: login}, Name: login, HasChildren: true})
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("listing organizations: %w", err)
	}
	sortNodes(orgs)
	return append(nodes, orgs...), nil
}

// repos lists the owner's repositories the token can see: an organization's
// through /orgs, a user's own (private ones included) through /user/repos.
func (d *discoverer) repos(ctx context.Context, owner string) ([]providers.Node, error) {
	listURL := d.apiBase + "/orgs/" + url.PathEscape(owner) + "/repos?type=all"
	if me, err := d.login(ctx); err == nil && strings.EqualFold(me, owner) {
		listURL = d.apiBase + "/user/repos?affiliation=owner"
	}
	var nodes []providers.Node
	collect := func(page []map[string]any) {
		for _, r := range page {
			name, _ := r["name"].(string)
			branch, _ := r["default_branch"].(string)
			archived, _ := r["archived"].(bool)
			if name == "" {
				continue
			}
			n := providers.Node{Ref: providers.NodeRef{Kind: providers.NodeRepo, Org: owner, Repo: name, Path: "/"}, Name: name, Detail: branch, HasChildren: true}
			if archived {
				n.Detail = strings.TrimSpace(branch + " · archived")
			}
			n.Offers = append(append([]string{}, providers.FileOffers...), models.SourceGitCommits, models.SourcePullRequest)
			nodes = append(nodes, n)
		}
	}
	err := d.pages(ctx, listURL, collect)
	if err != nil && providers.IsStatus(err, http.StatusNotFound) {
		// Not an organization: another user's public repositories.
		nodes = nil
		err = d.pages(ctx, d.apiBase+"/users/"+url.PathEscape(owner)+"/repos", collect)
	}
	if err != nil {
		return nil, fmt.Errorf("listing repositories of %s: %w", owner, err)
	}
	sortNodes(nodes)
	return nodes, nil
}

// contents lists one folder of the repository's default branch.
func (d *discoverer) contents(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	dir := strings.Trim(parent.Path, "/")
	u := d.apiBase + "/repos/" + url.PathEscape(parent.Org) + "/" + url.PathEscape(parent.Repo) + "/contents"
	if dir != "" {
		u += "/" + escapePath(dir)
	}
	var items []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"` // file | dir | symlink | submodule
	}
	if _, err := d.client.GetJSON(ctx, u, &items); err != nil {
		if providers.IsStatus(err, http.StatusNotFound) && dir == "" {
			return nil, nil // an empty repository
		}
		return nil, fmt.Errorf("listing /%s: %w", dir, err)
	}
	var folders, files []providers.Node
	for _, it := range items {
		ref := providers.NodeRef{Org: parent.Org, Repo: parent.Repo, Path: "/" + it.Path}
		n := providers.Node{Name: it.Name, Offers: providers.FileOffers}
		switch it.Type {
		case "dir":
			ref.Kind, n.HasChildren = providers.NodeFolder, true
			n.Ref = ref
			folders = append(folders, n)
		case "file":
			ref.Kind = providers.NodeFile
			n.Ref = ref
			files = append(files, n)
		}
	}
	sortNodes(folders)
	sortNodes(files)
	return append(folders, files...), nil
}

// issueTypes lists the organization's issue types, which narrow an issues
// source. Personal accounts and older servers have none (404).
func (d *discoverer) issueTypes(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	var resp []struct {
		Name      string `json:"name"`
		IsEnabled *bool  `json:"is_enabled"`
	}
	if _, err := d.client.GetJSON(ctx, d.apiBase+"/orgs/"+url.PathEscape(parent.Org)+"/issue-types", &resp); err != nil {
		if providers.IsStatus(err, http.StatusNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing issue types of %s: %w", parent.Org, err)
	}
	var nodes []providers.Node
	for _, t := range resp {
		if t.Name == "" || (t.IsEnabled != nil && !*t.IsEnabled) {
			continue
		}
		nodes = append(nodes, providers.Node{
			Ref:    providers.NodeRef{Kind: providers.NodeIssueType, Org: parent.Org, Repo: parent.Repo, ID: t.Name},
			Name:   t.Name,
			Offers: providers.WorkItemOffers,
		})
	}
	sortNodes(nodes)
	return nodes, nil
}

// workflows lists the repository's workflows, for reference: a pipeline-run
// source covers every workflow of the repository, and the agent names one
// when it asks for runs.
func (d *discoverer) workflows(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	var resp struct {
		Workflows []struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Path  string `json:"path"`
			State string `json:"state"`
		} `json:"workflows"`
	}
	u := d.apiBase + "/repos/" + url.PathEscape(parent.Org) + "/" + url.PathEscape(parent.Repo) + "/actions/workflows?per_page=100"
	if _, err := d.client.GetJSON(ctx, u, &resp); err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}
	nodes := make([]providers.Node, 0, len(resp.Workflows))
	for _, w := range resp.Workflows {
		detail := path.Base(w.Path)
		if w.State != "" && w.State != "active" {
			detail += " · " + w.State
		}
		nodes = append(nodes, providers.Node{
			Ref:    providers.NodeRef{Kind: providers.NodePipeline, Org: parent.Org, Repo: parent.Repo, ID: strconv.FormatInt(w.ID, 10)},
			Name:   w.Name,
			Detail: detail,
		})
	}
	sortNodes(nodes)
	return nodes, nil
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// pages follows a listing's Link: rel="next" headers through every page.
func (d *discoverer) pages(ctx context.Context, listURL string, each func([]map[string]any)) error {
	sep := "?"
	if strings.Contains(listURL, "?") {
		sep = "&"
	}
	next := listURL + sep + "per_page=100"
	for seen := map[string]bool{}; next != ""; {
		if seen[next] {
			return fmt.Errorf("listing returned the same next page twice")
		}
		seen[next] = true
		var page []map[string]any
		header, err := d.client.GetJSON(ctx, next, &page)
		if err != nil {
			return err
		}
		each(page)
		next = ""
		if m := nextLink.FindStringSubmatch(header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return nil
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func sortNodes(nodes []providers.Node) {
	sort.SliceStable(nodes, func(i, j int) bool { return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name) })
}
