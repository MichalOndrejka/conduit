// Attributing a rerun pipeline run's failed tests to the attempt that
// produced them, so the agent can tell which tests failed in which attempt.
package sources

import (
	"fmt"
	"strings"
	"time"
)

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

func testNameOf(r adoTestResult) string {
	if r.AutomatedTestName != "" {
		return r.AutomatedTestName
	}
	return r.TestCaseTitle
}
