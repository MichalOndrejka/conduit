// Pipeline runs, read live from Azure DevOps or GitHub Actions for the
// search_pipeline_run tool. Pipeline-run sources are not synced: a source
// only names the pipeline (definition or workflow) and the credentials, and
// every tool call asks the provider.
//
// A finished run hardly changes, so the details and logs of completed runs
// are kept in an in-process cache (see pipeline_cache.go), keyed by the
// run's finish time: a run that is rerun gets a new finish time and is read
// again. Listings and runs still in progress always go to the provider.
package sources

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

// DefaultPipelineRuns is how many runs a listing returns when neither the
// request nor the source's Runs field says otherwise.
const DefaultPipelineRuns = 5

// MaxPipelineRuns caps one listing.
const MaxPipelineRuns = 100

// PipelineRunFilter narrows a listing of a pipeline's runs, newest first.
// Values are passed to the provider as given (Azure DevOps and GitHub name
// results and statuses differently).
type PipelineRunFilter struct {
	Top          int
	Result       string
	Status       string
	Branch       string
	Reason       string // Azure DevOps reason or GitHub event
	RequestedFor string // Azure DevOps requester or GitHub actor
}

// PipelineRunSummary is one run as a listing shows it.
type PipelineRunSummary struct {
	ID           int64    `json:"id"`
	Number       string   `json:"number"`
	Pipeline     string   `json:"pipeline"`
	Title        string   `json:"title,omitempty"`
	Status       string   `json:"status"`
	Result       string   `json:"result,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	RequestedFor string   `json:"requested_for,omitempty"`
	Queued       string   `json:"queued,omitempty"`
	Started      string   `json:"started,omitempty"`
	Finished     string   `json:"finished,omitempty"`
	Duration     string   `json:"duration,omitempty"`
	Attempt      int      `json:"attempt,omitempty"` // GitHub: the latest attempt's number
	Tags         []string `json:"tags,omitempty"`
	WebURL       string   `json:"web_url"`

	ado *adoBuild
	gh  *githubRun
}

// completed reports whether the run has finished, so its details can be cached.
func (s *PipelineRunSummary) completed() bool { return strings.EqualFold(s.Status, "completed") }

// version changes whenever a finished run changes (a rerun gives it a new
// finish time and attempt).
func (s *PipelineRunSummary) version() string {
	return s.Finished + "#" + strconv.Itoa(s.Attempt)
}

// PipelineRun is one run with everything about why it failed: issues,
// failed steps of every attempt (their logs are read with Log), failed and
// flaky tests, the commits it built and the artifacts it published.
type PipelineRun struct {
	PipelineRunSummary
	Repository         string                 `json:"repository,omitempty"`
	TriggerInfo        map[string]string      `json:"trigger_info,omitempty"`
	Attempts           int                    `json:"attempts"`
	Issues             []string               `json:"issues"`
	FailedStages       []string               `json:"failed_stages,omitempty"`
	FailedJobs         []string               `json:"failed_jobs,omitempty"`
	FailedSteps        []PipelineStep         `json:"failed_steps"`
	Tests              []PipelineTest         `json:"tests"`
	TestRunAttachments []PipelineTestRunFiles `json:"test_run_attachments,omitempty"`
	Changes            []PipelineChange       `json:"changes"`
	Artifacts          []PipelineFile         `json:"artifacts"`
	// Notes say what couldn't be read (an API the server lacks, an expired
	// log); everything else is complete.
	Notes []string `json:"notes,omitempty"`
}

// PipelineStep is one failed step (Azure DevOps task or GitHub job) of any
// attempt; LogID reads its complete log.
type PipelineStep struct {
	Path        string   `json:"path"`
	Attempt     int      `json:"attempt"`
	Result      string   `json:"result"`
	FailedTasks []string `json:"failed_tasks,omitempty"` // GitHub: the job's failed steps
	LogID       int64    `json:"log_id,omitempty"`
	WebURL      string   `json:"web_url,omitempty"`
}

// PipelineTest is one failed test, or a flaky one (failed, then passed on an
// in-run retry).
type PipelineTest struct {
	Name         string              `json:"name"`
	Title        string              `json:"title,omitempty"`
	Outcome      string              `json:"outcome"`
	Flaky        bool                `json:"flaky,omitempty"`
	Attempt      int                 `json:"attempt"` // 0: the run had several attempts and this one can't be told
	Job          string              `json:"job,omitempty"`
	TestRun      string              `json:"test_run"`
	Storage      string              `json:"storage,omitempty"`
	Duration     string              `json:"duration,omitempty"`
	ErrorMessage string              `json:"error_message,omitempty"`
	StackTrace   string              `json:"stack_trace,omitempty"`
	SubResults   []PipelineSubResult `json:"sub_results,omitempty"`
	Attachments  []PipelineFile      `json:"attachments,omitempty"`
	WebURL       string              `json:"web_url"`
}

// PipelineSubResult is one attempt of a retried test or one iteration of a
// data-driven test.
type PipelineSubResult struct {
	Name         string              `json:"name"`
	Outcome      string              `json:"outcome"`
	Duration     string              `json:"duration,omitempty"`
	ErrorMessage string              `json:"error_message,omitempty"`
	StackTrace   string              `json:"stack_trace,omitempty"`
	SubResults   []PipelineSubResult `json:"sub_results,omitempty"`
}

// PipelineTestRunFiles is the attachments of one test run.
type PipelineTestRunFiles struct {
	TestRun string         `json:"test_run"`
	Files   []PipelineFile `json:"files"`
}

// PipelineChange is one commit the run built since the previous run.
type PipelineChange struct {
	ID      string `json:"id"`
	Author  string `json:"author,omitempty"`
	Date    string `json:"date,omitempty"`
	Message string `json:"message,omitempty"`
}

// PipelineFile is an artifact or attachment — metadata only, never contents.
type PipelineFile struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Size    string `json:"size,omitempty"`
	By      string `json:"by,omitempty"`
	Created string `json:"created,omitempty"`
	Comment string `json:"comment,omitempty"`
	URL     string `json:"url,omitempty"`
	Note    string `json:"note,omitempty"`
}

func pipelineFiles(atts []attachmentInfo) []PipelineFile {
	out := make([]PipelineFile, 0, len(atts))
	for _, a := range atts {
		f := PipelineFile{Name: a.Name, Kind: a.Kind, By: a.By, Created: a.Created, Comment: a.Comment, URL: a.URL, Note: a.Note}
		if a.Size >= 0 {
			f.Size = formatSize(a.Size)
		}
		out = append(out, f)
	}
	return out
}

// pipelineBackend is one provider's reads; PipelineProvider adds defaults
// and the cache.
type pipelineBackend interface {
	// cacheScope identifies the project/repository, so a run read through a
	// source and through a URL shares cache entries.
	cacheScope() string
	list(ctx context.Context, f PipelineRunFilter) ([]PipelineRunSummary, error)
	summary(ctx context.Context, id int64) (*PipelineRunSummary, error)
	detail(ctx context.Context, s *PipelineRunSummary) (*PipelineRun, error)
	log(ctx context.Context, s *PipelineRunSummary, logID int64) (string, error)
}

// PipelineProvider reads one pipeline's runs (or, resolved from a run URL,
// any run of the project or repository).
type PipelineProvider struct {
	b          pipelineBackend
	defaultTop int
}

// List returns the newest runs matching f.
func (p *PipelineProvider) List(ctx context.Context, f PipelineRunFilter) ([]PipelineRunSummary, error) {
	if f.Top <= 0 {
		f.Top = p.defaultTop
	}
	f.Top = min(f.Top, MaxPipelineRuns)
	return p.b.list(ctx, f)
}

// Get returns one run's details. cached reports a completed run served from
// the cache (one call to the provider, for the run's current state).
func (p *PipelineProvider) Get(ctx context.Context, id int64) (run *PipelineRun, cached bool, err error) {
	s, err := p.b.summary(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if !s.completed() {
		run, err = p.b.detail(ctx, s)
		return run, false, err
	}
	key := fmt.Sprintf("%s|run|%d|%s", p.b.cacheScope(), id, s.version())
	if v, ok := pipelineCache.get(key); ok {
		return v.(*PipelineRun), true, nil
	}
	if run, err = p.b.detail(ctx, s); err != nil {
		return nil, false, err
	}
	pipelineCache.put(key, run, runSize(run))
	return run, false, nil
}

// Log returns the complete, cleaned log of one step (Azure DevOps log id)
// or job (GitHub job id) of a run.
func (p *PipelineProvider) Log(ctx context.Context, runID, logID int64) (text string, cached bool, err error) {
	s, err := p.b.summary(ctx, runID)
	if err != nil {
		return "", false, err
	}
	if !s.completed() {
		text, err = p.b.log(ctx, s, logID)
		return text, false, err
	}
	key := fmt.Sprintf("%s|log|%d|%d|%s", p.b.cacheScope(), runID, logID, s.version())
	if v, ok := pipelineCache.get(key); ok {
		return v.(string), true, nil
	}
	if text, err = p.b.log(ctx, s, logID); err != nil {
		return "", false, err
	}
	pipelineCache.put(key, text, int64(len(text)))
	return text, false, nil
}

// PipelineProviderFor reads the runs of the pipeline a pipeline-run source
// names.
func PipelineProviderFor(src *models.SourceDefinition, store secrets.Reader) (*PipelineProvider, error) {
	if src.Type != models.SourcePipelineRun {
		return nil, fmt.Errorf("source %q is a %s source, not a pipeline-run source", src.Name, src.Type)
	}
	top := DefaultPipelineRuns
	if n, err := strconv.Atoi(strings.TrimSpace(src.GetConfig("Runs"))); err == nil && n > 0 {
		top = n
	}
	switch src.GetConfig("Provider") {
	case "github":
		g, err := newGitHubSource(src, store)
		if err != nil {
			return nil, err
		}
		workflow := strings.TrimSpace(src.GetConfig("Workflow"))
		if workflow == "" {
			return nil, fmt.Errorf("source %q names no workflow", src.Name)
		}
		return &PipelineProvider{b: &githubPipelineClient{g: g, workflow: workflow}, defaultTop: top}, nil
	case "", "api", "custom":
		org, project, _ := adoSourceScope(src)
		if org == "" || project == "" {
			return nil, fmt.Errorf("source %q doesn't name an Azure DevOps organization and project", src.Name)
		}
		def, err := strconv.Atoi(strings.TrimSpace(src.GetConfig("DefinitionId")))
		if err != nil || def <= 0 {
			return nil, fmt.Errorf("source %q has no numeric DefinitionId (got %q)", src.Name, src.GetConfig("DefinitionId"))
		}
		return &PipelineProvider{b: newAdoPipelineClient(src, store, org, project, def), defaultTop: top}, nil
	default:
		return nil, fmt.Errorf("source %q has provider %q, which has no pipelines", src.Name, src.GetConfig("Provider"))
	}
}

// ResolvedPipelineRun is a run URL matched to the source whose credentials
// read it.
type ResolvedPipelineRun struct {
	Provider *PipelineProvider
	RunID    int64
	Source   *models.SourceDefinition
}

// ResolvePipelineRunURL parses an Azure DevOps run link
// ({org}/{project}/_build/results?buildId=N) or a GitHub Actions run link
// ({server}/{owner}/{repo}/actions/runs/N) and picks the configured source
// whose credentials read it: a pipeline-run source of the same project or
// repository first, then any source of the same project/repository, owner
// or organization. Between equally close sources an enabled one wins.
func ResolvePipelineRunURL(rawURL string, all []models.SourceDefinition, store secrets.Reader) (*ResolvedPipelineRun, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("run URL must be an http(s) URL (got %q)", rawURL)
	}
	type candidate struct {
		src   *models.SourceDefinition
		rank  int
		id    int64
		build func() (pipelineBackend, error)
	}
	var best *candidate
	for i := range all {
		src := &all[i]
		var c *candidate
		switch src.GetConfig("Provider") {
		case "github":
			if ref, rank, ok := matchGitHubRun(src, u); ok {
				c = &candidate{src: src, rank: rank, id: ref.id, build: func() (pipelineBackend, error) {
					g, err := newGitHubSource(src, store)
					if err != nil {
						return nil, err
					}
					g.owner, g.repo = ref.owner, ref.repo
					return &githubPipelineClient{g: g}, nil
				}}
			}
		case "", "api", "custom":
			if ref, rank, ok := matchAdoRun(src, u); ok {
				c = &candidate{src: src, rank: rank, id: ref.id, build: func() (pipelineBackend, error) {
					return newAdoPipelineClient(src, store, ref.org, ref.project, 0), nil
				}}
			}
		}
		if c == nil {
			continue
		}
		c.rank *= 2
		if src.Type == models.SourcePipelineRun {
			c.rank++
		}
		c.rank *= 2
		if !src.Disabled {
			c.rank++
		}
		if best == nil || c.rank > best.rank {
			best = c
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no configured Azure DevOps or GitHub source matches %s — add a source for that "+
			"organization/server so its credentials can be used, or check the URL is a run link "+
			"(…/_build/results?buildId={id} or …/{owner}/{repo}/actions/runs/{id})", rawURL)
	}
	b, err := best.build()
	if err != nil {
		return nil, err
	}
	return &ResolvedPipelineRun{Provider: &PipelineProvider{b: b, defaultTop: DefaultPipelineRuns}, RunID: best.id, Source: best.src}, nil
}

type adoRunRef struct {
	org, project string
	id           int64
}

// matchAdoRun matches {org}/{project}/_build/results?buildId={id} (or
// …/_build?buildId=) against an Azure DevOps source's organization.
func matchAdoRun(src *models.SourceDefinition, u *url.URL) (adoRunRef, int, bool) {
	org, project, _ := adoSourceScope(src)
	if org == "" {
		return adoRunRef{}, 0, false
	}
	ou, err := url.Parse(org)
	if err != nil || !strings.EqualFold(ou.Host, u.Host) {
		return adoRunRef{}, 0, false
	}
	path := strings.Trim(u.Path, "/")
	if orgPath := strings.Trim(ou.Path, "/"); orgPath != "" {
		if !strings.HasPrefix(strings.ToLower(path), strings.ToLower(orgPath)+"/") {
			return adoRunRef{}, 0, false
		}
		path = path[len(orgPath)+1:]
	}
	segs := strings.Split(path, "/")
	if len(segs) < 2 || segs[1] != "_build" {
		return adoRunRef{}, 0, false
	}
	id, err := strconv.ParseInt(u.Query().Get("buildId"), 10, 64)
	if err != nil || id <= 0 {
		return adoRunRef{}, 0, false
	}
	ref := adoRunRef{org: org, project: segs[0], id: id}
	if p, err := url.PathUnescape(ref.project); err == nil {
		ref.project = p
	}
	rank := 1
	if strings.EqualFold(project, ref.project) {
		rank = 2
	}
	return ref, rank, true
}

type githubRunRef struct {
	owner, repo string
	id          int64
}

// matchGitHubRun matches {server}/{owner}/{repo}/actions/runs/{id}[/…]
// against a GitHub source's server.
func matchGitHubRun(src *models.SourceDefinition, u *url.URL) (githubRunRef, int, bool) {
	_, webBase, err := GitHubBases(src.GetConfig("GitHubUrl"))
	if err != nil {
		return githubRunRef{}, 0, false
	}
	wu, err := url.Parse(webBase)
	if err != nil || !strings.EqualFold(wu.Host, u.Host) {
		return githubRunRef{}, 0, false
	}
	path := strings.Trim(u.Path, "/")
	if base := strings.Trim(wu.Path, "/"); base != "" {
		rest, ok := strings.CutPrefix(path, base+"/")
		if !ok {
			return githubRunRef{}, 0, false
		}
		path = rest
	}
	segs := strings.Split(path, "/")
	if len(segs) < 5 || segs[2] != "actions" || segs[3] != "runs" {
		return githubRunRef{}, 0, false
	}
	id, err := strconv.ParseInt(segs[4], 10, 64)
	if err != nil || id <= 0 {
		return githubRunRef{}, 0, false
	}
	ref := githubRunRef{owner: segs[0], repo: segs[1], id: id}
	rank := 1
	if strings.EqualFold(src.GetConfig("GitHubOwner"), ref.owner) {
		rank = 2
		if strings.EqualFold(src.GetConfig("GitHubRepo"), ref.repo) {
			rank = 3
		}
	}
	return ref, rank, true
}
