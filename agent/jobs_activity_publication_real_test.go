package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestRealLifecycleEntryPointsPublishBeforeStableEven(t *testing.T) {
	jm := newTestJM(t)
	jm.parentJobID = "parent"
	steps := make(chan string, 32)
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps <- "odd"
		return &activityPublication{RootID: "root", Revision: 1}, nil
	}
	jm.commitActivityPublication = func(*activityPublication) error {
		steps <- "even"
		if len(jm.running) != 0 {
			// A newly-created root must not be exposed in the live map yet.
			t.Fatalf("running map exposed before stable even")
		}
		return nil
	}
	jm.abortActivityPublication = func(*activityPublication) { steps <- "abort" }
	appendEvent := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error { steps <- "append"; return appendEvent(e) }
	jm.forward = func(e jobstore.Event) error { steps <- "copy"; return nil }

	if _, err := jm.createShell(createShellOpts{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"odd", "append", "copy", "even"}
	for _, expected := range want {
		if got := <-steps; got != expected {
			t.Fatalf("start step = %q, want %q", got, expected)
		}
	}
}

func TestRealForwardedRecoveryUsesOnePublication(t *testing.T) {
	jm := newTestJM(t)
	jm.parentJobID = "parent"
	jm.appendEvent = jm.store.Append
	steps := make(chan string, 8)
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps <- "odd"
		return &activityPublication{RootID: "root", Revision: 3}, nil
	}
	jm.commitActivityPublication = func(*activityPublication) error { steps <- "even"; return nil }
	jm.abortActivityPublication = func(*activityPublication) { steps <- "abort" }
	jm.forward = func(e jobstore.Event) error { steps <- string(e.Kind); return nil }
	started := time.Unix(1, 0)
	if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "child", OwnerSessionID: jm.sessionID, ParentJobID: "parent", StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
	// Recovery requires a terminal record; use the normal store event fold.
	ended := jm.now()
	if err := jm.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, JobID: "child", OwnerSessionID: jm.sessionID, ParentJobID: "parent", Status: jobstore.StatusCompleted, EndedAt: &ended, TerminalGen: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := jm.recoverForwardedTerminalEvents(); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"odd", "job_started", "job_finished", "even"} {
		if got := <-steps; got != expected {
			t.Fatalf("recovery step = %q, want %q", got, expected)
		}
	}
}

func TestRealFinishEntryPublishesLiveOverlayBeforeEven(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	steps := make(chan string, 8)
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps <- "odd"
		return &activityPublication{RootID: "root", Revision: 5}, nil
	}
	jm.commitActivityPublication = func(*activityPublication) error {
		steps <- "even"
		jm.mu.Lock()
		defer jm.mu.Unlock()
		if jm.running[rec.JobID] == nil || jm.running[rec.JobID].rec.Status != jobstore.StatusCompleted {
			t.Fatalf("terminal live overlay was not installed before even")
		}
		return nil
	}
	jm.abortActivityPublication = func(*activityPublication) { steps <- "abort" }
	appendEvent := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error { steps <- "append"; return appendEvent(e) }
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	jm.mu.Unlock()
	if _, err := jm.writeFinishJob(run, jobstore.StatusCompleted, "done", nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"odd", "append", "even"} {
		if got := <-steps; got != expected {
			t.Fatalf("finish step = %q, want %q", got, expected)
		}
	}
}

func TestRealDelayedShellEntryPublishesBeforeEven(t *testing.T) {
	jm := newTestJM(t)
	steps := make(chan string, 8)
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps <- "odd"
		return &activityPublication{RootID: "root", Revision: 7}, nil
	}
	jm.commitActivityPublication = func(*activityPublication) error { steps <- "even"; return nil }
	appendEvent := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error { steps <- "append"; return appendEvent(e) }
	jm.forward = func(jobstore.Event) error { steps <- "copy"; return nil }
	jm.parentJobID = "parent"
	res := runShell(context.Background(), jm, s1cov_instantExitExecutor{}, shellArgs{Command: "true"})
	if res.settle == nil {
		t.Fatal("runShell did not return settle callback")
	}
	res.settle(true)
	for _, expected := range []string{"odd", "append", "copy", "even"} {
		if got := <-steps; got != expected {
			t.Fatalf("delayed start step = %q, want %q", got, expected)
		}
	}
}

func TestRealForwardFailureSyntheticFinishStaysInTransaction(t *testing.T) {
	jm := newTestJM(t)
	jm.parentJobID = "parent"
	jm.finalizeShellAsync = func(string, jobstore.Status, string, *int) {}
	steps := make(chan string, 8)
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps <- "odd"
		return &activityPublication{RootID: "root", Revision: 9}, nil
	}
	jm.commitActivityPublication = func(*activityPublication) error { steps <- "even"; return nil }
	appendEvent := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			steps <- "synthetic-finish"
		} else {
			steps <- "append"
		}
		return appendEvent(e)
	}
	jm.forward = func(jobstore.Event) error { steps <- "copy-failure"; return errors.New("copy failed") }
	if _, err := jm.createShell(createShellOpts{Command: "true"}); err == nil {
		t.Fatal("createShell succeeded despite forward failure")
	}
	for _, expected := range []string{"odd", "append", "copy-failure", "synthetic-finish", "even"} {
		if got := <-steps; got != expected {
			t.Fatalf("forward-failure step = %q, want %q", got, expected)
		}
	}
}

func TestRealLifecycleTransactionsSerializeRootAndDescendant(t *testing.T) {
	clock := newJobActivityClock("root")
	makeManager := func(name string) *jobManager {
		jm := &jobManager{}
		jm.beginActivityPublication = func() (*activityPublication, error) {
			if !clock.tryBeginPublication() {
				return nil, errors.New("publication unavailable")
			}
			_, revision, _ := clock.nextRevision()
			return &activityPublication{RootID: "root", Revision: revision}, nil
		}
		jm.commitActivityPublication = func(*activityPublication) error { clock.endPublication(); return nil }
		jm.abortActivityPublication = func(*activityPublication) { clock.endPublication() }
		_ = name
		return jm
	}
	root, child := makeManager("root"), makeManager("child")
	entered := make(chan struct{})
	release := make(chan struct{})
	rootDone := make(chan error, 1)
	go func() {
		rootDone <- root.publishLifecycleEvent(&jobstore.Event{Kind: jobstore.EventJobStarted}, func() error {
			close(entered)
			<-release
			return nil
		}, nil, nil)
	}()
	<-entered
	childAttempt := make(chan struct{})
	childBegin := child.beginActivityPublication
	child.beginActivityPublication = func() (*activityPublication, error) {
		close(childAttempt)
		return childBegin()
	}
	childDone := make(chan error, 1)
	go func() {
		childDone <- child.publishLifecycleEvent(&jobstore.Event{Kind: jobstore.EventJobStarted}, nil, nil, nil)
	}()
	<-childAttempt
	select {
	case err := <-childDone:
		if err == nil {
			t.Fatal("descendant published while root transaction was active")
		}
	default:
	}
	close(release)
	if err := <-rootDone; err != nil {
		t.Fatal(err)
	}
	if err := <-childDone; err != nil {
		t.Fatal(err)
	}
}
