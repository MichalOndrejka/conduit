// Azure DevOps pipeline runs, read live (see pipelines.go). A run's details
// cover every attempt: a run that went green after "Rerun failed jobs"
// still shows attempt 1's failed steps (from the previous-attempt
// timelines) and failed tests, and a test that passed only on an in-run
// retry (VSTest rerunFailedTests and the like, recorded as sub-results) is
// listed as flaky. The one shortcut: a green build with a single attempt
// costs only its timeline call — its test runs aren't checked for in-run
// retries.
//
// Nothing is dropped: every failed test result and every failed step is
// listed, and a step's whole log is read on request. Anything that can't be
// read fails the call, except listings an older server lacks, which are
// noted.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/secrets"
)

const (
	// adoTestRunsPageSize / adoTestResultsPageSize page the test APIs via
	// $top/$skip until a short page.
	adoTestRunsPageSize    = 100
	adoTestResultsPageSize = 1000

	// adoFailedTestOutcomes are the test outcomes listed as failed tests.
	adoFailedTestOutcomes = "Failed,Aborted,Error,Timeout"
)

// isFailedTestOutcome reports whether a test (or sub-result) outcome is one
// of adoFailedTestOutcomes.
func isFailedTestOutcome(o string) bool {
	switch strings.ToLower(o) {
	case "failed", "aborted", "error", "timeout":
		return true
	}
	return false
}

type adoBuild struct {
	ID            int    `json:"id"`
	BuildNumber   string `json:"buildNumber"`
	Status        string `json:"status"`
	Result        string `json:"result"`
	Reason        string `json:"reason"`
	SourceBranch  string `json:"sourceBranch"`
	SourceVersion string `json:"sourceVersion"`
	QueueTime     string `json:"queueTime"`
	StartTime     string `json:"startTime"`
	FinishTime    string `json:"finishTime"`
	Definition    struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Revision int    `json:"revision"`
	} `json:"definition"`
	// Parameters is the JSON object of variables set at queue time.
	Parameters         string         `json:"parameters"`
	TemplateParameters map[string]any `json:"templateParameters"`
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
	Repository struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"repository"`
	RequestedFor struct {
		DisplayName string `json:"displayName"`
		UniqueName  string `json:"uniqueName"`
	} `json:"requestedFor"`
	TriggerInfo map[string]string `json:"triggerInfo"`
	Tags        []string          `json:"tags"`
	Links       struct {
		Web struct {
			Href string `json:"href"`
		} `json:"web"`
	} `json:"_links"`
}

type adoTimelineRecord struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Result   string `json:"result"`
	State    string `json:"state"`
	Order    int    `json:"order"`
	Attempt  int    `json:"attempt"`
	// StartTime / FinishTime place a job execution in time, to match a test
	// run to the attempt that produced it when the run doesn't say.
	StartTime  string `json:"startTime"`
	FinishTime string `json:"finishTime"`
	Log        *struct {
		ID int `json:"id"`
	} `json:"log"`
	Issues []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"issues"`
	// PreviousAttempts points at the earlier attempts of a rerun job/stage;
	// each lives in its own timeline.
	PreviousAttempts []struct {
		Attempt    int    `json:"attempt"`
		TimelineID string `json:"timelineId"`
		RecordID   string `json:"recordId"`
	} `json:"previousAttempts"`
}

// attempt is the record's attempt number (ADO omits it on old servers).
func (r adoTimelineRecord) attempt() int { return max(r.Attempt, 1) }

type adoTestRun struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	StartedDate   string `json:"startedDate"`
	CompletedDate string `json:"completedDate"`
	// PipelineReference names the stage, phase and job — with their
	// attempts — whose task published the run (YAML pipelines).
	PipelineReference struct {
		StageReference struct {
			StageName string `json:"stageName"`
			Attempt   int    `json:"attempt"`
		} `json:"stageReference"`
		PhaseReference struct {
			PhaseName string `json:"phaseName"`
			Attempt   int    `json:"attempt"`
		} `json:"phaseReference"`
		JobReference struct {
			JobName string `json:"jobName"`
			Attempt int    `json:"attempt"`
		} `json:"jobReference"`
	} `json:"pipelineReference"`
	// RunStatistics (requested with includeRunDetails) count results by
	// outcome; ResultMetadata "rerun"/"flaky" flags results that were retried.
	RunStatistics []struct {
		Outcome        string `json:"outcome"`
		Count          int    `json:"count"`
		ResultMetadata string `json:"resultMetadata"`
	} `json:"runStatistics"`
}

// mayHaveRetriedResults reports whether the run may hold results that
// passed only after a retry. Without statistics it can't tell, so it says
// yes and the passed results are checked (lossless over cheap).
func (r adoTestRun) mayHaveRetriedResults() bool {
	if len(r.RunStatistics) == 0 {
		return true
	}
	for _, s := range r.RunStatistics {
		switch strings.ToLower(s.ResultMetadata) {
		case "rerun", "flaky":
			if s.Count > 0 {
				return true
			}
		}
	}
	return false
}

// mayHaveFailedResults reports whether the run may hold failed results.
// Without statistics it can't tell, so it says yes.
func (r adoTestRun) mayHaveFailedResults() bool {
	if len(r.RunStatistics) == 0 {
		return true
	}
	for _, s := range r.RunStatistics {
		if isFailedTestOutcome(s.Outcome) && s.Count > 0 {
			return true
		}
	}
	return false
}

type adoTestResult struct {
	ID                   int                `json:"id"`
	TestCaseTitle        string             `json:"testCaseTitle"`
	AutomatedTestName    string             `json:"automatedTestName"`
	AutomatedTestStorage string             `json:"automatedTestStorage"`
	Outcome              string             `json:"outcome"`
	DurationInMs         float64            `json:"durationInMs"`
	ErrorMessage         string             `json:"errorMessage"`
	StackTrace           string             `json:"stackTrace"`
	ResultGroupType      string             `json:"resultGroupType"`
	SubResults           []adoTestSubResult `json:"subResults"`
}

// adoTestSubResult is one attempt of a retried test (resultGroupType
// "rerun") or one iteration of a data-driven/ordered test.
type adoTestSubResult struct {
	ID           int                `json:"id"`
	DisplayName  string             `json:"displayName"`
	Outcome      string             `json:"outcome"`
	DurationInMs float64            `json:"durationInMs"`
	ErrorMessage string             `json:"errorMessage"`
	StackTrace   string             `json:"stackTrace"`
	SubResults   []adoTestSubResult `json:"subResults"`
}

// hasFailedSubResult reports whether any (nested) sub-result failed.
func hasFailedSubResult(subs []adoTestSubResult) bool {
	for _, s := range subs {
		if isFailedTestOutcome(s.Outcome) || hasFailedSubResult(s.SubResults) {
			return true
		}
	}
	return false
}

func testNameOf(r adoTestResult) string {
	if r.AutomatedTestName != "" {
		return r.AutomatedTestName
	}
	return r.TestCaseTitle
}

// adoPipelineClient reads one Azure DevOps project's runs. Listings are
// scoped to definitionIDs, or to every pipeline under folder (subfolders
// included, resolved on each listing so pipelines added later count); a
// client resolved from a run URL has neither and can't list.
type adoPipelineClient struct {
	api           *APISource // auth, headers and TLS settings of the matched source
	client        *http.Client
	org           string
	project       string
	version       string
	definitionIDs []int
	folder        string
}

func newAdoPipelineClient(src *models.SourceDefinition, store secrets.Reader, org, project string) *adoPipelineClient {
	api := &APISource{src: src, secrets: store}
	return &adoPipelineClient{
		api: api, client: api.httpClient(),
		org: strings.TrimRight(org, "/"), project: project,
		version: adoAPIVersion(src),
	}
}

// adoDefinitionRef is one pipeline definition as the definitions listing
// returns it.
type adoDefinitionRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// inFolder reports whether a definition path lies in folder or below it.
func inFolder(path, folder string) bool {
	path = NormalizePipelineFolder(path)
	if folder == `\` {
		return true
	}
	return strings.EqualFold(path, folder) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(folder)+`\`)
}

func (c *adoPipelineClient) pipelinesKey() string {
	if c.folder != "" {
		return "folder|" + strings.ToLower(c.folder)
	}
	ids := make([]string, len(c.definitionIDs))
	for i, id := range c.definitionIDs {
		ids[i] = strconv.Itoa(id)
	}
	return "ids|" + strings.Join(ids, ",")
}

// resolvesPipelines: a folder's runs are listed by its pipelines' ids.
func (c *adoPipelineClient) resolvesPipelines() bool { return c.folder != "" }

// pipelines reads the source's pipelines: the folder's (subfolders
// included), sorted by folder and name, or the configured ids in their
// order — an id the server doesn't return is kept with a note.
func (c *adoPipelineClient) pipelines(ctx context.Context) ([]PipelineInfo, error) {
	info := func(d adoDefinitionRef) PipelineInfo {
		return PipelineInfo{ID: int64(d.ID), Name: d.Name, Folder: NormalizePipelineFolder(d.Path),
			WebURL: fmt.Sprintf("%s/_build?definitionId=%d", c.webBase(), d.ID)}
	}
	if c.folder != "" {
		defs, err := c.folderDefinitions(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]PipelineInfo, 0, len(defs))
		for _, d := range defs {
			out = append(out, info(d))
		}
		sort.SliceStable(out, func(i, j int) bool {
			if fi, fj := strings.ToLower(out[i].Folder), strings.ToLower(out[j].Folder); fi != fj {
				return fi < fj
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
		return out, nil
	}
	if len(c.definitionIDs) == 0 {
		return nil, fmt.Errorf("no pipeline definition to list — use a pipeline-run source")
	}
	byID := map[int]adoDefinitionRef{}
	for start := 0; start < len(c.definitionIDs); start += adoDefinitionsPerListing {
		batch := c.definitionIDs[start:min(start+adoDefinitionsPerListing, len(c.definitionIDs))]
		ids := make([]string, len(batch))
		for i, id := range batch {
			ids[i] = strconv.Itoa(id)
		}
		q := url.Values{}
		q.Set("definitionIds", strings.Join(ids, ","))
		if err := c.pageDefinitions(ctx, q, "pipelines "+strings.Join(ids, ","), func(d adoDefinitionRef) { byID[d.ID] = d }); err != nil {
			return nil, err
		}
	}
	out := make([]PipelineInfo, 0, len(c.definitionIDs))
	for _, id := range c.definitionIDs {
		if d, ok := byID[id]; ok {
			out = append(out, info(d))
		} else {
			out = append(out, PipelineInfo{ID: int64(id), WebURL: fmt.Sprintf("%s/_build?definitionId=%d", c.webBase(), id),
				Note: "not returned by Azure DevOps: deleted, or not visible to this source's credentials"})
		}
	}
	return out, nil
}

// folderDefinitions returns the pipelines in c.folder and its subfolders.
// Azure DevOps filters by folder itself (definitions?path=), so the
// project's other pipelines are never listed. Whether that filter takes in
// subfolders isn't documented: when the folder's listing holds a pipeline of
// a subfolder it did; otherwise the folder's subfolders are read from the
// folders API and each is listed the same way.
func (c *adoPipelineClient) folderDefinitions(ctx context.Context) ([]adoDefinitionRef, error) {
	found := map[int]adoDefinitionRef{}
	recursive, err := c.definitionsInPath(ctx, c.folder, found)
	if err != nil {
		return nil, err
	}
	if !recursive {
		subs, err := c.subfolders(ctx)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			if _, err := c.definitionsInPath(ctx, sub, found); err != nil {
				return nil, err
			}
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no pipeline is in folder %s (or its subfolders)", c.folder)
	}
	out := make([]adoDefinitionRef, 0, len(found))
	for _, d := range found {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// definitionsInPath lists definitions?path=folder, adding to found those
// in c.folder or below it (the server's path filter is checked, not
// trusted). It reports whether any lay in a subfolder of folder.
func (c *adoPipelineClient) definitionsInPath(ctx context.Context, folder string, found map[int]adoDefinitionRef) (bool, error) {
	below := false
	q := url.Values{}
	q.Set("path", folder)
	err := c.pageDefinitions(ctx, q, "pipelines in folder "+folder, func(d adoDefinitionRef) {
		if !inFolder(d.Path, c.folder) {
			return
		}
		found[d.ID] = d
		if !strings.EqualFold(NormalizePipelineFolder(d.Path), folder) && inFolder(d.Path, folder) {
			below = true
		}
	})
	return below, err
}

// pageDefinitions pages through a filtered definitions listing. queryOrder
// is set because the continuation token of an unordered listing is a
// timestamp the server rejects as ambiguous on the next page.
func (c *adoPipelineClient) pageDefinitions(ctx context.Context, q url.Values, what string, each func(adoDefinitionRef)) error {
	q.Set("queryOrder", "definitionNameAscending")
	q.Set("$top", "1000")
	q.Set("api-version", c.version)
	base := c.apiBase() + "/build/definitions?" + q.Encode()
	token := ""
	seen := map[string]bool{}
	for {
		u := base
		if token != "" {
			u += "&continuationToken=" + url.QueryEscape(token)
		}
		data, header, err := c.api.getRawWithHeader(ctx, c.client, u)
		if err != nil {
			return fmt.Errorf("listing %s: %w", what, err)
		}
		var page struct {
			Value []adoDefinitionRef `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return fmt.Errorf("definitions response is not valid JSON: %w", err)
		}
		for _, d := range page.Value {
			each(d)
		}
		token = header.Get("x-ms-continuationtoken")
		if token == "" {
			return nil
		}
		if seen[token] {
			return fmt.Errorf("definitions endpoint returned continuation token %q twice", token)
		}
		seen[token] = true
	}
}

// subfolders lists the folders below c.folder (any depth) from the build
// folders API, a preview API on every version.
func (c *adoPipelineClient) subfolders(ctx context.Context) ([]string, error) {
	q := url.Values{}
	q.Set("queryOrder", "folderAscending")
	q.Set("api-version", previewVersion(c.version))
	var page struct {
		Value []struct {
			Path string `json:"path"`
		} `json:"value"`
	}
	u := c.apiBase() + "/build/folders/" + url.PathEscape(c.folder) + "?" + q.Encode()
	if err := c.getJSON(ctx, u, &page); err != nil {
		return nil, fmt.Errorf("listing subfolders of %s: %w", c.folder, err)
	}
	var out []string
	for _, f := range page.Value {
		p := NormalizePipelineFolder(f.Path)
		if !strings.EqualFold(p, c.folder) && inFolder(p, c.folder) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *adoPipelineClient) apiBase() string {
	return c.org + "/" + url.PathEscape(c.project) + "/_apis"
}

func (c *adoPipelineClient) webBase() string {
	return c.org + "/" + url.PathEscape(c.project)
}

func (c *adoPipelineClient) cacheScope() string { return "ado|" + c.webBase() }

func (c *adoPipelineClient) get(ctx context.Context, u string) ([]byte, error) {
	return c.api.getRaw(ctx, c.client, u)
}

func (c *adoPipelineClient) getJSON(ctx context.Context, u string, v any) error {
	data, err := c.get(ctx, u)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("response from %s is not valid JSON: %w", u, err)
	}
	return nil
}

// adoDefinitionsPerListing caps the definition ids one builds call names,
// keeping its query string well under IIS's default 2048-byte limit on
// Azure DevOps Server.
const adoDefinitionsPerListing = 100

// list returns the newest runs of the source's pipelines, or of f.only when
// set (a folder's pipelines, or the one f.Pipeline names). The builds API
// merges the runs of the definitions one call names; more definitions than
// one call takes are listed in batches, each batch's newest f.Top runs then
// merged newest first and cut to f.Top.
func (c *adoPipelineClient) list(ctx context.Context, f PipelineRunFilter) ([]PipelineRunSummary, error) {
	ids := c.definitionIDs
	if f.only != nil {
		ids = make([]int, len(f.only))
		for i, id := range f.only {
			ids[i] = int(id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no pipeline definition to list — use a pipeline-run source")
	}
	out := []PipelineRunSummary{}
	for start := 0; start < len(ids); start += adoDefinitionsPerListing {
		runs, err := c.listDefinitions(ctx, ids[start:min(start+adoDefinitionsPerListing, len(ids))], f)
		if err != nil {
			return nil, err
		}
		out = append(out, runs...)
	}
	if len(ids) > adoDefinitionsPerListing {
		sort.SliceStable(out, func(i, j int) bool {
			ti, tj := parseTime(out[i].Queued), parseTime(out[j].Queued)
			if !ti.Equal(tj) {
				return ti.After(tj)
			}
			return out[i].ID > out[j].ID
		})
		if len(out) > f.Top {
			out = out[:f.Top]
		}
	}
	return out, nil
}

// listDefinitions is one builds call: the newest f.Top runs across ids.
func (c *adoPipelineClient) listDefinitions(ctx context.Context, ids []int, f PipelineRunFilter) ([]PipelineRunSummary, error) {
	defs := make([]string, len(ids))
	for i, id := range ids {
		defs[i] = strconv.Itoa(id)
	}
	q := url.Values{}
	q.Set("definitions", strings.Join(defs, ","))
	q.Set("queryOrder", "queueTimeDescending")
	q.Set("$top", strconv.Itoa(f.Top))
	q.Set("api-version", c.version)
	if f.Result != "" {
		q.Set("resultFilter", f.Result)
	}
	if f.Status != "" {
		q.Set("statusFilter", f.Status)
	}
	if f.Reason != "" {
		q.Set("reasonFilter", f.Reason)
	}
	if f.RequestedFor != "" {
		q.Set("requestedFor", f.RequestedFor)
	}
	if b := strings.TrimSpace(f.Branch); b != "" {
		if !strings.HasPrefix(b, "refs/") {
			b = "refs/heads/" + b
		}
		q.Set("branchName", b)
	}
	var page struct {
		Value []adoBuild `json:"value"`
	}
	if err := c.getJSON(ctx, c.apiBase()+"/build/builds?"+q.Encode(), &page); err != nil {
		return nil, fmt.Errorf("listing pipeline runs: %w", err)
	}
	out := make([]PipelineRunSummary, 0, len(page.Value))
	for i := range page.Value {
		out = append(out, c.summaryOf(&page.Value[i]))
	}
	return out, nil
}

func (c *adoPipelineClient) summary(ctx context.Context, id int64) (*PipelineRunSummary, error) {
	var b adoBuild
	if err := c.getJSON(ctx, fmt.Sprintf("%s/build/builds/%d?api-version=%s", c.apiBase(), id, c.version), &b); err != nil {
		return nil, fmt.Errorf("reading run %d: %w", id, err)
	}
	s := c.summaryOf(&b)
	return &s, nil
}

func (c *adoPipelineClient) summaryOf(b *adoBuild) PipelineRunSummary {
	web := b.Links.Web.Href
	if !isWebURL(web) {
		web = fmt.Sprintf("%s/_build/results?buildId=%d", c.webBase(), b.ID)
	}
	s := PipelineRunSummary{
		ID: int64(b.ID), Number: b.BuildNumber, Pipeline: b.Definition.Name,
		Status: b.Status, Result: b.Result, Reason: b.Reason,
		Branch: b.SourceBranch, Commit: b.SourceVersion,
		RequestedFor: strings.TrimSpace(b.RequestedFor.DisplayName + " " + angle(b.RequestedFor.UniqueName)),
		Queued:       b.QueueTime, Started: b.StartTime, Finished: b.FinishTime,
		Tags: b.Tags, WebURL: web, ado: b,
	}
	if start, finish := parseTime(b.StartTime), parseTime(b.FinishTime); !start.IsZero() && !finish.IsZero() {
		s.Duration = finish.Sub(start).Round(time.Second).String()
	}
	return s
}

func (c *adoPipelineClient) log(ctx context.Context, s *PipelineRunSummary, logID int64) (string, error) {
	data, err := c.get(ctx, fmt.Sprintf("%s/build/builds/%d/logs/%d?api-version=%s", c.apiBase(), s.ID, logID, c.version))
	if err != nil {
		return "", fmt.Errorf("reading log %d of run %d: %w", logID, s.ID, err)
	}
	return cleanLogText(string(data)), nil
}

// detail reads a run's timeline issues, failed steps of every attempt,
// failed and flaky tests, test attachments, changes and artifacts.
func (c *adoPipelineClient) detail(ctx context.Context, s *PipelineRunSummary) (*PipelineRun, error) {
	b := s.ado
	run := &PipelineRun{
		PipelineRunSummary: *s, Repository: b.Repository.Name, TriggerInfo: b.TriggerInfo,
		Attempts: 1, Issues: []string{}, FailedSteps: []PipelineStep{}, Tests: []PipelineTest{},
	}
	if err := c.readTimelinesAndTests(ctx, b, run); err != nil {
		return nil, err
	}
	if err := c.readContext(ctx, b, run); err != nil {
		return nil, err
	}

	changes, err := c.buildChanges(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	run.Changes = changes
	arts, notes, err := c.buildArtifacts(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	run.Artifacts = pipelineFiles(arts)
	run.Notes = append(run.Notes, notes...)
	return run, nil
}

// allTimelines reads the build's latest timeline, which holds each record's
// newest attempt, and the timelines of every earlier attempt of rerun
// jobs/stages, which their previousAttempts point to (and those may point
// further back in turn).
func (c *adoPipelineClient) allTimelines(ctx context.Context, buildID int) (*timeline, []*timeline, error) {
	current, records, err := c.timeline(ctx, buildID, "")
	if err != nil {
		return nil, nil, err
	}
	latest := newTimeline(records)
	timelines := []*timeline{latest}
	seenTimeline := map[string]bool{current: true}
	for i := 0; i < len(timelines); i++ {
		for _, r := range timelines[i].ordered {
			for _, pa := range r.PreviousAttempts {
				if pa.TimelineID == "" || seenTimeline[pa.TimelineID] {
					continue
				}
				seenTimeline[pa.TimelineID] = true
				_, recs, err := c.timeline(ctx, buildID, pa.TimelineID)
				if err != nil {
					return nil, nil, fmt.Errorf("attempt %d of %q: %w", pa.Attempt, r.Name, err)
				}
				timelines = append(timelines, newTimeline(recs))
			}
		}
	}
	return latest, timelines, nil
}

// maxAttempt is the run's highest attempt number.
func maxAttempt(timelines []*timeline) int {
	n := 1
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			n = max(n, r.attempt())
		}
	}
	return n
}

// stepWebURL links a record's log view: its job, and the task within it.
func (c *adoPipelineClient) stepWebURL(buildID int, latest, tl *timeline, r adoTimelineRecord) string {
	jobID := latest.jobID(r)
	if jobID == "" {
		jobID = tl.jobID(r)
	}
	web := fmt.Sprintf("%s/_build/results?buildId=%d&view=logs", c.webBase(), buildID)
	if jobID != "" {
		web += "&j=" + jobID
	}
	if jobID != r.ID {
		web += "&t=" + r.ID
	}
	return web
}

// steps lists every stage, job and task of every attempt, newest attempt's
// tree first. Phase and checkpoint records are structural and left out.
func (c *adoPipelineClient) steps(ctx context.Context, s *PipelineRunSummary) ([]PipelineStepNode, error) {
	b := s.ado
	latest, timelines, err := c.allTimelines(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	type execKey struct {
		id      string
		attempt int
	}
	seen := map[execKey]bool{}
	out := []PipelineStepNode{}
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			kind := strings.ToLower(r.Type)
			if kind == "phase" || strings.HasPrefix(kind, "checkpoint") {
				continue
			}
			k := execKey{r.ID, r.attempt()}
			if seen[k] {
				continue
			}
			seen[k] = true
			n := PipelineStepNode{
				Path: latest.pathIn(tl, r), Type: kind, Attempt: r.attempt(), State: r.State, Result: r.Result,
				Started: r.StartTime, Finished: r.FinishTime, WebURL: c.stepWebURL(b.ID, latest, tl, r),
			}
			if start, finish := parseTime(r.StartTime), parseTime(r.FinishTime); !start.IsZero() && !finish.IsZero() {
				n.Duration = finish.Sub(start).Round(time.Second).String()
			}
			if r.Log != nil {
				n.LogID = int64(r.Log.ID)
			}
			for _, is := range r.Issues {
				switch strings.ToLower(is.Type) {
				case "error":
					n.Errors++
				case "warning":
					n.Warnings++
				}
			}
			out = append(out, n)
		}
	}
	return out, nil
}

// tests lists every result of every test run of the build, each placed in
// its attempt and job like get's failed tests. Attachments are left to get.
func (c *adoPipelineClient) tests(ctx context.Context, s *PipelineRunSummary) ([]PipelineTest, error) {
	b := s.ado
	latest, timelines, err := c.allTimelines(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	attempts, jobs := maxAttempt(timelines), jobExecutions(latest, timelines)
	testRuns, err := c.testRuns(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	out := []PipelineTest{}
	for _, tr := range testRuns {
		attempt, job := testRunAttempt(tr, jobs, attempts)
		results, err := c.testResults(ctx, tr.ID, "")
		if err != nil {
			return nil, fmt.Errorf("test run %d: %w", tr.ID, err)
		}
		for _, res := range results {
			out = append(out, c.testOf(b.ID, tr, res, attempt, job))
		}
	}
	return out, nil
}

// resultAttachments lists one test result's attachments (metadata only).
func (c *adoPipelineClient) resultAttachments(ctx context.Context, testRunID, resultID int) ([]PipelineFile, []string, error) {
	atts, notes, err := c.testAttachments(ctx, testRunID, resultID)
	if err != nil {
		return nil, nil, err
	}
	return pipelineFiles(atts), notes, nil
}

// testOf is one test result as get and tests mode show it.
func (c *adoPipelineClient) testOf(buildID int, tr adoTestRun, res adoTestResult, attempt int, job string) PipelineTest {
	t := PipelineTest{
		Name: testNameOf(res), Outcome: res.Outcome,
		Flaky:   !isFailedTestOutcome(res.Outcome) && hasFailedSubResult(res.SubResults),
		Attempt: attempt, Job: job, TestRun: fmt.Sprintf("%s (id %d)", tr.Name, tr.ID), Storage: res.AutomatedTestStorage,
		ErrorMessage: res.ErrorMessage, StackTrace: res.StackTrace, SubResults: subResults(res.SubResults),
		WebURL: fmt.Sprintf("%s/_build/results?buildId=%d&view=ms.vss-test-web.build-test-results-tab&runId=%d&resultId=%d",
			c.webBase(), buildID, tr.ID, res.ID),
		testRunID: tr.ID, resultID: res.ID,
	}
	if res.TestCaseTitle != t.Name {
		t.Title = res.TestCaseTitle
	}
	if res.DurationInMs > 0 {
		t.Duration = (time.Duration(res.DurationInMs) * time.Millisecond).String()
	}
	return t
}

// adoDefinitionDetail is what get reads of the run's pipeline definition.
type adoDefinitionDetail struct {
	Process struct {
		YamlFilename string `json:"yamlFilename"`
	} `json:"process"`
	Variables map[string]struct {
		Value    *string `json:"value"`
		IsSecret bool    `json:"isSecret"`
	} `json:"variables"`
}

// readContext adds the run's parameters and queue-time variables, its
// pipeline's YAML file and variables at the run's revision, and the work
// items linked to it. A definition or work item listing the server doesn't
// have (a deleted pipeline, an older server) is noted.
func (c *adoPipelineClient) readContext(ctx context.Context, b *adoBuild, run *PipelineRun) error {
	run.Parameters = stringMap(b.TemplateParameters)
	if p := strings.TrimSpace(b.Parameters); p != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(p), &m); err != nil {
			run.Notes = append(run.Notes, "queue-time variables aren't a JSON object: "+p)
		} else {
			run.QueueVariables = stringMap(m)
		}
	}

	defURL := fmt.Sprintf("%s/build/definitions/%d?api-version=%s", c.apiBase(), b.Definition.ID, c.version)
	if b.Definition.Revision > 0 {
		defURL += fmt.Sprintf("&revision=%d", b.Definition.Revision)
	}
	if data, err := c.get(ctx, defURL); err != nil {
		note, err := listUnavailable("the pipeline definition", err)
		if err != nil {
			return fmt.Errorf("reading pipeline definition %d: %w", b.Definition.ID, err)
		}
		run.Notes = append(run.Notes, note)
	} else {
		var def adoDefinitionDetail
		if err := json.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("pipeline definition response is not valid JSON: %w", err)
		}
		run.PipelineFile = def.Process.YamlFilename
		for name, v := range def.Variables {
			if run.Variables == nil {
				run.Variables = map[string]string{}
			}
			switch {
			case v.IsSecret:
				run.Variables[name] = "***"
			case v.Value != nil:
				run.Variables[name] = *v.Value
			default:
				run.Variables[name] = ""
			}
		}
	}

	data, err := c.get(ctx, fmt.Sprintf("%s/build/builds/%d/workitems?$top=10000&api-version=%s", c.apiBase(), b.ID, c.version))
	if err != nil {
		note, err := listUnavailable("linked work items", err)
		if err != nil {
			return fmt.Errorf("listing linked work items: %w", err)
		}
		run.Notes = append(run.Notes, note)
		return nil
	}
	var page struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return fmt.Errorf("work items response is not valid JSON: %w", err)
	}
	for _, wi := range page.Value {
		run.WorkItems = append(run.WorkItems, PipelineWorkItem{ID: wi.ID, WebURL: c.webBase() + "/_workitems/edit/" + wi.ID})
	}
	return nil
}

// stringMap renders a decoded JSON object's values as strings (objects and
// arrays as JSON); nil for an empty one.
func stringMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch x := v.(type) {
		case string:
			out[k] = x
		case nil:
			out[k] = ""
		case map[string]any, []any:
			data, _ := json.Marshal(x)
			out[k] = string(data)
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

func (c *adoPipelineClient) readTimelinesAndTests(ctx context.Context, b *adoBuild, run *PipelineRun) error {
	latest, timelines, err := c.allTimelines(ctx, b.ID)
	if err != nil {
		return err
	}

	for _, tl := range timelines {
		for _, r := range tl.ordered {
			run.Attempts = max(run.Attempts, r.attempt())
		}
	}
	attemptLabel := func(n int) string {
		if run.Attempts <= 1 {
			return ""
		}
		return fmt.Sprintf(" (attempt %d)", n)
	}
	// A record id is shared by all attempts of a job/task, so (id, attempt)
	// identifies one execution; a previous timeline may repeat records that
	// weren't rerun.
	type execKey struct {
		id      string
		attempt int
	}
	seenIssues, seenSteps := map[execKey]bool{}, map[execKey]bool{}
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			k := execKey{r.ID, r.attempt()}
			if seenIssues[k] {
				continue
			}
			seenIssues[k] = true
			for _, is := range r.Issues {
				run.Issues = append(run.Issues, fmt.Sprintf("[%s] %s%s: %s", is.Type, latest.pathIn(tl, r), attemptLabel(r.attempt()), is.Message))
			}
		}
		for _, r := range tl.failedLeaves() {
			k := execKey{r.ID, r.attempt()}
			if seenSteps[k] {
				continue
			}
			seenSteps[k] = true
			web := c.stepWebURL(b.ID, latest, tl, r)
			run.FailedSteps = append(run.FailedSteps, PipelineStep{
				Path: latest.pathIn(tl, r), Attempt: r.attempt(), Result: r.Result,
				LogID: int64(r.Log.ID), WebURL: web,
			})
		}
	}
	run.FailedStages, run.FailedJobs = failedNames(timelines, "stage"), failedNames(timelines, "job")

	// A green build that was never rerun has no failed test to find (short
	// of an in-run test retry, deliberately not checked — it would cost the
	// test-run calls on every green build).
	if strings.EqualFold(b.Result, "succeeded") && run.Attempts <= 1 && len(run.FailedSteps) == 0 {
		return nil
	}

	// Test runs of every attempt are attached to the build, so this covers
	// rerun jobs too.
	testRuns, err := c.testRuns(ctx, b.ID)
	if err != nil {
		return err
	}
	jobs := jobExecutions(latest, timelines)
	for _, tr := range testRuns {
		attempt, job := testRunAttempt(tr, jobs, run.Attempts)
		runLabel := fmt.Sprintf("%s (id %d)", tr.Name, tr.ID)
		atts, notes, err := c.testAttachments(ctx, tr.ID, 0)
		if err != nil {
			return fmt.Errorf("test run %d: %w", tr.ID, err)
		}
		if len(atts) > 0 {
			run.TestRunAttachments = append(run.TestRunAttachments, PipelineTestRunFiles{TestRun: runLabel, Files: pipelineFiles(atts)})
		}
		run.Notes = append(run.Notes, notes...)

		var results []adoTestResult
		if tr.mayHaveFailedResults() {
			failed, err := c.testResults(ctx, tr.ID, adoFailedTestOutcomes)
			if err != nil {
				return fmt.Errorf("test run %d: %w", tr.ID, err)
			}
			results = append(results, failed...)
		}
		if tr.mayHaveRetriedResults() {
			passed, err := c.testResults(ctx, tr.ID, "Passed")
			if err != nil {
				return fmt.Errorf("test run %d: %w", tr.ID, err)
			}
			for _, res := range passed {
				if hasFailedSubResult(res.SubResults) {
					results = append(results, res)
				}
			}
		}
		for _, res := range results {
			t := c.testOf(b.ID, tr, res, attempt, job)
			run.Tests = append(run.Tests, t)
		}
	}
	return nil
}

func subResults(subs []adoTestSubResult) []PipelineSubResult {
	out := make([]PipelineSubResult, 0, len(subs))
	for i, s := range subs {
		r := PipelineSubResult{Name: orDefault(s.DisplayName, fmt.Sprintf("#%d", i+1)), Outcome: s.Outcome,
			ErrorMessage: s.ErrorMessage, StackTrace: s.StackTrace, SubResults: subResults(s.SubResults)}
		if s.DurationInMs > 0 {
			r.Duration = (time.Duration(s.DurationInMs) * time.Millisecond).String()
		}
		out = append(out, r)
	}
	return out
}

// timeline fetches the build's latest timeline (timelineID "") or a
// specific one, returning its id and records.
func (c *adoPipelineClient) timeline(ctx context.Context, buildID int, timelineID string) (string, []adoTimelineRecord, error) {
	u := fmt.Sprintf("%s/build/builds/%d/timeline", c.apiBase(), buildID)
	if timelineID != "" {
		u += "/" + url.PathEscape(timelineID)
	}
	var tl struct {
		ID      string              `json:"id"`
		Records []adoTimelineRecord `json:"records"`
	}
	if err := c.getJSON(ctx, u+"?api-version="+c.version, &tl); err != nil {
		return "", nil, fmt.Errorf("fetching timeline %s: %w", timelineID, err)
	}
	return tl.ID, tl.Records, nil
}

func (c *adoPipelineClient) testRuns(ctx context.Context, buildID int) ([]adoTestRun, error) {
	buildURI := url.QueryEscape(fmt.Sprintf("vstfs:///Build/Build/%d", buildID))
	var all []adoTestRun
	for skip := 0; ; skip += adoTestRunsPageSize {
		var page struct {
			Value []adoTestRun `json:"value"`
		}
		u := fmt.Sprintf("%s/test/runs?buildUri=%s&includeRunDetails=true&$top=%d&$skip=%d&api-version=%s",
			c.apiBase(), buildURI, adoTestRunsPageSize, skip, c.version)
		if err := c.getJSON(ctx, u, &page); err != nil {
			return nil, fmt.Errorf("listing test runs: %w", err)
		}
		all = append(all, page.Value...)
		if len(page.Value) < adoTestRunsPageSize {
			return all, nil
		}
	}
}

// testResults pages through the run's results with the given outcomes,
// including their sub-results (the attempts of a retried test).
func (c *adoPipelineClient) testResults(ctx context.Context, runID int, outcomes string) ([]adoTestResult, error) {
	var all []adoTestResult
	for skip := 0; ; skip += adoTestResultsPageSize {
		var page struct {
			Value []adoTestResult `json:"value"`
		}
		u := fmt.Sprintf("%s/test/Runs/%d/results?detailsToInclude=SubResults&$top=%d&$skip=%d&api-version=%s",
			c.apiBase(), runID, adoTestResultsPageSize, skip, c.version)
		if outcomes != "" {
			u += "&outcomes=" + outcomes
		}
		if err := c.getJSON(ctx, u, &page); err != nil {
			return nil, fmt.Errorf("listing %s test results: %w", outcomes, err)
		}
		all = append(all, page.Value...)
		if len(page.Value) < adoTestResultsPageSize {
			return all, nil
		}
	}
}

// adoBuildChange is one commit a build picked up since the previous build.
type adoBuildChange struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Author  struct {
		DisplayName string `json:"displayName"`
		UniqueName  string `json:"uniqueName"`
	} `json:"author"`
	Timestamp string `json:"timestamp"`
}

// buildChanges pages through every change (commit) of a build.
func (c *adoPipelineClient) buildChanges(ctx context.Context, buildID int) ([]PipelineChange, error) {
	base := fmt.Sprintf("%s/build/builds/%d/changes?api-version=%s&$top=1000", c.apiBase(), buildID, c.version)
	out := []PipelineChange{}
	token := ""
	seen := map[string]bool{}
	for {
		u := base
		if token != "" {
			u += "&continuationToken=" + url.QueryEscape(token)
		}
		data, header, err := c.api.getRawWithHeader(ctx, c.client, u)
		if err != nil {
			return nil, fmt.Errorf("listing changes: %w", err)
		}
		var page struct {
			Value []adoBuildChange `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("changes response is not valid JSON: %w", err)
		}
		for _, ch := range page.Value {
			msg, _, _ := strings.Cut(strings.TrimSpace(ch.Message), "\n")
			out = append(out, PipelineChange{
				ID: ch.ID, Author: strings.TrimSpace(ch.Author.DisplayName + " " + angle(ch.Author.UniqueName)),
				Date: ch.Timestamp, Message: msg,
			})
		}
		token = header.Get("x-ms-continuationtoken")
		if token == "" {
			return out, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("changes endpoint returned continuation token %q twice", token)
		}
		seen[token] = true
	}
}

// adoBuildArtifact is one artifact a build published (listing only).
type adoBuildArtifact struct {
	Name     string `json:"name"`
	Resource struct {
		Type        string         `json:"type"`
		DownloadURL string         `json:"downloadUrl"`
		URL         string         `json:"url"`
		Properties  map[string]any `json:"properties"`
	} `json:"resource"`
}

// buildArtifacts lists the artifacts a build published — names, types,
// sizes and download links, never their contents. A server without the
// artifacts API yields a note instead.
func (c *adoPipelineClient) buildArtifacts(ctx context.Context, buildID int) ([]attachmentInfo, []string, error) {
	data, err := c.get(ctx, fmt.Sprintf("%s/build/builds/%d/artifacts?api-version=%s", c.apiBase(), buildID, c.version))
	if err != nil {
		note, err := listUnavailable("artifacts", err)
		if err != nil {
			return nil, nil, fmt.Errorf("listing artifacts: %w", err)
		}
		return nil, []string{note}, nil
	}
	var page struct {
		Value []adoBuildArtifact `json:"value"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, nil, fmt.Errorf("artifacts response is not valid JSON: %w", err)
	}
	out := make([]attachmentInfo, 0, len(page.Value))
	for _, art := range page.Value {
		link := art.Resource.DownloadURL
		if !isWebURL(link) {
			link = art.Resource.URL
		}
		size := int64(-1)
		if v, ok := art.Resource.Properties["artifactsize"]; ok {
			size = anyInt64(v)
		}
		out = append(out, attachmentInfo{Name: art.Name, Kind: art.Resource.Type, Size: size, URL: link})
	}
	return out, nil, nil
}

// adoTestAttachment is one file attached to a test run or test result.
type adoTestAttachment struct {
	FileName       string `json:"fileName"`
	Comment        string `json:"comment"`
	AttachmentType string `json:"attachmentType"`
	CreatedDate    string `json:"createdDate"`
	Size           *int64 `json:"size"`
	URL            string `json:"url"`
}

// testAttachments lists the attachments of a test run (resultID 0) or of
// one of its results — metadata only. The API is preview-only on Azure
// DevOps; a server without it yields a note instead.
func (c *adoPipelineClient) testAttachments(ctx context.Context, runID, resultID int) ([]attachmentInfo, []string, error) {
	u := fmt.Sprintf("%s/test/Runs/%d", c.apiBase(), runID)
	if resultID != 0 {
		u += fmt.Sprintf("/Results/%d", resultID)
	}
	data, err := c.get(ctx, u+"/attachments?api-version="+previewVersion(c.version))
	if err != nil {
		note, err := listUnavailable("attachments", err)
		if err != nil {
			return nil, nil, fmt.Errorf("listing attachments: %w", err)
		}
		return nil, []string{note}, nil
	}
	var page struct {
		Value []adoTestAttachment `json:"value"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, nil, fmt.Errorf("attachments response is not valid JSON: %w", err)
	}
	out := make([]attachmentInfo, 0, len(page.Value))
	for _, t := range page.Value {
		size := int64(-1)
		if t.Size != nil {
			size = *t.Size
		}
		out = append(out, attachmentInfo{
			Name: t.FileName, Kind: t.AttachmentType, Size: size, Created: t.CreatedDate, Comment: t.Comment, URL: t.URL,
		})
	}
	return out, nil, nil
}

// failedNames lists the names of failed records of a type (stage, job)
// across every attempt's timeline.
func failedNames(timelines []*timeline, kind string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			if isFailedRecord(r) && r.Name != "" && strings.EqualFold(r.Type, kind) && !seen[r.Name] {
				seen[r.Name] = true
				out = append(out, r.Name)
			}
		}
	}
	return out
}

// jobExecution is one execution (attempt) of a job, placed in time.
type jobExecution struct {
	name, path    string
	attempt       int
	start, finish time.Time
}

// jobExecutions lists every job execution across all attempts' timelines.
func jobExecutions(latest *timeline, timelines []*timeline) []jobExecution {
	var out []jobExecution
	seen := map[string]bool{}
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			if !strings.EqualFold(r.Type, "job") {
				continue
			}
			key := fmt.Sprintf("%s#%d", r.ID, r.attempt())
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, jobExecution{name: r.Name, path: latest.pathIn(tl, r), attempt: r.attempt(),
				start: parseTime(r.StartTime), finish: parseTime(r.FinishTime)})
		}
	}
	return out
}

// testRunAttempt finds the pipeline attempt (and job) that published a test
// run: from the run's pipeline reference when the server records one, else
// the job execution whose time span holds the run. A build with one attempt
// is attempt 1; 0 means it can't be told.
func testRunAttempt(run adoTestRun, jobs []jobExecution, attempts int) (int, string) {
	ref := run.PipelineReference
	attempt := ref.JobReference.Attempt
	if attempt == 0 {
		attempt = max(ref.PhaseReference.Attempt, ref.StageReference.Attempt)
	}
	if attempt > 0 {
		job := ""
		for _, j := range jobs {
			if j.attempt == attempt && ref.JobReference.JobName != "" &&
				(strings.EqualFold(j.name, ref.JobReference.JobName) || strings.HasSuffix(strings.ToLower(j.path), strings.ToLower(ref.JobReference.JobName))) {
				job = j.path
				break
			}
		}
		if job == "" {
			var parts []string
			for _, n := range []string{ref.StageReference.StageName, ref.JobReference.JobName} {
				if n != "" && n != "__default" {
					parts = append(parts, n)
				}
			}
			job = strings.Join(parts, " > ")
		}
		return attempt, job
	}

	// No reference: the job execution whose span holds the run's
	// completion (or start), the narrowest one if several do.
	at := parseTime(run.CompletedDate)
	if at.IsZero() {
		at = parseTime(run.StartedDate)
	}
	var best *jobExecution
	if !at.IsZero() {
		for i := range jobs {
			j := &jobs[i]
			if j.start.IsZero() || j.finish.IsZero() || at.Before(j.start) || at.After(j.finish) {
				continue
			}
			if best == nil || j.finish.Sub(j.start) < best.finish.Sub(best.start) {
				best = j
			}
		}
	}
	if best != nil {
		return best.attempt, best.path
	}
	if attempts <= 1 {
		return 1, ""
	}
	return 0, ""
}

// timeline indexes a build's timeline records by id and parent.
type timeline struct {
	byID     map[string]adoTimelineRecord
	children map[string][]adoTimelineRecord
	ordered  []adoTimelineRecord // depth-first, siblings by Order
}

func newTimeline(records []adoTimelineRecord) *timeline {
	tl := &timeline{byID: map[string]adoTimelineRecord{}, children: map[string][]adoTimelineRecord{}}
	for _, r := range records {
		tl.byID[r.ID] = r
	}
	for _, r := range records {
		parent := r.ParentID
		if _, ok := tl.byID[parent]; !ok {
			parent = ""
		}
		tl.children[parent] = append(tl.children[parent], r)
	}
	for _, kids := range tl.children {
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].Order != kids[j].Order {
				return kids[i].Order < kids[j].Order
			}
			return kids[i].Name < kids[j].Name
		})
	}
	var walk func(parent string)
	walk = func(parent string) {
		for _, r := range tl.children[parent] {
			tl.ordered = append(tl.ordered, r)
			walk(r.ID)
		}
	}
	walk("")
	return tl
}

// path names r by its Stage > Job > Task ancestry. Phase and Checkpoint
// records are structural noise and left out.
func (tl *timeline) path(r adoTimelineRecord) string {
	var names []string
	for cur, ok := r, true; ok; cur, ok = tl.byID[cur.ParentID] {
		switch strings.ToLower(cur.Type) {
		case "phase", "checkpoint":
			continue
		}
		names = append(names, cur.Name)
	}
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, " > ")
}

// pathIn names r, a record of timeline from, preferring tl's ancestry: a
// previous-attempt timeline may hold only the rerun job, without the stage
// above it, while record ids are shared across attempts.
func (tl *timeline) pathIn(from *timeline, r adoTimelineRecord) string {
	if _, ok := tl.byID[r.ID]; ok {
		return tl.path(r)
	}
	if _, ok := tl.byID[r.ParentID]; ok {
		return tl.path(r)
	}
	return from.path(r)
}

// jobID returns the id of r's Job record (r itself if it is one), or "".
func (tl *timeline) jobID(r adoTimelineRecord) string {
	for cur, ok := r, true; ok; cur, ok = tl.byID[cur.ParentID] {
		if strings.EqualFold(cur.Type, "job") {
			return cur.ID
		}
	}
	return ""
}

// failedLeaves returns the failed records that have a log and no failed
// descendant — the innermost failure, so a job that failed without any
// failed task (timeout, lost agent) is still captured with its own log.
func (tl *timeline) failedLeaves() []adoTimelineRecord {
	var out []adoTimelineRecord
	for _, r := range tl.ordered {
		if isFailedRecord(r) && r.Log != nil && !tl.hasFailedDescendant(r.ID) {
			out = append(out, r)
		}
	}
	return out
}

func (tl *timeline) hasFailedDescendant(id string) bool {
	for _, c := range tl.children[id] {
		if isFailedRecord(c) || tl.hasFailedDescendant(c.ID) {
			return true
		}
	}
	return false
}

func isFailedRecord(r adoTimelineRecord) bool { return strings.EqualFold(r.Result, "failed") }

var (
	// logTimestamp matches the ISO-8601 timestamp ADO prefixes every log
	// line with (optionally preceded by a UTF-8 BOM on the first line).
	logTimestamp = regexp.MustCompile(`(?m)^\x{FEFF}?\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z ?`)
	// ansiEscape matches ANSI CSI escape sequences (colors, cursor moves).
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

// cleanLogText strips per-line timestamps and ANSI escape codes from a
// build log. Every line is kept.
func cleanLogText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = logTimestamp.ReplaceAllString(s, "")
	s = ansiEscape.ReplaceAllString(s, "")
	return strings.TrimRight(s, "\n")
}

func angle(s string) string {
	if s == "" {
		return ""
	}
	return "<" + s + ">"
}
