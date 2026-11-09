package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func (f *fakeGitHub) pipeline(t *testing.T, cfg map[string]string) *PipelineProvider {
	t.Helper()
	full := map[string]string{
		"Provider": "github", "GitHubUrl": f.srv.URL, "GitHubOwner": "acme", "GitHubRepo": "app", "Token": "gh", "Workflow": "ci.yml",
	}
	for k, v := range cfg {
		full[k] = v
	}
	p, err := PipelineProviderFor(&models.SourceDefinition{ID: "s", Name: "GH", Type: models.SourcePipelineRun, Config: full}, fakeSecrets{"gh": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGitHubPipelineList(t *testing.T) {
	f := newFakeGitHub(t)
	f.HandleFunc("/api/v3/repos/acme/app/actions/workflows/ci.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "5" || q.Get("status") != "failure" || q.Get("branch") != "main" || q.Get("event") != "push" {
			t.Errorf("query = %v", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []any{
			map[string]any{"id": 11, "name": "CI", "run_number": 7, "run_attempt": 1, "status": "completed", "conclusion": "failure",
				"head_branch": "main", "head_sha": "abc", "event": "push", "actor": map[string]any{"login": "jane"},
				"run_started_at": "2026-10-01T10:00:00Z", "updated_at": "2026-10-01T10:05:00Z"},
		}})
	})
	runs, err := f.pipeline(t, nil).List(context.Background(), PipelineRunFilter{Result: "failure", Branch: "refs/heads/main", Reason: "push"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Number != "7" || runs[0].Duration != "5m0s" || runs[0].RequestedFor != "jane" ||
		runs[0].WebURL != f.srv.URL+"/acme/app/actions/runs/11" {
		t.Fatalf("runs = %+v", runs)
	}
}

// A run re-run to green still lists attempt 1's failed job with its
// annotations; its log is read on request; changes come from comparing with
// the previous run on the branch.
func TestGitHubPipelineGetAndLog(t *testing.T) {
	f := newFakeGitHub(t)
	f.handleJSON("/actions/runs/21", map[string]any{"id": 21, "name": "CI", "run_number": 9, "run_attempt": 2, "workflow_id": 77,
		"status": "completed", "conclusion": "success", "head_branch": "main", "head_sha": "new", "created_at": "2026-10-02T10:00:00Z",
		"updated_at": "2026-10-02T10:09:00Z"})
	f.HandleFunc("/api/v3/repos/acme/app/actions/runs/21/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "all" {
			t.Errorf("jobs filter = %q, want all attempts", r.URL.Query().Get("filter"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []any{
			map[string]any{"id": 202, "name": "test", "run_attempt": 2, "conclusion": "success"},
			map[string]any{"id": 201, "name": "test", "run_attempt": 1, "conclusion": "failure",
				"steps": []any{map[string]any{"name": "go test", "conclusion": "failure"}}},
		}})
	})
	f.handleJSON("/check-runs/201/annotations", []any{map[string]any{"path": "a_test.go", "start_line": 12,
		"annotation_level": "failure", "message": "expected 1, got 2"}})
	f.HandleFunc("/api/v3/repos/acme/app/actions/workflows/77/runs", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("branch") != "main" || q.Get("created") != "<2026-10-02T10:00:00Z" {
			t.Errorf("previous-run query = %v", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []any{map[string]any{"id": 20, "head_sha": "old"}}})
	})
	f.handleJSON("/compare/old...new", map[string]any{"total_commits": 1, "commits": []any{
		map[string]any{"sha": "new", "commit": map[string]any{"message": "Fix race\n\nbody", "author": map[string]any{"name": "Jane", "email": "j@x"}}},
	}})
	logs := 0
	f.HandleFunc("/api/v3/repos/acme/app/actions/jobs/201/logs", func(w http.ResponseWriter, r *http.Request) {
		logs++
		fmt.Fprint(w, "2026-10-02T10:00:01.0000000Z --- FAIL: TestRace\n")
	})
	f.HandleFunc("/api/v3/repos/acme/app/actions/jobs/9/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})

	p := f.pipeline(t, nil)
	run, _, err := p.Get(context.Background(), 21)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempts != 2 || len(run.FailedSteps) != 1 || run.FailedSteps[0].LogID != 201 || run.FailedSteps[0].Attempt != 1 ||
		strings.Join(run.FailedSteps[0].FailedTasks, ",") != "go test" {
		t.Fatalf("steps = %+v", run.FailedSteps)
	}
	if len(run.Issues) != 1 || run.Issues[0] != "[failure] test (attempt 1): expected 1, got 2 (a_test.go:12)" {
		t.Errorf("issues = %v", run.Issues)
	}
	if len(run.Changes) != 1 || run.Changes[0].Message != "Fix race" || run.Changes[0].Author != "Jane <j@x>" {
		t.Errorf("changes = %+v", run.Changes)
	}
	if logs != 0 {
		t.Error("get must not read logs")
	}

	for range 2 {
		text, _, err := p.Log(context.Background(), 21, 201)
		if err != nil || text != "--- FAIL: TestRace" {
			t.Fatalf("log = %q, %v", text, err)
		}
	}
	if logs != 1 {
		t.Errorf("log read %d times, want once (cached)", logs)
	}
	if text, _, err := p.Log(context.Background(), 21, 9); err != nil || !strings.Contains(text, "no longer available on GitHub: HTTP 410") {
		t.Errorf("expired log = %q, %v", text, err)
	}
}
