package agent

import (
	"errors"
	"os"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestReconcileOnRestoreFinalizesLostJob(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/sessions/S1/jobs", 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(dir + "/sessions/S1/jobs.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1, 0).UTC()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_lost", Type: jobstore.JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &start}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/sessions/S1/jobs/job_lost.log", []byte("lost output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var queued []jobNotification
	jm, err := newJobManager(dir, "S1", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }

	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_lost"].Status != jobstore.StatusStopped || recs["job_lost"].Reason != "runtime_lost" {
		t.Fatalf("job_lost = %+v, want stopped/runtime_lost", recs["job_lost"])
	}
	if recs["job_lost"].OutputBytes != int64(len("lost output\n")) {
		t.Fatalf("job_lost output bytes = %d, want %d", recs["job_lost"].OutputBytes, len("lost output\n"))
	}
	if len(queued) != 1 || queued[0].JobID != "job_lost" || queued[0].OutputBytes != int64(len("lost output\n")) {
		t.Fatalf("expected one queued runtime_lost notification, got %+v", queued)
	}
}

func TestReconcileOnRestoreSkipsForwardedChildOwnedJob(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/sessions/PARENT/jobs", 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(dir + "/sessions/PARENT/jobs.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1, 0).UTC()
	if err := st.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		JobID:            "job_child_owned",
		Type:             jobstore.JobShell,
		OwnerSessionID:   "CHILD",
		VisibleToSession: "PARENT",
		ParentJobID:      "job_delegate",
		StartedAt:        &start,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var queued []jobNotification
	jm, err := newJobManager(dir, "PARENT", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }

	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs["job_child_owned"].Status != jobstore.StatusRunning {
		t.Fatalf("job_child_owned = %+v, want still running for child owner recovery", recs["job_child_owned"])
	}
	if len(queued) != 0 {
		t.Fatalf("queued notifications = %+v, want none for forwarded child-owned job", queued)
	}
}

func TestReconcileOnRestoreUsesPrunedOutputLifetimeBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/sessions/S1/jobs", 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := jobstore.Open(dir + "/sessions/S1/jobs.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1, 0).UTC()
	if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_lost", Type: jobstore.JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &start}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := jobstore.OpenOutput(dir+"/sessions/S1/jobs/job_lost.log", int64(len("keep\n")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Append([]byte("drop\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Append([]byte("keep\n")); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	var queued []jobNotification
	jm, err := newJobManager(dir, "S1", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }

	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(len("drop\nkeep\n"))
	if recs["job_lost"].OutputBytes != wantBytes {
		t.Fatalf("job_lost output bytes = %d, want lifetime %d", recs["job_lost"].OutputBytes, wantBytes)
	}
	if len(queued) != 1 || queued[0].OutputBytes != wantBytes {
		t.Fatalf("queued notifications = %+v, want lifetime output bytes %d", queued, wantBytes)
	}
}

func TestReconcileOnRestoreRequeuesTerminalNotifications(t *testing.T) {
	for _, tc := range []struct {
		name           string
		alreadyPending bool
	}{
		{name: "not_armed"},
		{name: "pending", alreadyPending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sessionID := "S1"
			if err := os.MkdirAll(dir+"/sessions/"+sessionID+"/jobs", 0o755); err != nil {
				t.Fatal(err)
			}
			st, err := jobstore.Open(dir + "/sessions/" + sessionID + "/jobs.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Unix(1, 0).UTC()
			end := time.Unix(2, 0).UTC()
			if err := st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_done", Type: jobstore.JobShell, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &start}); err != nil {
				t.Fatal(err)
			}
			if err := st.Append(jobstore.Event{Kind: jobstore.EventJobFinished, JobID: "job_done", Status: jobstore.StatusCompleted, Reason: "exit_zero", EndedAt: &end, OutputBytes: 12, TerminalGen: "GEN1"}); err != nil {
				t.Fatal(err)
			}
			if tc.alreadyPending {
				if err := st.Append(jobstore.Event{Kind: jobstore.EventJobNotificationPending, JobID: "job_done", TerminalGen: "GEN1"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai"})
			meta := schema.SessionMeta{
				ID:        sessionID,
				ProfileID: "openai",
				Model:     "gpt-5.2",
				Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
				CreatedAt: start,
			}
			sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: dir})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer sess.Close()

			if got := sess.peekNotifications(); got != 1 {
				t.Fatalf("peekNotifications = %d, want 1", got)
			}
			var wakes int
			sess.SetNotifyFunc(func() { wakes++ })
			if wakes != 1 {
				t.Fatalf("late notify wakes = %d, want 1", wakes)
			}
			recs, err := sess.jobManager.store.Load()
			if err != nil {
				t.Fatalf("load jobs: %v", err)
			}
			if got := recs["job_done"].NotifyState; got != jobstore.NotifyPending {
				t.Fatalf("notify state = %q, want pending", got)
			}
		})
	}
}

func TestJobManagerNotificationCallbackWakesSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: dir, NoProjectPrompts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var wakes int
	sess.SetNotifyFunc(func() { wakes++ })
	sess.jobManager.enqueue(jobNotification{JobID: "job_done"})

	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications = %d, want 1", got)
	}
	if wakes != 1 {
		t.Fatalf("notify wakes = %d, want 1", wakes)
	}
}

func TestJobManagerNotificationCallbackRegistrationRace(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: dir, NoProjectPrompts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			sess.SetNotifyFunc(func() {})
			sess.SetNotifyFunc(nil)
		}
	}()
	for i := 0; i < 100; i++ {
		sess.enqueueJobNotificationAndNotify(jobNotification{JobID: "job_done"})
	}
	<-done
}

func TestSessionCloseClosesJobStore(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: dir, NoProjectPrompts: true})
	if err != nil {
		t.Fatal(err)
	}
	store := sess.jobManager.store

	sess.Close()

	if _, err := store.Load(); !errors.Is(err, jobstore.ErrStoreClosed) {
		t.Fatalf("store.Load after Close err = %v, want ErrStoreClosed", err)
	}
}
