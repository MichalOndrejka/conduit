// Package providers defines how Conduit talks to source systems. A saved
// models.Provider is one account (an Azure DevOps organization or server, a
// GitHub instance) with its token; an Integration knows one kind of system:
// it checks a provider, lists what the token can see (Discover), validates a
// source's selection inside it, and compiles that selection into the
// configuration the fetchers in internal/sources read.
//
// Adding a kind means implementing Integration in a new subpackage and
// listing it in the registry package.
package providers

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// Conn is a provider resolved for use: its stored definition plus its token.
type Conn struct {
	models.Provider
	Token string
}

// HTTPClient returns a client honouring the provider's TLS setting.
func (c Conn) HTTPClient() *http.Client {
	client := &http.Client{Timeout: requestTimeout}
	if c.SkipTLSVerify {
		client.Transport = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-hosted instances with private CAs
		}
	}
	return client
}

// requestTimeout bounds one HTTP request.
const requestTimeout = 60 * time.Second

// Integration is one kind of source system.
type Integration interface {
	// Kind is the stable identifier stored in models.Provider.Kind.
	Kind() string
	// Label is the display name.
	Label() string
	// DefaultBaseURL pre-fills the base URL of a new provider.
	DefaultBaseURL() string
	// BaseURLHint describes the expected base URL.
	BaseURLHint() string
	// SourceTypes lists the source types this kind can read.
	SourceTypes() []string
	// Test checks that the provider is reachable and its token accepted.
	Test(ctx context.Context, conn Conn) error
	// Discover lists the children of parent in the provider's resource tree
	// (a zero NodeRef is the root): everything the token can see, one level
	// at a time.
	Discover(ctx context.Context, conn Conn, parent NodeRef) ([]Node, error)
	// Validate checks a source's config, returning a user-facing message,
	// or "" if valid.
	Validate(srcType string, cfg map[string]string) string
	// Compile returns the configuration the fetchers in internal/sources read
	// for src: the provider's URL, TLS and token reference plus the source's
	// own selection.
	Compile(prov models.Provider, src *models.SourceDefinition) map[string]string
}

// Node kinds of a provider's resource tree.
const (
	NodeRoot           = ""
	NodeOrg            = "org"            // ADO organization / collection, GitHub owner
	NodeProject        = "project"        // ADO project
	NodeRepos          = "repos"          // group: a project's repositories
	NodeRepo           = "repo"           // repository
	NodeFolder         = "folder"         // folder inside a repository
	NodeFile           = "file"           // file inside a repository
	NodeWorkItems      = "workitems"      // group: a project's work item types
	NodeWorkItemType   = "workitemtype"   // one work item type (ID = its name)
	NodeIssues         = "issues"         // a GitHub repository's issues
	NodeIssueType      = "issuetype"      // one GitHub issue type (ID = its name)
	NodePipelines      = "pipelines"      // group: a project's / repository's pipelines
	NodePipelineFolder = "pipelinefolder" // ADO pipeline folder (Path = \-separated folder)
	NodePipeline       = "pipeline"       // one pipeline (ID = definition / workflow ID)
	NodeWikis          = "wikis"          // group: a project's wikis
	NodeWiki           = "wiki"           // one wiki (Repo = its backing repository)
)

// NodeRef locates a node in a provider's resource tree. The same fields
// become a selected source's config (Organization, Project, Repo, …), so
// selections stay provider-neutral.
type NodeRef struct {
	Kind    string `json:"kind"`
	Org     string `json:"org,omitempty"`
	Project string `json:"project,omitempty"`
	Repo    string `json:"repo,omitempty"`
	// Path is a folder/file path inside Repo ("/"-rooted), or an ADO
	// pipeline folder ("\"-separated, no leading backslash).
	Path string `json:"path,omitempty"`
	ID   string `json:"id,omitempty"` // work item / issue type name, pipeline ID
}

// Node is one entry of a Discover listing.
type Node struct {
	Ref         NodeRef `json:"ref"`
	Name        string  `json:"name"`
	Detail      string  `json:"detail,omitempty"`
	HasChildren bool    `json:"has_children"`
	// Offers lists the source types that can be selected on this node.
	Offers []string `json:"offers,omitempty"`
}

// FileOffers are the source types selectable on repositories, folders and
// files.
var FileOffers = []string{
	models.SourceCodeRepo, models.SourceTestCodeRepo, models.SourceDocumentation,
	models.SourceRequirements, models.SourceTestCase,
}

// WorkItemOffers are the source types selectable on a work item or issue
// type (and on a GitHub repository's issues).
var WorkItemOffers = []string{models.SourceWorkItemQuery, models.SourceRequirements, models.SourceTestCase}

// Source config keys. A source stores only its selection inside its
// provider; Compile turns it into what the fetchers read.
const (
	KeyOrg            = "Organization"   // ADO organization/collection under the provider URL ("" = the URL itself); GitHub owner
	KeyProject        = "Project"        // ADO project
	KeyRepo           = "Repo"           // repository
	KeyBranch         = "Branch"         // files and commits: branch ("" = the default branch)
	KeyPathFilter     = "PathFilter"     // files: comma-separated globs ("dir/**", a file path, "*.cs"); "" = every file
	KeyFetchMode      = "FetchMode"      // requirements / test cases: "files" (default) or "workitems"
	KeyWorkItemTypes  = "WorkItemTypes"  // work items: comma-separated types
	KeyAreaPaths      = "AreaPaths"      // ADO work items: comma-separated area paths (sub-areas included)
	KeyLabels         = "Labels"         // GitHub issues: comma-separated labels
	KeyTop            = "Top"            // commits and issues: max items, most recent first
	KeyRuns           = "Runs"           // pipeline runs: how many runs a listing returns by default
	KeyPipelineFolder = "PipelineFolder" // ADO pipeline runs: comma-separated folders, \-separated ("" = every pipeline of the project)
	KeyRepositories   = "Repositories"   // ADO pull requests: comma-separated repositories ("" = every repository of the project)

	FetchModeFiles     = "files"
	FetchModeWorkItems = "workitems"
)

// SettingKeys are the config keys a source's settings page edits — what a
// tick in the browse tree doesn't decide.
var SettingKeys = []string{
	KeyBranch, KeyPathFilter, KeyAreaPaths, KeyLabels, KeyTop, KeyRuns,
}

// IsWorkItemType reports whether a source fetches work items / issues.
func IsWorkItemType(srcType string, cfg map[string]string) bool {
	switch srcType {
	case models.SourceWorkItemQuery:
		return true
	case models.SourceRequirements, models.SourceTestCase:
		return cfg[KeyFetchMode] == FetchModeWorkItems
	default:
		return false
	}
}

// IsFileType reports whether a source fetches repository files.
func IsFileType(srcType string, cfg map[string]string) bool {
	switch srcType {
	case models.SourceCodeRepo, models.SourceTestCodeRepo, models.SourceDocumentation:
		return true
	case models.SourceRequirements, models.SourceTestCase:
		return cfg[KeyFetchMode] != FetchModeWorkItems
	default:
		return false
	}
}

// ParseList splits a comma-separated list into trimmed, non-empty entries.
func ParseList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CopyKeys copies the non-empty values of keys from src into dst.
func CopyKeys(dst, src map[string]string, keys ...string) {
	for _, k := range keys {
		if v := strings.TrimSpace(src[k]); v != "" {
			dst[k] = v
		}
	}
}
