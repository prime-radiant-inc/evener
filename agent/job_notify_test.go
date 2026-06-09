package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func TestFormatJobNotification(t *testing.T) {
	code := 0
	block := formatJobNotificationBlock(jobNotification{
		JobID: "job_X", JobType: "shell", Status: "completed", Reason: "exit_zero",
		OutputBytes: 42, ExitCode: &code,
	})
	for _, want := range []string{`job_id="job_X"`, `event="completed"`, `job_type="shell"`, `status="completed"`, `reason="exit_zero"`, "job_read_output"} {
		if !strings.Contains(block, want) {
			t.Errorf("notification missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "subagent-notification") {
		t.Errorf("must use <job-notification>, not subagent")
	}

	emptyReason := formatJobNotificationBlock(jobNotification{
		JobID: "job_Y", JobType: "shell", Status: "completed",
	})
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

func TestJobNotificationTurnDeliversPendingDurableRecord(t *testing.T) {
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

func TestJobNotificationTurnRequeuesWhenDeliveredMarkFails(t *testing.T) {
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
	sess := newTestSession(t)
	sess.pendingNotifsMu.Lock()
	sess.jobNotifyRetry.delay = 10 * time.Millisecond
	sess.pendingNotifsMu.Unlock()

	sess.requeueJobNotifications([]jobNotification{{JobID: "job_X"}})
	_ = sess.drainJobNotifications()
	sess.resetJobNotificationRetry()
	time.Sleep(50 * time.Millisecond)

	sess.pendingNotifsMu.Lock()
	delay := sess.jobNotifyRetry.delay
	active := sess.jobNotifyRetry.active
	sess.pendingNotifsMu.Unlock()
	if active {
		t.Fatal("retry timer still active after reset")
	}
	if delay != jobNotificationRetryInitialDelay {
		t.Fatalf("retry delay = %s, want reset %s", delay, jobNotificationRetryInitialDelay)
	}
}

func TestJobNotificationRetryResetDoesNotCancelPendingRetry(t *testing.T) {
	sess := newTestSession(t)
	sess.pendingNotifsMu.Lock()
	sess.jobNotifyRetry.delay = 10 * time.Millisecond
	sess.pendingNotifsMu.Unlock()

	sess.requeueJobNotifications([]jobNotification{{JobID: "job_X"}})
	sess.resetJobNotificationRetry()
	time.Sleep(50 * time.Millisecond)

	sess.pendingNotifsMu.Lock()
	delay := sess.jobNotifyRetry.delay
	active := sess.jobNotifyRetry.active
	sess.pendingNotifsMu.Unlock()
	if active {
		t.Fatal("retry timer still active after firing")
	}
	if delay != 20*time.Millisecond {
		t.Fatalf("retry delay = %s, want pending retry to advance to 20ms", delay)
	}
}
