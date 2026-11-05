// Azure DevOps pipeline-run fetching for APISource. A pipeline-run source
// covers one build definition: every completed run of the last Days days
// becomes a run document, and each failed or partially succeeded run also
// yields one document per failed step (its complete log) and one per failed
// test result, both linked back to the run via the parent_doc_id property.
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
	adoFailedTestOutcomes = "Failed,Aborted,Error"
)

// adoDetailedResults are the run results whose failed steps and failed
// tests are fetched in full; any other run gets only its summary document.
var adoDetailedResults = map[string]bool{"failed": true, "partiallysucceeded": true}

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
	Log      *struct {
		ID int `json:"id"`
	} `json:"log"`
	Issues []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"issues"`
}

type adoTestRun struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type adoTestResult struct {
	ID                   int     `json:"id"`
	TestCaseTitle        string  `json:"testCaseTitle"`
	AutomatedTestName    string  `json:"automatedTestName"`
	AutomatedTestStorage string  `json:"automatedTestStorage"`
	Outcome              string  `json:"outcome"`
	DurationInMs         float64 `json:"durationInMs"`
	ErrorMessage         string  `json:"errorMessage"`
	StackTrace           string  `json:"stackTrace"`
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

// pipelineRunDetail is everything fetched for a failed/partial run.
type pipelineRunDetail struct {
	issues []string // "[error] Stage > Job > Task: message"
	steps  []pipelineStep
	tests  []pipelineTest
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

	for i := 0; i < n && workCtx.Err() == nil; i++ {
		if !adoDetailedResults[strings.ToLower(builds[i].Result)] {
			report()
			continue
		}
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

// fetchPipelineRunDetail fetches a failed run's timeline issues, the full
// log of every failed step, and every failed test result.
func (a *APISource) fetchPipelineRunDetail(ctx context.Context, client *http.Client, scope adoPipelineScope, b adoBuild) (*pipelineRunDetail, error) {
	records, err := a.fetchAdoTimeline(ctx, client, scope, b.ID)
	if err != nil {
		return nil, err
	}
	detail := &pipelineRunDetail{}
	tl := newTimeline(records)
	for _, r := range tl.ordered {
		for _, is := range r.Issues {
			detail.issues = append(detail.issues, fmt.Sprintf("[%s] %s: %s", is.Type, tl.path(r), is.Message))
		}
	}
	for _, r := range tl.failedLeaves() {
		logURL := fmt.Sprintf("%s/build/builds/%d/logs/%d?api-version=%s", scope.apiBase(), b.ID, r.Log.ID, scope.apiVersion)
		data, err := a.getRaw(ctx, client, logURL)
		if err != nil {
			return nil, fmt.Errorf("fetching log %d of step %q: %w", r.Log.ID, tl.path(r), err)
		}
		detail.steps = append(detail.steps, pipelineStep{
			record: r, jobID: tl.jobID(r), path: tl.path(r), log: cleanLogText(string(data)),
		})
	}

	runs, err := a.fetchAdoTestRuns(ctx, client, scope, b.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		results, err := a.fetchAdoFailedTestResults(ctx, client, scope, run.ID)
		if err != nil {
			return nil, fmt.Errorf("test run %d: %w", run.ID, err)
		}
		for _, res := range results {
			detail.tests = append(detail.tests, pipelineTest{run: run, result: res})
		}
	}
	return detail, nil
}

func (a *APISource) fetchAdoTimeline(ctx context.Context, client *http.Client, scope adoPipelineScope, buildID int) ([]adoTimelineRecord, error) {
	data, err := a.getRaw(ctx, client, fmt.Sprintf("%s/build/builds/%d/timeline?api-version=%s", scope.apiBase(), buildID, scope.apiVersion))
	if err != nil {
		return nil, fmt.Errorf("fetching timeline: %w", err)
	}
	var tl struct {
		Records []adoTimelineRecord `json:"records"`
	}
	if err := json.Unmarshal(data, &tl); err != nil {
		return nil, fmt.Errorf("timeline response is not valid JSON: %w", err)
	}
	return tl.Records, nil
}

func (a *APISource) fetchAdoTestRuns(ctx context.Context, client *http.Client, scope adoPipelineScope, buildID int) ([]adoTestRun, error) {
	buildURI := url.QueryEscape(fmt.Sprintf("vstfs:///Build/Build/%d", buildID))
	var all []adoTestRun
	for skip := 0; ; skip += adoTestRunsPageSize {
		runsURL := fmt.Sprintf("%s/test/runs?buildUri=%s&$top=%d&$skip=%d&api-version=%s",
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

func (a *APISource) fetchAdoFailedTestResults(ctx context.Context, client *http.Client, scope adoPipelineScope, runID int) ([]adoTestResult, error) {
	var all []adoTestResult
	for skip := 0; ; skip += adoTestResultsPageSize {
		resultsURL := fmt.Sprintf("%s/test/Runs/%d/results?outcomes=%s&$top=%d&$skip=%d&api-version=%s",
			scope.apiBase(), runID, adoFailedTestOutcomes, adoTestResultsPageSize, skip, scope.apiVersion)
		data, err := a.getRaw(ctx, client, resultsURL)
		if err != nil {
			return nil, fmt.Errorf("listing failed test results: %w", err)
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
			id := fmt.Sprintf("%s_step_%s", runDocID, st.record.ID)
			fmt.Fprintf(&sb, "- %s (document %s)\n", st.path, id)
			webURL := fmt.Sprintf("%s/_build/results?buildId=%d&view=logs", scope.webBase(), b.ID)
			if st.jobID != "" {
				webURL += "&j=" + st.jobID
			}
			if st.jobID != st.record.ID {
				webURL += "&t=" + st.record.ID
			}
			doc := newDoc(id, fmt.Sprintf("%s failed step: %s", runLabel, st.path), st.log, "step", webURL,
				fmt.Sprintf("Run #%s > %s", b.BuildNumber, st.path))
			doc.Properties["step_path"] = st.path
			children = append(children, doc)
		}
		if len(detail.tests) > 0 {
			sb.WriteString("\nFailed tests:\n")
		}
		for _, t := range detail.tests {
			id := fmt.Sprintf("%s_test_%d_%d", runDocID, t.run.ID, t.result.ID)
			name := t.result.AutomatedTestName
			if name == "" {
				name = t.result.TestCaseTitle
			}
			fmt.Fprintf(&sb, "- %s [%s] (document %s)\n", name, t.result.Outcome, id)

			var tb strings.Builder
			fmt.Fprintf(&tb, "Failed test: %s\n", name)
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
			webURL := fmt.Sprintf("%s/_build/results?buildId=%d&view=ms.vss-test-web.build-test-results-tab&runId=%d&resultId=%d",
				scope.webBase(), b.ID, t.run.ID, t.result.ID)
			children = append(children, newDoc(id, fmt.Sprintf("%s failed test: %s", runLabel, name), tb.String(), "test", webURL,
				fmt.Sprintf("Run #%s > test %s", b.BuildNumber, name)))
		}
	}

	runDoc := newDoc(runDocID, fmt.Sprintf("%s %s", runLabel, b.Result), sb.String(), "run", runWebURL, "Run #"+b.BuildNumber)
	if strings.EqualFold(b.Repository.Type, "TfsGit") && b.Repository.Name != "" && b.SourceVersion != "" {
		runDoc.Properties["commit_web_url"] = fmt.Sprintf("%s/_git/%s/commit/%s", scope.webBase(), url.PathEscape(b.Repository.Name), b.SourceVersion)
	}
	return append([]models.SourceDocument{runDoc}, children...)
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
