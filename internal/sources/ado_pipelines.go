// Azure DevOps pipeline-run fetching for APISource. A pipeline-run source
// covers one build definition: every completed run of the last Days days
// becomes a run document. Verbosity follows failure: a run additionally
// yields one document per failed step (its complete log) and one per failed
// test result, both linked back to the run via the parent_doc_id property —
// and that includes failures of earlier attempts. A run that went green after
// "Rerun failed jobs" still carries attempt 1's failed steps (from the
// previous-attempt timelines) and failed tests, and a test that passed only
// on an in-run retry (VSTest rerunFailedTests and the like, recorded as
// sub-results) gets a flaky-test document, so flaky failures aren't lost.
// The one exception: a green build with a single attempt costs only its
// timeline call — its test runs aren't checked for in-run retries.
//
// Fetching is lossless by design: every page of runs, every failed step's
// whole log and every failed test result is pulled, and anything that can't
// be fetched fails the sync rather than being silently dropped — the
// previous index then stays intact.
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
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

const (
	// adoBuildsPageSize is the $top requested per page of the builds list;
	// further pages follow ADO's x-ms-continuationtoken response header.
	adoBuildsPageSize = 1000
	// adoTestRunsPageSize / adoTestResultsPageSize page the test APIs via
	// $top/$skip until a short page.
	adoTestRunsPageSize    = 100
	adoTestResultsPageSize = 1000

	// adoFailedTestOutcomes are the test outcomes that get a test document.
	adoFailedTestOutcomes = "Failed,Aborted,Error,Timeout"
)

// adoDetailedResults are the run results whose timeline issues are always
// listed on the run document. Runs with any other result list them only when
// some attempt had a failed step or a failed test.
var adoDetailedResults = map[string]bool{"failed": true, "partiallysucceeded": true}

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
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"definition"`
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
	Order    int    `json:"order"`
	Attempt  int    `json:"attempt"`
	Log      *struct {
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
	ID   int    `json:"id"`
	Name string `json:"name"`
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

// pipelineStep is one failed timeline record with its fetched log.
type pipelineStep struct {
	record adoTimelineRecord
	jobID  string
	path   string
	log    string
}

// pipelineTest is one failed test result and the test run it belongs to.
type pipelineTest struct {
	run    adoTestRun
	result adoTestResult
}

// pipelineRunDetail is everything fetched for a run that failed, partially
// succeeded, or had a failure in some attempt.
type pipelineRunDetail struct {
	attempts int      // highest attempt number in the timeline
	issues   []string // "[error] Stage > Job > Task: message"
	steps    []pipelineStep
	tests    []pipelineTest
}

// adoPipelineScope holds the resolved configuration of a pipeline-run source.
type adoPipelineScope struct {
	org, project, apiVersion string
	definitionID, days       int
}

func (s adoPipelineScope) apiBase() string {
	return s.org + "/" + url.PathEscape(s.project) + "/_apis"
}

func (s adoPipelineScope) webBase() string {
	return s.org + "/" + url.PathEscape(s.project)
}

func (a *APISource) pipelineScope() (adoPipelineScope, error) {
	cfg := a.src
	s := adoPipelineScope{
		org:        strings.TrimRight(cfg.GetConfig("AdoOrg"), "/"),
		project:    cfg.GetConfig("AdoProject"),
		apiVersion: cfg.GetConfig("AdoApiVersion"),
	}
	if s.apiVersion == "" {
		s.apiVersion = "7.1"
	}
	if s.org == "" || s.project == "" {
		return s, fmt.Errorf("pipeline-run sources require an Azure DevOps organization and project")
	}
	var err error
	if s.definitionID, err = strconv.Atoi(strings.TrimSpace(cfg.GetConfig("DefinitionId"))); err != nil || s.definitionID <= 0 {
		return s, fmt.Errorf("pipeline-run sources require a numeric DefinitionId (got %q)", cfg.GetConfig("DefinitionId"))
	}
	if s.days, err = strconv.Atoi(strings.TrimSpace(cfg.GetConfig("Days"))); err != nil || s.days <= 0 {
		return s, fmt.Errorf("pipeline-run sources require a positive number of Days (got %q)", cfg.GetConfig("Days"))
	}
	return s, nil
}

// fetchAdoPipelineRuns lists the definition's completed runs of the last
// Days days and turns them into run, step and test documents.
func (a *APISource) fetchAdoPipelineRuns(ctx context.Context, client *http.Client, progress ProgressCallback) ([]models.SourceDocument, error) {
	scope, err := a.pipelineScope()
	if err != nil {
		return nil, err
	}
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Listing pipeline runs"})
	}
	// Top is never offered on the pipeline-run form — a real sync fetches
	// every run in the window. Only the form's preview sets it, to keep a
	// preview from pulling every failed run's logs.
	top := unlimitedTop
	if n, err := strconv.Atoi(a.src.GetConfig("Top")); err == nil && n > 0 {
		top = n
	}
	builds, err := a.listAdoBuilds(ctx, client, scope, time.Now().UTC().AddDate(0, 0, -scope.days), top)
	if err != nil {
		return nil, err
	}

	n := len(builds)
	details := make([]*pipelineRunDetail, n)

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	report := func() {
		mu.Lock()
		completed++
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: completed, Total: n,
				Message: fmt.Sprintf("Fetching pipeline run %d/%d", completed, n),
			})
		}
		mu.Unlock()
	}

	// Every run is inspected, not only failed ones: a run rerun to green
	// still holds its earlier attempts' failures.
	for i := 0; i < n && workCtx.Err() == nil; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			detail, err := a.fetchPipelineRunDetail(workCtx, client, scope, builds[i])
			if err != nil {
				fail(fmt.Errorf("pipeline run #%s (build %d): %w", builds[i].BuildNumber, builds[i].ID, err))
				return
			}
			details[i] = detail
			report()
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	var docs []models.SourceDocument
	for i, b := range builds {
		docs = append(docs, a.pipelineRunDocuments(scope, b, details[i])...)
	}
	return docs, nil
}

// listAdoBuilds pages through every completed run of the definition that
// finished at or after minTime (newest first, at most top), following
// x-ms-continuationtoken.
func (a *APISource) listAdoBuilds(ctx context.Context, client *http.Client, scope adoPipelineScope, minTime time.Time, top int) ([]adoBuild, error) {
	q := url.Values{}
	q.Set("definitions", strconv.Itoa(scope.definitionID))
	q.Set("statusFilter", "completed")
	q.Set("minTime", minTime.Format(time.RFC3339))
	q.Set("queryOrder", "finishTimeDescending")
	q.Set("$top", strconv.Itoa(min(adoBuildsPageSize, top)))
	q.Set("api-version", scope.apiVersion)
	baseURL := scope.apiBase() + "/build/builds?" + q.Encode()

	var all []adoBuild
	token := ""
	seen := map[string]bool{}
	for {
		pageURL := baseURL
		if token != "" {
			pageURL += "&continuationToken=" + url.QueryEscape(token)
		}
		data, header, err := a.getRawWithHeader(ctx, client, pageURL)
		if err != nil {
			return nil, fmt.Errorf("listing pipeline runs: %w", err)
		}
		var page struct {
			Value []adoBuild `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("builds response is not valid JSON: %w", err)
		}
		all = append(all, page.Value...)
		token = header.Get("x-ms-continuationtoken")
		if len(all) >= top {
			return all[:top], nil
		}
		if token == "" {
			return all, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("builds endpoint returned continuation token %q twice", token)
		}
		seen[token] = true
	}
}

// fetchPipelineRunDetail fetches a run's timeline issues, the full log of
// every failed step of every attempt, every failed test result and every
// test that passed only on a retry. It returns nil when the run has nothing
// beyond its summary to embed: no failure in any attempt and a result that
// isn't failed/partial.
func (a *APISource) fetchPipelineRunDetail(ctx context.Context, client *http.Client, scope adoPipelineScope, b adoBuild) (*pipelineRunDetail, error) {
	current, records, err := a.fetchAdoTimeline(ctx, client, scope, b.ID, "")
	if err != nil {
		return nil, err
	}
	latest := newTimeline(records)

	// The latest timeline holds each record's newest attempt; earlier
	// attempts of rerun jobs/stages live in the timelines their
	// previousAttempts point to. Those may point further back in turn.
	timelines := []*timeline{latest}
	seenTimeline := map[string]bool{current: true}
	for i := 0; i < len(timelines); i++ {
		for _, r := range timelines[i].ordered {
			for _, pa := range r.PreviousAttempts {
				if pa.TimelineID == "" || seenTimeline[pa.TimelineID] {
					continue
				}
				seenTimeline[pa.TimelineID] = true
				_, recs, err := a.fetchAdoTimeline(ctx, client, scope, b.ID, pa.TimelineID)
				if err != nil {
					return nil, fmt.Errorf("attempt %d of %q: %w", pa.Attempt, r.Name, err)
				}
				timelines = append(timelines, newTimeline(recs))
			}
		}
	}

	detail := &pipelineRunDetail{attempts: 1}
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			detail.attempts = max(detail.attempts, r.attempt())
		}
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
				detail.issues = append(detail.issues, fmt.Sprintf("[%s] %s%s: %s", is.Type, latest.pathIn(tl, r), detail.attemptLabel(r.attempt()), is.Message))
			}
		}
		for _, r := range tl.failedLeaves() {
			k := execKey{r.ID, r.attempt()}
			if seenSteps[k] {
				continue
			}
			seenSteps[k] = true
			path := latest.pathIn(tl, r)
			logURL := fmt.Sprintf("%s/build/builds/%d/logs/%d?api-version=%s", scope.apiBase(), b.ID, r.Log.ID, scope.apiVersion)
			data, err := a.getRaw(ctx, client, logURL)
			if err != nil {
				return nil, fmt.Errorf("fetching log %d of step %q (attempt %d): %w", r.Log.ID, path, r.attempt(), err)
			}
			jobID := latest.jobID(r)
			if jobID == "" {
				jobID = tl.jobID(r)
			}
			detail.steps = append(detail.steps, pipelineStep{
				record: r, jobID: jobID, path: path, log: cleanLogText(string(data)),
			})
		}
	}

	// A green build that was never rerun has no failed test to find (short
	// of an in-run test retry, deliberately not checked — it would cost the
	// test-run calls on every green build), so it stays summary-only after
	// this one timeline call.
	if strings.EqualFold(b.Result, "succeeded") && detail.attempts <= 1 && len(detail.steps) == 0 {
		return nil, nil
	}

	// Test runs of every attempt are attached to the build, so this covers
	// rerun jobs too.
	runs, err := a.fetchAdoTestRuns(ctx, client, scope, b.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		failed, err := a.fetchAdoTestResults(ctx, client, scope, run.ID, adoFailedTestOutcomes)
		if err != nil {
			return nil, fmt.Errorf("test run %d: %w", run.ID, err)
		}
		for _, res := range failed {
			detail.tests = append(detail.tests, pipelineTest{run: run, result: res})
		}
		if !run.mayHaveRetriedResults() {
			continue
		}
		passed, err := a.fetchAdoTestResults(ctx, client, scope, run.ID, "Passed")
		if err != nil {
			return nil, fmt.Errorf("test run %d: %w", run.ID, err)
		}
		for _, res := range passed {
			if hasFailedSubResult(res.SubResults) {
				detail.tests = append(detail.tests, pipelineTest{run: run, result: res})
			}
		}
	}

	if len(detail.steps) == 0 && len(detail.tests) == 0 && !adoDetailedResults[strings.ToLower(b.Result)] {
		return nil, nil
	}
	return detail, nil
}

// attemptLabel is " (attempt n)" when the run was rerun, else "".
func (d *pipelineRunDetail) attemptLabel(n int) string {
	if d.attempts <= 1 {
		return ""
	}
	return fmt.Sprintf(" (attempt %d)", n)
}

// fetchAdoTimeline fetches the build's latest timeline (timelineID "") or a
// specific one, returning its id and records.
func (a *APISource) fetchAdoTimeline(ctx context.Context, client *http.Client, scope adoPipelineScope, buildID int, timelineID string) (string, []adoTimelineRecord, error) {
	u := fmt.Sprintf("%s/build/builds/%d/timeline", scope.apiBase(), buildID)
	if timelineID != "" {
		u += "/" + url.PathEscape(timelineID)
	}
	data, err := a.getRaw(ctx, client, u+"?api-version="+scope.apiVersion)
	if err != nil {
		return "", nil, fmt.Errorf("fetching timeline %s: %w", timelineID, err)
	}
	var tl struct {
		ID      string              `json:"id"`
		Records []adoTimelineRecord `json:"records"`
	}
	if err := json.Unmarshal(data, &tl); err != nil {
		return "", nil, fmt.Errorf("timeline response is not valid JSON: %w", err)
	}
	return tl.ID, tl.Records, nil
}

func (a *APISource) fetchAdoTestRuns(ctx context.Context, client *http.Client, scope adoPipelineScope, buildID int) ([]adoTestRun, error) {
	buildURI := url.QueryEscape(fmt.Sprintf("vstfs:///Build/Build/%d", buildID))
	var all []adoTestRun
	for skip := 0; ; skip += adoTestRunsPageSize {
		runsURL := fmt.Sprintf("%s/test/runs?buildUri=%s&includeRunDetails=true&$top=%d&$skip=%d&api-version=%s",
			scope.apiBase(), buildURI, adoTestRunsPageSize, skip, scope.apiVersion)
		data, err := a.getRaw(ctx, client, runsURL)
		if err != nil {
			return nil, fmt.Errorf("listing test runs: %w", err)
		}
		var page struct {
			Value []adoTestRun `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("test runs response is not valid JSON: %w", err)
		}
		all = append(all, page.Value...)
		if len(page.Value) < adoTestRunsPageSize {
			return all, nil
		}
	}
}

// fetchAdoTestResults pages through the run's results with the given
// outcomes, including their sub-results (the attempts of a retried test).
func (a *APISource) fetchAdoTestResults(ctx context.Context, client *http.Client, scope adoPipelineScope, runID int, outcomes string) ([]adoTestResult, error) {
	var all []adoTestResult
	for skip := 0; ; skip += adoTestResultsPageSize {
		resultsURL := fmt.Sprintf("%s/test/Runs/%d/results?outcomes=%s&detailsToInclude=SubResults&$top=%d&$skip=%d&api-version=%s",
			scope.apiBase(), runID, outcomes, adoTestResultsPageSize, skip, scope.apiVersion)
		data, err := a.getRaw(ctx, client, resultsURL)
		if err != nil {
			return nil, fmt.Errorf("listing %s test results: %w", outcomes, err)
		}
		var page struct {
			Value []adoTestResult `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("test results response is not valid JSON: %w", err)
		}
		all = append(all, page.Value...)
		if len(page.Value) < adoTestResultsPageSize {
			return all, nil
		}
	}
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

// cleanLogText strips ADO's per-line timestamps and ANSI escape codes from a
// build log. Every line is kept.
func cleanLogText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = logTimestamp.ReplaceAllString(s, "")
	s = ansiEscape.ReplaceAllString(s, "")
	return strings.TrimRight(s, "\n")
}

// shortBranch drops the refs/heads/ or refs/ prefix of a source branch.
func shortBranch(ref string) string {
	if s, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return s
	}
	return strings.TrimPrefix(ref, "refs/")
}

// pipelineRunDocuments builds a run's document plus, for failed/partial
// runs (detail != nil), its step and test documents.
func (a *APISource) pipelineRunDocuments(scope adoPipelineScope, b adoBuild, detail *pipelineRunDetail) []models.SourceDocument {
	runDocID := fmt.Sprintf("%s_run_%d", a.src.ID, b.ID)
	runLabel := fmt.Sprintf("%s #%s", b.Definition.Name, b.BuildNumber)
	runWebURL := b.Links.Web.Href
	if !isWebURL(runWebURL) {
		runWebURL = fmt.Sprintf("%s/_build/results?buildId=%d", scope.webBase(), b.ID)
	}
	project := b.Project.Name
	if project == "" {
		project = scope.project
	}

	var filters models.DocumentFilters
	filters.Add(models.FilterResult, b.Result)
	filters.Add(models.FilterBranch, b.SourceBranch, shortBranch(b.SourceBranch))

	newDoc := func(id, title, text, kind, webURL, header string) models.SourceDocument {
		props := map[string]string{
			"title":    title,
			"type":     kind,
			"project":  project,
			"build_id": strconv.Itoa(b.ID),
			"result":   b.Result,
			"branch":   b.SourceBranch,
			"commit":   b.SourceVersion,
			"web_url":  webURL,
		}
		if b.Repository.Name != "" {
			props["repository"] = b.Repository.Name
		}
		if id != runDocID {
			props["parent_doc_id"] = runDocID
		}
		return models.SourceDocument{
			ID:          id,
			Text:        strings.TrimSpace(text),
			Tags:        map[string]string{"source_id": a.src.ID, "source_name": a.src.Name},
			Properties:  props,
			Filters:     filters,
			ChunkHeader: header,
		}
	}

	// Run document.
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s\n", runLabel, b.Result)
	fmt.Fprintf(&sb, "Pipeline: %s (definition %d)\n", b.Definition.Name, b.Definition.ID)
	fmt.Fprintf(&sb, "Run: #%s (build id %d)\n", b.BuildNumber, b.ID)
	writeField(&sb, "Result", b.Result)
	writeField(&sb, "Status", b.Status)
	writeField(&sb, "Reason", b.Reason)
	writeField(&sb, "Branch", b.SourceBranch)
	writeField(&sb, "Commit", b.SourceVersion)
	writeField(&sb, "Repository", b.Repository.Name)
	writeField(&sb, "Requested for", strings.TrimSpace(b.RequestedFor.DisplayName+" "+angle(b.RequestedFor.UniqueName)))
	keys := make([]string, 0, len(b.TriggerInfo))
	for k := range b.TriggerInfo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeField(&sb, "Trigger "+k, b.TriggerInfo[k])
	}
	writeField(&sb, "Queued", b.QueueTime)
	writeField(&sb, "Started", b.StartTime)
	writeField(&sb, "Finished", b.FinishTime)
	if start, finish := parseTime(b.StartTime), parseTime(b.FinishTime); !start.IsZero() && !finish.IsZero() {
		writeField(&sb, "Duration", finish.Sub(start).Round(time.Second).String())
	}

	var children []models.SourceDocument
	if detail != nil {
		if detail.attempts > 1 {
			writeField(&sb, "Attempts", strconv.Itoa(detail.attempts))
		}
		if !adoDetailedResults[strings.ToLower(b.Result)] && (len(detail.steps) > 0 || len(detail.tests) > 0) {
			sb.WriteString("Passed after rerun: the run's final result is " + b.Result +
				", but earlier attempts or test retries failed (possibly flaky) — listed below.\n")
		}
		if len(detail.issues) > 0 {
			sb.WriteString("\nIssues:\n")
			for _, is := range detail.issues {
				sb.WriteString("- " + is + "\n")
			}
		}
		if len(detail.steps) > 0 {
			sb.WriteString("\nFailed steps:\n")
		}
		for _, st := range detail.steps {
			n := st.record.attempt()
			id := fmt.Sprintf("%s_step_%s", runDocID, st.record.ID)
			if n > 1 {
				id += fmt.Sprintf("_attempt%d", n)
			}
			label := st.path + detail.attemptLabel(n)
			fmt.Fprintf(&sb, "- %s (document %s)\n", label, id)
			webURL := fmt.Sprintf("%s/_build/results?buildId=%d&view=logs", scope.webBase(), b.ID)
			if st.jobID != "" {
				webURL += "&j=" + st.jobID
			}
			if st.jobID != st.record.ID {
				webURL += "&t=" + st.record.ID
			}
			doc := newDoc(id, fmt.Sprintf("%s failed step: %s", runLabel, label), st.log, "step", webURL,
				fmt.Sprintf("Run #%s > %s", b.BuildNumber, label))
			doc.Properties["step_path"] = st.path
			if detail.attempts > 1 {
				doc.Properties["attempt"] = strconv.Itoa(n)
			}
			children = append(children, doc)
		}
		var failedTests, flakyTests []pipelineTest
		for _, t := range detail.tests {
			if isFailedTestOutcome(t.result.Outcome) {
				failedTests = append(failedTests, t)
			} else {
				flakyTests = append(flakyTests, t)
			}
		}
		if len(failedTests) > 0 {
			sb.WriteString("\nFailed tests:\n")
		}
		testName := func(t pipelineTest) string {
			if t.result.AutomatedTestName != "" {
				return t.result.AutomatedTestName
			}
			return t.result.TestCaseTitle
		}
		testDocID := func(t pipelineTest) string {
			return fmt.Sprintf("%s_test_%d_%d", runDocID, t.run.ID, t.result.ID)
		}
		for _, t := range failedTests {
			fmt.Fprintf(&sb, "- %s [%s] (document %s)\n", testName(t), t.result.Outcome, testDocID(t))
		}
		if len(flakyTests) > 0 {
			sb.WriteString("\nFlaky tests (passed on retry after failing):\n")
		}
		for _, t := range flakyTests {
			fmt.Fprintf(&sb, "- %s [%s on retry] (document %s)\n", testName(t), t.result.Outcome, testDocID(t))
		}
		for _, t := range append(failedTests, flakyTests...) {
			id := testDocID(t)
			name := testName(t)
			flaky := !isFailedTestOutcome(t.result.Outcome)
			kind := "failed"
			if flaky {
				kind = "flaky"
			}

			var tb strings.Builder
			if flaky {
				fmt.Fprintf(&tb, "Flaky test (failed, then passed on retry): %s\n", name)
			} else {
				fmt.Fprintf(&tb, "Failed test: %s\n", name)
			}
			if t.result.TestCaseTitle != "" && t.result.TestCaseTitle != name {
				writeField(&tb, "Title", t.result.TestCaseTitle)
			}
			writeField(&tb, "Outcome", t.result.Outcome)
			writeField(&tb, "Test storage", t.result.AutomatedTestStorage)
			writeField(&tb, "Test run", fmt.Sprintf("%s (id %d)", t.run.Name, t.run.ID))
			if t.result.DurationInMs > 0 {
				writeField(&tb, "Duration", (time.Duration(t.result.DurationInMs) * time.Millisecond).String())
			}
			writeField(&tb, "Pipeline run", fmt.Sprintf("%s (%s)", runLabel, b.Result))
			writeField(&tb, "Branch", b.SourceBranch)
			writeField(&tb, "Commit", b.SourceVersion)
			if t.result.ErrorMessage != "" {
				tb.WriteString("\nError message:\n" + t.result.ErrorMessage + "\n")
			}
			if t.result.StackTrace != "" {
				tb.WriteString("\nStack trace:\n" + t.result.StackTrace + "\n")
			}
			writeSubResults(&tb, t.result.SubResults, 0)
			webURL := fmt.Sprintf("%s/_build/results?buildId=%d&view=ms.vss-test-web.build-test-results-tab&runId=%d&resultId=%d",
				scope.webBase(), b.ID, t.run.ID, t.result.ID)
			doc := newDoc(id, fmt.Sprintf("%s %s test: %s", runLabel, kind, name), tb.String(), "test", webURL,
				fmt.Sprintf("Run #%s > test %s", b.BuildNumber, name))
			if flaky {
				doc.Properties["flaky"] = "true"
			}
			children = append(children, doc)
		}
	}

	runDoc := newDoc(runDocID, fmt.Sprintf("%s %s", runLabel, b.Result), sb.String(), "run", runWebURL, "Run #"+b.BuildNumber)
	if strings.EqualFold(b.Repository.Type, "TfsGit") && b.Repository.Name != "" && b.SourceVersion != "" {
		runDoc.Properties["commit_web_url"] = fmt.Sprintf("%s/_git/%s/commit/%s", scope.webBase(), url.PathEscape(b.Repository.Name), b.SourceVersion)
	}
	return append([]models.SourceDocument{runDoc}, children...)
}

// writeSubResults lists every attempt/iteration of a test, with the error
// and stack trace of each failed one, nested sub-results indented.
func writeSubResults(sb *strings.Builder, subs []adoTestSubResult, depth int) {
	if len(subs) == 0 {
		return
	}
	indent := strings.Repeat("  ", depth)
	if depth == 0 {
		sb.WriteString("\nAttempts / sub-results:\n")
	}
	for i, s := range subs {
		name := s.DisplayName
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		fmt.Fprintf(sb, "%s- %s: %s", indent, name, s.Outcome)
		if s.DurationInMs > 0 {
			fmt.Fprintf(sb, " (%s)", time.Duration(s.DurationInMs)*time.Millisecond)
		}
		sb.WriteString("\n")
		if s.ErrorMessage != "" {
			sb.WriteString(indent + "  Error message:\n" + s.ErrorMessage + "\n")
		}
		if s.StackTrace != "" {
			sb.WriteString(indent + "  Stack trace:\n" + s.StackTrace + "\n")
		}
		writeSubResults(sb, s.SubResults, depth+1)
	}
}

func writeField(sb *strings.Builder, label, value string) {
	if value != "" {
		fmt.Fprintf(sb, "%s: %s\n", label, value)
	}
}

func angle(s string) string {
	if s == "" {
		return ""
	}
	return "<" + s + ">"
}
