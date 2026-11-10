// Resource discovery for the Azure DevOps provider: everything the
// connection's PAT can see, one tree level per call — organizations (or
// Azure DevOps Server collections) → projects → repositories → folders and
// files, plus each project's work item types and pipelines.
package ado

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
)

// vsspsURL is the Azure DevOps Services profile/accounts API, which lists
// the organizations a PAT can access. A var so tests can fake it.
var vsspsURL = "https://app.vssps.visualstudio.com"

// listPageSize pages project and pipeline listings (continuation token).
const listPageSize = 500

func (i Integration) Discover(ctx context.Context, conn providers.Conn, parent providers.NodeRef) ([]providers.Node, error) {
	d := &discoverer{client: newClient(conn), conn: conn, apiVersion: apiVersionOf(conn.Provider)}
	switch parent.Kind {
	case providers.NodeRoot:
		return d.root(ctx)
	case providers.NodeOrg:
		return d.projects(ctx, parent.Org)
	case providers.NodeProject:
		return projectGroups(parent), nil
	case providers.NodeRepos:
		return d.repos(ctx, parent)
	case providers.NodeRepo, providers.NodeFolder:
		return d.items(ctx, parent)
	case providers.NodeWorkItems:
		return d.workItemTypes(ctx, parent)
	case providers.NodePipelines, providers.NodePipelineFolder:
		return d.pipelines(ctx, parent)
	case providers.NodeWikis:
		return d.wikis(ctx, parent)
	default:
		return nil, fmt.Errorf("nothing to list under a %q node", parent.Kind)
	}
}

type discoverer struct {
	client     *providers.Client
	conn       providers.Conn
	apiVersion string
}

func (d *discoverer) org(org string) string { return OrgURL(d.conn.BaseURL, org) }

// root lists what the connection URL holds: the organizations of the PAT
// (https://dev.azure.com), the collections of an Azure DevOps Server, or —
// when the URL already is one organization or collection — its projects.
func (d *discoverer) root(ctx context.Context) ([]providers.Node, error) {
	if isServicesRoot(d.conn.BaseURL) {
		return d.organizations(ctx)
	}
	projects, projErr := d.projects(ctx, "")
	if projErr == nil {
		return projects, nil
	}
	collections, err := d.collections(ctx)
	if err == nil && len(collections) > 0 {
		return collections, nil
	}
	return nil, projErr
}

// isServicesRoot reports whether u is Azure DevOps Services itself rather
// than one of its organizations.
func isServicesRoot(u string) bool {
	parsed, err := url.Parse(u)
	return err == nil && strings.EqualFold(parsed.Host, "dev.azure.com") && strings.Trim(parsed.Path, "/") == ""
}

func (d *discoverer) organizations(ctx context.Context) ([]providers.Node, error) {
	var me struct {
		ID string `json:"id"`
	}
	if _, err := d.client.GetJSON(ctx, vsspsURL+"/_apis/profile/profiles/me?api-version="+d.apiVersion, &me); err != nil {
		return nil, fmt.Errorf("listing your organizations: %w — the PAT must be created for \"All accessible organizations\"; "+
			"or set the connection URL to one organization (https://dev.azure.com/my-org)", err)
	}
	var accounts struct {
		Value []struct {
			AccountName string `json:"accountName"`
		} `json:"value"`
	}
	if _, err := d.client.GetJSON(ctx, vsspsURL+"/_apis/accounts?memberId="+url.QueryEscape(me.ID)+"&api-version="+d.apiVersion, &accounts); err != nil {
		return nil, fmt.Errorf("listing your organizations: %w", err)
	}
	nodes := make([]providers.Node, 0, len(accounts.Value))
	for _, a := range accounts.Value {
		nodes = append(nodes, providers.Node{
			Ref:  providers.NodeRef{Kind: providers.NodeOrg, Org: a.AccountName},
			Name: a.AccountName, HasChildren: true,
		})
	}
	sortNodes(nodes)
	return nodes, nil
}

func (d *discoverer) collections(ctx context.Context) ([]providers.Node, error) {
	var resp struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	if _, err := d.client.GetJSON(ctx, strings.TrimRight(d.conn.BaseURL, "/")+"/_apis/projectCollections?api-version="+d.apiVersion, &resp); err != nil {
		return nil, err
	}
	nodes := make([]providers.Node, 0, len(resp.Value))
	for _, c := range resp.Value {
		nodes = append(nodes, providers.Node{
			Ref:  providers.NodeRef{Kind: providers.NodeOrg, Org: c.Name},
			Name: c.Name, HasChildren: true,
		})
	}
	sortNodes(nodes)
	return nodes, nil
}

func (d *discoverer) projects(ctx context.Context, org string) ([]providers.Node, error) {
	var nodes []providers.Node
	err := d.pages(ctx, fmt.Sprintf("%s/_apis/projects?$top=%d&api-version=%s", d.org(org), listPageSize, d.apiVersion), func(data pageData) {
		for _, p := range data.Value {
			nodes = append(nodes, providers.Node{
				Ref:  providers.NodeRef{Kind: providers.NodeProject, Org: org, Project: p.Name},
				Name: p.Name, Detail: p.Description, HasChildren: true,
			})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	sortNodes(nodes)
	return nodes, nil
}

// projectGroups are a project's fixed children.
func projectGroups(p providers.NodeRef) []providers.Node {
	group := func(kind, name string, offers ...string) providers.Node {
		return providers.Node{Ref: providers.NodeRef{Kind: kind, Org: p.Org, Project: p.Project}, Name: name, HasChildren: true, Offers: offers}
	}
	return []providers.Node{
		// Pull requests of every repository; ticking a single repository
		// narrows the project's pull-request source to it.
		group(providers.NodeRepos, "Repositories", models.SourcePullRequest),
		group(providers.NodeWorkItems, "Work items"),
		// Runs of every pipeline; ticking a folder narrows to it.
		group(providers.NodePipelines, "Pipelines", models.SourcePipelineRun),
		group(providers.NodeWikis, "Wikis"),
	}
}

func (d *discoverer) projectAPI(r providers.NodeRef) string {
	return d.org(r.Org) + "/" + url.PathEscape(r.Project) + "/_apis"
}

func (d *discoverer) repos(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	var resp struct {
		Value []struct {
			Name          string `json:"name"`
			DefaultBranch string `json:"defaultBranch"`
			IsDisabled    bool   `json:"isDisabled"`
		} `json:"value"`
	}
	if _, err := d.client.GetJSON(ctx, d.projectAPI(parent)+"/git/repositories?api-version="+d.apiVersion, &resp); err != nil {
		return nil, fmt.Errorf("listing repositories: %w", err)
	}
	nodes := make([]providers.Node, 0, len(resp.Value))
	for _, r := range resp.Value {
		n := providers.Node{
			Ref:  providers.NodeRef{Kind: providers.NodeRepo, Org: parent.Org, Project: parent.Project, Repo: r.Name, Path: "/"},
			Name: r.Name,
		}
		switch {
		case r.IsDisabled:
			n.Detail = "disabled"
		case r.DefaultBranch == "":
			n.Detail = "empty"
		default:
			n.Detail = strings.TrimPrefix(r.DefaultBranch, "refs/heads/")
			n.HasChildren = true
			n.Offers = append(append([]string{}, providers.FileOffers...), models.SourceGitCommits, models.SourcePullRequest)
		}
		nodes = append(nodes, n)
	}
	sortNodes(nodes)
	return nodes, nil
}

// items lists one folder level of a repository's default branch.
func (d *discoverer) items(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	scope := parent.Path
	if scope == "" {
		scope = "/"
	}
	q := url.Values{}
	q.Set("scopePath", scope)
	q.Set("recursionLevel", "OneLevel")
	q.Set("api-version", d.apiVersion)
	var resp struct {
		Value []gitItem `json:"value"`
	}
	itemsURL := d.projectAPI(parent) + "/git/repositories/" + url.PathEscape(parent.Repo) + "/items?" + q.Encode()
	if _, err := d.client.GetJSON(ctx, itemsURL, &resp); err != nil {
		return nil, fmt.Errorf("listing %s: %w", scope, err)
	}
	var folders, files []providers.Node
	for _, it := range resp.Value {
		if it.Path == "" || it.Path == scope {
			continue // the scope folder itself
		}
		ref := providers.NodeRef{Org: parent.Org, Project: parent.Project, Repo: parent.Repo, Path: it.Path}
		n := providers.Node{Name: path.Base(it.Path), Offers: providers.FileOffers}
		if it.IsFolder || strings.EqualFold(it.GitObjectType, "tree") {
			ref.Kind, n.HasChildren = providers.NodeFolder, true
			n.Ref = ref
			folders = append(folders, n)
		} else {
			ref.Kind = providers.NodeFile
			n.Ref = ref
			files = append(files, n)
		}
	}
	sortNodes(folders)
	sortNodes(files)
	return append(folders, files...), nil
}

func (d *discoverer) workItemTypes(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	var resp struct {
		Value []struct {
			Name       string `json:"name"`
			IsDisabled bool   `json:"isDisabled"`
		} `json:"value"`
	}
	if _, err := d.client.GetJSON(ctx, d.projectAPI(parent)+"/wit/workitemtypes?api-version="+d.apiVersion, &resp); err != nil {
		return nil, fmt.Errorf("listing work item types: %w", err)
	}
	var nodes []providers.Node
	for _, t := range resp.Value {
		if t.IsDisabled {
			continue
		}
		nodes = append(nodes, providers.Node{
			Ref:    providers.NodeRef{Kind: providers.NodeWorkItemType, Org: parent.Org, Project: parent.Project, ID: t.Name},
			Name:   t.Name,
			Offers: providers.WorkItemOffers,
		})
	}
	sortNodes(nodes)
	return nodes, nil
}

// pipelines lists one level of the project's pipeline folders: the
// subfolders of parent (the Pipelines group is the root folder), which can
// be ticked to scope a pipeline-run source, then the pipelines directly in
// it, for reference (a source covers folders, not single pipelines; the
// agent names the pipeline when it asks for runs).
func (d *discoverer) pipelines(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	folder := ""
	if parent.Kind == providers.NodePipelineFolder {
		folder = strings.Trim(parent.Path, `\`)
	}
	var defs []pageEntry
	listURL := fmt.Sprintf("%s/build/definitions?$top=%d&api-version=%s", d.projectAPI(parent), listPageSize, d.apiVersion)
	err := d.pages(ctx, listURL, func(data pageData) { defs = append(defs, data.Value...) })
	if err != nil {
		return nil, fmt.Errorf("listing pipelines: %w", err)
	}
	subfolders := map[string]bool{}
	var folders, leaves []providers.Node
	for _, p := range defs {
		path := strings.Trim(p.Path, `\`)
		rest, ok := path, folder == ""
		if !ok {
			if strings.EqualFold(path, folder) {
				rest, ok = "", true
			} else if len(path) > len(folder) && strings.EqualFold(path[:len(folder)+1], folder+`\`) {
				rest, ok = path[len(folder)+1:], true
			}
		}
		if !ok {
			continue
		}
		if rest == "" {
			leaves = append(leaves, providers.Node{
				Ref:    providers.NodeRef{Kind: providers.NodePipeline, Org: parent.Org, Project: parent.Project, Path: path, ID: strconv.Itoa(p.ID)},
				Name:   p.Name,
				Detail: "id " + strconv.Itoa(p.ID),
			})
			continue
		}
		child, _, _ := strings.Cut(rest, `\`)
		full := child
		if folder != "" {
			full = path[:len(folder)] + `\` + child
		}
		if subfolders[strings.ToLower(full)] {
			continue
		}
		subfolders[strings.ToLower(full)] = true
		folders = append(folders, providers.Node{
			Ref:         providers.NodeRef{Kind: providers.NodePipelineFolder, Org: parent.Org, Project: parent.Project, Path: full},
			Name:        child,
			HasChildren: true,
			Offers:      []string{models.SourcePipelineRun},
		})
	}
	sortNodes(folders)
	sortNodes(leaves)
	return append(folders, leaves...), nil
}

// wikis lists the project's wikis. Each is backed by a Git repository
// (a project wiki's is hidden from the repository list), so it is selected
// as Documentation of that repository — a code wiki only of its mapped
// folder.
func (d *discoverer) wikis(ctx context.Context, parent providers.NodeRef) ([]providers.Node, error) {
	var resp struct {
		Value []struct {
			Name         string `json:"name"`
			Type         string `json:"type"` // projectWiki | codeWiki
			RepositoryID string `json:"repositoryId"`
			MappedPath   string `json:"mappedPath"`
		} `json:"value"`
	}
	if _, err := d.client.GetJSON(ctx, d.projectAPI(parent)+"/wiki/wikis?api-version="+d.apiVersion, &resp); err != nil {
		return nil, fmt.Errorf("listing wikis: %w", err)
	}
	nodes := make([]providers.Node, 0, len(resp.Value))
	for _, w := range resp.Value {
		if w.RepositoryID == "" {
			continue
		}
		path := "/"
		if w.Type == "codeWiki" && strings.Trim(w.MappedPath, "/") != "" {
			path = "/" + strings.Trim(w.MappedPath, "/")
		}
		kind := providers.NodeRepo
		if path != "/" {
			kind = providers.NodeFolder
		}
		detail := "project wiki"
		if w.Type == "codeWiki" {
			detail = "code wiki · " + path
		}
		nodes = append(nodes, providers.Node{
			Ref:    providers.NodeRef{Kind: kind, Org: parent.Org, Project: parent.Project, Repo: w.RepositoryID, Path: path},
			Name:   w.Name,
			Detail: detail,
			Offers: []string{models.SourceDocumentation},
		})
	}
	sortNodes(nodes)
	return nodes, nil
}

// pageData is the shared shape of the paged project/definition listings.
type pageData struct {
	Value []pageEntry `json:"value"`
}

type pageEntry struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// gitItem is one entry of a repository items listing.
type gitItem struct {
	ObjectID      string `json:"objectId"`
	GitObjectType string `json:"gitObjectType"`
	Path          string `json:"path"`
	IsFolder      bool   `json:"isFolder"`
}

// pages follows x-ms-continuationtoken through every page of listURL.
func (d *discoverer) pages(ctx context.Context, listURL string, each func(pageData)) error {
	seen := map[string]bool{}
	token := ""
	for {
		pageURL := listURL
		if token != "" {
			pageURL += "&continuationToken=" + url.QueryEscape(token)
		}
		var page pageData
		header, err := d.client.GetJSON(ctx, pageURL, &page)
		if err != nil {
			return err
		}
		each(page)
		token = header.Get("x-ms-continuationtoken")
		if token == "" {
			return nil
		}
		if seen[token] {
			return errors.New("listing returned the same continuation token twice")
		}
		seen[token] = true
	}
}

func sortNodes(nodes []providers.Node) {
	sort.SliceStable(nodes, func(i, j int) bool { return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name) })
}
