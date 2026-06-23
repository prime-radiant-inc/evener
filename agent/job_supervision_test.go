package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// mutableClock is a race-safe, advanceable clock for supervision tests. The
// watchdog goroutine reads jm.now() under jm.mu and the test advances it from
// the test goroutine, so the backing store is atomic.
type mutableClock struct {
	nanos atomic.Int64
}

func newMutableClock(start time.Time) *mutableClock {
	c := &mutableClock{}
	c.nanos.Store(start.UnixNano())
	return c
}

func (c *mutableClock) now() time.Time { return time.Unix(0, c.nanos.Load()).UTC() }

func (c *mutableClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

// runningJobByID returns the live runningJob for jobID or fails the test.
func runningJobByID(t *testing.T, jm *jobManager, jobID string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		t.Fatalf("job %q is not running", jobID)
	}
	return run
}

// readJobListEntry drives the real job_list tool and returns the row for jobID.
func readJobListEntry(t *testing.T, s *Session, jobID string) jobListToolEntry {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{"include_nested":true,"status":["running","completed","failed","cancelled","stopped"]}`),
	})
	if res.IsError {
		t.Fatalf("job_list returned error: %s", res.Output)
	}
	var out jobListToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
	}
	entry := findJobListToolOutput(out.Jobs, jobID)
	if entry == nil {
		t.Fatalf("job_list missing job %q; jobs = %+v", jobID, out.Jobs)
	}
	return *entry
}

type jobStatusToolOutput struct {
	JobID              string `json:"job_id"`
	Kind               string `json:"kind"`
	Status             string `json:"status"`
	Phase              string `json:"phase"`
	Reason             string `json:"reason"`
	RunningForMS       int64  `json:"running_for_ms"`
	DurationMS         int64  `json:"duration_ms"`
	QuietForMS         int64  `json:"quiet_for_ms"`
	StartedAt          string `json:"started_at"`
	EndedAt            string `json:"ended_at"`
	LastEventAt        string `json:"last_event_at"`
	TranscriptRef      string `json:"transcript_ref"`
	NotificationStatus string `json:"notification_status"`
}

func TestJobStatusRunningShellProjectsSupervisionFields(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(5000, 0).UTC())
	jm.now = clk.now

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	clk.advance(90 * time.Second)
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_status returned error: %s", res.Output)
	}

	var out jobStatusToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_status: %v (output: %s)", err, res.Output)
	}
	if out.JobID != rec.JobID {
		t.Fatalf("job_id = %q, want %q", out.JobID, rec.JobID)
	}
	if out.Kind != "shell" {
		t.Fatalf("kind = %q, want shell", out.Kind)
	}
	if out.Status != "running" {
		t.Fatalf("status = %q, want running", out.Status)
	}
	if out.Phase != "process_running" {
		t.Fatalf("phase = %q, want process_running", out.Phase)
	}
	if out.RunningForMS != 90000 {
		t.Fatalf("running_for_ms = %d, want 90000", out.RunningForMS)
	}
	if out.QuietForMS != 90000 {
		t.Fatalf("quiet_for_ms = %d, want 90000", out.QuietForMS)
	}
	if out.TranscriptRef != "job:"+rec.JobID {
		t.Fatalf("transcript_ref = %q, want job:%s", out.TranscriptRef, rec.JobID)
	}
	if out.StartedAt == "" || out.LastEventAt == "" {
		t.Fatalf("missing timestamps: %+v", out)
	}
	if out.NotificationStatus != "" {
		t.Fatalf("notification_status leaked into normal status: %+v", out)
	}
}

// TestJobListLastActivityAdvancesWithShellOutput proves last_activity is stamped
// at the clock's value when output is appended for a running shell job.
func TestJobListLastActivityAdvancesWithShellOutput(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(1000, 0).UTC())
	jm.now = clk.now

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	start := readJobListEntry(t, s, rec.JobID)
	if start.LastActivity == nil {
		t.Fatalf("running shell row has no last_activity; want StartedAt seed")
	}
	if *start.LastActivity != start.StartedAt {
		t.Fatalf("initial last_activity = %q, want StartedAt %q", *start.LastActivity, start.StartedAt)
	}

	// Advance the clock and append output: the stamp must follow the new now().
	clk.advance(5 * time.Minute)
	run := runningJobByID(t, jm, rec.JobID)
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("progress\n")); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}

	after := readJobListEntry(t, s, rec.JobID)
	if after.LastActivity == nil {
		t.Fatalf("after output, last_activity is nil")
	}
	want := clk.now().Format(time.RFC3339Nano)
	if *after.LastActivity != want {
		t.Fatalf("last_activity after output = %q, want %q", *after.LastActivity, want)
	}
	if *after.LastActivity == *start.LastActivity {
		t.Fatalf("last_activity did not advance after output: %q", *after.LastActivity)
	}
}

// TestJobListDelegateLastActivitySeededAtStart proves a running delegate row
// carries last_activity (at least the StartedAt seed).
func TestJobListDelegateLastActivitySeededAtStart(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	clk := newMutableClock(time.Unix(2000, 0).UTC())
	parent.jobManager.now = clk.now

	sub := completedDelegateSubagent(child, "report")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "investigate", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		_ = parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
		waitForShellDone(t, parent.jobManager, run.rec.JobID)
	})

	entry := readJobListEntry(t, parent, run.rec.JobID)
	if entry.Type != string(jobstore.JobDelegate) {
		t.Fatalf("entry type = %q, want delegate", entry.Type)
	}
	if entry.LastActivity == nil {
		t.Fatalf("running delegate row has no last_activity")
	}
	if *entry.LastActivity != entry.StartedAt {
		t.Fatalf("delegate last_activity = %q, want StartedAt %q", *entry.LastActivity, entry.StartedAt)
	}
}

// TestJobReadOutputCarriesLastActivity proves job_read_output results carry
// last_activity for a running job.
func TestJobReadOutputCarriesLastActivity(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(3000, 0).UTC())
	jm.now = clk.now

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
	}
	if out.LastActivity == nil {
		t.Fatalf("job_read_output result missing last_activity: %s", res.Output)
	}
	want := clk.now().Format(time.RFC3339Nano)
	if *out.LastActivity != want {
		t.Fatalf("read_output last_activity = %q, want %q", *out.LastActivity, want)
	}
}

// TestJobListTerminalLastActivityFallsBackToEndedAt proves that a terminal
// record reloaded from the store (no live LastActivity stamp) falls back to
// EndedAt in the projection.
func TestJobListTerminalLastActivityFallsBackToEndedAt(t *testing.T) {
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(4000, 0).UTC())
	jm.now = clk.now

	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	// Finish at a later instant so EndedAt differs from StartedAt.
	clk.advance(2 * time.Minute)
	finishRunningTestJob(t, jm, rec.JobID)

	// After finalize the runningJob is gone; the projection reads the durable
	// store record, whose LastActivity is nil, and must fall back to EndedAt.
	entry := readJobListEntry(t, s, rec.JobID)
	if entry.EndedAt == nil {
		t.Fatalf("terminal job row has no ended_at: %+v", entry)
	}
	if entry.LastActivity == nil {
		t.Fatalf("terminal job row has no last_activity fallback: %+v", entry)
	}
	if *entry.LastActivity != *entry.EndedAt {
		t.Fatalf("terminal last_activity = %q, want EndedAt fallback %q", *entry.LastActivity, *entry.EndedAt)
	}
	if *entry.LastActivity == entry.StartedAt {
		t.Fatalf("terminal last_activity fell back to StartedAt %q, want EndedAt %q", entry.StartedAt, *entry.EndedAt)
	}
}

// --- Quiet-job watchdog ---

// TestQuietWatchdogMessageNamesProductionWindow locks the production message
// wording: the default 10-minute window must read "quiet for 10m" (not
// "10m0s"), and the message carries the last-activity timestamp.
func TestQuietWatchdogMessageNamesProductionWindow(t *testing.T) {
	last := time.Unix(1000, 0).UTC()
	msg := quietWatchdogMessage(10*time.Minute, last)
	if !strings.HasPrefix(msg, "quiet for 10m;") {
		t.Fatalf("production quiet message = %q, want it to start with %q", msg, "quiet for 10m;")
	}
	if !strings.Contains(msg, last.Format(time.RFC3339Nano)) {
		t.Fatalf("quiet message = %q, want it to carry last activity %q", msg, last.Format(time.RFC3339Nano))
	}
}

// withQuietWatchdogScaling sets small watchdog timing vars for the duration of a
// test and restores them on cleanup.
func withQuietWatchdogScaling(t *testing.T, window, check time.Duration) {
	t.Helper()
	origWindow := delegateQuietWindow
	origCheck := delegateQuietCheckInterval
	delegateQuietWindow = window
	delegateQuietCheckInterval = check
	t.Cleanup(func() {
		delegateQuietWindow = origWindow
		delegateQuietCheckInterval = origCheck
	})
}

// quietCapture records quiet notifications enqueued for a job id.
type quietCapture struct {
	mu      sync.Mutex
	all     []jobNotification
	kicks   atomic.Int64
	deliver func(jobNotification)
}

func newQuietCapture() *quietCapture { return &quietCapture{} }

func (q *quietCapture) enqueue(n jobNotification) {
	q.mu.Lock()
	q.all = append(q.all, n)
	q.mu.Unlock()
	if q.deliver != nil {
		q.deliver(n)
	}
}

func (q *quietCapture) quietFor(jobID string) []jobNotification {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []jobNotification
	for _, n := range q.all {
		if n.JobID == jobID && strings.Contains(n.Reason, "quiet") {
			out = append(out, n)
		}
	}
	return out
}

func (q *quietCapture) waitForQuiet(t *testing.T, jobID string, want int, timeout time.Duration) []jobNotification {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := q.quietFor(jobID)
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return q.quietFor(jobID)
}

// startRunningDelegateForWatchdog attaches a delegate runningJob on a parent
// jobManager driven by the supplied clock and returns the parent session, job
// id, the quiet-capture, and a cleanup that finalizes the delegate.
func startRunningDelegateForWatchdog(t *testing.T, clk *mutableClock) (*Session, string, *quietCapture, func()) {
	t.Helper()
	parent := newTestSession(t)
	child := newTestSession(t)
	parent.jobManager.now = clk.now

	qc := newQuietCapture()
	// Wire enqueue + kick capture without touching delivery (spec §3).
	parent.jobManager.enqueue = qc.enqueue
	parent.jobManager.wake = func() { qc.kicks.Add(1) }

	sub := completedDelegateSubagent(child, "report")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "long task", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	jobID := run.rec.JobID
	cleanup := func() {
		_ = parent.finalizeDelegate(jobID, child.ID(), sub)
		waitForShellDone(t, parent.jobManager, jobID)
	}
	return parent, jobID, qc, cleanup
}

func TestQuietWatchdogFiresOnceForQuietDelegate(t *testing.T) {
	withQuietWatchdogScaling(t, 50*time.Millisecond, 5*time.Millisecond)
	clk := newMutableClock(time.Unix(5000, 0).UTC())
	parent, jobID, qc, cleanup := startRunningDelegateForWatchdog(t, clk)
	defer cleanup()
	_ = parent

	// Push the clock well past the quiet window with no activity.
	clk.advance(10 * time.Minute)

	got := qc.waitForQuiet(t, jobID, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("quiet notifications = %d, want exactly 1; all=%+v", len(got), got)
	}
	n := got[0]
	if n.JobID != jobID {
		t.Fatalf("quiet notification job_id = %q, want %q", n.JobID, jobID)
	}
	if !strings.Contains(n.Reason, "quiet") {
		t.Fatalf("quiet notification reason = %q, want it to mention quiet", n.Reason)
	}
	if qc.kicks.Load() == 0 {
		t.Fatalf("watchdog enqueued a notification but never kicked the drain loop")
	}

	// Latch: more ticks with still-no-activity must not fire a second time.
	again := qc.waitForQuiet(t, jobID, 2, 200*time.Millisecond)
	if len(again) != 1 {
		t.Fatalf("quiet notifications after latch = %d, want still 1; all=%+v", len(again), again)
	}
}

func TestQuietWatchdogResetsOnActivity(t *testing.T) {
	withQuietWatchdogScaling(t, 50*time.Millisecond, 5*time.Millisecond)
	clk := newMutableClock(time.Unix(6000, 0).UTC())
	parent, jobID, qc, cleanup := startRunningDelegateForWatchdog(t, clk)
	defer cleanup()

	clk.advance(10 * time.Minute)
	if got := qc.waitForQuiet(t, jobID, 1, 2*time.Second); len(got) != 1 {
		t.Fatalf("first quiet notifications = %d, want 1", len(got))
	}

	// Stamp activity (advance LastActivity to "now") and let quiet build again.
	run := runningJobByID(t, parent.jobManager, jobID)
	parent.jobManager.mu.Lock()
	now := parent.jobManager.now()
	run.rec.LastActivity = &now
	parent.jobManager.mu.Unlock()

	// Within window after the stamp: latch must have cleared and not re-fired yet.
	if got := qc.waitForQuiet(t, jobID, 2, 100*time.Millisecond); len(got) != 1 {
		t.Fatalf("quiet notifications right after activity = %d, want still 1", len(got))
	}

	// Build quiet again past the window: a SECOND notification must fire.
	clk.advance(10 * time.Minute)
	if got := qc.waitForQuiet(t, jobID, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("quiet notifications after second quiet stretch = %d, want 2", len(got))
	}
}

func TestQuietWatchdogIgnoresShellJobs(t *testing.T) {
	withQuietWatchdogScaling(t, 50*time.Millisecond, 5*time.Millisecond)
	s := newTestSession(t)
	jm := s.jobManager
	clk := newMutableClock(time.Unix(7000, 0).UTC())
	jm.now = clk.now
	qc := newQuietCapture()
	jm.enqueue = qc.enqueue

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	clk.advance(10 * time.Minute)
	got := qc.waitForQuiet(t, rec.JobID, 1, 300*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("shell job produced quiet notifications = %d, want 0 (watchdog is delegate-only)", len(got))
	}
}

func TestQuietWatchdogStopsOnFinalize(t *testing.T) {
	withQuietWatchdogScaling(t, 50*time.Millisecond, 5*time.Millisecond)
	clk := newMutableClock(time.Unix(8000, 0).UTC())
	parent, jobID, qc, cleanup := startRunningDelegateForWatchdog(t, clk)

	// Finalize immediately (no quiet stretch yet). The watchdog goroutine must
	// exit; no quiet notification may appear after finalize.
	cleanup()

	clk.advance(10 * time.Minute)
	got := qc.waitForQuiet(t, jobID, 1, 300*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("quiet notifications after finalize = %d, want 0 (watchdog must stop)", len(got))
	}
	_ = parent
}

// TestQuietWatchdogDoesNotDeliver asserts the watchdog only enqueues an owner
// notification and never steers/delivers to the delegate (spec §3): the child
// session must receive no injected input from the watchdog firing.
func TestQuietWatchdogDoesNotDeliver(t *testing.T) {
	withQuietWatchdogScaling(t, 50*time.Millisecond, 5*time.Millisecond)
	clk := newMutableClock(time.Unix(9000, 0).UTC())
	parent, jobID, qc, cleanup := startRunningDelegateForWatchdog(t, clk)
	defer cleanup()

	clk.advance(10 * time.Minute)
	got := qc.waitForQuiet(t, jobID, 1, 2*time.Second)
	if len(got) != 1 {
		t.Fatalf("quiet notifications = %d, want 1", len(got))
	}
	// The notification is a watch-style owner notification: no WatchSend frame
	// (that would be a delivery to a target), and it targets the owner via the
	// job id, not the child runtime.
	n := got[0]
	if n.WatchSend != nil {
		t.Fatalf("quiet notification carried a WatchSend frame; watchdog must not deliver")
	}
	if n.Status != jobNotificationEventWatch {
		t.Fatalf("quiet notification status = %q, want %q (owner notification, not delivery)", n.Status, jobNotificationEventWatch)
	}
	_ = parent
}
