// Azure DevOps pipeline-run extras computed at sync time, so every filter
// and sort the agent needs is answered from the index (Conduit is a cache;
// tool calls never reach Azure DevOps):
//
//   - run-level filters and sort keys on every run, step and test document
//     (pipeline, status, reason, requester, build number, tags, rerun, the
//     names of failed stages/jobs/steps/tests, the commits the run built);
//   - the commits each run built since the previous run (build changes);
//   - runs still queued or in progress at sync, as summary documents;
//   - deployment records of the pipeline's environments;
//   - one test-history document per test that failed or was flaky in the
//     window.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// adoBuildChange is one commit a build picked up since the previous build.
type adoBuildChange struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Author  struct {
		DisplayName string `json:"displayName"`
		UniqueName  string `json:"uniqueName"`
	} `json:"author"`
	Timestamp string `json:"timestamp"`
}

// fetchBuildChanges pages through every change (commit) of a build.
func (a *APISource) fetchBuildChanges(ctx context.Context, client *http.Client, scope adoPipelineScope, buildID int) ([]adoBuildChange, error) {
	base := fmt.Sprintf("%s/build/builds/%d/changes?api-version=%s&$top=1000", scope.apiBase(), buildID, scope.apiVersion)
	var all []adoBuildChange
	token := ""
	seen := map[string]bool{}
	for {
		u := base
		if token != "" {
			u += "&continuationToken=" + url.QueryEscape(token)
		}
		data, header, err := a.getRawWithHeader(ctx, client, u)
		if err != nil {
			return nil, fmt.Errorf("listing changes: %w", err)
		}
		var page struct {
			Value []adoBuildChange `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("changes response is not valid JSON: %w", err)
		}
		all = append(all, page.Value...)
		token = header.Get("x-ms-continuationtoken")
		if token == "" {
			return all, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("changes endpoint returned continuation token %q twice", token)
		}
		seen[token] = true
	}
}

// collectFailedNames records the names of failed stages, jobs and steps
// across every attempt's timeline.
func (d *pipelineRunDetail) collectFailedNames(timelines []*timeline) {
	add := func(list *[]string, name string) {
		for _, n := range *list {
			if n == name {
				return
			}
		}
		*list = append(*list, name)
	}
	for _, tl := range timelines {
		for _, r := range tl.ordered {
			if !isFailedRecord(r) || r.Name == "" {
				continue
			}
			switch strings.ToLower(r.Type) {
			case "stage":
				add(&d.failedStages, r.Name)
			case "job":
				add(&d.failedJobs, r.Name)
			case "task":
				add(&d.failedSteps, r.Name)
			}
		}
	}
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
// the job execution whose time span holds the run. A run with one attempt
// is attempt 1; 0 means it can't be told.
func (d *pipelineRunDetail) testRunAttempt(run adoTestRun, jobs []jobExecution) (int, string) {
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
	if d.attempts <= 1 {
		return 1, ""
	}
	return 0, ""
}

// testsByAttempt summarizes, for a rerun run, which tests failed in which
// attempt, so the attempts can be compared at a glance.
func testsByAttempt(d *pipelineRunDetail) string {
	by := map[int][]string{}
	for _, t := range d.tests {
		label := testNameOf(t.result)
		if !isFailedTestOutcome(t.result.Outcome) {
			label += " (passed on in-run retry)"
		}
		by[t.attempt] = append(by[t.attempt], label)
	}
	var sb strings.Builder
	sb.WriteString("\nTests by attempt:\n")
	for n := 1; n <= d.attempts; n++ {
		if len(by[n]) == 0 {
			fmt.Fprintf(&sb, "- attempt %d: no failed tests\n", n)
			continue
		}
		fmt.Fprintf(&sb, "- attempt %d: %s\n", n, strings.Join(by[n], ", "))
	}
	if len(by[0]) > 0 {
		fmt.Fprintf(&sb, "- attempt unknown: %s\n", strings.Join(by[0], ", "))
	}
	return sb.String()
}

// adoReasonValues maps Azure DevOps' build reasons onto plain filter values
// (both are stored, so either matches).
func adoReasonValues(reason string) []string {
	r := strings.ToLower(reason)
	out := []string{r}
	switch {
	case r == "manual":
	case strings.Contains(r, "ci"):
		out = append(out, "ci")
	case r == "pullrequest":
		out = append(out, "pr")
	case r == "schedule":
		out = append(out, "scheduled")
	}
	return out
}

func unixOrZero(s string) float64 {
	if t := parseTime(s); !t.IsZero() {
		return float64(t.Unix())
	}
	return 0
}

// adoRunSort is a run's sort keys: queued/started/finished time, duration
// in seconds and the number of attempts.
func adoRunSort(b adoBuild, attempts int) map[string]float64 {
	sortKeys := map[string]float64{}
	for k, v := range map[string]string{"queued": b.QueueTime, "started": b.StartTime, "finished": b.FinishTime} {
		if ts := unixOrZero(v); ts != 0 {
			sortKeys[k] = ts
		}
	}
	if start, finish := parseTime(b.StartTime), parseTime(b.FinishTime); !start.IsZero() && !finish.IsZero() {
		sortKeys["duration"] = finish.Sub(start).Seconds()
	}
	if attempts < 1 {
		attempts = 1
	}
	sortKeys["attempts"] = float64(attempts)
	return sortKeys
}

// adoRunFilters adds the run-level filters every document of the run gets.
func adoRunFilters(f *models.DocumentFilters, b adoBuild, detail *pipelineRunDetail, changes []adoBuildChange) {
	if f.Date = parseTime(b.FinishTime); f.Date.IsZero() {
		f.Date = parseTime(b.QueueTime)
	}
	f.Add(models.FilterBuildID, strconv.Itoa(b.ID))
	f.Add(models.FilterCommit, b.SourceVersion)
	f.Add(models.FilterPipeline, b.Definition.Name, strconv.Itoa(b.Definition.ID))
	f.Add(models.FilterStatus, b.Status)
	f.Add(models.FilterReason, adoReasonValues(b.Reason)...)
	f.Add(models.FilterRequestedFor, b.RequestedFor.DisplayName, b.RequestedFor.UniqueName)
	f.Add(models.FilterBuildNumber, b.BuildNumber)
	f.Add(models.FilterTag, b.Tags...)
	f.Add(models.FilterRepository, b.Repository.Name)
	rerun := detail != nil && detail.attempts > 1
	f.Add(models.FilterRerun, strconv.FormatBool(rerun))
	if detail != nil {
		f.Add(models.FilterFailedStage, detail.failedStages...)
		f.Add(models.FilterFailedJob, detail.failedJobs...)
		f.Add(models.FilterFailedStep, detail.failedSteps...)
		for _, t := range detail.tests {
			f.Add(models.FilterFailedTest, testNameOf(t.result))
		}
	}
	for _, c := range changes {
		f.Add(models.FilterContainsCommit, c.ID)
	}
}

func testNameOf(r adoTestResult) string {
	if r.AutomatedTestName != "" {
		return r.AutomatedTestName
	}
	return r.TestCaseTitle
}

// writeChanges lists the commits a run built since the previous run.
func writeChanges(sb *strings.Builder, changes []adoBuildChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(sb, "\nChanges since the previous run (%d):\n", len(changes))
	for _, c := range changes {
		msg, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
		who := strings.TrimSpace(c.Author.DisplayName + " " + angle(c.Author.UniqueName))
		fmt.Fprintf(sb, "- %s", c.ID)
		if who != "" {
			fmt.Fprintf(sb, " %s", who)
		}
		if c.Timestamp != "" {
			fmt.Fprintf(sb, " (%s)", c.Timestamp)
		}
		if msg != "" {
			fmt.Fprintf(sb, ": %s", msg)
		}
		sb.WriteString("\n")
	}
}

// adoPendingRunDocument is a run still queued or in progress at sync: a
// summary only — its timeline, logs and tests come once it has completed.
func (a *APISource) adoPendingRunDocument(scope adoPipelineScope, b adoBuild) models.SourceDocument {
	runLabel := fmt.Sprintf("%s #%s", b.Definition.Name, b.BuildNumber)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s (at the last sync)\n", runLabel, b.Status)
	fmt.Fprintf(&sb, "Pipeline: %s (definition %d)\n", b.Definition.Name, b.Definition.ID)
	fmt.Fprintf(&sb, "Run: #%s (build id %d)\n", b.BuildNumber, b.ID)
	writeField(&sb, "Status", b.Status)
	writeField(&sb, "Reason", b.Reason)
	writeField(&sb, "Branch", b.SourceBranch)
	writeField(&sb, "Commit", b.SourceVersion)
	writeField(&sb, "Requested for", strings.TrimSpace(b.RequestedFor.DisplayName+" "+angle(b.RequestedFor.UniqueName)))
	writeField(&sb, "Queued", b.QueueTime)
	writeField(&sb, "Started", b.StartTime)
	sb.WriteString("The run hadn't completed when Conduit last synced; sync again for its result.\n")
	webURL := b.Links.Web.Href
	if !isWebURL(webURL) {
		webURL = fmt.Sprintf("%s/_build/results?buildId=%d", scope.webBase(), b.ID)
	}
	var f models.DocumentFilters
	f.Add(models.FilterType, "run")
	f.Add(models.FilterBranch, b.SourceBranch, shortBranch(b.SourceBranch))
	adoRunFilters(&f, b, nil, nil)
	return models.SourceDocument{
		ID:   fmt.Sprintf("%s_run_%d", a.src.ID, b.ID),
		Text: strings.TrimSpace(sb.String()),
		Tags: map[string]string{"source_id": a.src.ID, "source_name": a.src.Name},
		Properties: map[string]string{
			"title": fmt.Sprintf("%s %s", runLabel, b.Status), "type": "run", "build_id": strconv.Itoa(b.ID),
			"status": b.Status, "branch": b.SourceBranch, "commit": b.SourceVersion, "web_url": webURL,
		},
		Filters:     f,
		Sort:        adoRunSort(b, 1),
		ChunkHeader: "Run #" + b.BuildNumber,
	}
}

// adoDeploymentRecord is one deployment of a pipeline run to an environment.
type adoDeploymentRecord struct {
	ID         int    `json:"id"`
	StageName  string `json:"stageName"`
	JobName    string `json:"jobName"`
	Result     string `json:"result"`
	QueueTime  string `json:"queueTime"`
	StartTime  string `json:"startTime"`
	FinishTime string `json:"finishTime"`
	Definition struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"definition"`
	Owner struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"owner"`
}

// fetchAdoDeployments lists the pipeline's deployments to every environment
// of the project in the window. A server without environments (404) has no
// deployments to record.
func (a *APISource) fetchAdoDeployments(ctx context.Context, client *http.Client, scope adoPipelineScope, minTime time.Time, builds map[int]adoBuild) ([]models.SourceDocument, error) {
	version := commentsAPIVersion(scope.apiVersion)
	data, err := a.getRaw(ctx, client, fmt.Sprintf("%s/distributedtask/environments?api-version=%s&$top=1000", scope.apiBase(), version))
	var status *httpStatusError
	if errors.As(err, &status) && status.status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing environments: %w", err)
	}
	var envs struct {
		Value []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &envs); err != nil {
		return nil, fmt.Errorf("environments response is not valid JSON: %w", err)
	}
	var docs []models.SourceDocument
	for _, env := range envs.Value {
		records, err := a.environmentDeployments(ctx, client, scope, env.ID, minTime)
		if err != nil {
			return nil, fmt.Errorf("environment %q: %w", env.Name, err)
		}
		for _, r := range records {
			if r.Definition.ID != scope.definitionID {
				continue
			}
			docs = append(docs, a.deploymentDocument(scope, env.Name, r, builds[r.Owner.ID]))
		}
	}
	return docs, nil
}

// environmentDeployments pages through an environment's deployment records,
// newest first, until they are older than minTime.
func (a *APISource) environmentDeployments(ctx context.Context, client *http.Client, scope adoPipelineScope, envID int, minTime time.Time) ([]adoDeploymentRecord, error) {
	base := fmt.Sprintf("%s/distributedtask/environments/%d/environmentdeploymentrecords?api-version=%s&top=1000",
		scope.apiBase(), envID, commentsAPIVersion(scope.apiVersion))
	var out []adoDeploymentRecord
	token := ""
	seen := map[string]bool{}
	for {
		u := base
		if token != "" {
			u += "&continuationToken=" + url.QueryEscape(token)
		}
		data, header, err := a.getRawWithHeader(ctx, client, u)
		if err != nil {
			return nil, err
		}
		var page struct {
			Value []adoDeploymentRecord `json:"value"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("deployment records response is not valid JSON: %w", err)
		}
		older := false
		for _, r := range page.Value {
			when := parseTime(r.QueueTime)
			if !when.IsZero() && when.Before(minTime) {
				older = true
				continue
			}
			out = append(out, r)
		}
		token = header.Get("x-ms-continuationtoken")
		if token == "" || older {
			return out, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("deployment records endpoint returned continuation token %q twice", token)
		}
		seen[token] = true
	}
}

func (a *APISource) deploymentDocument(scope adoPipelineScope, env string, r adoDeploymentRecord, b adoBuild) models.SourceDocument {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Deployment of %s to %s: %s\n", orDefault(r.Owner.Name, fmt.Sprintf("build %d", r.Owner.ID)), env, r.Result)
	writeField(&sb, "Environment", env)
	writeField(&sb, "Result", r.Result)
	fmt.Fprintf(&sb, "Pipeline: %s (definition %d)\n", r.Definition.Name, r.Definition.ID)
	fmt.Fprintf(&sb, "Run: %s (build id %d)\n", r.Owner.Name, r.Owner.ID)
	writeField(&sb, "Stage", r.StageName)
	writeField(&sb, "Job", r.JobName)
	writeField(&sb, "Branch", b.SourceBranch)
	writeField(&sb, "Commit", b.SourceVersion)
	writeField(&sb, "Queued", r.QueueTime)
	writeField(&sb, "Started", r.StartTime)
	writeField(&sb, "Finished", r.FinishTime)

	var f models.DocumentFilters
	f.Add(models.FilterType, "deployment")
	f.Add(models.FilterEnvironment, env)
	f.Add(models.FilterResult, r.Result)
	f.Add(models.FilterPipeline, r.Definition.Name, strconv.Itoa(r.Definition.ID))
	f.Add(models.FilterBuildID, strconv.Itoa(r.Owner.ID))
	f.Add(models.FilterCommit, b.SourceVersion)
	f.Add(models.FilterBranch, b.SourceBranch, shortBranch(b.SourceBranch))
	if f.Date = parseTime(r.FinishTime); f.Date.IsZero() {
		f.Date = parseTime(r.QueueTime)
	}
	sortKeys := map[string]float64{}
	for k, v := range map[string]string{"queued": r.QueueTime, "started": r.StartTime, "finished": r.FinishTime} {
		if ts := unixOrZero(v); ts != 0 {
			sortKeys[k] = ts
		}
	}
	return models.SourceDocument{
		ID:   fmt.Sprintf("%s_deployment_%d", a.src.ID, r.ID),
		Text: strings.TrimSpace(sb.String()),
		Tags: map[string]string{"source_id": a.src.ID, "source_name": a.src.Name},
		Properties: map[string]string{
			"title": fmt.Sprintf("Deployment to %s: %s (%s)", env, r.Result, r.Owner.Name), "type": "deployment",
			"environment": env, "build_id": strconv.Itoa(r.Owner.ID), "result": r.Result,
			"web_url": fmt.Sprintf("%s/_build/results?buildId=%d", scope.webBase(), r.Owner.ID),
		},
		Filters:     f,
		Sort:        sortKeys,
		ChunkHeader: "Deployment to " + env,
	}
}

// testOccurrence is one run in which a test failed or passed only on retry.
type testOccurrence struct {
	build   adoBuild
	flaky   bool
	err     string
	attempt int // 0 when unknown or the run had a single attempt
}

// testHistoryDocuments builds one document per test that failed or was
// flaky in any run of the window: every such run, first and last failure,
// counts out of the window's completed runs.
func (a *APISource) testHistoryDocuments(scope adoPipelineScope, builds []adoBuild, details []*pipelineRunDetail) []models.SourceDocument {
	byTest := map[string][]testOccurrence{}
	completed := 0
	for i, b := range builds {
		if !strings.EqualFold(b.Status, "completed") {
			continue
		}
		completed++
		d := details[i]
		if d == nil {
			continue
		}
		for _, t := range d.tests {
			name := testNameOf(t.result)
			msg, _, _ := strings.Cut(strings.TrimSpace(t.result.ErrorMessage), "\n")
			attempt := t.attempt
			if d.attempts <= 1 {
				attempt = 0
			}
			byTest[name] = append(byTest[name], testOccurrence{build: b, flaky: !isFailedTestOutcome(t.result.Outcome), err: msg, attempt: attempt})
		}
	}
	names := make([]string, 0, len(byTest))
	for n := range byTest {
		names = append(names, n)
	}
	sort.Strings(names)

	var docs []models.SourceDocument
	for _, name := range names {
		occ := byTest[name]
		sort.SliceStable(occ, func(i, j int) bool { return occ[i].build.FinishTime > occ[j].build.FinishTime })
		failed, flaky := 0, 0
		for _, o := range occ {
			if o.flaky {
				flaky++
			} else {
				failed++
			}
		}
		pipeline := occ[0].build.Definition.Name
		var sb strings.Builder
		fmt.Fprintf(&sb, "Test history: %s\n", name)
		fmt.Fprintf(&sb, "Pipeline: %s (definition %d)\n", pipeline, scope.definitionID)
		fmt.Fprintf(&sb, "Failed in %d and passed only on retry in %d of the %d completed runs of the last %d days.\n",
			failed, flaky, completed, scope.days)
		writeField(&sb, "Last failure", occ[0].build.FinishTime+" (#"+occ[0].build.BuildNumber+")")
		last := occ[len(occ)-1]
		writeField(&sb, "First failure in the window", last.build.FinishTime+" (#"+last.build.BuildNumber+")")
		sb.WriteString("\nRuns:\n")
		for _, o := range occ {
			kind := "failed"
			if o.flaky {
				kind = "passed on retry"
			}
			fmt.Fprintf(&sb, "- #%s (build id %d, %s, %s) %s", o.build.BuildNumber, o.build.ID, o.build.FinishTime,
				shortBranch(o.build.SourceBranch), kind)
			if o.attempt > 0 {
				fmt.Fprintf(&sb, " in attempt %d", o.attempt)
			}
			if o.err != "" {
				fmt.Fprintf(&sb, ": %s", o.err)
			}
			sb.WriteString("\n")
		}

		h := fnv.New64a()
		h.Write([]byte(name))
		var f models.DocumentFilters
		f.Add(models.FilterType, "test_history")
		f.Add(models.FilterFailedTest, name)
		f.Add(models.FilterPipeline, pipeline, strconv.Itoa(scope.definitionID))
		f.Add(models.FilterFlaky, strconv.FormatBool(flaky > 0))
		f.Date = parseTime(occ[0].build.FinishTime)
		docs = append(docs, models.SourceDocument{
			ID:   fmt.Sprintf("%s_testhistory_%x", a.src.ID, h.Sum64()),
			Text: strings.TrimSpace(sb.String()),
			Tags: map[string]string{"source_id": a.src.ID, "source_name": a.src.Name},
			Properties: map[string]string{
				"title": "Test history: " + name, "type": "test_history",
				"fail_count": strconv.Itoa(failed), "flaky_count": strconv.Itoa(flaky),
				"web_url": fmt.Sprintf("%s/_build/results?buildId=%d&view=ms.vss-test-web.build-test-results-tab", scope.webBase(), occ[0].build.ID),
			},
			Filters: f,
			Sort: map[string]float64{
				"last_failed": unixOrZero(occ[0].build.FinishTime),
				"fail_count":  float64(failed),
				"flaky_count": float64(flaky),
			},
			ChunkHeader: "Test history: " + name,
		})
	}
	return docs
}

// adoBuildArtifact is one artifact a build published (listing only).
type adoBuildArtifact struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Resource struct {
		Type        string         `json:"type"`
		DownloadURL string         `json:"downloadUrl"`
		URL         string         `json:"url"`
		Properties  map[string]any `json:"properties"`
	} `json:"resource"`
}

// fetchBuildArtifacts lists the artifacts a build published — names, types,
// sizes and download links, never their contents. A server without the
// artifacts API yields a note instead.
func (a *APISource) fetchBuildArtifacts(ctx context.Context, client *http.Client, scope adoPipelineScope, buildID int) ([]attachmentInfo, []string, error) {
	u := fmt.Sprintf("%s/build/builds/%d/artifacts?api-version=%s", scope.apiBase(), buildID, scope.apiVersion)
	data, err := a.getRaw(ctx, client, u)
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

// fetchTestAttachments lists the attachments of a test run (resultID 0) or
// of one of its results — metadata only. The API is preview-only on Azure
// DevOps; a server without it yields a note instead.
func (a *APISource) fetchTestAttachments(ctx context.Context, client *http.Client, scope adoPipelineScope, runID, resultID int) ([]attachmentInfo, []string, error) {
	u := fmt.Sprintf("%s/test/Runs/%d", scope.apiBase(), runID)
	if resultID != 0 {
		u += fmt.Sprintf("/Results/%d", resultID)
	}
	u += "/attachments?api-version=" + previewVersion(scope.apiVersion)
	data, err := a.getRaw(ctx, client, u)
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
