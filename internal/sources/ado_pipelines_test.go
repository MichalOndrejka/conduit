package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
// and another passed only on an in-run retry — and build 5 is still running.
type pipelineStub struct {
	mu       sync.Mutex
	requests []string
	failLog  int // HTTP status to return for log requests, 0 = serve them
}

func (p *pipelineStub) count(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
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
			writeJSON(w, map[string]any{"value": []any{build(3, "20261014.3", "failed", "refs/heads/main"),
				build(4, "20261014.4", "succeeded", "refs/heads/main")}})
		case path == "build/builds/2":
			writeJSON(w, build(2, "20261014.2", "succeeded", "refs/heads/main"))
		case path == "build/builds/3":
			writeJSON(w, build(3, "20261014.3", "failed", "refs/heads/main"))
		case path == "build/builds/4":
			writeJSON(w, build(4, "20261014.4", "succeeded", "refs/heads/main"))
		case path == "build/builds/5":
			running := build(5, "20261014.5", "", "refs/heads/main")
			running["status"], running["finishTime"] = "inProgress", ""
			writeJSON(w, running)
		case path == "build/builds/5/timeline":
			writeJSON(w, map[string]any{"records": []any{}})
		case path == "build/builds/3/changes":
			writeJSON(w, map[string]any{"value": []any{
				map[string]any{"id": "abc1234def", "message": "Break the parser\n\nlonger body", "author": map[string]any{"displayName": "Jane"}, "timestamp": "2026-10-14T07:50:00Z"},
				map[string]any{"id": "0123456789", "message": "Bump version", "author": map[string]any{"displayName": "Bot"}},
			}})
		case strings.HasPrefix(path, "build/builds/") && strings.HasSuffix(path, "/changes"):
			writeJSON(w, map[string]any{"value": []any{}})
		case path == "build/builds/3/artifacts":
			writeJSON(w, map[string]any{"value": []any{map[string]any{"name": "drop", "resource": map[string]any{
				"type": "PipelineArtifact", "downloadUrl": "https://ado.example/drop.zip",
				"properties": map[string]any{"artifactsize": "9019431321"}}}}})
		case strings.HasSuffix(path, "/artifacts"):
			writeJSON(w, map[string]any{"value": []any{}})
		case path == "test/Runs/100/attachments":
			writeJSON(w, map[string]any{"value": []any{map[string]any{"fileName": "coverage.xml", "attachmentType": "CodeCoverage",
				"size": 2048, "url": "https://ado.example/_apis/test/Runs/100/Attachments/1"}}})
		case path == "test/Runs/100/Results/7/attachments":
			if !strings.HasSuffix(q.Get("api-version"), "-preview.1") {
				t.Errorf("test attachments api-version = %q, want a preview version", q.Get("api-version"))
			}
			writeJSON(w, map[string]any{"value": []any{map[string]any{"fileName": "screenshot.png", "comment": "at failure",
				"size": 123456, "createdDate": "2026-10-14T08:01:00Z", "url": "https://ado.example/_apis/test/Runs/100/Results/7/Attachments/2"}}})
		case path == "test/Runs/200/attachments":
			http.NotFound(w, r) // an older server without the API
		case strings.HasSuffix(path, "/attachments"):
			writeJSON(w, map[string]any{"value": []any{}})
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
					"startTime": "2026-10-14T08:30:00Z", "finishTime": "2026-10-14T08:40:00Z",
					"log": map[string]any{"id": 30}, "previousAttempts": []any{map[string]any{"attempt": 1, "timelineId": "tl-2a1", "recordId": "j1"}}},
				map[string]any{"id": "t1", "parentId": "j1", "type": "Task", "name": "Test", "result": "succeeded", "order": 1, "attempt": 2, "log": map[string]any{"id": 31}},
			}})
		case path == "build/builds/2/timeline/tl-2a1":
			// The previous attempt's timeline holds only the rerun job.
			writeJSON(w, map[string]any{"id": "tl-2a1", "records": []any{
				map[string]any{"id": "j1", "parentId": "p1", "type": "Job", "name": "Linux", "result": "failed", "order": 1, "attempt": 1,
					"startTime": "2026-10-14T08:00:00Z", "finishTime": "2026-10-14T08:10:00Z", "log": map[string]any{"id": 20}},
				map[string]any{"id": "t1", "parentId": "j1", "type": "Task", "name": "Test", "result": "failed", "order": 1, "attempt": 1, "log": map[string]any{"id": 21},
					"issues": []any{map[string]any{"type": "error", "message": "connection reset"}}},
			}})
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
		case strings.HasPrefix(path, "build/builds/3/logs/"):
			if failLog != 0 {
				http.Error(w, "gone", failLog)
				return
			}
			switch strings.TrimPrefix(path, "build/builds/3/logs/") {
			case "5":
				fmt.Fprint(w, bom+"2026-10-14T08:00:00.1234567Z \x1b[31merror CS1002: ; expected\x1b[0m\r\n2026-10-14T08:00:01.0000000Z Build FAILED.\r\n")
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
					map[string]any{"id": 200, "name": "VSTest_TestResults_1", "pipelineReference": map[string]any{
						"stageReference": map[string]any{"stageName": "Build", "attempt": 1},
						"jobReference":   map[string]any{"jobName": "Linux", "attempt": 1}}, "runStatistics": []any{
						map[string]any{"outcome": "Failed", "count": 1}, map[string]any{"outcome": "Passed", "count": 5}}},
					map[string]any{"id": 201, "name": "VSTest_TestResults_2", "completedDate": "2026-10-14T08:35:00Z", "runStatistics": []any{
						map[string]any{"outcome": "Passed", "count": 6},
						map[string]any{"outcome": "Passed", "count": 1, "resultMetadata": "rerun"}}},
				}})
			case "vstfs:///Build/Build/5": // running: nothing published yet
				writeJSON(w, map[string]any{"value": []any{}})
			default:
				t.Errorf("test runs requested for %s", q.Get("buildUri"))
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
				t.Errorf("run 201's statistics show no failure; failed results must not be fetched")
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

// adoPipeline serves stub and returns a provider for the pipeline-run source
// (a fresh server URL, so cache entries never leak between tests).
func adoPipeline(t *testing.T, stub *pipelineStub, cfg map[string]string) *PipelineProvider {
	t.Helper()
	srv := httptest.NewServer(stub.handler(t))
	t.Cleanup(srv.Close)
	p, err := PipelineProviderFor(pipelineSrc(srv.URL, cfg), fakeSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAdoPipelineList(t *testing.T) {
	stub := &pipelineStub{}
	p := adoPipeline(t, stub, nil)
	runs, err := p.List(context.Background(), PipelineRunFilter{Result: "failed", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != 3 || runs[0].Result != "failed" || runs[0].Duration != "10m30s" ||
		runs[0].RequestedFor != "Jane <jane@example.com>" || runs[0].WebURL != "https://ado.example/proj/_build/results?buildId=3" {
		t.Fatalf("runs = %+v", runs)
	}
	req := stub.requests[0]
	for _, want := range []string{"definitions=12", "%24top=5", "resultFilter=failed", "branchName=refs%2Fheads%2Fmain", "queryOrder=queueTimeDescending"} {
		if !strings.Contains(req, want) {
			t.Errorf("list request %s lacks %s", req, want)
		}
	}

	// The source's Runs field sets the default; a request's top wins, capped.
	p = adoPipeline(t, stub, map[string]string{"Runs": "12"})
	stub.requests = nil
	_, _ = p.List(context.Background(), PipelineRunFilter{})
	_, _ = p.List(context.Background(), PipelineRunFilter{Top: 500})
	if !strings.Contains(stub.requests[0], "%24top=12") || !strings.Contains(stub.requests[1], "%24top=100") {
		t.Errorf("tops = %v", stub.requests)
	}
}

func TestAdoPipelineGetFailedRun(t *testing.T) {
	stub := &pipelineStub{}
	run, cached, err := adoPipeline(t, stub, nil).Get(context.Background(), 3)
	if err != nil || cached {
		t.Fatalf("err %v cached %v", err, cached)
	}
	if got := strings.Join(run.Issues, "\n"); got != "[error] Build > Linux > Compile: CS1002: ; expected\n"+
		"[error] Build > Windows: The job exceeded the maximum time" {
		t.Errorf("issues:\n%s", got)
	}
	if len(run.FailedSteps) != 2 || run.FailedSteps[0].Path != "Build > Linux > Compile" || run.FailedSteps[0].LogID != 5 ||
		run.FailedSteps[1].Path != "Build > Windows" || run.FailedSteps[1].LogID != 7 || run.FailedSteps[0].Attempt != 1 {
		t.Errorf("steps = %+v", run.FailedSteps)
	}
	if !strings.Contains(run.FailedSteps[0].WebURL, "view=logs&j=j1&t=t1") || strings.Contains(run.FailedSteps[1].WebURL, "&t=") {
		t.Errorf("step links = %s, %s", run.FailedSteps[0].WebURL, run.FailedSteps[1].WebURL)
	}
	if strings.Join(run.FailedStages, ",") != "Build" || strings.Join(run.FailedJobs, ",") != "Linux,Windows" {
		t.Errorf("failed stages %v jobs %v", run.FailedStages, run.FailedJobs)
	}
	if len(run.Tests) != 1 {
		t.Fatalf("tests = %+v", run.Tests)
	}
	tst := run.Tests[0]
	if tst.Name != "Parser.Tests.ParsesEmpty" || tst.Flaky || tst.Attempt != 1 || tst.Duration != "1.5s" ||
		tst.StackTrace == "" || len(tst.Attachments) != 1 || tst.Attachments[0].Size != "120.6 KB" ||
		!strings.Contains(tst.WebURL, "runId=100&resultId=7") {
		t.Errorf("test = %+v", tst)
	}
	if len(run.TestRunAttachments) != 1 || run.TestRunAttachments[0].Files[0].Name != "coverage.xml" {
		t.Errorf("test run attachments = %+v", run.TestRunAttachments)
	}
	if len(run.Changes) != 2 || run.Changes[0].Message != "Break the parser" {
		t.Errorf("changes = %+v", run.Changes)
	}
	if len(run.Artifacts) != 1 || run.Artifacts[0].Size != "8.4 GB" {
		t.Errorf("artifacts = %+v", run.Artifacts)
	}
	if n := stub.count("/proj/_apis/build/builds/3/logs"); n != 0 {
		t.Errorf("get read %d logs; logs are read only by log mode", n)
	}
}

// A run that went green after "Rerun failed jobs" still shows attempt 1's
// failed step and test, and a test that passed only on an in-run retry.
func TestAdoPipelineGetRerunRun(t *testing.T) {
	run, _, err := adoPipeline(t, &pipelineStub{}, nil).Get(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempts != 2 || len(run.FailedSteps) != 1 || run.FailedSteps[0].Attempt != 1 ||
		run.FailedSteps[0].Path != "Build > Linux > Test" || run.FailedSteps[0].LogID != 21 {
		t.Fatalf("attempts %d steps %+v", run.Attempts, run.FailedSteps)
	}
	if len(run.Issues) != 1 || run.Issues[0] != "[error] Build > Linux > Test (attempt 1): connection reset" {
		t.Errorf("issues = %v", run.Issues)
	}
	if len(run.Tests) != 2 {
		t.Fatalf("tests = %+v", run.Tests)
	}
	upload, retry := run.Tests[0], run.Tests[1]
	if upload.Name != "Api.Tests.Upload" || upload.Flaky || upload.Attempt != 1 || upload.Job != "Build > Linux" {
		t.Errorf("attempt-1 test = %+v", upload)
	}
	// Placed by time: run 201 completed inside attempt 2's job.
	if retry.Name != "Api.Tests.Retry" || !retry.Flaky || retry.Attempt != 2 || len(retry.SubResults) != 2 ||
		retry.SubResults[0].ErrorMessage != "timeout waiting for lock" {
		t.Errorf("flaky test = %+v", retry)
	}
	if strings.Join(run.Notes, "") != "(attachments couldn't be listed: the server answered HTTP 404)" {
		t.Errorf("notes = %v", run.Notes)
	}
}

// A green build with one attempt costs its summary, timeline, changes and
// artifacts — its test runs aren't read.
func TestAdoPipelineGreenRunSkipsTests(t *testing.T) {
	stub := &pipelineStub{}
	run, _, err := adoPipeline(t, stub, nil).Get(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Issues) != 1 || len(run.FailedSteps) != 0 || len(run.Tests) != 0 {
		t.Errorf("run = %+v", run)
	}
	if n := stub.count("/proj/_apis/test/"); n != 0 {
		t.Errorf("%d test API calls for a clean green build", n)
	}
}

// A completed run's details and logs come from the cache after the first
// read; only the run's current state is asked again. A run still in
// progress is never cached.
func TestPipelineCacheServesCompletedRuns(t *testing.T) {
	stub := &pipelineStub{}
	p := adoPipeline(t, stub, nil)
	ctx := context.Background()
	if _, cached, _ := p.Get(ctx, 3); cached {
		t.Fatal("first read cached")
	}
	before := len(stub.requests)
	run, cached, err := p.Get(ctx, 3)
	if err != nil || !cached || run.ID != 3 {
		t.Fatalf("second read: cached %v err %v", cached, err)
	}
	if got := stub.requests[before:]; len(got) != 1 || !strings.HasPrefix(got[0], "/proj/_apis/build/builds/3?") {
		t.Errorf("second read requested %v, want only the run itself", got)
	}

	text, cached, err := p.Log(ctx, 3, 5)
	if err != nil || cached || text != "error CS1002: ; expected\nBuild FAILED." {
		t.Fatalf("log = %q cached %v err %v", text, cached, err)
	}
	if _, cached, _ = p.Log(ctx, 3, 5); !cached || stub.count("/proj/_apis/build/builds/3/logs/5") != 1 {
		t.Errorf("log re-read: cached %v, %d log requests", cached, stub.count("/proj/_apis/build/builds/3/logs/5"))
	}

	for range 2 {
		if _, cached, err := p.Get(ctx, 5); err != nil || cached {
			t.Fatalf("running build: cached %v err %v", cached, err)
		}
	}
	if n := stub.count("/proj/_apis/build/builds/5/timeline"); n != 2 {
		t.Errorf("running build timeline read %d times, want every time", n)
	}
}

func TestAdoPipelineLogErrors(t *testing.T) {
	_, _, err := adoPipeline(t, &pipelineStub{failLog: http.StatusNotFound}, nil).Log(context.Background(), 3, 5)
	if err == nil || !strings.Contains(err.Error(), "log 5 of run 3") {
		t.Errorf("err = %v", err)
	}
}

func TestPipelineProviderForErrors(t *testing.T) {
	for name, src := range map[string]*models.SourceDefinition{
		"no definition": pipelineSrc("https://ado.example/org", map[string]string{"DefinitionId": ""}),
		"not pipeline":  {Name: "Code", Type: models.SourceCodeRepo, Config: map[string]string{"Provider": "api"}},
		"no workflow":   {Name: "GH", Type: models.SourcePipelineRun, Config: map[string]string{"Provider": "github", "GitHubOwner": "a", "GitHubRepo": "b"}},
	} {
		if _, err := PipelineProviderFor(src, fakeSecrets{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestResolvePipelineRunURL(t *testing.T) {
	all := []models.SourceDefinition{
		{ID: "code", Type: models.SourceCodeRepo, Config: map[string]string{"Provider": "api", "AdoOrg": "https://ado.example/org", "AdoProject": "proj"}},
		{ID: "ci", Type: models.SourcePipelineRun, Config: map[string]string{"Provider": "api", "AdoOrg": "https://ado.example/org", "AdoProject": "proj", "DefinitionId": "12"}},
		{ID: "gh", Type: models.SourceCodeRepo, Config: map[string]string{"Provider": "github", "GitHubOwner": "acme", "GitHubRepo": "app"}},
	}
	r, err := ResolvePipelineRunURL("https://ado.example/org/proj/_build/results?buildId=42&view=logs", all, fakeSecrets{})
	if err != nil || r.RunID != 42 || r.Source.ID != "ci" {
		t.Fatalf("ado: %+v %v", r, err)
	}
	r, err = ResolvePipelineRunURL("https://github.com/acme/app/actions/runs/777/job/1", all, fakeSecrets{})
	if err != nil || r.RunID != 777 || r.Source.ID != "gh" {
		t.Fatalf("github: %+v %v", r, err)
	}
	for _, bad := range []string{"https://ado.example/org/proj/_build/results", "https://other.example/org/proj/_build/results?buildId=1",
		"https://github.com/acme/app/pull/3", "not a url"} {
		if _, err := ResolvePipelineRunURL(bad, all, fakeSecrets{}); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func TestCleanLogText(t *testing.T) {
	in := bom + "2026-10-14T08:00:00.1234567Z \x1b[31merror\x1b[0m\r\n" +
		"2026-10-14T08:00:01Z next line\r\n" +
		"no timestamp here\r\n\r\n"
	want := "error\nnext line\nno timestamp here"
	if got := cleanLogText(in); got != want {
		t.Errorf("cleanLogText = %q, want %q", got, want)
	}
}

func TestTestRunAttemptUnknownOutsideEveryJob(t *testing.T) {
	jobs := []jobExecution{{name: "Linux", path: "Build > Linux", attempt: 1,
		start: parseTime("2026-10-14T08:00:00Z"), finish: parseTime("2026-10-14T08:10:00Z")}}
	if a, job := testRunAttempt(adoTestRun{ID: 1, CompletedDate: "2026-10-14T12:00:00Z"}, jobs, 2); a != 0 || job != "" {
		t.Errorf("a run outside every job = attempt %d %q, want unknown", a, job)
	}
	if a, _ := testRunAttempt(adoTestRun{}, nil, 1); a != 1 {
		t.Errorf("a single-attempt run's tests are attempt 1, got %d", a)
	}
}

func TestLRUCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newLRUCache(10)
	c.put("a", 1, 4)
	c.put("b", 2, 4)
	c.get("a") // a is now the most recent
	c.put("c", 3, 4)
	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.get("a"); !ok {
		t.Error("a was used recently and should be kept")
	}
	c.put("huge", 4, 11)
	if _, ok := c.get("huge"); ok {
		t.Error("a value larger than the cache must not be kept")
	}
}

// bom is the UTF-8 byte order mark ADO puts before a log's first line.
const bom = string(rune(0xFEFF))
