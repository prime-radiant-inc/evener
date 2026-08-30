package doctor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

// JobReport is the forensic job view of one session's jobs.jsonl: every job the
// session ran, as the runtime's own fold reconstructs it from the event log.
type JobReport struct {
	SessionID     string         `json:"session_id"`
	JobsPath      string         `json:"jobs_path"`
	DelegatesPath string         `json:"delegates_path,omitempty"`
	Filtered      string         `json:"filtered,omitempty"` // which filter narrowed the result: "job:<id>"
	Jobs          []JobView      `json:"jobs"`
	Delegates     []DelegateView `json:"delegates,omitempty"`
	Failures      []StateFailure `json:"failures,omitempty"`
	Diagnostics   []string       `json:"diagnostics,omitempty"`
}

type StateFailure struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type DelegateView struct {
	DelegateID         string                        `json:"delegate_id"`
	OwnerSessionID     string                        `json:"owner_session_id"`
	VisibleSessionID   string                        `json:"visible_session_id,omitempty"`
	ParentDelegateID   string                        `json:"parent_delegate_id,omitempty"`
	ChildSessionID     string                        `json:"child_session_id"`
	TranscriptRef      string                        `json:"transcript_ref"`
	Task               string                        `json:"task"`
	Description        string                        `json:"description,omitempty"`
	AgentType          string                        `json:"agent_type"`
	RequestedModel     string                        `json:"requested_model,omitempty"`
	ResolvedProfileID  string                        `json:"resolved_profile_id,omitempty"`
	ResolvedModel      string                        `json:"resolved_model,omitempty"`
	ReasoningEffort    string                        `json:"reasoning_effort,omitempty"`
	Phase              string                        `json:"phase"`
	Resumable          bool                          `json:"resumable"`
	NotResumableReason string                        `json:"not_resumable_reason,omitempty"`
	RunStartedAt       time.Time                     `json:"run_started_at"`
	LatestActivityAt   time.Time                     `json:"latest_activity_at"`
	LatestOutcome      *delegatestore.Outcome        `json:"latest_outcome,omitempty"`
	LatestPacket       *delegatestore.TerminalPacket `json:"latest_packet,omitempty"`
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
	OutputPath       string `json:"output_path,omitempty"`

	// NotifyState is the terminal-notification progress: a terminal job still
	// "pending" is one whose caller was never told it ended. "delivered" was
	// rendered into the caller's own notification turn; "consumed" means the
	// caller read the terminal job_status itself, which settles the pending
	// without a turn. Both mean told — do not count only "delivered".
	NotifyState      string `json:"terminal_notification_state,omitempty"`
	ExhaustionBudget string `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int    `json:"exhaustion_limit,omitempty"`
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
	report := buildJobReport(paths, events, opts)
	delegatesPath, delegates, diagnostics, err := stableDoctorDelegates(paths)
	if err != nil {
		return JobReport{}, err
	}
	report.DelegatesPath = delegatesPath
	report.Delegates = projectDoctorDelegates(paths.SessionID, delegates)
	report.Diagnostics = diagnostics
	report.Failures = legacyDelegateFailures(events)
	return report, nil
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

// delegateJournalRead is the delegate-journal read seam. Production code
// never assigns it; tests swap it to count reads (the shared-root refold
// regression). It mirrors the openDoctorTranscriptFile hook convention.
var delegateJournalRead = func(path string) {}

// delegateCacheEntry is one cached root-session delegates read: the folded
// state plus the diagnostics from the single read that produced it.
type delegateCacheEntry struct {
	state       delegatestore.State
	diagnostics []string
}

// delegateCache memoizes stableDoctorDelegates per (bucket dir, resolved root
// session id) for the lifetime of one sweeping call (ListSessions, RunAudit,
// Tree). Child sessions in one job tree share a root and therefore a
// delegates.jsonl — without the cache a sweep re-reads and re-folds that
// journal once per child (measured 18 redundant folds of one 1.7MB journal on
// a real state root). The bucket dir is part of the key because Tree walks
// across buckets with one cache; a root id alone would collide across buckets
// (locateAcrossBuckets resolves the same sid in only one bucket, but the cache
// must not depend on that invariant). The cache is per-invocation and never
// persisted: reads stay pure, and a fresh call re-reads the journal, so a
// mutating writer between two calls is always observed.
type delegateCache map[delegateCacheKey]delegateCacheEntry

// delegateCacheKey is the (bucketDir, rootSessionID) pair a cache entry keys on.
type delegateCacheKey struct {
	bucketDir     string
	rootSessionID string
}

// cachedDoctorDelegates resolves the session's delegates state through the
// cache. A root's entry is computed at most once per cache; errors are never
// cached — a failed read is retried on the next session that needs the same
// root, matching the uncached behavior where each session sees the error.
// The root is resolved once here (not again inside stableDoctorDelegates for
// the miss path): the miss path calls stableDoctorDelegatesForRoot directly.
// Callers holding the session's meta already (ListSessions does) can skip the
// per-session meta read with getForRoot.
func (c delegateCache) get(paths Paths) (string, delegatestore.State, []string, error) {
	return c.getForRoot(paths.BucketDir, resolveDelegateRoot(paths))
}

// getForRoot is get with a pre-resolved root — the caller already read the
// session's meta and knows its job-tree root, so the cache lookup needs no
// further I/O.
func (c delegateCache) getForRoot(bucketDir, rootSessionID string) (string, delegatestore.State, []string, error) {
	key := delegateCacheKey{bucketDir: bucketDir, rootSessionID: rootSessionID}
	if entry, ok := c[key]; ok {
		return delegateJournalPath(bucketDir, rootSessionID), entry.state, entry.diagnostics, nil
	}
	path, state, diagnostics, err := stableDoctorDelegatesForRoot(bucketDir, rootSessionID)
	if err != nil {
		return path, nil, nil, err
	}
	c[key] = delegateCacheEntry{state: state, diagnostics: diagnostics}
	return path, state, diagnostics, nil
}

// resolveDelegateRoot returns the session id whose delegates.jsonl is the
// lifecycle authority for this session: the meta's JobTreeRootSessionID when
// set, else the session itself. It reads the session's meta from disk; callers
// that already hold the meta (e.g. ListSessions' sweep) should call
// delegateRootFromMeta and getForRoot instead.
func resolveDelegateRoot(paths Paths) string {
	if meta, err := schema.LoadSessionMeta(paths.BucketDir, paths.SessionID); err == nil {
		return delegateRootFromMeta(meta, paths.SessionID)
	}
	return paths.SessionID
}

// delegateRootFromMeta is the single encoding of "who owns this session's
// delegates journal": the meta's JobTreeRootSessionID when set, else the
// session itself. resolveDelegateRoot (disk read) and sessionRow (meta in
// hand) both go through it so the two paths cannot diverge.
func delegateRootFromMeta(meta schema.SessionMeta, self string) string {
	if root := strings.TrimSpace(meta.JobTreeRootSessionID); root != "" {
		return root
	}
	return self
}

func delegateJournalPath(bucketDir, rootSessionID string) string {
	return filepath.Join(bucketDir, "sessions", rootSessionID, "delegates.jsonl")
}

// stableDoctorDelegates resolves the root session (meta.JobTreeRootSessionID
// override, else the session itself) and reads that root's delegates.jsonl.
func stableDoctorDelegates(paths Paths) (string, delegatestore.State, []string, error) {
	return stableDoctorDelegatesForRoot(paths.BucketDir, resolveDelegateRoot(paths))
}

// stableDoctorDelegatesForRoot reads one resolved root's delegates.jsonl
// through the read-only event reader and folds it — the stable delegate
// lifecycle authority. Reads are pure: this never opens an append-capable
// store.
func stableDoctorDelegatesForRoot(bucketDir, rootSessionID string) (string, delegatestore.State, []string, error) {
	path := delegateJournalPath(bucketDir, rootSessionID)
	delegateJournalRead(path)
	events, readDiagnostics, err := delegatestore.ReadEventsWithDiagnostics(path)
	if err != nil {
		return path, nil, nil, err
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		return path, nil, nil, err
	}
	var diagnostics []string
	if readDiagnostics.TornTail {
		diagnostics = append(diagnostics, "delegate_journal_torn_tail: ignored unterminated trailing batch")
	}
	return path, state, diagnostics, nil
}

func projectDoctorDelegates(ownerSessionID string, state delegatestore.State) []DelegateView {
	ids := make([]string, 0, len(state))
	for id, aggregate := range state {
		if aggregate != nil && aggregate.Descriptor.OwnerSessionID == ownerSessionID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	rows := make([]DelegateView, 0, len(ids))
	for _, id := range ids {
		aggregate := state[id]
		descriptor := aggregate.Descriptor
		rows = append(rows, DelegateView{
			DelegateID: id, OwnerSessionID: descriptor.OwnerSessionID, VisibleSessionID: descriptor.VisibleSessionID,
			ParentDelegateID: descriptor.ParentDelegateID, ChildSessionID: descriptor.ChildSessionID, TranscriptRef: descriptor.TranscriptRef,
			Task: descriptor.Task, Description: descriptor.Description, AgentType: descriptor.AgentType,
			RequestedModel: descriptor.RequestedModel, ResolvedProfileID: descriptor.ResolvedProfileID,
			ResolvedModel: descriptor.ResolvedModel, ReasoningEffort: descriptor.Config.ReasoningEffort,
			Phase: string(aggregate.Phase), Resumable: aggregate.Resumable, NotResumableReason: aggregate.NotResumableReason,
			RunStartedAt: aggregate.RunStartedAt, LatestActivityAt: aggregate.LatestActivityAt,
			LatestOutcome: aggregate.LatestOutcome, LatestPacket: aggregate.LatestPacket,
		})
	}
	return rows
}

func legacyDelegateFailures(events []jobstore.Event) []StateFailure {
	legacyIDs := make(map[string]bool)
	for _, record := range jobstore.Fold(events) {
		if record != nil && string(record.Type) == "delegate" {
			legacyIDs[record.JobID] = true
		}
	}
	var failures []StateFailure
	if len(legacyIDs) != 0 {
		ids := make([]string, 0, len(legacyIDs))
		for id := range legacyIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		failures = append(failures, StateFailure{Code: "legacy_delegate_state", Detail: "retired delegate activation jobs: " + strings.Join(ids, ", ")})
	}
	var watchIDs []string
	for id, watch := range jobstore.FoldWatches(events) {
		if watch == nil {
			continue
		}
		target := strings.TrimPrefix(strings.TrimSpace(watch.Target), "job:")
		if legacyIDs[target] {
			watchIDs = append(watchIDs, id)
		}
	}
	if len(watchIDs) != 0 {
		sort.Strings(watchIDs)
		failures = append(failures, StateFailure{Code: "legacy_delegate_watch_state", Detail: "retired delegate activation watches: " + strings.Join(watchIDs, ", ")})
	}
	return failures
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
		OutputPath:       rec.OutputPath,
		NotifyState:      string(rec.NotifyState),
		ExhaustionBudget: rec.ExhaustionBudget,
		ExhaustionLimit:  rec.ExhaustionLimit,
	}
}

// RenderJobs renders a JobReport as a human-readable summary (the default,
// non-JSON output): one block per job, in the log's append order.
func RenderJobs(r JobReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "session %s  (jobs: %s)\n", r.SessionID, r.JobsPath)
	if len(r.Jobs) == 0 && len(r.Delegates) == 0 && len(r.Failures) == 0 {
		b.WriteString(emptyJobsMessage(r.Filtered))
		b.WriteString("\n")
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
		if j.ParentJobID != "" {
			fmt.Fprintf(&b, "  parent_job=%s\n", j.ParentJobID)
		}
		if j.OutputPath != "" {
			fmt.Fprintf(&b, "  output=%s\n", j.OutputPath)
		}
		if j.ExhaustionBudget != "" || j.ExhaustionLimit > 0 {
			fmt.Fprintf(&b, "  exhaustion: budget=%s  limit=%d\n", dash(j.ExhaustionBudget), j.ExhaustionLimit)
		}
	}
	for _, d := range r.Delegates {
		b.WriteString("\n")
		fmt.Fprintf(&b, "delegate %s  (%s)\n", d.DelegateID, d.Phase)
		fmt.Fprintf(&b, "  owner=%s  child=%s  transcript=%s\n", d.OwnerSessionID, d.ChildSessionID, d.TranscriptRef)
		fmt.Fprintf(&b, "  task=%s  agent_type=%s  model=%s  resumable=%t\n", d.Task, d.AgentType, d.ResolvedModel, d.Resumable)
	}
	for _, failure := range r.Failures {
		fmt.Fprintf(&b, "\n%s: %s\n", failure.Code, failure.Detail)
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

// emptyJobsMessage renders the right "no rows" message for the active filter, so
// a session that ran jobs does not read as "no jobs recorded" just because a
// --job filter matched none of them.
func emptyJobsMessage(filtered string) string {
	if id, ok := strings.CutPrefix(filtered, "job:"); ok {
		return "job " + id + " not found in this session"
	}
	return "no jobs recorded"
}
