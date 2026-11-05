package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func pipelineSrc(orgURL string, cfg map[string]string) *models.SourceDefinition {
	merged := map[string]string{
		"Provider":     "api",
		"Platform":     "ado",
		"AdoOrg":       orgURL,
		"AdoProject":   "proj",
		"Url":          orgURL + "/proj",
		"DefinitionId": "12",
		"Days":         "30",
	}
	for k, v := range cfg {
		merged[k] = v
	}
	return &models.SourceDefinition{ID: "src-1", Name: "CI", Type: models.SourcePipelineRun, Config: merged}
}

func build(id int, number, result, branch string) map[string]any {
	return map[string]any{
		"id": id, "buildNumber": number, "status": "completed", "result": result,
		"reason": "individualCI", "sourceBranch": branch, "sourceVersion": fmt.Sprintf("sha%d", id),
		"startTime": "2026-10-14T08:00:00Z", "finishTime": "2026-10-14T08:10:30Z",
		"definition":   map[string]any{"id": 12, "name": "CI"},
		"project":      map[string]any{"name": "proj"},
		"repository":   map[string]any{"name": "api", "type": "TfsGit"},
		"requestedFor": map[string]any{"displayName": "Jane", "uniqueName": "jane@example.com"},
		"triggerInfo":  map[string]any{"ci.message": "Fix parser"},
		"_links":       map[string]any{"web": map[string]any{"href": fmt.Sprintf("https://ado.example/proj/_build/results?buildId=%d", id)}},
	}
}

// pipelineStub fakes the ADO build and test APIs for one definition: build 3
// failed (a failed task and a job that failed without a failed task), build 4
// succeeded cleanly (only a warning), build 2 succeeded after "Rerun failed
// jobs" — attempt 1's Test task failed, a test failed in attempt 1's test run
// and another passed only on an in-run retry — and build 1 (on a second page)
// partially succeeded with only a warning and no test runs.
type pipelineStub struct {
	mu       sync.Mutex
	requests []string
	failLog  int // HTTP status to return for log requests, 0 = serve them
}

func (p *pipelineStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.URL.String())
		failLog := p.failLog
		p.mu.Unlock()
		q := r.URL.Query()
		path := strings.TrimPrefix(r.URL.Path, "/proj/_apis/")
		switch {
		case path == "build/builds":
			if q.Get("continuationToken") == "" {
				w.Header().Set("x-ms-continuationtoken", "tok1")
				writeJSON(w, map[string]any{"value": []any{build(3, "20261014.3", "failed", "refs/heads/main"),
					build(4, "20261014.4", "succeeded", "refs/heads/main"), build(2, "20261014.2", "succeeded", "refs/heads/main")}})
				return
			}
			writeJSON(w, map[string]any{"value": []any{build(1, "20261014.1", "partiallySucceeded", "refs/pull/42/merge")}})
		case path == "build/builds/4/timeline":
			writeJSON(w, map[string]any{"id": "tl-4", "records": []any{
				map[string]any{"id": "y1", "type": "Task", "name": "Lint", "result": "succeededWithIssues", "log": map[string]any{"id": 2},
					"issues": []any{map[string]any{"type": "warning", "message": "deprecated API"}}},
			}})
		case path == "build/builds/2/timeline":
			writeJSON(w, map[string]any{"id": "tl-2", "records": []any{
				map[string]any{"id": "s1", "type": "Stage", "name": "Build", "result": "succeeded", "order": 1, "attempt": 1},
				map[string]any{"id": "p1", "parentId": "s1", "type": "Phase", "name": "Linux", "result": "succeeded", "order": 1, "attempt": 2},
				map[string]any{"id": "j1", "parentId": "p1", "type": "Job", "name": "Linux", "result": "succeeded", "order": 1, "attempt": 2,
					"log": map[string]any{"id": 30}, "previousAttempts": []any{map[string]any{"attempt": 1, "timelineId": "tl-2a1", "recordId": "j1"}}},
				map[string]any{"id": "t1", "parentId": "j1", "type": "Task", "name": "Test", "result": "succeeded", "order": 1, "attempt": 2, "log": map[string]any{"id": 31}},
			}})
		case path == "build/builds/2/timeline/tl-2a1":
			// The previous attempt's timeline holds only the rerun job.
			writeJSON(w, map[string]any{"id": "tl-2a1", "records": []any{
				map[string]any{"id": "j1", "parentId": "p1", "type": "Job", "name": "Linux", "result": "failed", "order": 1, "attempt": 1, "log": map[string]any{"id": 20}},
				map[string]any{"id": "t1", "parentId": "j1", "type": "Task", "name": "Test", "result": "failed", "order": 1, "attempt": 1, "log": map[string]any{"id": 21},
					"issues": []any{map[string]any{"type": "error", "message": "connection reset"}}},
			}})
		case path == "build/builds/2/logs/21":
			fmt.Fprint(w, "2026-10-14T08:00:00.0000000Z ##[error]connection reset\n")
		case path == "build/builds/3/timeline":
			writeJSON(w, map[string]any{"records": []any{
				map[string]any{"id": "s1", "type": "Stage", "name": "Build", "result": "failed", "order": 1},
				map[string]any{"id": "p1", "parentId": "s1", "type": "Phase", "name": "Linux", "result": "failed", "order": 1},
				map[string]any{"id": "j1", "parentId": "p1", "type": "Job", "name": "Linux", "result": "failed", "order": 1, "log": map[string]any{"id": 3}},
				map[string]any{"id": "t0", "parentId": "j1", "type": "Task", "name": "Checkout", "result": "succeeded", "order": 1, "log": map[string]any{"id": 4}},
				map[string]any{"id": "t1", "parentId": "j1", "type": "Task", "name": "Compile", "result": "failed", "order": 2, "log": map[string]any{"id": 5},
					"issues": []any{map[string]any{"type": "error", "message": "CS1002: ; expected"}}},
				map[string]any{"id": "j2", "parentId": "p1", "type": "Job", "name": "Windows", "result": "failed", "order": 2, "log": map[string]any{"id": 7},
					"issues": []any{map[string]any{"type": "error", "message": "The job exceeded the maximum time"}}},
				map[string]any{"id": "t3", "parentId": "j2", "type": "Task", "name": "Test", "result": "succeeded", "order": 1, "log": map[string]any{"id": 8}},
			}})
		case path == "build/builds/1/timeline":
			writeJSON(w, map[string]any{"records": []any{
				map[string]any{"id": "x1", "type": "Task", "name": "Lint", "result": "succeededWithIssues", "log": map[string]any{"id": 2},
					"issues": []any{map[string]any{"type": "warning", "message": "deprecated API"}}},
			}})
		case strings.HasPrefix(path, "build/builds/3/logs/"):
			if failLog != 0 {
				http.Error(w, "gone", failLog)
				return
			}
			switch strings.TrimPrefix(path, "build/builds/3/logs/") {
			case "5":
				fmt.Fprint(w, "\uFEFF2026-10-14T08:00:00.1234567Z \x1b[31merror CS1002: ; expected\x1b[0m\r\n2026-10-14T08:00:01.0000000Z Build FAILED.\r\n")
			case "7":
				fmt.Fprint(w, "2026-10-14T08:09:00.0000000Z ##[error]The job exceeded the maximum time\n")
			default:
				t.Errorf("unexpected log request %s", path)
				http.NotFound(w, r)
			}
		case path == "test/runs":
			switch q.Get("buildUri") {
			case "vstfs:///Build/Build/3":
				// No runStatistics: passed results must be checked for retries too.
				writeJSON(w, map[string]any{"value": []any{map[string]any{"id": 100, "name": "Unit tests"}}})
			case "vstfs:///Build/Build/2":
				writeJSON(w, map[string]any{"value": []any{
					map[string]any{"id": 200, "name": "Integration (attempt 1)", "runStatistics": []any{
						map[string]any{"outcome": "Failed", "count": 1}, map[string]any{"outcome": "Passed", "count": 5}}},
					map[string]any{"id": 201, "name": "Integration (attempt 2)", "runStatistics": []any{
						map[string]any{"outcome": "Passed", "count": 6},
						map[string]any{"outcome": "Passed", "count": 1, "resultMetadata": "rerun"}}},
				}})
			default:
				writeJSON(w, map[string]any{"value": []any{}})
			}
		case path == "test/Runs/100/results":
			if q.Get("outcomes") == "Passed" {
				writeJSON(w, map[string]any{"value": []any{map[string]any{"id": 8, "automatedTestName": "Parser.Tests.Stable", "outcome": "Passed"}}})
				return
			}
			writeJSON(w, map[string]any{"value": []any{map[string]any{
				"id": 7, "automatedTestName": "Parser.Tests.ParsesEmpty", "automatedTestStorage": "parser.tests.dll",
				"outcome": "Failed", "durationInMs": 1500, "errorMessage": "Expected 0 but was 1",
				"stackTrace": "at Parser.Tests.ParsesEmpty() in ParserTests.cs:line 42",
			}}})
		case path == "test/Runs/200/results":
			if q.Get("outcomes") == "Passed" {
				t.Errorf("run 200 has no retried results; passed results must not be fetched")
			}
			writeJSON(w, map[string]any{"value": []any{map[string]any{
				"id": 9, "automatedTestName": "Api.Tests.Upload", "outcome": "Failed", "errorMessage": "socket closed",
			}}})
		case path == "test/Runs/201/results":
			if q.Get("outcomes") != "Passed" {
				writeJSON(w, map[string]any{"value": []any{}})
				return
			}
			writeJSON(w, map[string]any{"value": []any{
				map[string]any{"id": 10, "automatedTestName": "Api.Tests.Download", "outcome": "Passed"},
				map[string]any{"id": 11, "automatedTestName": "Api.Tests.Retry", "outcome": "Passed", "resultGroupType": "rerun",
					"subResults": []any{
						map[string]any{"id": 1, "displayName": "Attempt 1", "outcome": "Failed", "errorMessage": "timeout waiting for lock", "stackTrace": "at Lock.Wait()"},
						map[string]any{"id": 2, "displayName": "Attempt 2", "outcome": "Passed"},
					}},
			}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fetchPipeline(t *testing.T, stub *pipelineStub, cfg map[string]string) ([]models.SourceDocument, error) {
	t.Helper()
	srv := httptest.NewServer(stub.handler(t))
	t.Cleanup(srv.Close)
	s := &APISource{src: pipelineSrc(srv.URL, cfg)}
	return s.FetchDocuments(context.Background(), nil)
}

func docByID(docs []models.SourceDocument, id string) *models.SourceDocument {
	for i := range docs {
		if docs[i].ID == id {
			return &docs[i]
		}
	}
	return nil
}

func TestFetchPipelineRunsBuildsRunStepAndTestDocuments(t *testing.T) {
	stub := &pipelineStub{}
	docs, err := fetchPipeline(t, stub, nil)
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	want := []string{
		"src-1_run_3", "src-1_run_3_step_t1", "src-1_run_3_step_j2", "src-1_run_3_test_100_7",
		"src-1_run_4",
		"src-1_run_2", "src-1_run_2_step_t1", "src-1_run_2_test_200_9", "src-1_run_2_test_201_11",
		"src-1_run_1",
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("doc IDs = %v, want %v", ids, want)
	}

	run := docByID(docs, "src-1_run_3")
	for _, s := range []string{
		"CI #20261014.3 failed", "Branch: refs/heads/main", "Commit: sha3", "Trigger ci.message: Fix parser",
		"Requested for: Jane <jane@example.com>", "Duration: 10m30s",
		"[error] Build > Linux > Compile: CS1002: ; expected",
		"[error] Build > Windows: The job exceeded the maximum time",
		"- Build > Linux > Compile (document src-1_run_3_step_t1)",
		"- Parser.Tests.ParsesEmpty [Failed] (document src-1_run_3_test_100_7)",
	} {
		if !strings.Contains(run.Text, s) {
			t.Errorf("run doc text missing %q:\n%s", s, run.Text)
		}
	}
	if run.Properties["type"] != "run" || run.Properties["parent_doc_id"] != "" {
		t.Errorf("run doc type/parent = %q/%q", run.Properties["type"], run.Properties["parent_doc_id"])
	}
	if got := run.Properties["web_url"]; got != "https://ado.example/proj/_build/results?buildId=3" {
		t.Errorf("run web_url = %q", got)
	}
	if !strings.HasSuffix(run.Properties["commit_web_url"], "/proj/_git/api/commit/sha3") {
		t.Errorf("commit_web_url = %q", run.Properties["commit_web_url"])
	}
	if run.ChunkHeader != "Run #20261014.3" {
		t.Errorf("run ChunkHeader = %q", run.ChunkHeader)
	}

	step := docByID(docs, "src-1_run_3_step_t1")
	if step.Text != "error CS1002: ; expected\nBuild FAILED." {
		t.Errorf("step log not cleaned losslessly: %q", step.Text)
	}
	if step.Properties["parent_doc_id"] != "src-1_run_3" || step.Properties["type"] != "step" ||
		step.Properties["step_path"] != "Build > Linux > Compile" {
		t.Errorf("step props = %v", step.Properties)
	}
	if step.ChunkHeader != "Run #20261014.3 > Build > Linux > Compile" {
		t.Errorf("step ChunkHeader = %q", step.ChunkHeader)
	}
	if !strings.HasSuffix(step.Properties["web_url"], "/proj/_build/results?buildId=3&view=logs&j=j1&t=t1") {
		t.Errorf("step web_url = %q", step.Properties["web_url"])
	}
	job := docByID(docs, "src-1_run_3_step_j2")
	if !strings.HasSuffix(job.Properties["web_url"], "&view=logs&j=j2") {
		t.Errorf("job-level step web_url = %q", job.Properties["web_url"])
	}

	test := docByID(docs, "src-1_run_3_test_100_7")
	for _, s := range []string{"Failed test: Parser.Tests.ParsesEmpty", "Outcome: Failed", "Duration: 1.5s",
		"Expected 0 but was 1", "ParserTests.cs:line 42", "Test run: Unit tests (id 100)"} {
		if !strings.Contains(test.Text, s) {
			t.Errorf("test doc text missing %q:\n%s", s, test.Text)
		}
	}
	if test.Properties["parent_doc_id"] != "src-1_run_3" || test.Properties["type"] != "test" {
		t.Errorf("test props = %v", test.Properties)
	}
	if !strings.Contains(test.Properties["web_url"], "runId=100&resultId=7") {
		t.Errorf("test web_url = %q", test.Properties["web_url"])
	}

	if got := step.Filters.Fields[models.FilterResult]; len(got) != 1 || got[0] != "failed" {
		t.Errorf("step result filter = %v", got)
	}
	// build_id ties a run to its step and test documents for one filtered search.
	for _, d := range []*models.SourceDocument{docByID(docs, "src-1_run_3"), step, test} {
		if got := d.Filters.Fields[models.FilterBuildID]; len(got) != 1 || got[0] != "3" {
			t.Errorf("%s build_id filter = %v", d.ID, got)
		}
	}
	pr := docByID(docs, "src-1_run_1")
	if got := strings.Join(pr.Filters.Fields[models.FilterBranch], ","); got != "refs/pull/42/merge,pull/42/merge" {
		t.Errorf("PR branch filter = %q", got)
	}
	if !strings.Contains(pr.Text, "[warning] Lint: deprecated API") {
		t.Errorf("partially succeeded run doc missing its warning:\n%s", pr.Text)
	}
	if clean := docByID(docs, "src-1_run_4"); strings.Contains(clean.Text, "Issues:") || strings.Contains(clean.Text, "rerun") {
		t.Errorf("a cleanly succeeded run must embed only its summary:\n%s", clean.Text)
	}
}

// A run that went green after "Rerun failed jobs" keeps attempt 1's failed
// step and failed test, and a test that passed only on an in-run retry is
// embedded as a flaky test.
func TestFetchPipelineRunsKeepsFailuresOfRerunAttempts(t *testing.T) {
	docs, err := fetchPipeline(t, &pipelineStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := docByID(docs, "src-1_run_2")
	for _, s := range []string{
		"CI #20261014.2 succeeded", "Attempts: 2", "Passed after rerun",
		"[error] Build > Linux > Test (attempt 1): connection reset",
		"- Build > Linux > Test (attempt 1) (document src-1_run_2_step_t1)",
		"- Api.Tests.Upload [Failed] (document src-1_run_2_test_200_9)",
		"Flaky tests (passed on retry after failing):\n- Api.Tests.Retry [Passed on retry] (document src-1_run_2_test_201_11)",
	} {
		if !strings.Contains(run.Text, s) {
			t.Errorf("rerun run doc missing %q:\n%s", s, run.Text)
		}
	}
	if strings.Contains(run.Text, "Api.Tests.Download") {
		t.Error("a test that passed without retries must not be listed")
	}

	step := docByID(docs, "src-1_run_2_step_t1")
	if step.Text != "##[error]connection reset" || step.Properties["attempt"] != "1" ||
		step.Properties["step_path"] != "Build > Linux > Test" || step.Properties["parent_doc_id"] != "src-1_run_2" {
		t.Errorf("attempt-1 step doc = %q %v", step.Text, step.Properties)
	}
	if !strings.HasSuffix(step.Properties["web_url"], "&j=j1&t=t1") {
		t.Errorf("attempt-1 step web_url = %q", step.Properties["web_url"])
	}

	flaky := docByID(docs, "src-1_run_2_test_201_11")
	for _, s := range []string{"Flaky test (failed, then passed on retry): Api.Tests.Retry", "Outcome: Passed",
		"- Attempt 1: Failed", "timeout waiting for lock", "at Lock.Wait()", "- Attempt 2: Passed"} {
		if !strings.Contains(flaky.Text, s) {
			t.Errorf("flaky test doc missing %q:\n%s", s, flaky.Text)
		}
	}
	if flaky.Properties["flaky"] != "true" || flaky.Properties["title"] != "CI #20261014.2 flaky test: Api.Tests.Retry" {
		t.Errorf("flaky test props = %v", flaky.Properties)
	}
}

// A rerun whose latest attempt failed again keeps both attempts' failed
// steps under distinct document ids.
func TestFetchPipelineRunsDistinguishesAttemptsOfTheSameStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := strings.TrimPrefix(r.URL.Path, "/proj/_apis/"); path {
		case "build/builds":
			writeJSON(w, map[string]any{"value": []any{build(5, "5", "failed", "refs/heads/main")}})
		case "build/builds/5/timeline":
			writeJSON(w, map[string]any{"id": "tl", "records": []any{
				map[string]any{"id": "j1", "type": "Job", "name": "Linux", "result": "failed", "attempt": 2, "log": map[string]any{"id": 2},
					"previousAttempts": []any{map[string]any{"attempt": 1, "timelineId": "tl-a1", "recordId": "j1"}}},
			}})
		case "build/builds/5/timeline/tl-a1":
			writeJSON(w, map[string]any{"id": "tl-a1", "records": []any{
				map[string]any{"id": "j1", "type": "Job", "name": "Linux", "result": "failed", "attempt": 1, "log": map[string]any{"id": 1}},
			}})
		case "build/builds/5/logs/1":
			fmt.Fprint(w, "first try")
		case "build/builds/5/logs/2":
			fmt.Fprint(w, "second try")
		case "test/runs":
			writeJSON(w, map[string]any{"value": []any{}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	docs, err := (&APISource{src: pipelineSrc(srv.URL, nil)}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a2, a1 := docByID(docs, "src-1_run_5_step_j1_attempt2"), docByID(docs, "src-1_run_5_step_j1")
	if a2 == nil || a1 == nil || a2.Text != "second try" || a1.Text != "first try" {
		t.Fatalf("attempt step docs missing or wrong: %v", docs)
	}
}

func TestFetchPipelineRunsSendsWindowAndTestQueries(t *testing.T) {
	stub := &pipelineStub{}
	before := time.Now().UTC().AddDate(0, 0, -30).Add(-time.Minute)
	if _, err := fetchPipeline(t, stub, nil); err != nil {
		t.Fatal(err)
	}
	var listCalls int
	for _, r := range stub.requests {
		if strings.Contains(r, "/build/builds/4/") && !strings.HasPrefix(r, "/proj/_apis/build/builds/4/timeline?") {
			t.Errorf("a green single-attempt run may cost only its timeline call: %s", r)
		}
		if strings.Contains(r, "buildUri=vstfs%3A%2F%2F%2FBuild%2FBuild%2F4") || strings.Contains(r, "Build/Build/4") {
			t.Errorf("checked test runs of a green single-attempt run: %s", r)
		}
		if strings.HasPrefix(r, "/proj/_apis/test/runs?") && !strings.Contains(r, "includeRunDetails=true") {
			t.Errorf("test runs request lacks run statistics: %s", r)
		}
		if strings.Contains(r, "/results?") && !strings.Contains(r, "detailsToInclude=SubResults") {
			t.Errorf("test results request lacks sub-results: %s", r)
		}
		if strings.HasPrefix(r, "/proj/_apis/build/builds?") {
			listCalls++
			u, _ := http.NewRequest(http.MethodGet, r, nil)
			q := u.URL.Query()
			if q.Get("definitions") != "12" || q.Get("statusFilter") != "completed" {
				t.Errorf("builds list query = %v", q)
			}
			minTime, err := time.Parse(time.RFC3339, q.Get("minTime"))
			if err != nil || minTime.Before(before) {
				t.Errorf("minTime = %q, want about 30 days ago", q.Get("minTime"))
			}
		}
		if strings.HasPrefix(r, "/proj/_apis/test/Runs/100/results?") && !strings.Contains(r, "outcomes=Failed,Aborted,Error,Timeout") &&
			!strings.Contains(r, "outcomes=Passed") {
			t.Errorf("test results request lacks failed outcomes filter: %s", r)
		}
	}
	if listCalls != 2 {
		t.Errorf("builds list fetched %d times, want 2 (continuation token followed)", listCalls)
	}
}

func TestFetchPipelineRunsFailsWhenALogCannotBeFetched(t *testing.T) {
	defer func(d time.Duration) { retryBaseDelay = d }(retryBaseDelay)
	retryBaseDelay = time.Millisecond
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		docs, err := fetchPipeline(t, &pipelineStub{failLog: status}, nil)
		if err == nil || docs != nil {
			t.Errorf("HTTP %d log: got %d docs, err %v; want the sync to fail", status, len(docs), err)
			continue
		}
		if !strings.Contains(err.Error(), "20261014.3") {
			t.Errorf("error %q does not name the run", err)
		}
	}
}

func TestFetchPipelineRunsPagesThroughTestResults(t *testing.T) {
	var skips []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/proj/_apis/")
		switch path {
		case "build/builds":
			writeJSON(w, map[string]any{"value": []any{build(3, "3", "failed", "refs/heads/main")}})
		case "build/builds/3/timeline":
			writeJSON(w, map[string]any{"records": []any{}})
		case "test/runs":
			writeJSON(w, map[string]any{"value": []any{map[string]any{"id": 100, "name": "Unit"}}})
		case "test/Runs/100/results":
			if r.URL.Query().Get("outcomes") == "Passed" {
				writeJSON(w, map[string]any{"value": []any{}})
				return
			}
			skip, _ := strconv.Atoi(r.URL.Query().Get("$skip"))
			skips = append(skips, r.URL.Query().Get("$skip"))
			n := adoTestResultsPageSize
			if skip > 0 {
				n = 1
			}
			var page []any
			for i := 0; i < n; i++ {
				page = append(page, map[string]any{"id": skip + i, "automatedTestName": "T", "outcome": "Failed"})
			}
			writeJSON(w, map[string]any{"value": page})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	docs, err := (&APISource{src: pipelineSrc(srv.URL, nil)}).FetchDocuments(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(docs) - 1; got != adoTestResultsPageSize+1 {
		t.Errorf("got %d test docs, want %d", got, adoTestResultsPageSize+1)
	}
	if strings.Join(skips, ",") != "0,1000" {
		t.Errorf("$skip sequence = %v", skips)
	}
}

func TestFetchPipelineRunsTopCapsRuns(t *testing.T) {
	stub := &pipelineStub{}
	docs, err := fetchPipeline(t, stub, map[string]string{"Top": "1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Properties["build_id"] != "3" {
			t.Errorf("Top=1 returned doc of build %s", d.Properties["build_id"])
		}
	}
	for _, r := range stub.requests {
		if strings.Contains(r, "continuationToken") {
			t.Error("Top=1 still fetched the next page")
		}
	}
}

func TestFetchPipelineRunsRequiresDefinitionAndDays(t *testing.T) {
	for _, cfg := range []map[string]string{{"DefinitionId": ""}, {"DefinitionId": "abc"}, {"Days": "0"}, {"Days": ""}} {
		s := &APISource{src: pipelineSrc("https://ado.example", cfg)}
		if _, err := s.FetchDocuments(context.Background(), nil); err == nil {
			t.Errorf("config %v: expected an error", cfg)
		}
	}
}

func TestCleanLogText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"timestamps and BOM", "\uFEFF2026-10-14T08:00:00.1234567Z first\n2026-10-14T08:00:01Z second", "first\nsecond"},
		{"ansi colors", "\x1b[1;31merror\x1b[0m here", "error here"},
		{"crlf and blank lines kept", "a\r\n\r\nb\r\n", "a\n\nb"},
		{"mid-line timestamp untouched", "value 2026-10-14T08:00:00Z kept", "value 2026-10-14T08:00:00Z kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanLogText(tt.in); got != tt.want {
				t.Errorf("cleanLogText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
