// GitHub Actions workflow runs for GitHubSource, mirroring the Azure DevOps
// pipeline-run documents: every completed run of the workflow created in the
// last Days days becomes a run document, and each failed job — of any
// attempt, so a run re-run to green keeps attempt 1's failures — yields one
// document holding its complete log, linked back to the run via the
// parent_doc_id property. The run document lists the failed jobs'
// annotations (errors and warnings), GitHub's counterpart of ADO's timeline
// issues.
//
// Anything that can't be fetched fails the sync rather than being dropped,
// except logs GitHub has already deleted (past the repository's retention),
// which are noted on the job document.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
)

// githubMaxRunResults is the most runs GitHub returns for one filtered
// workflow-runs query.
const githubMaxRunResults = 1000

// githubDetailedConclusions are the run/job conclusions whose failed jobs
// are fetched in full; any other run gets only its summary document.
var githubDetailedConclusions = map[string]bool{"failure": true, "timed_out": true, "startup_failure": true}

type githubRun struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	DisplayTitle string     `json:"display_title"`
	RunNumber    int        `json:"run_number"`
	RunAttempt   int        `json:"run_attempt"`
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
	Title           string `json:"title"`
	Message         string `json:"message"`
}

// githubFailedJob is one failed job with its fetched log.
type githubFailedJob struct {
	job githubJob
	log string
}

// githubRunDetail is everything fetched for a failed run.
type githubRunDetail struct {
	annotations []string // "[failure] Job: message (path:line)"
	jobs        []githubFailedJob
}

func (g *GitHubSource) fetchWorkflowRuns(ctx context.Context, progress ProgressCallback) ([]models.SourceDocument, error) {
	workflow := strings.TrimSpace(g.src.GetConfig("Workflow"))
	if workflow == "" {
		return nil, fmt.Errorf("GitHub pipeline-run sources require a workflow file name or ID")
	}
	days, err := strconv.Atoi(strings.TrimSpace(g.src.GetConfig("Days")))
	if err != nil || days <= 0 {
		return nil, fmt.Errorf("pipeline-run sources require a positive number of Days (got %q)", g.src.GetConfig("Days"))
	}
	if progress != nil {
		progress(models.SyncProgress{Phase: "fetching", Message: "Listing workflow runs"})
	}
	runs, err := g.listRuns(ctx, workflow, time.Now().UTC().AddDate(0, 0, -days))
	if err != nil {
		return nil, err
	}

	n := len(runs)
	details := make([]*githubRunDetail, n)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		completed int
		sem       = make(chan struct{}, diffFetchConcurrency)
	)
	report := func() {
		mu.Lock()
		completed++
		if progress != nil {
			progress(models.SyncProgress{
				Phase: "fetching", Current: completed, Total: n,
				Message: fmt.Sprintf("Fetching workflow run %d/%d", completed, n),
			})
		}
		mu.Unlock()
	}
	for i := 0; i < n && workCtx.Err() == nil; i++ {
		// A re-run run reports its latest attempt's conclusion; its earlier
		// attempts may still have failed.
		if !githubDetailedConclusions[runs[i].Conclusion] && runs[i].RunAttempt <= 1 {
			report()
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			d, err := g.fetchRunDetail(workCtx, runs[i])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("workflow run #%d (id %d): %w", runs[i].RunNumber, runs[i].ID, err)
				}
				mu.Unlock()
				cancel()
				return
			}
			details[i] = d
			report()
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var docs []models.SourceDocument
	for i, r := range runs {
		docs = append(docs, g.runDocuments(r, details[i])...)
	}
	return docs, nil
}

// listRuns lists every completed run of workflow created at or after since,
// newest first. Top (set only by the form's preview) caps the list.
func (g *GitHubSource) listRuns(ctx context.Context, workflow string, since time.Time) ([]githubRun, error) {
	top := g.top()
	listURL := fmt.Sprintf("%s/actions/workflows/%s/runs?status=completed&created=%s",
		g.repoAPI(), url.PathEscape(workflow), url.QueryEscape(">="+since.Format("2006-01-02T15:04:05Z")))
	var all []githubRun
	seen := map[int64]bool{}
	total := 0
	err := g.pages(ctx, listURL, func(data []byte) (int, bool, error) {
		var page struct {
			TotalCount   int         `json:"total_count"`
			WorkflowRuns []githubRun `json:"workflow_runs"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		total = page.TotalCount
		// A run finishing during listing shifts the pages; skip the repeats.
		for _, r := range page.WorkflowRuns {
			if !seen[r.ID] {
				seen[r.ID] = true
				all = append(all, r)
			}
		}
		return len(page.WorkflowRuns), top == 0 || len(all) < top, nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing runs of workflow %q: %w", workflow, err)
	}
	if top > 0 {
		if len(all) > top {
			all = all[:top]
		}
		return all, nil
	}
	if total > githubMaxRunResults {
		return nil, fmt.Errorf("workflow %q has %d runs in the window but GitHub returns at most %d per query — lower Days",
			workflow, total, githubMaxRunResults)
	}
	return all, nil
}

// fetchRunDetail fetches the jobs of every attempt of a run, the complete
// log of every failed job and the annotations of each failed job. It returns
// nil for a run whose conclusion isn't a failure and none of whose attempts
// had a failed job.
func (g *GitHubSource) fetchRunDetail(ctx context.Context, r githubRun) (*githubRunDetail, error) {
	var jobs []githubJob
	err := g.pages(ctx, fmt.Sprintf("%s/actions/runs/%d/jobs?filter=all", g.repoAPI(), r.ID), func(data []byte) (int, bool, error) {
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

	detail := &githubRunDetail{}
	for _, j := range jobs {
		if !githubDetailedConclusions[j.Conclusion] {
			continue
		}
		annotations, err := g.listAnnotations(ctx, j.ID)
		if err != nil {
			return nil, fmt.Errorf("annotations of job %q: %w", j.Name, err)
		}
		for _, a := range annotations {
			line := fmt.Sprintf("[%s] %s%s: %s", a.AnnotationLevel, j.Name, attemptLabel(r, j), strings.TrimSpace(a.Message))
			if a.Path != "" && a.Path != ".github" {
				line += fmt.Sprintf(" (%s:%d)", a.Path, a.StartLine)
			}
			detail.annotations = append(detail.annotations, line)
		}

		data, _, err := g.get(ctx, fmt.Sprintf("%s/actions/jobs/%d/logs", g.repoAPI(), j.ID), "")
		log := cleanLogText(string(data))
		var se *httpStatusError
		switch {
		case errors.As(err, &se) && (se.status == http.StatusGone || se.status == http.StatusNotFound):
			log = fmt.Sprintf("(log no longer available on GitHub: HTTP %d)", se.status)
		case err != nil:
			return nil, fmt.Errorf("fetching log of job %q: %w", j.Name, err)
		}
		detail.jobs = append(detail.jobs, githubFailedJob{job: j, log: log})
	}
	if len(detail.jobs) == 0 && !githubDetailedConclusions[r.Conclusion] {
		return nil, nil
	}
	return detail, nil
}

// attemptLabel is " (attempt n)" for a job of a re-run run, else "".
func attemptLabel(r githubRun, j githubJob) string {
	if r.RunAttempt <= 1 {
		return ""
	}
	return fmt.Sprintf(" (attempt %d)", max(j.RunAttempt, 1))
}

func (g *GitHubSource) listAnnotations(ctx context.Context, jobID int64) ([]githubAnnotation, error) {
	var all []githubAnnotation
	err := g.pages(ctx, fmt.Sprintf("%s/check-runs/%d/annotations", g.repoAPI(), jobID), func(data []byte) (int, bool, error) {
		var page []githubAnnotation
		if err := json.Unmarshal(data, &page); err != nil {
			return 0, false, err
		}
		all = append(all, page...)
		return len(page), true, nil
	})
	return all, err
}

// runDocuments builds a run's document plus, for failed runs (detail !=
// nil), one document per failed job.
func (g *GitHubSource) runDocuments(r githubRun, detail *githubRunDetail) []models.SourceDocument {
	runDocID := fmt.Sprintf("%s_run_%d", g.src.ID, r.ID)
	runLabel := fmt.Sprintf("%s #%d", r.Name, r.RunNumber)
	runWebURL := r.HTMLURL
	if !isWebURL(runWebURL) {
		runWebURL = fmt.Sprintf("%s/actions/runs/%d", g.repoWeb(), r.ID)
	}

	newDoc := func(id, title, text, kind, webURL, header string) models.SourceDocument {
		props := map[string]string{
			"title":    title,
			"type":     kind,
			"build_id": strconv.FormatInt(r.ID, 10),
			"result":   r.Conclusion,
			"branch":   r.HeadBranch,
			"commit":   r.HeadSHA,
			"web_url":  webURL,
		}
		filters := g.scopeFilters(props)
		filters.Add(models.FilterResult, r.Conclusion)
		filters.Add(models.FilterBranch, r.HeadBranch)
		filters.Add(models.FilterBuildID, strconv.FormatInt(r.ID, 10))
		filters.Add(models.FilterCommit, r.HeadSHA)
		if id != runDocID {
			props["parent_doc_id"] = runDocID
		}
		return models.SourceDocument{
			ID:          id,
			Text:        strings.TrimSpace(text),
			Tags:        g.tags(),
			Properties:  props,
			Filters:     filters,
			ChunkHeader: header,
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s\n", runLabel, r.Conclusion)
	writeField(&sb, "Workflow", r.Name)
	writeField(&sb, "Run", fmt.Sprintf("#%d (id %d, attempt %d)", r.RunNumber, r.ID, r.RunAttempt))
	writeField(&sb, "Title", r.DisplayTitle)
	writeField(&sb, "Result", r.Conclusion)
	writeField(&sb, "Status", r.Status)
	writeField(&sb, "Event", r.Event)
	writeField(&sb, "Branch", r.HeadBranch)
	writeField(&sb, "Commit", r.HeadSHA)
	writeField(&sb, "Repository", g.owner+"/"+g.repo)
	writeField(&sb, "Triggered by", r.Actor.Login)
	writeField(&sb, "Created", r.CreatedAt)
	writeField(&sb, "Started", r.RunStartedAt)
	writeField(&sb, "Finished", r.UpdatedAt)
	if start, finish := parseTime(r.RunStartedAt), parseTime(r.UpdatedAt); !start.IsZero() && !finish.IsZero() {
		writeField(&sb, "Duration", finish.Sub(start).Round(time.Second).String())
	}

	var children []models.SourceDocument
	if detail != nil {
		if !githubDetailedConclusions[r.Conclusion] && len(detail.jobs) > 0 {
			sb.WriteString("Passed after re-run: the run's final conclusion is " + r.Conclusion +
				", but jobs of earlier attempts failed (possibly flaky) — listed below.\n")
		}
		if len(detail.annotations) > 0 {
			sb.WriteString("\nAnnotations:\n")
			for _, a := range detail.annotations {
				sb.WriteString("- " + a + "\n")
			}
		}
		if len(detail.jobs) > 0 {
			sb.WriteString("\nFailed jobs:\n")
		}
		for _, fj := range detail.jobs {
			j := fj.job
			id := fmt.Sprintf("%s_job_%d", runDocID, j.ID)
			label := j.Name + attemptLabel(r, j)
			fmt.Fprintf(&sb, "- %s (document %s)\n", label, id)

			var failedSteps []string
			for _, st := range j.Steps {
				if githubDetailedConclusions[st.Conclusion] {
					failedSteps = append(failedSteps, st.Name)
				}
			}
			var jb strings.Builder
			fmt.Fprintf(&jb, "Failed job: %s\n", label)
			writeField(&jb, "Result", j.Conclusion)
			writeField(&jb, "Failed steps", strings.Join(failedSteps, ", "))
			writeField(&jb, "Workflow run", fmt.Sprintf("%s (%s)", runLabel, r.Conclusion))
			writeField(&jb, "Branch", r.HeadBranch)
			writeField(&jb, "Commit", r.HeadSHA)
			writeField(&jb, "Started", j.StartedAt)
			writeField(&jb, "Finished", j.CompletedAt)
			jb.WriteString("\nLog:\n" + fj.log + "\n")

			webURL := j.HTMLURL
			if !isWebURL(webURL) {
				webURL = fmt.Sprintf("%s/actions/runs/%d/job/%d", g.repoWeb(), r.ID, j.ID)
			}
			doc := newDoc(id, fmt.Sprintf("%s failed job: %s", runLabel, label), jb.String(), "job", webURL,
				fmt.Sprintf("Run #%d > %s", r.RunNumber, label))
			doc.Properties["step_path"] = j.Name
			if r.RunAttempt > 1 {
				doc.Properties["attempt"] = strconv.Itoa(max(j.RunAttempt, 1))
				doc.Filters.Add(models.FilterAttempt, strconv.Itoa(max(j.RunAttempt, 1)))
			}
			children = append(children, doc)
		}
	}

	runDoc := newDoc(runDocID, fmt.Sprintf("%s %s", runLabel, r.Conclusion), sb.String(), "run", runWebURL, fmt.Sprintf("Run #%d", r.RunNumber))
	if r.HeadSHA != "" {
		runDoc.Properties["commit_web_url"] = g.repoWeb() + "/commit/" + r.HeadSHA
	}
	return append([]models.SourceDocument{runDoc}, children...)
}
