package doctor

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// JobReport is the forensic job view of one session's jobs.jsonl: every job the
// session ran, as the runtime's own fold reconstructs it from the event log.
type JobReport struct {
	SessionID string    `json:"session_id"`
	JobsPath  string    `json:"jobs_path"`
	Filtered  string    `json:"filtered,omitempty"` // which filter narrowed the result: "job:<id>"
	Jobs      []JobView `json:"jobs"`
}

// JobView is one folded JobRecord: what the job was, what state it reached, and
// the links a diagnostician pivots on (delegate, child transcript, parent job).
//
// It is a curated projection, not the whole record: the delegate restore
// descriptor (frozen prompts and skill bodies) and structured results are
// deliberately left out — they are payloads, not job state.
type JobView struct {
	JobID  string `json:"job_id"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`

	ExitCode    *int       `json:"exit_code,omitempty"`
	OutputBytes int64      `json:"output_bytes"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`

	Command     string `json:"command,omitempty"`
	Task        string `json:"task,omitempty"`
	Description string `json:"description,omitempty"`

	OwnerSessionID   string `json:"owner_session_id,omitempty"`
	VisibleToSession string `json:"visible_to_session_id,omitempty"`
	ParentJobID      string `json:"parent_job_id,omitempty"`
	DelegateID       string `json:"delegate_id,omitempty"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	OutputPath       string `json:"output_path,omitempty"`

	// NotifyState is the terminal-notification progress: a terminal job still
	// "pending" is one whose caller was never told it ended.
	NotifyState      string `json:"terminal_notification_state,omitempty"`
	ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`

	// Resumable / Disposed answer "can this delegate job be revived": a disposed
	// isolation lane makes a job not-resumable however Resumable itself folded.
	Resumable       *bool  `json:"resumable,omitempty"`
	NotResumableWhy string `json:"not_resumable_reason,omitempty"`
	Disposed        bool   `json:"disposed,omitempty"`
}

// JobOpts narrows a job report.
type JobOpts struct {
	JobID string // scope to one job id
}

// Jobs resolves the selector and reports every job the session ran, folded from
// jobs.jsonl by the runtime's own jobstore.FoldOrdered — so the list is in the
// durable append order the log defines and carries the runtime's own numbers.
func Jobs(stateBase, selector string, opts JobOpts) (JobReport, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return JobReport{}, err
	}
	events, err := jobstore.ReadEvents(paths.JobsPath)
	if err != nil {
		return JobReport{}, err
	}
	return buildJobReport(paths, events, opts), nil
}

func buildJobReport(paths Paths, events []jobstore.Event, opts JobOpts) JobReport {
	report := JobReport{SessionID: paths.SessionID, JobsPath: paths.JobsPath, Jobs: []JobView{}}
	if opts.JobID != "" {
		report.Filtered = "job:" + opts.JobID
	}
	for _, rec := range jobstore.FoldOrdered(events) {
		if opts.JobID != "" && rec.JobID != opts.JobID {
			continue
		}
		report.Jobs = append(report.Jobs, jobViewFrom(rec))
	}
	return report
}

// jobViewFrom projects one folded JobRecord onto the forensic view. It is the
// single projection both the jobs report and the watches target-job join use.
func jobViewFrom(rec *jobstore.JobRecord) JobView {
	return JobView{
		JobID:            rec.JobID,
		Type:             string(rec.Type),
		Status:           string(rec.Status),
		Reason:           rec.Reason,
		ExitCode:         rec.ExitCode,
		OutputBytes:      rec.OutputBytes,
		StartedAt:        rec.StartedAt,
		EndedAt:          rec.EndedAt,
		Command:          rec.Command,
		Task:             rec.Task,
		Description:      rec.Description,
		OwnerSessionID:   rec.OwnerSessionID,
		VisibleToSession: rec.VisibleToSession,
		ParentJobID:      rec.ParentJobID,
		DelegateID:       rec.DelegateID,
		TranscriptRef:    rec.TranscriptRef,
		OutputPath:       rec.OutputPath,
		NotifyState:      string(rec.NotifyState),
		ExhaustionBudget: rec.ExhaustionBudget,
		ExhaustionLimit:  rec.ExhaustionLimit,
		Resumable:        rec.Resumable,
		NotResumableWhy:  rec.NotResumableWhy,
		Disposed:         rec.Disposed,
	}
}

// RenderJobs renders a JobReport as a human-readable summary (the default,
// non-JSON output): one block per job, in the log's append order.
func RenderJobs(r JobReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s  (jobs: %s)\n", r.SessionID, r.JobsPath)
	if len(r.Jobs) == 0 {
		b.WriteString(emptyJobsMessage(r.Filtered) + "\n")
		return b.String()
	}
	for _, j := range r.Jobs {
		b.WriteString("\n")
		fmt.Fprintf(&b, "job %s  (%s)\n", j.JobID, jobStateLabel(j))
		fmt.Fprintf(&b, "  type=%s  exit=%s  output_bytes=%d  notify=%s\n",
			dash(j.Type), optionalIntString(j.ExitCode), j.OutputBytes, dash(j.NotifyState))
		fmt.Fprintf(&b, "  started=%s  ended=%s\n", jobTime(j.StartedAt), jobTimePtr(j.EndedAt))
		if j.Command != "" {
			fmt.Fprintf(&b, "  command=%s\n", j.Command)
		}
		if j.Task != "" {
			fmt.Fprintf(&b, "  task=%s\n", j.Task)
		}
		if j.Description != "" {
			fmt.Fprintf(&b, "  description=%s\n", j.Description)
		}
		if j.OwnerSessionID != "" || j.VisibleToSession != "" {
			fmt.Fprintf(&b, "  owner=%s  visible=%s\n", dash(j.OwnerSessionID), dash(j.VisibleToSession))
		}
		if j.DelegateID != "" || j.TranscriptRef != "" {
			fmt.Fprintf(&b, "  delegate=%s  transcript=%s\n", dash(j.DelegateID), dash(j.TranscriptRef))
		}
		if j.ParentJobID != "" {
			fmt.Fprintf(&b, "  parent_job=%s\n", j.ParentJobID)
		}
		if j.OutputPath != "" {
			fmt.Fprintf(&b, "  output=%s\n", j.OutputPath)
		}
		if j.ExhaustionBudget != "" || j.ExhaustionLimit > 0 {
			fmt.Fprintf(&b, "  exhaustion: budget=%s  limit=%d\n", dash(j.ExhaustionBudget), j.ExhaustionLimit)
		}
		if j.Resumable != nil || j.NotResumableWhy != "" || j.Disposed {
			fmt.Fprintf(&b, "  resumable=%s  disposed=%t  not_resumable_reason=%s\n",
				optionalBoolString(j.Resumable), j.Disposed, dash(j.NotResumableWhy))
		}
	}
	return b.String()
}

// jobStateLabel is the parenthetical state a job block leads with: the status,
// plus the reason that produced it ("stopped: run_timeout").
func jobStateLabel(j JobView) string {
	status := j.Status
	if status == "" {
		status = "unknown"
	}
	if j.Reason != "" {
		return status + ": " + j.Reason
	}
	return status
}

// jobTime renders a job timestamp, or "-" when the fold never set one.
func jobTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339Nano)
}

func jobTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return jobTime(*t)
}

func optionalBoolString(v *bool) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatBool(*v)
}

// emptyJobsMessage renders the right "no rows" message for the active filter, so
// a session that ran jobs does not read as "no jobs recorded" just because a
// --job filter matched none of them.
func emptyJobsMessage(filtered string) string {
	if id := strings.TrimPrefix(filtered, "job:"); id != filtered {
		return "job " + id + " not found in this session"
	}
	return "no jobs recorded"
}
