package agent

import (
	"context"
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
	for _, want := range []string{`job_id="job_X"`, `event="completed"`, `job_type="shell"`, `status="completed"`, "job_read_output"} {
		if !strings.Contains(block, want) {
			t.Errorf("notification missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "subagent-notification") {
		t.Errorf("must use <job-notification>, not subagent")
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
	started := time.Unix(1000, 0).UTC()
	ended := time.Unix(1001, 0).UTC()
	code := 0
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_X",
		Type:             jobstore.JobShell,
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
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
