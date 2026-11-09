package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/sources"
)

// fakeAdoPipeline serves definition 12 of project proj: run 3 failed with a
// 1,200-line step log and 25 failed tests.
func fakeAdoPipeline(t *testing.T) (*httptest.Server, fakeSources) {
	t.Helper()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	run := map[string]any{"id": 3, "buildNumber": "20261109.3", "status": "completed", "result": "failed",
		"sourceBranch": "refs/heads/main", "finishTime": "2026-11-09T10:00:00Z", "definition": map[string]any{"id": 12, "name": "CI"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := strings.TrimPrefix(r.URL.Path, "/org/proj/_apis/"); p {
		case "build/builds":
			if r.URL.Query().Get("$top") != "2" {
				t.Errorf("$top = %q, want the source's Runs", r.URL.Query().Get("$top"))
			}
			write(w, map[string]any{"value": []any{run}})
		case "build/builds/3":
			write(w, run)
		case "build/builds/3/timeline":
			write(w, map[string]any{"records": []any{map[string]any{"id": "t1", "type": "Task", "name": "Test",
				"result": "failed", "log": map[string]any{"id": 5}}}})
		case "build/builds/3/logs/5":
			for i := 1; i <= 1200; i++ {
				fmt.Fprintf(w, "line %d\n", i)
			}
		case "test/runs":
			write(w, map[string]any{"value": []any{map[string]any{"id": 100, "name": "Unit",
				"runStatistics": []any{map[string]any{"outcome": "Failed", "count": 25}}}}})
		case "test/Runs/100/results":
			var res []any
			for i := 1; i <= 25; i++ {
				res = append(res, map[string]any{"id": i, "automatedTestName": fmt.Sprintf("T%02d", i), "outcome": "Failed"})
			}
			write(w, map[string]any{"value": res})
		default:
			write(w, map[string]any{"value": []any{}}) // changes, artifacts, attachments
		}
	}))
	t.Cleanup(srv.Close)
	return srv, fakeSources{{ID: "ci", Name: "CI", Type: models.SourcePipelineRun, Config: map[string]string{
		"Provider": "api", "AdoOrg": srv.URL + "/org", "AdoProject": "proj", "DefinitionId": "12", "Runs": "2"}}}
}

func pipelineCall(t *testing.T, srcs fakeSources, req map[string]any) map[string]any {
	t.Helper()
	out, err := handlePipelineRequest(context.Background(), map[string]any{"request": req}, srcs, fakeCreds{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPipelineTool_ListGetLog(t *testing.T) {
	srv, srcs := fakeAdoPipeline(t)

	// The only pipeline-run source is used without naming it.
	out := pipelineCall(t, srcs, map[string]any{"mode": "list"})
	if runs := out["runs"].([]sources.PipelineRunSummary); len(runs) != 1 || runs[0].ID != 3 {
		t.Fatalf("list = %+v", out)
	}

	out = pipelineCall(t, srcs, map[string]any{"mode": "get", "run_id": float64(3), "page": float64(2)})
	run := out["run"].(sources.PipelineRun)
	if out["total_tests"] != 25 || out["tests_has_more"] != false || len(run.Tests) != 5 || run.Tests[0].Name != "T21" {
		t.Fatalf("tests page 2: total %v more %v got %d", out["total_tests"], out["tests_has_more"], len(run.Tests))
	}
	if len(run.FailedSteps) != 1 || run.FailedSteps[0].LogID != 5 {
		t.Fatalf("steps = %+v", run.FailedSteps)
	}

	// The run link works in place of source_id + run_id; the log defaults to its last page.
	out = pipelineCall(t, srcs, map[string]any{"mode": "log", "url": srv.URL + "/org/proj/_build/results?buildId=3", "log_id": float64(5)})
	if out["page"] != 3 || out["total_pages"] != 3 || out["first_line"] != 1001 || out["last_line"] != 1200 ||
		!strings.HasPrefix(out["text"].(string), "line 1001\n") || !strings.HasSuffix(out["text"].(string), "line 1200") {
		t.Fatalf("last page = %v", out)
	}
	if out["cached"] != false {
		t.Error("the first log read can't be cached")
	}
	out = pipelineCall(t, srcs, map[string]any{"mode": "log", "run_id": float64(3), "log_id": float64(5), "page": float64(1)})
	if out["first_line"] != 1 || out["last_line"] != 500 || out["cached"] != true {
		t.Fatalf("first page = %v", out)
	}
}

func TestPipelineTool_RequestErrors(t *testing.T) {
	_, srcs := fakeAdoPipeline(t)
	two := append(fakeSources{}, srcs...)
	two = append(two, models.SourceDefinition{ID: "ci2", Name: "Nightly", Type: models.SourcePipelineRun,
		Config: map[string]string{"Provider": "api", "AdoOrg": "https://ado.example/org", "AdoProject": "proj", "DefinitionId": "13"}})
	code := fakeSources{{ID: "code", Type: models.SourceCodeRepo, Config: map[string]string{"Provider": "api"}}}
	for name, c := range map[string]struct {
		srcs fakeSources
		req  map[string]any
	}{
		"unknown mode":        {srcs, map[string]any{"mode": "search"}},
		"get without run":     {srcs, map[string]any{"mode": "get"}},
		"log without log_id":  {srcs, map[string]any{"mode": "log", "run_id": float64(3)}},
		"page past the end":   {srcs, map[string]any{"mode": "log", "run_id": float64(3), "log_id": float64(5), "page": float64(9)}},
		"ambiguous source":    {two, map[string]any{"mode": "list"}},
		"no pipeline sources": {code, map[string]any{"mode": "list"}},
		"not a pipeline":      {code, map[string]any{"mode": "list", "source_id": "code"}},
		"unknown source":      {srcs, map[string]any{"mode": "list", "source_id": "nope"}},
		"unmatched url":       {srcs, map[string]any{"mode": "get", "url": "https://elsewhere.example/org/proj/_build/results?buildId=3"}},
	} {
		if _, err := handlePipelineRequest(context.Background(), map[string]any{"request": c.req}, c.srcs, fakeCreds{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
