// GitHub Actions extras computed at sync time, mirroring the Azure DevOps
// ones (see ado_pipeline_extras.go): run-level filters and sort keys, the
// commits each run built since the previous run on its branch, runs still
// queued or in progress, and deployments of the runs' commits. GitHub has
// no test results API, so there is no test history here.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// githubReasonValues maps a workflow run's event onto the plain reason
// values the ADO runs use too (both are stored).
func githubReasonValues(event string) []string {
	e := strings.ToLower(event)
	out := []string{e}
	switch e {
	case "push":
		out = append(out, "ci")
	case "pull_request", "pull_request_target":
		out = append(out, "pullrequest", "pr")
	case "schedule":
		out = append(out, "scheduled")
	case "workflow_dispatch", "repository_dispatch":
		out = append(out, "manual")
	}
	return out
}

// githubRunSort is a run's sort keys.
func githubRunSort(r githubRun) map[string]float64 {
	s := map[string]float64{"attempts": float64(max(r.RunAttempt, 1))}
	for k, v := range map[string]string{"queued": r.CreatedAt, "started": r.RunStartedAt} {
		if ts := unixOrZero(v); ts != 0 {
			s[k] = ts
		}
	}
	if strings.EqualFold(r.Status, "completed") {
		if ts := unixOrZero(r.UpdatedAt); ts != 0 {
			s["finished"] = ts
		}
		if start, finish := parseTime(r.RunStartedAt), parseTime(r.UpdatedAt); !start.IsZero() && !finish.IsZero() {
			s["duration"] = finish.Sub(start).Seconds()
		}
	}
	return s
}

// githubRunFilters adds the run-level filters every document of the run
// gets.
func githubRunFilters(f *models.DocumentFilters, r githubRun, workflow string, detail *githubRunDetail, changes []githubCompareCommit) {
	if strings.EqualFold(r.Status, "completed") {
		f.Date = parseTime(r.UpdatedAt)
	}
	if f.Date.IsZero() {
		f.Date = parseTime(r.CreatedAt)
	}
	f.Add(models.FilterPipeline, r.Name, workflow)
	f.Add(models.FilterStatus, r.Status)
	f.Add(models.FilterReason, githubReasonValues(r.Event)...)
	f.Add(models.FilterRequestedFor, r.Actor.Login)
	f.Add(models.FilterBuildNumber, strconv.Itoa(r.RunNumber))
	f.Add(models.FilterRerun, strconv.FormatBool(r.RunAttempt > 1))
	if detail != nil {
		for _, fj := range detail.jobs {
			f.Add(models.FilterFailedJob, fj.job.Name)
			for _, st := range fj.job.Steps {
				if githubDetailedConclusions[st.Conclusion] {
					f.Add(models.FilterFailedStep, st.Name)
				}
			}
		}
	}
	for _, c := range changes {
		f.Add(models.FilterContainsCommit, c.SHA)
	}
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

// compareCommits lists every commit in base...head, paging through the
// compare API.
func (g *GitHubSource) compareCommits(ctx context.Context, base, head string) ([]githubCompareCommit, error) {
	var all []githubCompareCommit
	total := 0
	err := g.pages(ctx, fmt.Sprintf("%s/compare/%s...%s", g.repoAPI(), base, head), func(data []byte) (int, bool, error) {
		var page struct {
			TotalCommits int                   `json:"total_commits"`
			Commits      []githubCompareCommit `json:"commits"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		total = page.TotalCommits
		all = append(all, page.Commits...)
		return len(page.Commits), len(all) < total, nil
	})
	if err != nil {
		return nil, err
	}
	if len(all) < total {
		return nil, fmt.Errorf("compare %s...%s listed %d of %d commits", base, head, len(all), total)
	}
	return all, nil
}

// runChanges finds, for each run, the commits it built since the previous
// run on the same branch in the window (runs are newest first). The oldest
// run of each branch has no previous run to compare with.
func (g *GitHubSource) runChanges(ctx context.Context, runs []githubRun) ([][]githubCompareCommit, error) {
	out := make([][]githubCompareCommit, len(runs))
	for i, r := range runs {
		var prev *githubRun
		for j := i + 1; j < len(runs); j++ {
			if runs[j].HeadBranch == r.HeadBranch {
				prev = &runs[j]
				break
			}
		}
		if prev == nil || prev.HeadSHA == "" || r.HeadSHA == "" || prev.HeadSHA == r.HeadSHA {
			continue
		}
		commits, err := g.compareCommits(ctx, prev.HeadSHA, r.HeadSHA)
		if err != nil {
			return nil, fmt.Errorf("workflow run #%d (id %d): changes: %w", r.RunNumber, r.ID, err)
		}
		out[i] = commits
	}
	return out, nil
}

func writeGitHubChanges(sb *strings.Builder, changes []githubCompareCommit) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(sb, "\nChanges since the previous run on this branch (%d):\n", len(changes))
	for _, c := range changes {
		msg, _, _ := strings.Cut(strings.TrimSpace(c.Commit.Message), "\n")
		fmt.Fprintf(sb, "- %s %s", c.SHA, strings.TrimSpace(c.Commit.Author.Name+" "+angle(c.Commit.Author.Email)))
		if c.Commit.Author.Date != "" {
			fmt.Fprintf(sb, " (%s)", c.Commit.Author.Date)
		}
		if msg != "" {
			fmt.Fprintf(sb, ": %s", msg)
		}
		sb.WriteString("\n")
	}
}

// listPendingRuns lists the workflow's runs still queued or in progress.
func (g *GitHubSource) listPendingRuns(ctx context.Context, workflow string, since time.Time) ([]githubRun, error) {
	var all []githubRun
	for _, status := range []string{"in_progress", "queued", "waiting", "pending"} {
		listURL := fmt.Sprintf("%s/actions/workflows/%s/runs?status=%s&created=%s", g.repoAPI(), workflow, status,
			">="+since.Format("2006-01-02T15:04:05Z"))
		err := g.pages(ctx, listURL, func(data []byte) (int, bool, error) {
			var page struct {
				WorkflowRuns []githubRun `json:"workflow_runs"`
			}
			if err := json.Unmarshal(data, &page); err != nil {
				return 0, false, err
			}
			for _, r := range page.WorkflowRuns {
				// Only runs really in that state: a server that ignores the
				// status parameter must not turn finished runs into pending ones.
				if strings.EqualFold(r.Status, status) {
					all = append(all, r)
				}
			}
			return len(page.WorkflowRuns), true, nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing %s runs of workflow %q: %w", status, workflow, err)
		}
	}
	return all, nil
}

// githubDeployment is one deployment with its latest status.
type githubDeployment struct {
	ID          int64      `json:"id"`
	SHA         string     `json:"sha"`
	Ref         string     `json:"ref"`
	Environment string     `json:"environment"`
	CreatedAt   string     `json:"created_at"`
	Creator     githubUser `json:"creator"`
}

type githubDeploymentStatus struct {
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	TargetURL string `json:"target_url"`
	LogURL    string `json:"log_url"`
}

// fetchGitHubDeployments records the deployments created since since whose
// commit one of the workflow's runs built.
func (g *GitHubSource) fetchGitHubDeployments(ctx context.Context, since time.Time, runsBySHA map[string]githubRun) ([]models.SourceDocument, error) {
	var deployments []githubDeployment
	err := g.pages(ctx, g.repoAPI()+"/deployments", func(data []byte) (int, bool, error) {
		var page []githubDeployment
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		older := false
		for _, d := range page {
			if t := parseTime(d.CreatedAt); !t.IsZero() && t.Before(since) {
				older = true
				continue
			}
			deployments = append(deployments, d)
		}
		return len(page), !older, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	var docs []models.SourceDocument
	for _, d := range deployments {
		run, ok := runsBySHA[d.SHA]
		if !ok {
			continue
		}
		var statuses []githubDeploymentStatus
		if _, err := g.getJSON(ctx, fmt.Sprintf("%s/deployments/%d/statuses?per_page=1", g.repoAPI(), d.ID), &statuses); err != nil {
			return nil, fmt.Errorf("deployment %d statuses: %w", d.ID, err)
		}
		var st githubDeploymentStatus
		if len(statuses) > 0 {
			st = statuses[0]
		}
		docs = append(docs, g.deploymentDocument(d, st, run))
	}
	return docs, nil
}

func (g *GitHubSource) deploymentDocument(d githubDeployment, st githubDeploymentStatus, r githubRun) models.SourceDocument {
	state := orDefault(st.State, "unknown")
	var sb strings.Builder
	fmt.Fprintf(&sb, "Deployment of %s to %s: %s\n", shortSHA(d.SHA), d.Environment, state)
	writeField(&sb, "Environment", d.Environment)
	writeField(&sb, "Result", state)
	writeField(&sb, "Workflow run", fmt.Sprintf("%s #%d (id %d)", r.Name, r.RunNumber, r.ID))
	writeField(&sb, "Ref", d.Ref)
	writeField(&sb, "Commit", d.SHA)
	writeField(&sb, "Created by", d.Creator.Login)
	writeField(&sb, "Created", d.CreatedAt)
	writeField(&sb, "Status updated", st.CreatedAt)

	props := map[string]string{
		"title": fmt.Sprintf("Deployment to %s: %s (%s)", d.Environment, state, shortSHA(d.SHA)), "type": "deployment",
		"environment": d.Environment, "build_id": strconv.FormatInt(r.ID, 10), "result": state,
		"web_url": orDefault(st.LogURL, orDefault(st.TargetURL, fmt.Sprintf("%s/deployments", g.repoWeb()))),
	}
	f := g.scopeFilters(props)
	f.Add(models.FilterType, "deployment")
	f.Add(models.FilterEnvironment, d.Environment)
	f.Add(models.FilterResult, state)
	f.Add(models.FilterPipeline, r.Name)
	f.Add(models.FilterBuildID, strconv.FormatInt(r.ID, 10))
	f.Add(models.FilterCommit, d.SHA)
	f.Add(models.FilterBranch, d.Ref)
	if f.Date = parseTime(st.CreatedAt); f.Date.IsZero() {
		f.Date = parseTime(d.CreatedAt)
	}
	sortKeys := map[string]float64{}
	if ts := unixOrZero(d.CreatedAt); ts != 0 {
		sortKeys["queued"] = ts
	}
	if ts := unixOrZero(st.CreatedAt); ts != 0 {
		sortKeys["finished"] = ts
	}
	return models.SourceDocument{
		ID:          fmt.Sprintf("%s_deployment_%d", g.src.ID, d.ID),
		Text:        strings.TrimSpace(sb.String()),
		Tags:        g.tags(),
		Properties:  props,
		Filters:     f,
		Sort:        sortKeys,
		ChunkHeader: "Deployment to " + d.Environment,
	}
}
