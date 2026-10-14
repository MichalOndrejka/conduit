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
// failed (a failed task and a job that failed without a failed task), build 2
// succeeded, and build 1 (on a second page) partially succeeded with only a
// warning and no test runs.
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
				writeJSON(w, map[string]any{"value": []any{build(3, "20261014.3", "failed", "refs/heads/main"), build(2, "20261014.2", "succeeded", "refs/heads/main")}})
				return
			}
			writeJSON(w, map[string]any{"value": []any{build(1, "20261014.1", "partiallySucceeded", "refs/pull/42/merge")}})
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
			if q.Get("buildUri") == "vstfs:///Build/Build/3" {
				writeJSON(w, map[string]any{"value": []any{map[string]any{"id": 100, "name": "Unit tests"}}})
				return
			}
			writeJSON(w, map[string]any{"value": []any{}})
		case path == "test/Runs/100/results":
			writeJSON(w, map[string]any{"value": []any{map[string]any{
				"id": 7, "automatedTestName": "Parser.Tests.ParsesEmpty", "automatedTestStorage": "parser.tests.dll",
				"outcome": "Failed", "durationInMs": 1500, "errorMessage": "Expected 0 but was 1",
				"stackTrace": "at Parser.Tests.ParsesEmpty() in ParserTests.cs:line 42",
			}}})
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
		"src-1_run_2",
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
	pr := docByID(docs, "src-1_run_1")
	if got := strings.Join(pr.Filters.Fields[models.FilterBranch], ","); got != "refs/pull/42/merge,pull/42/merge" {
		t.Errorf("PR branch filter = %q", got)
	}
	if !strings.Contains(pr.Text, "[warning] Lint: deprecated API") {
		t.Errorf("partially succeeded run doc missing its warning:\n%s", pr.Text)
	}
	if strings.Contains(docByID(docs, "src-1_run_2").Text, "Issues:") {
		t.Error("succeeded run must not fetch timeline details")
	}
}

func TestFetchPipelineRunsSendsWindowAndOnlyFetchesFailedRunDetails(t *testing.T) {
	stub := &pipelineStub{}
	before := time.Now().UTC().AddDate(0, 0, -30).Add(-time.Minute)
	if _, err := fetchPipeline(t, stub, nil); err != nil {
		t.Fatal(err)
	}
	var listCalls int
	for _, r := range stub.requests {
		if strings.Contains(r, "/build/builds/2/") {
			t.Errorf("fetched details of a succeeded run: %s", r)
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
		if strings.HasPrefix(r, "/proj/_apis/test/Runs/100/results?") && !strings.Contains(r, "outcomes=Failed,Aborted,Error") {
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
