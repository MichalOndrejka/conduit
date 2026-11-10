// GitHub Actions workflow runs, read live (see pipelines.go), mirroring the
// Azure DevOps runs: a run's details list each failed job of every attempt
// (its log read on request by job id), the failed jobs' annotations — GitHub's
// counterpart of ADO's timeline issues —, the commits built since the
// previous run on the branch and the artifacts uploaded. GitHub has no test
// results API, so there are no tests.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// githubFailedConclusions are the job conclusions listed as failed steps.
var githubFailedConclusions = map[string]bool{"failure": true, "timed_out": true, "startup_failure": true}

type githubRun struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	DisplayTitle string     `json:"display_title"`
	RunNumber    int        `json:"run_number"`
	RunAttempt   int        `json:"run_attempt"`
	WorkflowID   int64      `json:"workflow_id"`
	Event        string     `json:"event"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion"`
	HeadBranch   string     `json:"head_branch"`
	HeadSHA      string     `json:"head_sha"`
	HTMLURL      string     `json:"html_url"`
	CreatedAt    string     `json:"created_at"`
	RunStartedAt string     `json:"run_started_at"`
	UpdatedAt    string     `json:"updated_at"`
	Actor        githubUser `json:"actor"`
}

type githubJob struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Conclusion  string `json:"conclusion"`
	RunAttempt  int    `json:"run_attempt"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	HTMLURL     string `json:"html_url"`
	Steps       []struct {
		Name       string `json:"name"`
		Number     int    `json:"number"`
		Conclusion string `json:"conclusion"`
	} `json:"steps"`
}

type githubAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	AnnotationLevel string `json:"annotation_level"`
	Message         string `json:"message"`
}

// githubPipelineClient reads one repository's workflow runs: of every
// workflow, or of the one a listing names.
type githubPipelineClient struct {
	g *GitHubSource
}

func (c *githubPipelineClient) cacheScope() string { return "github|" + c.g.repoAPI() }

// pipelines lists the repository's workflows.
func (c *githubPipelineClient) pipelines(ctx context.Context) ([]PipelineDefinition, error) {
	var out []PipelineDefinition
	err := c.g.pages(ctx, c.g.repoAPI()+"/actions/workflows", func(data []byte) (int, bool, error) {
		var page struct {
			Workflows []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Path    string `json:"path"`
				State   string `json:"state"`
				HTMLURL string `json:"html_url"`
			} `json:"workflows"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, fmt.Errorf("listing workflows: %w", err)
		}
		for _, w := range page.Workflows {
			out = append(out, PipelineDefinition{
				ID: strconv.FormatInt(w.ID, 10), Name: w.Name, File: path.Base(w.Path), State: w.State, WebURL: w.HTMLURL,
			})
		}
		return len(page.Workflows), true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (c *githubPipelineClient) list(ctx context.Context, f PipelineRunFilter) ([]PipelineRunSummary, error) {
	listURL := c.g.repoAPI() + "/actions/runs"
	what := "the repository"
	if want := strings.TrimSpace(f.Pipeline); want != "" {
		workflow := want
		// A workflow ID or file name goes straight to GitHub; a name is
		// looked up first.
		if _, err := strconv.ParseInt(want, 10, 64); err != nil && !strings.HasSuffix(want, ".yml") && !strings.HasSuffix(want, ".yaml") {
			defs, err := c.pipelines(ctx)
			if err != nil {
				return nil, err
			}
			d, err := matchPipeline(defs, want)
			if err != nil {
				return nil, err
			}
			workflow = d.ID
		}
		listURL = c.g.repoAPI() + "/actions/workflows/" + url.PathEscape(workflow) + "/runs"
		what = "workflow " + strconv.Quote(want)
	}
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(f.Top))
	if f.Branch != "" {
		q.Set("branch", strings.TrimPrefix(f.Branch, "refs/heads/"))
	}
	if f.Reason != "" {
		q.Set("event", f.Reason)
	}
	if f.RequestedFor != "" {
		q.Set("actor", f.RequestedFor)
	}
	// GitHub's status parameter takes either a status or a conclusion.
	if f.Result != "" {
		q.Set("status", f.Result)
	} else if f.Status != "" {
		q.Set("status", f.Status)
	}
	var page struct {
		WorkflowRuns []githubRun `json:"workflow_runs"`
	}
	listURL += "?" + q.Encode()
	if _, err := c.g.getJSON(ctx, listURL, &page); err != nil {
		return nil, fmt.Errorf("listing runs of %s: %w", what, err)
	}
	out := make([]PipelineRunSummary, 0, len(page.WorkflowRuns))
	for i := range page.WorkflowRuns {
		r := &page.WorkflowRuns[i]
		// Both given: the result went to GitHub, the status is checked here.
		if f.Result != "" && f.Status != "" && !strings.EqualFold(r.Status, f.Status) {
			continue
		}
		out = append(out, c.summaryOf(r))
	}
	return out, nil
}

func (c *githubPipelineClient) summary(ctx context.Context, id int64) (*PipelineRunSummary, error) {
	var r githubRun
	if _, err := c.g.getJSON(ctx, fmt.Sprintf("%s/actions/runs/%d", c.g.repoAPI(), id), &r); err != nil {
		return nil, fmt.Errorf("reading run %d: %w", id, err)
	}
	s := c.summaryOf(&r)
	return &s, nil
}

func (c *githubPipelineClient) summaryOf(r *githubRun) PipelineRunSummary {
	web := r.HTMLURL
	if !isWebURL(web) {
		web = fmt.Sprintf("%s/actions/runs/%d", c.g.repoWeb(), r.ID)
	}
	s := PipelineRunSummary{
		ID: r.ID, Number: strconv.Itoa(r.RunNumber), Pipeline: r.Name, Title: r.DisplayTitle,
		Status: r.Status, Result: r.Conclusion, Reason: r.Event,
		Branch: r.HeadBranch, Commit: r.HeadSHA, RequestedFor: r.Actor.Login,
		Queued: r.CreatedAt, Started: r.RunStartedAt, Attempt: max(r.RunAttempt, 1),
		WebURL: web, gh: r,
	}
	if strings.EqualFold(r.Status, "completed") {
		s.Finished = r.UpdatedAt
		if start, finish := parseTime(r.RunStartedAt), parseTime(r.UpdatedAt); !start.IsZero() && !finish.IsZero() {
			s.Duration = finish.Sub(start).Round(time.Second).String()
		}
	}
	return s
}

// log reads a job's complete log. GitHub deletes logs past the repository's
// retention; that is said instead of failing.
func (c *githubPipelineClient) log(ctx context.Context, s *PipelineRunSummary, jobID int64) (string, error) {
	data, _, err := c.g.get(ctx, fmt.Sprintf("%s/actions/jobs/%d/logs", c.g.repoAPI(), jobID), "")
	var se *httpStatusError
	switch {
	case errors.As(err, &se) && (se.status == http.StatusGone || se.status == http.StatusNotFound):
		return fmt.Sprintf("(log of job %d no longer available on GitHub: HTTP %d)", jobID, se.status), nil
	case err != nil:
		return "", fmt.Errorf("reading log of job %d: %w", jobID, err)
	}
	return cleanLogText(string(data)), nil
}

// detail reads the jobs of every attempt, the annotations of each failed
// job, the run's changes and its artifacts.
func (c *githubPipelineClient) detail(ctx context.Context, s *PipelineRunSummary) (*PipelineRun, error) {
	r := s.gh
	run := &PipelineRun{
		PipelineRunSummary: *s, Repository: c.g.owner + "/" + c.g.repo, Attempts: max(r.RunAttempt, 1),
		Issues: []string{}, FailedSteps: []PipelineStep{}, Tests: []PipelineTest{},
	}

	var jobs []githubJob
	err := c.g.pages(ctx, fmt.Sprintf("%s/actions/runs/%d/jobs?filter=all", c.g.repoAPI(), r.ID), func(data []byte) (int, bool, error) {
		var page struct {
			Jobs []githubJob `json:"jobs"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		jobs = append(jobs, page.Jobs...)
		return len(page.Jobs), true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	seenJob := map[string]bool{}
	for _, j := range jobs {
		if !githubFailedConclusions[j.Conclusion] {
			continue
		}
		label := j.Name
		if run.Attempts > 1 {
			label += fmt.Sprintf(" (attempt %d)", max(j.RunAttempt, 1))
		}
		annotations, err := c.annotations(ctx, j.ID)
		if err != nil {
			return nil, fmt.Errorf("annotations of job %q: %w", j.Name, err)
		}
		for _, a := range annotations {
			line := fmt.Sprintf("[%s] %s: %s", a.AnnotationLevel, label, strings.TrimSpace(a.Message))
			if a.Path != "" && a.Path != ".github" {
				line += fmt.Sprintf(" (%s:%d)", a.Path, a.StartLine)
			}
			run.Issues = append(run.Issues, line)
		}
		step := PipelineStep{Path: j.Name, Attempt: max(j.RunAttempt, 1), Result: j.Conclusion, LogID: j.ID, WebURL: j.HTMLURL}
		if !isWebURL(step.WebURL) {
			step.WebURL = fmt.Sprintf("%s/actions/runs/%d/job/%d", c.g.repoWeb(), r.ID, j.ID)
		}
		for _, st := range j.Steps {
			if githubFailedConclusions[st.Conclusion] {
				step.FailedTasks = append(step.FailedTasks, st.Name)
			}
		}
		run.FailedSteps = append(run.FailedSteps, step)
		if !seenJob[j.Name] {
			seenJob[j.Name] = true
			run.FailedJobs = append(run.FailedJobs, j.Name)
		}
	}

	changes, note, err := c.changes(ctx, r)
	if err != nil {
		return nil, err
	}
	run.Changes = changes
	if note != "" {
		run.Notes = append(run.Notes, note)
	}
	arts, err := c.artifacts(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	run.Artifacts = pipelineFiles(arts)
	return run, nil
}

func (c *githubPipelineClient) annotations(ctx context.Context, jobID int64) ([]githubAnnotation, error) {
	var all []githubAnnotation
	err := c.g.pages(ctx, fmt.Sprintf("%s/check-runs/%d/annotations", c.g.repoAPI(), jobID), func(data []byte) (int, bool, error) {
		var page []githubAnnotation
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		all = append(all, page...)
		return len(page), true, nil
	})
	return all, err
}

// githubCompareCommit is one commit of a compare range.
type githubCompareCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

// changes lists the commits r built since the previous run of its workflow
// on its branch, with a note when there is nothing to compare with.
func (c *githubPipelineClient) changes(ctx context.Context, r *githubRun) ([]PipelineChange, string, error) {
	out := []PipelineChange{}
	if r.WorkflowID == 0 || r.HeadBranch == "" || r.HeadSHA == "" {
		return out, "", nil
	}
	q := url.Values{}
	q.Set("branch", r.HeadBranch)
	q.Set("created", "<"+r.CreatedAt)
	q.Set("per_page", "1")
	var prev struct {
		WorkflowRuns []githubRun `json:"workflow_runs"`
	}
	if _, err := c.g.getJSON(ctx, fmt.Sprintf("%s/actions/workflows/%d/runs?%s", c.g.repoAPI(), r.WorkflowID, q.Encode()), &prev); err != nil {
		return nil, "", fmt.Errorf("finding the previous run on %s: %w", r.HeadBranch, err)
	}
	if len(prev.WorkflowRuns) == 0 {
		return out, "changes: no earlier run of this workflow on " + r.HeadBranch + " to compare with", nil
	}
	base := prev.WorkflowRuns[0].HeadSHA
	if base == "" || base == r.HeadSHA {
		return out, "", nil
	}
	total := 0
	var commits []githubCompareCommit
	err := c.g.pages(ctx, fmt.Sprintf("%s/compare/%s...%s", c.g.repoAPI(), base, r.HeadSHA), func(data []byte) (int, bool, error) {
		var page struct {
			TotalCommits int                   `json:"total_commits"`
			Commits      []githubCompareCommit `json:"commits"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		total = page.TotalCommits
		commits = append(commits, page.Commits...)
		return len(page.Commits), len(commits) < total, nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("comparing %s...%s: %w", base, r.HeadSHA, err)
	}
	if len(commits) < total {
		return nil, "", fmt.Errorf("compare %s...%s listed %d of %d commits", base, r.HeadSHA, len(commits), total)
	}
	for _, cm := range commits {
		msg, _, _ := strings.Cut(strings.TrimSpace(cm.Commit.Message), "\n")
		out = append(out, PipelineChange{
			ID: cm.SHA, Author: strings.TrimSpace(cm.Commit.Author.Name + " " + angle(cm.Commit.Author.Email)),
			Date: cm.Commit.Author.Date, Message: msg,
		})
	}
	return out, "", nil
}

// artifacts lists the artifacts a workflow run uploaded — names, sizes,
// expiry and download links, never their contents.
func (c *githubPipelineClient) artifacts(ctx context.Context, runID int64) ([]attachmentInfo, error) {
	var out []attachmentInfo
	err := c.g.pages(ctx, fmt.Sprintf("%s/actions/runs/%d/artifacts", c.g.repoAPI(), runID), func(data []byte) (int, bool, error) {
		var page struct {
			Artifacts []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				SizeInBytes int64  `json:"size_in_bytes"`
				Expired     bool   `json:"expired"`
				CreatedAt   string `json:"created_at"`
				ExpiresAt   string `json:"expires_at"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		for _, a := range page.Artifacts {
			info := attachmentInfo{Name: a.Name, Kind: "zip", Size: a.SizeInBytes, Created: a.CreatedAt}
			switch {
			case a.Expired:
				info.Note = "expired, no longer downloadable"
			case a.ExpiresAt != "":
				info.Note = "expires " + a.ExpiresAt
			}
			if !a.Expired {
				// The browser download link; the API's archive_download_url needs a token.
				info.URL = fmt.Sprintf("%s/actions/runs/%d/artifacts/%d", c.g.repoWeb(), runID, a.ID)
			}
			out = append(out, info)
		}
		return len(page.Artifacts), true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	return out, nil
}
