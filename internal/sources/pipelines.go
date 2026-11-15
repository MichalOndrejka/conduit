// Pipeline runs, read live from Azure DevOps or GitHub Actions for the
// search_pipeline_run tool. Pipeline-run sources are not synced: a source
// only names the pipelines (Azure DevOps definitions by id or by folder, or
// GitHub workflows) and the credentials, and every tool call asks the provider.
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
	"slices"
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
	// Pipeline narrows a source of several pipelines to one: its id
	// (definition id or workflow id), or its name or workflow file, matched
	// exactly, case-insensitively.
	Pipeline string

	// only, set by PipelineProvider.List, is the pipeline ids the backend
	// lists runs of instead of its configured ones.
	only []int64
}

// PipelineInfo is one pipeline a source covers.
type PipelineInfo struct {
	ID     int64  `json:"id"`             // Azure DevOps definition id, GitHub workflow id
	Name   string `json:"name,omitempty"` // as the provider names it
	Folder string `json:"folder,omitempty"`
	File   string `json:"file,omitempty"` // GitHub: the workflow file, e.g. ci.yml
	WebURL string `json:"web_url,omitempty"`
	// Note says why a configured pipeline couldn't be read (deleted, or not
	// visible to the source's credentials).
	Note string `json:"note,omitempty"`
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
	// Run context: the pipeline file and what the run was queued with.
	PipelineFile   string            `json:"pipeline_file,omitempty"`   // YAML path (Azure DevOps YAML pipelines, GitHub workflows)
	Parameters     map[string]string `json:"parameters,omitempty"`      // Azure DevOps template parameters
	QueueVariables map[string]string `json:"queue_variables,omitempty"` // Azure DevOps variables set at queue time
	// Variables are the Azure DevOps pipeline's variables at the run's
	// revision; secret values read "***".
	Variables map[string]string  `json:"variables,omitempty"`
	WorkItems []PipelineWorkItem `json:"work_items,omitempty"` // Azure DevOps work items linked to the run
	// Notes say what couldn't be read (an API the server lacks, an expired
	// log); everything else is complete.
	Notes []string `json:"notes,omitempty"`
}

// PipelineWorkItem is a work item linked to a run.
type PipelineWorkItem struct {
	ID     string `json:"id"`
	WebURL string `json:"web_url,omitempty"`
}

// PipelineStepNode is one record of a run's tree, of any result and
// attempt: Azure DevOps stage, job or task; GitHub job or step. LogID reads
// its log (a GitHub step's is its job's log).
type PipelineStepNode struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Attempt  int    `json:"attempt"`
	State    string `json:"state,omitempty"`
	Result   string `json:"result,omitempty"`
	Started  string `json:"started,omitempty"`
	Finished string `json:"finished,omitempty"`
	Duration string `json:"duration,omitempty"`
	Errors   int    `json:"errors,omitempty"`
	Warnings int    `json:"warnings,omitempty"`
	LogID    int64  `json:"log_id,omitempty"`
	WebURL   string `json:"web_url,omitempty"`
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

	// testRunID and resultID read the result's attachments (Azure DevOps).
	testRunID, resultID int
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
	// pipelinesKey identifies the source's pipeline selection within the
	// cache scope.
	pipelinesKey() string
	// pipelines reads the pipelines the source covers.
	pipelines(ctx context.Context) ([]PipelineInfo, error)
	// resolvesPipelines reports whether listing runs needs pipelines first
	// (an Azure DevOps folder: its pipelines' ids).
	resolvesPipelines() bool
	list(ctx context.Context, f PipelineRunFilter) ([]PipelineRunSummary, error)
	// steps reads every stage/job/task (GitHub job/step) of every attempt.
	steps(ctx context.Context, s *PipelineRunSummary) ([]PipelineStepNode, error)
	// tests reads every test result of the run, of any outcome.
	tests(ctx context.Context, s *PipelineRunSummary) ([]PipelineTest, error)
	// resultAttachments reads one test result's attachments.
	resultAttachments(ctx context.Context, testRunID, resultID int) ([]PipelineFile, []string, error)
	summary(ctx context.Context, id int64) (*PipelineRunSummary, error)
	detail(ctx context.Context, s *PipelineRunSummary) (*PipelineRun, error)
	log(ctx context.Context, s *PipelineRunSummary, logID int64) (string, error)
}

// PipelineProvider reads the runs of a source's pipelines (or, resolved from
// a run URL, any run of the project or repository).
type PipelineProvider struct {
	b          pipelineBackend
	defaultTop int
}

// List returns the newest runs matching f, of f.Pipeline only when given.
func (p *PipelineProvider) List(ctx context.Context, f PipelineRunFilter) ([]PipelineRunSummary, error) {
	if f.Top <= 0 {
		f.Top = p.defaultTop
	}
	f.Top = min(f.Top, MaxPipelineRuns)
	f.only = nil
	if sel := strings.TrimSpace(f.Pipeline); sel != "" || p.b.resolvesPipelines() {
		all, _, err := p.Pipelines(ctx)
		if err != nil {
			return nil, err
		}
		if sel != "" {
			m, err := matchPipeline(all, sel)
			if err != nil {
				return nil, err
			}
			f.only = []int64{m.ID}
		} else {
			for _, pi := range all {
				f.only = append(f.only, pi.ID)
			}
			slices.Sort(f.only)
		}
	}
	return p.b.list(ctx, f)
}

// Pipelines returns the pipelines the source covers. They are kept for
// pipelineListTTL, so a folder's pipelines aren't looked up on every call;
// cached reports a kept answer.
func (p *PipelineProvider) Pipelines(ctx context.Context) (infos []PipelineInfo, cached bool, err error) {
	key := p.b.cacheScope() + "|pipelines|" + p.b.pipelinesKey()
	if v, ok := shortCache.get(key); ok {
		return v.([]PipelineInfo), true, nil
	}
	if infos, err = p.b.pipelines(ctx); err != nil {
		return nil, false, err
	}
	shortCache.put(key, infos, pipelineListTTL)
	return infos, false, nil
}

// matchPipeline finds the one pipeline sel names: its id, or its name or
// workflow file, compared case-insensitively.
func matchPipeline(all []PipelineInfo, sel string) (PipelineInfo, error) {
	var hits []PipelineInfo
	if id, err := strconv.ParseInt(sel, 10, 64); err == nil {
		for _, pi := range all {
			if pi.ID == id {
				hits = append(hits, pi)
			}
		}
	}
	if len(hits) == 0 {
		for _, pi := range all {
			if strings.EqualFold(pi.Name, sel) || (pi.File != "" && strings.EqualFold(pi.File, sel)) {
				hits = append(hits, pi)
			}
		}
	}
	describe := func(list []PipelineInfo) string {
		parts := make([]string, len(list))
		for i, pi := range list {
			parts[i] = fmt.Sprintf("%s (id %d", orDefault(pi.Name, "?"), pi.ID)
			if pi.Folder != "" {
				parts[i] += ", folder " + pi.Folder
			}
			if pi.File != "" {
				parts[i] += ", file " + pi.File
			}
			parts[i] += ")"
		}
		return strings.Join(parts, "; ")
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return PipelineInfo{}, fmt.Errorf("no pipeline of this source is %q; its pipelines: %s", sel, describe(all))
	default:
		return PipelineInfo{}, fmt.Errorf("%q names several pipelines — pass one's id: %s", sel, describe(hits))
	}
}

// Get returns one run's details. cached reports details served from the
// cache. A failed test's attachments are not included: TestAttachments reads
// them for the tests a page shows.
func (p *PipelineProvider) Get(ctx context.Context, id int64) (*PipelineRun, bool, error) {
	return runCached(ctx, p, id, "run", p.b.detail)
}

// Steps returns every stage, job and task (GitHub job and step) of every
// attempt of a run, whatever their result.
func (p *PipelineProvider) Steps(ctx context.Context, runID int64) ([]PipelineStepNode, bool, error) {
	return runCached(ctx, p, runID, "steps", p.b.steps)
}

// Tests returns every test result of a run, whatever its outcome.
func (p *PipelineProvider) Tests(ctx context.Context, runID int64) ([]PipelineTest, bool, error) {
	return runCached(ctx, p, runID, "tests", p.b.tests)
}

// Log returns the complete, cleaned log of one step (Azure DevOps log id)
// or job (GitHub job id) of a run.
func (p *PipelineProvider) Log(ctx context.Context, runID, logID int64) (string, bool, error) {
	return runCached(ctx, p, runID, fmt.Sprintf("log|%d", logID), func(ctx context.Context, s *PipelineRunSummary) (string, error) {
		return p.b.log(ctx, s, logID)
	})
}

// testAttachmentEntry is one test result's cached attachments.
type testAttachmentEntry struct {
	files []PipelineFile
	notes []string
}

// TestAttachments returns tests (a page of Get's tests) with each one's
// attachments, read one test at a time — only for the tests shown, never for
// every failed test of a run — and kept like the run's details.
func (p *PipelineProvider) TestAttachments(ctx context.Context, runID int64, tests []PipelineTest) ([]PipelineTest, []string, error) {
	s, err := p.summary(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	out := slices.Clone(tests)
	var notes []string
	for i := range out {
		t := &out[i]
		if t.resultID == 0 {
			continue
		}
		key := fmt.Sprintf("%s|testatt|%d|%d|%d|%s", p.b.cacheScope(), runID, t.testRunID, t.resultID, s.version())
		if !s.completed() {
			key = fmt.Sprintf("%s|testatt|%d|%d|%d|live", p.b.cacheScope(), runID, t.testRunID, t.resultID)
		}
		var e testAttachmentEntry
		if v, ok := pipelineCache.get(key); ok {
			e = v.(testAttachmentEntry)
		} else {
			if e.files, e.notes, err = p.b.resultAttachments(ctx, t.testRunID, t.resultID); err != nil {
				return nil, nil, fmt.Errorf("attachments of test %s: %w", t.Name, err)
			}
			if s.completed() {
				pipelineCache.put(key, e, jsonSize(e.files)+jsonSize(e.notes))
			}
		}
		t.Attachments = e.files
		for _, n := range e.notes {
			if !slices.Contains(notes, n) {
				notes = append(notes, n)
			}
		}
	}
	return out, notes, nil
}

// summary reads a run's current state. A completed run's is kept for
// runStateTTL, so paging through its cached details or logs asks the
// provider nothing; a run in progress is always asked.
func (p *PipelineProvider) summary(ctx context.Context, id int64) (*PipelineRunSummary, error) {
	key := fmt.Sprintf("%s|summary|%d", p.b.cacheScope(), id)
	if v, ok := shortCache.get(key); ok {
		return v.(*PipelineRunSummary), nil
	}
	s, err := p.b.summary(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.completed() {
		shortCache.put(key, s, runStateTTL)
	}
	return s, nil
}

// runCached reads something of a run. For a completed run it is kept until
// the run changes (a rerun gives it a new version); for a run in progress,
// for liveRunTTL. cached reports a kept answer.
func runCached[T any](ctx context.Context, p *PipelineProvider, runID int64, kind string,
	read func(context.Context, *PipelineRunSummary) (T, error)) (T, bool, error) {
	var zero T
	s, err := p.summary(ctx, runID)
	if err != nil {
		return zero, false, err
	}
	if !s.completed() {
		key := fmt.Sprintf("%s|%s|%d|live|%s", p.b.cacheScope(), kind, runID, s.Status)
		if v, ok := shortCache.get(key); ok {
			return v.(T), true, nil
		}
		v, err := read(ctx, s)
		if err != nil {
			return zero, false, err
		}
		shortCache.put(key, v, liveRunTTL)
		return v, false, nil
	}
	key := fmt.Sprintf("%s|%s|%d|%s", p.b.cacheScope(), kind, runID, s.version())
	if v, ok := pipelineCache.get(key); ok {
		return v.(T), true, nil
	}
	v, err := read(ctx, s)
	if err != nil {
		return zero, false, err
	}
	pipelineCache.put(key, v, jsonSize(v))
	return v, false, nil
}

// PipelineProviderFor reads the runs of the pipelines a pipeline-run source
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
		workflows := SplitPipelineList(src.GetConfig("Workflow"))
		if len(workflows) == 0 {
			return nil, fmt.Errorf("source %q names no workflow", src.Name)
		}
		return &PipelineProvider{b: &githubPipelineClient{g: g, workflows: workflows}, defaultTop: top}, nil
	case "", "api", "custom":
		org, project, _ := adoSourceScope(src)
		if org == "" || project == "" {
			return nil, fmt.Errorf("source %q doesn't name an Azure DevOps organization and project", src.Name)
		}
		c := newAdoPipelineClient(src, store, org, project)
		if folder := strings.TrimSpace(src.GetConfig("PipelineFolder")); folder != "" {
			c.folder = NormalizePipelineFolder(folder)
		} else {
			ids, err := ParseDefinitionIDs(src.GetConfig("DefinitionId"))
			if err != nil {
				return nil, fmt.Errorf("source %q: %w", src.Name, err)
			}
			c.definitionIDs = ids
		}
		return &PipelineProvider{b: c, defaultTop: top}, nil
	default:
		return nil, fmt.Errorf("source %q has provider %q, which has no pipelines", src.Name, src.GetConfig("Provider"))
	}
}

// SplitPipelineList splits a comma-separated list of pipeline ids or
// workflows, dropping blanks and repeats.
func SplitPipelineList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[strings.ToLower(part)] {
			continue
		}
		seen[strings.ToLower(part)] = true
		out = append(out, part)
	}
	return out
}

// ParseDefinitionIDs parses DefinitionId: one or more comma-separated
// positive Azure DevOps pipeline definition ids.
func ParseDefinitionIDs(s string) ([]int, error) {
	parts := SplitPipelineList(s)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no pipeline definition id given")
	}
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("pipeline definition id %q is not a positive number", p)
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// NormalizePipelineFolder turns a pipeline folder as typed ("Release/Services",
// "\Release\Services\") into Azure DevOps' form ("\Release\Services"); the
// root is "\".
func NormalizePipelineFolder(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "/", `\`)
	return `\` + strings.Trim(s, `\`)
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
					return newAdoPipelineClient(src, store, ref.org, ref.project), nil
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
