package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestFormatJobNotification(t *testing.T) {
	t.Parallel()
	code := 0
	block := formatJobNotificationBlock(jobNotification{
		JobID: "job_X", JobType: "shell", Status: "completed", Reason: "exit_zero",
		OutputBytes: 42, ExitCode: &code,
	}, notificationExcerpt{})
	for _, want := range []string{`job_id="job_X"`, `event="completed"`, `job_type="shell"`, `status="completed"`, `reason="exit_zero"`, "job_read_output"} {
		if !strings.Contains(block, want) {
			t.Errorf("notification missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "subagent") {
		t.Errorf("must use job notification wording, not subagent")
	}

	watchBlock := formatJobNotificationBlock(jobNotification{
		JobType: "watch",
		Status:  "watch",
		Reason:  "event: ASSISTANT_TEXT_END",
	}, notificationExcerpt{})
	if !strings.Contains(watchBlock, "Watch event triggered") {
		t.Fatalf("watch notification block = %q, want watch wording", watchBlock)
	}
	if strings.Contains(watchBlock, "job_read_output") {
		t.Fatalf("watch notification without job_id must not suggest job_read_output:\n%s", watchBlock)
	}

	emptyReason := formatJobNotificationBlock(jobNotification{
		JobID: "job_Y", JobType: "shell", Status: "completed",
	}, notificationExcerpt{})
	if !strings.Contains(emptyReason, `reason=""`) {
		t.Errorf("empty reason must still be rendered:\n%s", emptyReason)
	}
}

func appendPendingJobNotificationRecord(t *testing.T, jm *jobManager, sessionID string) {
	t.Helper()
	started := time.Unix(1000, 0).UTC()
	ended := time.Unix(1001, 0).UTC()
	code := 0
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_X",
		Type:             jobstore.JobShell,
		OwnerSessionID:   sessionID,
		VisibleToSession: sessionID,
		StartedAt:        &started,
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          ended,
		JobID:       "job_X",
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		ExitCode:    &code,
		EndedAt:     &ended,
		OutputBytes: 42,
		TerminalGen: "GEN1",
	}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          ended,
		JobID:       "job_X",
		TerminalGen: "GEN1",
	}); err != nil {
		t.Fatalf("append pending: %v", err)
	}
}

// appendPendingJobNotificationRecordWithProvenance persists a terminal job
// record (started → finished → notification-pending) for jobID whose pending
// event carries prov. Folded, prov lands on the record's NotificationProvenance,
// so a delivery rebuilt via jobNotificationFromRecord carries it.
func appendPendingJobNotificationRecordWithProvenance(t *testing.T, jm *jobManager, sessionID, jobID string, prov *provenance.Causal) {
	t.Helper()
	started := time.Unix(1000, 0).UTC()
	ended := time.Unix(1001, 0).UTC()
	gen := "GEN_" + jobID
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   sessionID,
		VisibleToSession: sessionID,
		StartedAt:        &started,
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          ended,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &ended,
		TerminalGen: gen,
	}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          ended,
		JobID:       jobID,
		TerminalGen: gen,
		Provenance:  prov,
	}); err != nil {
		t.Fatalf("append pending: %v", err)
	}
}

func TestNotificationTurnBareTextAckIsNotScolded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// The agent already observed this job's result, so it acknowledges the
			// terminal notification with bare text instead of calling communicate.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("Acknowledged — already saw this job, no action needed.")}
			},
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	sess.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, sess.ID())
	// An in-memory enqueue drives the notification turn; the durable record supplies
	// the rendered payload.
	sess.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn with a bare-text ack must not error: %v", err)
	}

	// Exactly one model request: a bare-text ack on a notification-driven turn ends
	// the turn idle, rather than being scolded into a retry and a no-op communicate.
	// A scold would re-call the model with steering, producing 2+ requests.
	if reqs := adapter.Requests(); len(reqs) != 1 {
		t.Fatalf("notification turn made %d model requests, want 1 (no bare-text scold/retry)", len(reqs))
	}
}

func TestJobNotificationTurnDeliversPendingDurableRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	sess.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, sess.ID())

	sess.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "stale", Status: "failed", OutputBytes: 999})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("job notification turn made no model request")
	}
	if !requestsContain(reqs, "<job-notification", `job_id="job_X"`, `job_type="shell"`, `status="completed"`, `output_bytes="42"`) {
		t.Fatalf("model request did not contain durable job notification payload")
	}
	if requestsContain(reqs, `job_type="stale"`, `output_bytes="999"`) {
		t.Fatalf("model request used stale queued job notification payload")
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load jobs: %v", err)
	}
	if got := recs["job_X"].NotifyState; got != jobstore.NotifyDelivered {
		t.Fatalf("notify state = %q, want delivered", got)
	}
}

func TestJobNotificationTurnDeliversWatchNotification(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.enqueueJobNotification(watchNotification("job_watch_target", "output_match: PHASE4_TASK10_READY"))

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if !requestsContain(adapter.Requests(),
		"<job-notification",
		`job_id="job_watch_target"`,
		`event="watch"`,
		`job_type="watch"`,
		`reason="output_match: PHASE4_TASK10_READY"`,
	) {
		t.Fatalf("model request did not contain watch notification")
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("peekNotifications = %d, want 0 after watch notification delivery", got)
	}
}

func TestWatchNotificationHistoryDoesNotSuppressDurableTerminalNotification(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	sess.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, sess.ID())
	sess.history = append(sess.history, schema.NewTurn(
		schema.TurnSteering,
		llm.User(formatJobNotificationBlock(watchNotification("job_X", "output_match: ready"), notificationExcerpt{})),
	))
	sess.enqueueJobNotification(jobNotification{JobID: "job_X"})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if !requestsContain(adapter.Requests(),
		"<job-notification",
		`job_id="job_X"`,
		`event="completed"`,
		`job_type="shell"`,
		`status="completed"`,
	) {
		t.Fatalf("model request did not contain durable terminal notification after prior watch notification")
	}
}

func TestJobNotificationTurnRequeuesWhenDeliveredMarkFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	sess.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, sess.ID())
	sess.enqueueJobNotification(jobNotification{JobID: "job_X"})

	appendErr := errors.New("append delivered failed")
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationDelivered {
			return appendErr
		}
		return origAppend(e)
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications = %d, want 1 after delivered mark failure", got)
	}
	if !requestsContain(adapter.Requests(), "<job-notification", `job_id="job_X"`, `status="completed"`) {
		t.Fatalf("model request did not contain durable job notification")
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load jobs: %v", err)
	}
	if got := recs["job_X"].NotifyState; got != jobstore.NotifyPending {
		t.Fatalf("notify state = %q, want pending after delivered mark failure", got)
	}

	jm.appendEvent = origAppend
	requestsAfterFailure := len(adapter.Requests())
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) retry: %v", err)
	}
	if got := len(adapter.Requests()); got != requestsAfterFailure {
		t.Fatalf("model requests after retry = %d, want %d; already-injected notification must be suppressed", got, requestsAfterFailure)
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("peekNotifications = %d, want 0 after delivered mark retry", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after delivered mark retry = %q, want %q", got, SessionIdle)
	}
	recs, err = jm.store.Load()
	if err != nil {
		t.Fatalf("reload jobs: %v", err)
	}
	if got := recs["job_X"].NotifyState; got != jobstore.NotifyDelivered {
		t.Fatalf("notify state after retry = %q, want delivered", got)
	}
}

func TestJobNotificationTurnRequeuesWhenJobManagerMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("should not run") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.jobManager = nil

	sess.enqueueJobNotification(jobNotification{JobID: "job_X"})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications = %d, want 1 after missing job manager", got)
	}
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("model requests = %d, want 0 when job notification cannot be inspected", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after missing job manager = %q, want %q", got, SessionIdle)
	}
}

func TestJobNotificationTurnRequeuesWhenStoreLoadFailsThenDelivers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	appendPendingJobNotificationRecord(t, jm, sess.ID())
	sess.jobManager = jm
	wake := make(chan struct{}, 1)
	sess.SetNotifyFunc(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	sess.enqueueJobNotification(jobNotification{JobID: "job_X"})
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) with closed store: %v", err)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications = %d, want 1 after store load failure", got)
	}
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("model requests = %d, want 0 when job notification cannot be inspected", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after store load failure = %q, want %q", got, SessionIdle)
	}
	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("requeued job notification did not schedule a retry wake")
	}

	reopened, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	sess.jobManager = reopened

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) after reopen: %v", err)
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("peekNotifications = %d, want 0 after successful delivery", got)
	}
	if !requestsContain(adapter.Requests(), "<job-notification", `job_id="job_X"`, `status="completed"`) {
		t.Fatalf("model request did not contain durable job notification after retry")
	}

	recs, err := reopened.store.Load()
	if err != nil {
		t.Fatalf("load jobs: %v", err)
	}
	if got := recs["job_X"].NotifyState; got != jobstore.NotifyDelivered {
		t.Fatalf("notify state = %q, want delivered", got)
	}
}

func TestJobNotificationRetryResetInvalidatesActiveTimer(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	sess.pendingJobNotifsMu.Lock()
	sess.jobNotifyRetry.delay = 10 * time.Millisecond
	sess.pendingJobNotifsMu.Unlock()

	sess.requeueJobNotifications([]jobNotification{{JobID: "job_X"}})
	_ = sess.drainJobNotifications()
	sess.resetJobNotificationRetry()
	time.Sleep(50 * time.Millisecond)

	sess.pendingJobNotifsMu.Lock()
	delay := sess.jobNotifyRetry.delay
	active := sess.jobNotifyRetry.active
	sess.pendingJobNotifsMu.Unlock()
	if active {
		t.Fatal("retry timer still active after reset")
	}
	if delay != jobNotificationRetryInitialDelay {
		t.Fatalf("retry delay = %s, want reset %s", delay, jobNotificationRetryInitialDelay)
	}
}

func TestJobNotificationRetryResetDoesNotCancelPendingRetry(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	sess.pendingJobNotifsMu.Lock()
	sess.jobNotifyRetry.delay = 10 * time.Millisecond
	sess.pendingJobNotifsMu.Unlock()

	sess.requeueJobNotifications([]jobNotification{{JobID: "job_X"}})
	sess.resetJobNotificationRetry()
	time.Sleep(50 * time.Millisecond)

	sess.pendingJobNotifsMu.Lock()
	delay := sess.jobNotifyRetry.delay
	active := sess.jobNotifyRetry.active
	sess.pendingJobNotifsMu.Unlock()
	if active {
		t.Fatal("retry timer still active after firing")
	}
	if delay != 20*time.Millisecond {
		t.Fatalf("retry delay = %s, want pending retry to advance to 20ms", delay)
	}
}

// writeFinishedJobWithOutput writes a durable finished job whose output file
// holds content, then enqueues a bare terminal notification for it. The finished
// event's OutputBytes matches the file so the render path's validated re-read
// succeeds. Returns the output path.
func writeFinishedJobWithOutput(t *testing.T, jm *jobManager, jobID string, jobType jobstore.JobType, content string) string {
	t.Helper()
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	out, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	if _, err := out.Append([]byte(content)); err != nil {
		t.Fatalf("append output: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	started := time.Unix(1000, 0).UTC()
	ended := time.Unix(1001, 0).UTC()
	gen := "GEN_" + jobID
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            jobID,
		Type:             jobType,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		StartedAt:        &started,
		OutputPath:       outputPath,
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          ended,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &ended,
		OutputBytes: int64(len(content)),
		TerminalGen: gen,
	}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          ended,
		JobID:       jobID,
		TerminalGen: gen,
	}); err != nil {
		t.Fatalf("append pending: %v", err)
	}
	// Seed the delivery queue with a bare entry; the render path re-reads the
	// authoritative payload and excerpt from the durable record.
	if jm.enqueue != nil {
		jm.enqueue(jobNotification{JobID: jobID})
	}
	return outputPath
}

func newNotificationExcerptSession(t *testing.T) (*Session, *fakeAdapter) {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ack") },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	jm, err := newJobManager(dir, sess.ID(), sess.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	sess.jobManager = jm
	return sess, adapter
}

func deliveredNotificationText(t *testing.T, adapter *fakeAdapter) string {
	t.Helper()
	for _, r := range adapter.Requests() {
		for _, m := range r.Messages {
			if text := m.Text(); strings.Contains(text, "<job-notification") {
				return text
			}
		}
	}
	t.Fatalf("no job-notification block reached the model")
	return ""
}

func TestTerminalNotificationShellExcerptIsTail(t *testing.T) {
	t.Parallel()
	sess, adapter := newNotificationExcerptSession(t)
	head := "HEAD_MARKER_" + strings.Repeat("h", 600)
	tail := strings.Repeat("t", 600) + "_TAIL_MARKER"
	writeFinishedJobWithOutput(t, sess.jobManager, "job_X", jobstore.JobShell, head+tail)

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if !strings.Contains(text, "excerpt:") {
		t.Fatalf("shell terminal block missing excerpt section:\n%s", text)
	}
	if !strings.Contains(text, "_TAIL_MARKER") {
		t.Fatalf("shell excerpt must contain the tail of the output:\n%s", text)
	}
	if strings.Contains(text, "HEAD_MARKER_") {
		t.Fatalf("shell excerpt must be the tail, not the head; head marker present:\n%s", text)
	}
	if !strings.Contains(text, "[excerpt truncated]") {
		t.Fatalf("shell excerpt over budget must carry truncation marker:\n%s", text)
	}
}

func TestTerminalNotificationDelegateExcerptIsHead(t *testing.T) {
	t.Parallel()
	sess, adapter := newNotificationExcerptSession(t)
	head := "HEAD_MARKER_" + strings.Repeat("h", 600)
	tail := strings.Repeat("t", 600) + "_TAIL_MARKER"
	writeFinishedJobWithOutput(t, sess.jobManager, "job_D", jobstore.JobDelegate, head+tail)

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if !strings.Contains(text, "excerpt:") {
		t.Fatalf("delegate terminal block missing excerpt section:\n%s", text)
	}
	if !strings.Contains(text, "HEAD_MARKER_") {
		t.Fatalf("delegate excerpt must contain the head of the report:\n%s", text)
	}
	if strings.Contains(text, "_TAIL_MARKER") {
		t.Fatalf("delegate excerpt must be the head, not the tail; tail marker present:\n%s", text)
	}
	if !strings.Contains(text, "[excerpt truncated]") {
		t.Fatalf("delegate excerpt over budget must carry truncation marker:\n%s", text)
	}
}

func TestTerminalNotificationShortOutputHasNoTruncationMarker(t *testing.T) {
	t.Parallel()
	sess, adapter := newNotificationExcerptSession(t)
	writeFinishedJobWithOutput(t, sess.jobManager, "job_S", jobstore.JobShell, "all done\n")

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if !strings.Contains(text, "excerpt:") {
		t.Fatalf("short-output terminal block should still carry the full excerpt:\n%s", text)
	}
	if !strings.Contains(text, "all done") {
		t.Fatalf("short output must appear in full:\n%s", text)
	}
	if strings.Contains(text, "[excerpt truncated]") {
		t.Fatalf("short output must not carry a truncation marker:\n%s", text)
	}
}

// A complete excerpt (the job's entire output fit the budget) makes a
// job_read_output call redundant — the body must say the output is complete
// instead of instructing a read (live finding 2026-06-12: the template nudged
// a wasted tool call while carrying the full result).
func TestTerminalNotificationCompleteExcerptOmitsReadInstruction(t *testing.T) {
	t.Parallel()
	sess, adapter := newNotificationExcerptSession(t)
	writeFinishedJobWithOutput(t, sess.jobManager, "job_C", jobstore.JobShell, "all done\n")

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if strings.Contains(text, "Use job_read_output") {
		t.Fatalf("complete excerpt must not instruct a redundant read:\n%s", text)
	}
	if !strings.Contains(text, "Complete output below.") {
		t.Fatalf("complete excerpt must say the output is complete:\n%s", text)
	}
	if !strings.Contains(text, "all done") {
		t.Fatalf("complete excerpt must carry the output:\n%s", text)
	}
}

// A truncated excerpt still has more to read, so the notification advertises
// the read affordance without making it the next required action.
func TestTerminalNotificationTruncatedExcerptAdvertisesReadAffordance(t *testing.T) {
	t.Parallel()
	sess, adapter := newNotificationExcerptSession(t)
	writeFinishedJobWithOutput(t, sess.jobManager, "job_T", jobstore.JobShell,
		strings.Repeat("x", 600)+"_TAIL_MARKER")

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if !strings.Contains(text, "Output is available through job_read_output if needed.") {
		t.Fatalf("truncated excerpt must advertise the read affordance:\n%s", text)
	}
	if !strings.Contains(text, "[excerpt truncated]") {
		t.Fatalf("truncated excerpt must carry the truncation marker:\n%s", text)
	}
}

func TestTerminalNotificationEmptyOutputHasNoExcerptSection(t *testing.T) {
	t.Parallel()
	sess, adapter := newNotificationExcerptSession(t)
	writeFinishedJobWithOutput(t, sess.jobManager, "job_E", jobstore.JobShell, "")

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if !strings.Contains(text, `job_id="job_E"`) {
		t.Fatalf("expected the terminal notification for job_E:\n%s", text)
	}
	if strings.Contains(text, "excerpt:") {
		t.Fatalf("empty output must not produce an excerpt section:\n%s", text)
	}
}

func TestTerminalNotificationExcerptReReadsAtRenderFromDurableRecord(t *testing.T) {
	t.Parallel()
	// The enqueued notification carries no output; the excerpt must be fetched
	// from the durable record/output at render time (the durable-replay path).
	sess, adapter := newNotificationExcerptSession(t)
	writeFinishedJobWithOutput(t, sess.jobManager, "job_R", jobstore.JobShell, "RENDER_TIME_EXCERPT\n")

	// Confirm the enqueued struct holds no excerpt bytes (render re-reads).
	if !strings.Contains(formatJobNotificationBlock(jobNotification{JobID: "job_R", JobType: "shell", Status: "completed"}, notificationExcerpt{}), `job_id="job_R"`) {
		t.Fatal("baseline block render failed")
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	text := deliveredNotificationText(t, adapter)
	if !strings.Contains(text, "RENDER_TIME_EXCERPT") {
		t.Fatalf("excerpt must be re-read from the durable record at render time:\n%s", text)
	}
}

func TestTerminalNotificationWatchBranchesUnaffectedByExcerpt(t *testing.T) {
	t.Parallel()
	// Watch-send token block: no excerpt.
	sendBlock := formatJobNotificationBlock(func() jobNotification {
		n := watchSendTokenNotification("", jobstore.WatchSendState{
			Key:           jobstore.WatchSendKey{ResolvedWatchedIdentity: "job_w", ResolvedSendTo: "caller"},
			DeliveryID:    "dlv_1",
			TriggerReason: "event: ASSISTANT_TEXT_END",
		})
		n.watchSendFrame = "frame text"
		return n
	}(), notificationExcerpt{text: "should-not-appear", complete: true})
	if strings.Contains(sendBlock, "excerpt:") || strings.Contains(sendBlock, "should-not-appear") {
		t.Fatalf("watch-send token block must not carry an excerpt:\n%s", sendBlock)
	}

	// No-job watch event block: no excerpt.
	eventBlock := formatJobNotificationBlock(watchNotification("", "output_match: ready"), notificationExcerpt{text: "should-not-appear", complete: true})
	if strings.Contains(eventBlock, "excerpt:") || strings.Contains(eventBlock, "should-not-appear") {
		t.Fatalf("no-job watch event block must not carry an excerpt:\n%s", eventBlock)
	}
}

func TestJobNotificationFromRecordUsesNotificationProvenance(t *testing.T) {
	t.Parallel()
	jobProv := provenance.WithWatch(nil, "watch_job", "wg_1", "wd_job", "session_1", "caller")
	notificationProv := provenance.WithWatch(nil, "watch_note", "wg_1", "wd_note", "session_1", "caller")
	n := jobNotificationFromRecord(&jobstore.JobRecord{
		JobID:                  "job_A",
		Type:                   jobstore.JobDelegate,
		Status:                 jobstore.StatusCompleted,
		Provenance:             jobProv,
		NotificationProvenance: notificationProv,
	})
	if !provenance.ContainsWatch(n.Provenance, "watch_note", "wg_1") {
		t.Fatalf("notification provenance = %+v, want notification provenance", n.Provenance)
	}
	if provenance.ContainsWatch(n.Provenance, "watch_job", "wg_1") {
		t.Fatalf("notification provenance = %+v, should prefer notification provenance over job provenance", n.Provenance)
	}
}

func TestJobNotificationFromRecordFallsBackToJobProvenance(t *testing.T) {
	t.Parallel()
	jobProv := provenance.WithWatch(nil, "watch_job", "wg_1", "wd_job", "session_1", "caller")
	n := jobNotificationFromRecord(&jobstore.JobRecord{
		JobID:      "job_A",
		Type:       jobstore.JobDelegate,
		Status:     jobstore.StatusCompleted,
		Provenance: jobProv,
	})
	if !provenance.ContainsWatch(n.Provenance, "watch_job", "wg_1") {
		t.Fatalf("notification provenance = %+v, want job provenance fallback", n.Provenance)
	}
}

func TestFormatWatchSendNotificationBlock(t *testing.T) {
	t.Parallel()
	n := watchSendTokenNotification("", jobstore.WatchSendState{
		Key:           jobstore.WatchSendKey{ResolvedWatchedIdentity: "job_w", ResolvedSendTo: "caller"},
		DeliveryID:    "dlv_1",
		TriggerReason: "event: ASSISTANT_TEXT_END",
	})
	n.watchSendFrame = "frame text" // populated at render time
	got := formatJobNotificationBlock(n, notificationExcerpt{})
	for _, want := range []string{"watch_send", "job_w", "dlv_1", "frame text"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
