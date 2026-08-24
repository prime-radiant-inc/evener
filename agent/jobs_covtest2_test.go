package agent

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/identifier"
)

// TestCovFinalizeWithRunNoNotification covers finalizeWithRunNoNotification
// (jobs.go lines 1622-1624): the no-notification finalize path used by
// complete-or-handle kept shells.
func TestCovFinalizeWithRunNoNotification(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	jobID := rec.JobID
	code := 0
	prepare := func(run *runningJob) (jobstore.Status, string, *int, error) {
		return jobstore.StatusCompleted, "exit_zero", &code, nil
	}
	if err := jm.finalizeWithRunNoNotification(jobID, prepare); err != nil {
		t.Fatalf("finalizeWithRunNoNotification: %v", err)
	}
	// The job should be finalized and removed from running.
	jm.mu.Lock()
	_, stillRunning := jm.running[jobID]
	jm.mu.Unlock()
	if stillRunning {
		t.Fatal("job should be removed from running after no-notification finalize")
	}
}

// TestCovFinalizeWithRunNoNotification_AlreadyFinalized covers the case where
// finalizeWithRunNoNotification is called on a job that already has a terminal.
func TestCovFinalizeWithRunNoNotification_AlreadyFinalized(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	jobID := rec.JobID
	code := 0
	// First finalize — establishes terminal.
	if err := jm.finalize(jobID, jobstore.StatusCompleted, "done", &code); err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	// Second finalize via no-notification path — should not error (already finalized).
	prepare := func(run *runningJob) (jobstore.Status, string, *int, error) {
		return jobstore.StatusCompleted, "done", &code, nil
	}
	// The job is already removed from running, so finalizeWithRunMode returns nil.
	if err := jm.finalizeWithRunNoNotification(jobID, prepare); err != nil {
		t.Fatalf("second finalize: %v", err)
	}
}

// TestCovFinalizeWithRunNoNotification_NilRun covers the nil-run early return.
func TestCovFinalizeWithRunNoNotification_NilRun(t *testing.T) {
	jm := newTestJM(t)
	prepare := func(run *runningJob) (jobstore.Status, string, *int, error) {
		t.Fatal("prepare should not be called for nil run")
		return "", "", nil, nil
	}
	if err := jm.finalizeWithRunNoNotification("job_nonexistent", prepare); err != nil {
		t.Fatalf("nil run should return nil, got %v", err)
	}
}

// TestCovStopChildren covers stopChildren (jobs_nested.go lines 406-432).
func TestCovStopChildren(t *testing.T) {
	jm := newTestJM(t)
	s := &Session{jobManager: jm, id: jm.sessionID}

	// No children — empty result.
	stopped, err := s.stopChildren("parent_nonexistent")
	if err != nil {
		t.Fatalf("stopChildren with no children: %v", err)
	}
	if stopped != nil {
		t.Fatalf("no children should yield nil, got %+v", stopped)
	}

	// Create two shell jobs with a parent job ID set.
	jm.setParentJobID("parent_job_1")
	rec1, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell 1: %v", err)
	}
	rec2, err := jm.createShell(createShellOpts{Command: "y"})
	if err != nil {
		t.Fatalf("createShell 2: %v", err)
	}
	jm.setParentJobID("")

	// Verify the records have the parent set.
	if rec1.ParentJobID != "parent_job_1" || rec2.ParentJobID != "parent_job_1" {
		t.Fatalf("parent job IDs: %q %q, want parent_job_1", rec1.ParentJobID, rec2.ParentJobID)
	}

	// Stop children of parent_job_1 — both should be stopped.
	// The jobs are owned by this session (OwnerSessionID == s.id), so
	// stopNestedOrLocal falls through to local.stop, which succeeds.
	stopped, err = s.stopChildren("parent_job_1")
	if err != nil {
		t.Fatalf("stopChildren: %v", err)
	}
	if len(stopped) != 2 {
		t.Fatalf("want 2 stopped, got %d", len(stopped))
	}
}

// TestCovStopChildren_NoJobManager covers stopChildren with no job manager.
func TestCovStopChildren_NoJobManager(t *testing.T) {
	s := &Session{}
	stopped, err := s.stopChildren("parent_1")
	if err == nil {
		t.Fatal("nil job manager should return error")
	}
	if stopped != nil {
		t.Fatal("nil job manager should return nil stopped")
	}
}

// TestCovCreateJobOutputForID covers createJobOutputForID (jobs.go lines 641-662).
func TestCovCreateJobOutputForID(t *testing.T) {
	jm := newTestJM(t)

	// Invalid job ID (wrong owner session).
	_, _, err := jm.createJobOutputForID("job_wrong_owner_000000000000")
	if err == nil {
		t.Fatal("wrong owner should return error")
	}

	// Valid job ID for this session.
	validJobID := "job_" + jm.sessionID + "_000000000000"
	path, output, err := jm.createJobOutputForID(validJobID)
	if err != nil {
		t.Fatalf("createJobOutputForID: %v", err)
	}
	t.Cleanup(func() { _ = output.Close() })
	if !strings.HasSuffix(path, validJobID+".log") {
		t.Fatalf("path = %q, want suffix %q", path, validJobID+".log")
	}

	// Duplicate job ID — should fail with ErrExist.
	_, _, err = jm.createJobOutputForID(validJobID)
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate should return ErrExist, got %v", err)
	}
}

// TestCovCreateJobOutputForID_BadID covers createJobOutputForID with a malformed ID.
func TestCovCreateJobOutputForID_BadID(t *testing.T) {
	jm := newTestJM(t)
	// A job ID that cannot be parsed for owner session extraction.
	_, _, err := jm.createJobOutputForID("not_a_valid_job_id")
	if err == nil {
		t.Fatal("malformed job ID should return error")
	}
}

// TestCovValidateStructuredResultWithAddResource covers
// validateStructuredResultWithAddResource (jobs.go lines 1842-1863):
// the panic recovery, marshal error, addResource error, compile error, and
// successful validation paths.
func TestCovValidateStructuredResultWithAddResource(t *testing.T) {
	// Successful validation: a simple object schema with a required field.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
		"required": []any{"name"},
	}
	value := map[string]any{"name": "test"}

	// Test with the real validateStructuredResult — valid value.
	err := validateStructuredResult(value, schema)
	if err != nil {
		t.Fatalf("valid value should pass: %v", err)
	}

	// Invalid value (missing required field).
	invalidValue := map[string]any{}
	err = validateStructuredResult(invalidValue, schema)
	if err == nil {
		t.Fatal("missing required field should fail validation")
	}

	// Schema that causes a marshal error (channel cannot be marshaled).
	err = validateStructuredResultWithAddResource(nil, make(chan int), nil)
	if err == nil {
		t.Fatal("channel schema should cause marshal error")
	}

	// addResource error — inject a failing addResource.
	err = validateStructuredResultWithAddResource(
		nil,
		map[string]any{"type": "object"},
		func(c *jsonschema.Compiler, uri string, r io.Reader) error {
			return errors.New("addResource failed")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "addResource failed") {
		t.Fatalf("addResource error should propagate: %v", err)
	}
}

// TestCovValidateStructuredResultWithAddResource_Panic covers the panic recovery
// path in validateStructuredResultWithAddResource.
func TestCovValidateStructuredResultWithAddResource_Panic(t *testing.T) {
	// A schema that will cause the compiler to panic during Compile.
	// Use a circular reference schema to trigger a panic.
	panicAddResource := func(c *jsonschema.Compiler, uri string, r io.Reader) error {
		panic("injected panic")
	}
	err := validateStructuredResultWithAddResource(
		map[string]any{},
		map[string]any{"type": "object"},
		panicAddResource,
	)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("panic should be recovered: %v", err)
	}
}

// TestCovRearmTerminalNotificationDecision covers
// rearmTerminalNotificationDecision (jobs.go lines 2082-2092).
func TestCovRearmTerminalNotificationDecision(t *testing.T) {
	sessionID := "sess_1"

	// Non-terminal status — no rearm.
	rec := &jobstore.JobRecord{Status: jobstore.StatusRunning, OwnerSessionID: sessionID}
	rearm, appendEvent := rearmTerminalNotificationDecision(rec, sessionID)
	if rearm || appendEvent {
		t.Fatal("non-terminal should not rearm")
	}

	// Terminal but no TerminalGen — no rearm.
	rec = &jobstore.JobRecord{Status: jobstore.StatusCompleted, OwnerSessionID: sessionID}
	rearm, appendEvent = rearmTerminalNotificationDecision(rec, sessionID)
	if rearm || appendEvent {
		t.Fatal("terminal without gen should not rearm")
	}

	// Terminal with gen and NotifyNotArmed — rearm + appendEvent.
	rec = &jobstore.JobRecord{
		Status:         jobstore.StatusCompleted,
		OwnerSessionID: sessionID,
		TerminalGen:    "gen_1",
		NotifyState:    jobstore.NotifyNotArmed,
	}
	rearm, appendEvent = rearmTerminalNotificationDecision(rec, sessionID)
	if !rearm {
		t.Fatal("terminal with gen and NotArmed should rearm")
	}
	if !appendEvent {
		t.Fatal("NotifyNotArmed should require appendEvent")
	}

	// Terminal with gen and NotifyPending — rearm but no appendEvent.
	rec.NotifyState = jobstore.NotifyPending
	rearm, appendEvent = rearmTerminalNotificationDecision(rec, sessionID)
	if !rearm {
		t.Fatal("terminal with gen and Pending should rearm")
	}
	if appendEvent {
		t.Fatal("NotifyPending should not require appendEvent")
	}

	// Terminal with gen but already delivered — no rearm.
	rec.NotifyState = jobstore.NotifyDelivered
	rearm, appendEvent = rearmTerminalNotificationDecision(rec, sessionID)
	if rearm || appendEvent {
		t.Fatal("already delivered should not rearm")
	}

	// Different owner session — no rearm.
	rec.OwnerSessionID = "other_session"
	rec.NotifyState = jobstore.NotifyNotArmed
	rearm, appendEvent = rearmTerminalNotificationDecision(rec, sessionID)
	if rearm || appendEvent {
		t.Fatal("different owner should not rearm")
	}

	// Empty owner session — should rearm (uses caller's session).
	rec.OwnerSessionID = ""
	rec.NotifyState = jobstore.NotifyNotArmed
	rearm, _ = rearmTerminalNotificationDecision(rec, sessionID)
	if !rearm {
		t.Fatal("empty owner with terminal should rearm")
	}
}

// TestCovArmPendingTerminalNotifications covers armPendingTerminalNotifications
// (jobs.go lines 2094-2155).
func TestCovArmPendingTerminalNotifications(t *testing.T) {
	jm := newTestJM(t)

	// No terminal jobs — no error.
	if err := jm.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("no terminal jobs: %v", err)
	}

	// Create a terminal job with NotifyNotArmed state.
	startedAt := jm.now()
	jobID := "job_" + jm.sessionID + "_000000000000"
	if err := jm.appendEvent(jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             startedAt,
		JobID:          jobID,
		Type:           jobstore.JobShell,
		OwnerSessionID: jm.sessionID,
		StartedAt:      &startedAt,
	}); err != nil {
		t.Fatalf("appendEvent started: %v", err)
	}
	endedAt := jm.now()
	exitCode := 0
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "done",
		ExitCode:    &exitCode,
		EndedAt:     &endedAt,
		TerminalGen: "gen_1",
	}); err != nil {
		t.Fatalf("appendEvent finished: %v", err)
	}
	// Add notification pending event.
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          endedAt,
		JobID:       jobID,
		TerminalGen: "gen_1",
	}); err != nil {
		t.Fatalf("appendEvent pending: %v", err)
	}

	// armPendingTerminalNotifications should process the terminal job.
	// Since jm.enqueue is nil (from newTestJM), it just processes silently.
	if err := jm.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("armPendingTerminalNotifications: %v", err)
	}
}

// TestCovArmPendingTerminalNotifications_WithEnqueue covers the enqueue path.
func TestCovArmPendingTerminalNotifications_WithEnqueue(t *testing.T) {
	jm := newTestJM(t)
	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }

	// No terminal jobs — no notifications.
	if err := jm.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("no terminal: %v", err)
	}
	if len(notifications) != 0 {
		t.Fatalf("expected 0 notifications, got %d", len(notifications))
	}
}

// TestCovValidatedOutputStatsForRecord covers validatedOutputStatsForRecord
// (jobs.go lines 2175-2184): the mismatch path and the nil-record path.
func TestCovValidatedOutputStatsForRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Nil record — should return stats without mismatch check.
	total, retainedStart, err := validatedOutputStatsForRecord(path, nil)
	if err != nil {
		t.Fatalf("nil rec: %v", err)
	}
	if total != 12 { // "hello world\n" = 12 bytes
		t.Fatalf("total = %d, want 12", total)
	}
	if retainedStart != 0 {
		t.Fatalf("retainedStart = %d, want 0", retainedStart)
	}

	// Terminal record with matching output bytes — OK.
	rec := &jobstore.JobRecord{Status: jobstore.StatusCompleted, OutputBytes: 12}
	total, _, err = validatedOutputStatsForRecord(path, rec)
	if err != nil {
		t.Fatalf("matching bytes: %v", err)
	}
	if total != 12 {
		t.Fatalf("total = %d, want 12", total)
	}

	// Terminal record with mismatched output bytes — error.
	rec.OutputBytes = 99
	_, _, err = validatedOutputStatsForRecord(path, rec)
	if err == nil {
		t.Fatal("mismatched bytes should return error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error should mention mismatch: %v", err)
	}

	// Non-existent file — error.
	_, _, err = validatedOutputStatsForRecord(filepath.Join(dir, "nonexistent.log"), nil)
	if err == nil {
		t.Fatal("non-existent file should return error")
	}

	// Non-terminal record — no mismatch check (only terminal records are checked).
	rec.Status = jobstore.StatusRunning
	rec.OutputBytes = 99
	total, _, err = validatedOutputStatsForRecord(path, rec)
	if err != nil {
		t.Fatalf("non-terminal rec should not check mismatch: %v", err)
	}
	if total != 12 {
		t.Fatalf("total = %d, want 12", total)
	}
}

// TestCovStringOutputResult covers stringOutputResult (jobs.go lines 2168-2173).
func TestCovStringOutputResult2(t *testing.T) {
	// Error case.
	out, total, truncated, err := stringOutputResult(nil, 100, true, errors.New("read error"))
	if err == nil || out != "" {
		t.Fatalf("error case: out=%q err=%v", out, err)
	}
	if total != 100 || truncated != true {
		t.Fatalf("error case: total=%d truncated=%v", total, truncated)
	}

	// Success case.
	out, total, truncated, err = stringOutputResult([]byte("hello"), 5, false, nil)
	if err != nil || out != "hello" {
		t.Fatalf("success case: out=%q err=%v", out, err)
	}
	if total != 5 || truncated != false {
		t.Fatalf("success case: total=%d truncated=%v", total, truncated)
	}
}

// TestCovCloneJobRecord covers cloneJobRecord (jobs.go lines 2355+).
func TestCovCloneJobRecord2(t *testing.T) {
	original := &jobstore.JobRecord{
		JobID:          "job_test",
		Type:           jobstore.JobShell,
		Status:         jobstore.StatusCompleted,
		Reason:         "done",
		Command:        "echo hi",
		OwnerSessionID: "sess_1",
	}
	cloned := cloneJobRecord(original)
	if cloned == original {
		t.Fatal("clone should not be the same pointer")
	}
	if cloned.JobID != original.JobID || cloned.Type != original.Type || cloned.Status != original.Status {
		t.Fatalf("clone fields mismatch: %+v", cloned)
	}

	// Mutating the clone should not affect the original.
	cloned.Reason = "changed"
	if original.Reason == "changed" {
		t.Fatal("mutating clone should not affect original")
	}

	// Nil record — should return nil.
	if cloneJobRecord(nil) != nil {
		t.Fatal("nil record should clone to nil")
	}
}

// TestCovTailOutputFileWithOpen covers tailOutputFileWithOpen
// (jobs.go lines 2252+): negative tailBytes error and open error.
func TestCovTailOutputFileWithOpen(t *testing.T) {
	// Negative tailBytes.
	_, _, _, err := tailOutputFileWithOpen("/nonexistent", -1, 0, func(string) (jobOutputReadFile, error) {
		t.Fatal("open should not be called for negative tailBytes")
		return nil, nil
	})
	if err == nil {
		t.Fatal("negative tailBytes should return error")
	}

	// Open error.
	_, _, _, err = tailOutputFileWithOpen("/nonexistent", 100, 0, func(path string) (jobOutputReadFile, error) {
		return nil, errors.New("open failed")
	})
	if err == nil || !strings.Contains(err.Error(), "open output") {
		t.Fatalf("open error should wrap: %v", err)
	}

	// Successful read.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, total, truncated, err := tailOutputFileWithOpen(path, 100, int64(len(content)), func(p string) (jobOutputReadFile, error) {
		return os.Open(p)
	})
	if err != nil {
		t.Fatalf("successful read: %v", err)
	}
	if total != int64(len(content)) {
		t.Fatalf("total = %d, want %d", total, len(content))
	}
	if truncated {
		t.Fatal("should not be truncated")
	}
	if !strings.Contains(out, "line3") {
		t.Fatalf("output should contain last line: %q", out)
	}
}

// TestCovJobIDForSession validates the job ID construction.
func TestCovJobsDir_TempDirFallback(t *testing.T) {
	// Verify the temp dir fallback path uses the correct separator.
	got := jobsDir("", "SESS")
	if !strings.HasSuffix(got, filepath.Join("evener-jobs", "SESS")) {
		t.Fatalf("temp dir fallback = %q", got)
	}
}

// TestCovForwardDisabled covers forwardDisabled (jobs.go lines 2070-2074).
func TestCovForwardDisabled(t *testing.T) {
	jm := newTestJM(t)

	// nil run — false.
	if jm.forwardDisabled(nil) {
		t.Fatal("nil run should return false")
	}

	// Run with forwardDisabled=false.
	run := &runningJob{forwardDisabled: false}
	if jm.forwardDisabled(run) {
		t.Fatal("forwardDisabled=false should return false")
	}

	// Run with forwardDisabled=true.
	run.forwardDisabled = true
	if !jm.forwardDisabled(run) {
		t.Fatal("forwardDisabled=true should return true")
	}
}

// TestCovCurrentCausalProvenance covers currentCausalProvenance
// (jobs.go lines 283-288): nil manager, nil source, and non-nil source.
func TestCovCurrentCausalProvenance2(t *testing.T) {
	// nil manager.
	var jm *jobManager
	if jm.currentCausalProvenance() != nil {
		t.Fatal("nil manager should return nil")
	}

	// Manager with nil source.
	jm = &jobManager{}
	if jm.currentCausalProvenance() != nil {
		t.Fatal("nil source should return nil")
	}

	// Manager with non-nil source.
	called := false
	jm = &jobManager{
		currentProvenance: func() *provenance.Causal {
			called = true
			return nil
		},
	}
	jm.currentCausalProvenance()
	if !called {
		t.Fatal("currentProvenance should be called")
	}
}

// TestCovTreeReservationRelease covers treeReservation.release
// (jobs.go lines 322-329): nil receiver and double-release.
func TestCovTreeReservationRelease(t *testing.T) {
	// nil receiver — no panic.
	var r *treeReservation
	r.release()

	// Normal release.
	counter := newTreeCounter(1)
	r2 := &treeReservation{counter: counter, kind: slotKindJob}
	r2.release()
	// The slot should have been released (counter back to available).
	_, jobs, _, _ := counter.occupancy()
	if jobs != 0 {
		t.Fatalf("after release, jobs = %d, want 0", jobs)
	}

	// Double release — should not release twice.
	r2.release()
	_, jobs, _, _ = counter.occupancy()
	if jobs != 0 {
		t.Fatalf("after double release, jobs = %d, want 0", jobs)
	}
}

// TestCovRunningJobCloseDone covers the closeDone variants
// (jobs.go lines 344-364).
func TestCovRunningJobCloseDone(t *testing.T) {
	run := &runningJob{done: make(chan struct{})}
	run.closeDone()
	select {
	case <-run.done:
	default:
		t.Fatal("closeDone should close the done channel")
	}

	// Verify completion type is Unknown for closeDone.
	if runningJobCompletion(run.completion.Load()) != runningJobCompletionUnknown {
		t.Fatal("closeDone should set completion to Unknown")
	}
}

// TestCovRunningJobCloseDoneDurable covers closeDoneDurable.
func TestCovRunningJobCloseDoneDurable(t *testing.T) {
	run := &runningJob{done: make(chan struct{})}
	run.closeDoneDurable()
	if runningJobCompletion(run.completion.Load()) != runningJobCompletionDurable {
		t.Fatal("closeDoneDurable should set completion to Durable")
	}
}

// TestCovRunningJobCloseDoneAbandoned covers closeDoneAbandoned.
func TestCovRunningJobCloseDoneAbandoned(t *testing.T) {
	run := &runningJob{done: make(chan struct{})}
	run.closeDoneAbandoned()
	if runningJobCompletion(run.completion.Load()) != runningJobCompletionAbandoned {
		t.Fatal("closeDoneAbandoned should set completion to Abandoned")
	}
}

// TestCovRunningJobCloseDoneOnce covers that closeDone is idempotent.
func TestCovRunningJobCloseDoneOnce(t *testing.T) {
	run := &runningJob{done: make(chan struct{})}
	run.closeDoneDurable()
	// Second call should not panic.
	run.closeDoneAbandoned()
	// The completion should still be Durable (first call wins).
	if runningJobCompletion(run.completion.Load()) != runningJobCompletionDurable {
		t.Fatal("first closeDoneDurable should win")
	}
}

// TestCovEnqueueNotifications covers enqueueNotifications (jobs.go lines 1872-1885).
func TestCovEnqueueNotifications(t *testing.T) {
	jm := newTestJM(t)

	// nil enqueue — returns false.
	origEnqueue := jm.enqueue
	jm.enqueue = nil
	if jm.enqueueNotifications([]jobNotification{{JobID: "j1"}}) {
		t.Fatal("nil enqueue should return false")
	}
	jm.enqueue = origEnqueue

	// Empty notifs — returns false.
	var notifs []jobNotification
	if jm.enqueueNotifications(notifs) {
		t.Fatal("empty notifs should return false")
	}

	// Non-empty with enqueue — returns true and enqueues each.
	var queued []jobNotification
	jm.enqueue = func(n jobNotification) { queued = append(queued, n) }
	if !jm.enqueueNotifications([]jobNotification{{JobID: "j1"}, {JobID: "j2"}}) {
		t.Fatal("non-empty notifs should return true")
	}
	if len(queued) != 2 {
		t.Fatalf("queued %d, want 2", len(queued))
	}

	// With holdWake.
	holdCalled := false
	jm.holdWake = func() func() {
		holdCalled = true
		return func() {}
	}
	jm.enqueueNotifications([]jobNotification{{JobID: "j3"}})
	if !holdCalled {
		t.Fatal("holdWake should be called")
	}
}

// TestCovAppendStartForwardFailure covers appendStartForwardFailure
// (jobs.go lines 1783-1802).
func TestCovAppendStartForwardFailure(t *testing.T) {
	jm := newTestJM(t)

	// nil output — outputBytes stays 0.
	err := jm.appendStartForwardFailure("job_1", nil, nil)
	if err != nil {
		t.Fatalf("nil output: %v", err)
	}

	// With output store — gets output bytes.
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	output := jm.running[rec.JobID].output
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("some output\n")); err != nil {
		t.Fatalf("appendJobOutput: %v", err)
	}
	err = jm.appendStartForwardFailure(rec.JobID, output, nil)
	if err != nil {
		t.Fatalf("with output: %v", err)
	}

	// Verify the event was appended.
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	failedRec := recs[rec.JobID]
	if failedRec == nil {
		t.Fatal("failed job record should exist")
	}
	if failedRec.Status != jobstore.StatusFailed {
		t.Fatalf("status = %q, want %q", failedRec.Status, jobstore.StatusFailed)
	}
	if failedRec.Reason != "forward_failed" {
		t.Fatalf("reason = %q, want forward_failed", failedRec.Reason)
	}
}

// TestCovBoundedStructuredResult covers boundedStructuredResult
// (jobs.go lines 1804-1834): nil value, capture-failed, schema-requested,
// too-large, and valid paths.
func TestCovBoundedStructuredResult(t *testing.T) {
	// nil value, no schema, no capture failed — returns nil, nil, "".
	v, valid, reason := boundedStructuredResult(nil, nil, false)
	if v != nil || valid != nil || reason != "" {
		t.Fatalf("nil value no schema: v=%v valid=%v reason=%q", v, valid, reason)
	}

	// nil value, capture failed — returns nil, false, capture_failed.
	v, valid, reason = boundedStructuredResult(nil, nil, true)
	if v != nil || valid == nil || *valid != false {
		t.Fatal("capture failed should return valid=false")
	}
	if reason != structuredResultReasonSchemaCaptureFailed {
		t.Fatalf("reason = %q, want %q", reason, structuredResultReasonSchemaCaptureFailed)
	}

	// nil value, schema requested — returns nil, false, result_missing.
	v, valid, reason = boundedStructuredResult(nil, map[string]any{"type": "object"}, false)
	if v != nil || valid == nil || *valid != false {
		t.Fatal("schema requested with nil should return valid=false")
	}
	if reason != structuredResultReasonSchemaResultMissing {
		t.Fatalf("reason = %q, want %q", reason, structuredResultReasonSchemaResultMissing)
	}

	// Valid value, no schema — returns value, true, "".
	v, valid, reason = boundedStructuredResult(map[string]any{"key": "val"}, nil, false)
	if v == nil || valid == nil || *valid != true {
		t.Fatal("valid value should return valid=true")
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}

	// Value too large for JSON — returns nil, false, too_large.
	largeValue := strings.Repeat("x", maxPersistedStructuredResultJSONBytes+1)
	v, valid, reason = boundedStructuredResult(largeValue, nil, false)
	if v != nil || valid == nil || *valid != false {
		t.Fatal("too large should return valid=false")
	}
	if reason != structuredResultReasonSchemaResultTooLarge {
		t.Fatalf("reason = %q, want %q", reason, structuredResultReasonSchemaResultTooLarge)
	}
}

// TestCovIdentifierJobOwnerSessionID is a helper to verify the identifier package
// is used correctly for job IDs in createJobOutputForID.
func TestCovCreateJobOutputForID_ValidID(t *testing.T) {
	jm := newTestJM(t)
	// Construct a valid job ID for this session.
	validJobID, err := identifier.NewJobID(jm.sessionID)
	if err != nil {
		t.Fatalf("identifier.NewJobID: %v", err)
	}
	path, output, err := jm.createJobOutputForID(validJobID)
	if err != nil {
		t.Fatalf("createJobOutputForID: %v", err)
	}
	t.Cleanup(func() { _ = output.Close() })
	if path == "" {
		t.Fatal("path should be non-empty")
	}
	// Verify the file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("output file should exist: %v", err)
	}
}

// TestCovJobsDir covers jobsDir (already partially covered, but add the temp
// dir path structure assertion).
func TestCovJobsDir_StateDir(t *testing.T) {
	got := jobsDir("/state/dir", "SESS")
	expected := "/state/dir/sessions/SESS"
	if got != expected {
		t.Fatalf("jobsDir = %q, want %q", got, expected)
	}
}

// TestCovJobStopReceiptWait covers jobStopReceipt.wait (jobs.go lines 1465-1477):
// nil run and non-durable completion.
func TestCovJobStopReceiptWait_NilRun(t *testing.T) {
	r := jobStopReceipt{manager: newTestJM(t), jobID: "job_nil"}
	err := r.wait()
	if err == nil {
		t.Fatal("nil run should return error from wait")
	}
	if !strings.Contains(err.Error(), "no completion receipt") {
		t.Fatalf("error = %v, want completion receipt error", err)
	}
}

// TestCovJobStopReceiptWait_Durable covers a successful durable wait.
func TestCovJobStopReceiptWait_Durable(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	run := jm.running[rec.JobID]
	r := jobStopReceipt{manager: jm, jobID: rec.JobID, run: run}
	// Close done as durable immediately (deterministic, no sleep).
	run.closeDoneDurable()
	if err := r.wait(); err != nil {
		t.Fatalf("durable wait: %v", err)
	}
}

// TestCovJobStopReceiptWait_Abandoned covers a non-durable (abandoned) completion.
func TestCovJobStopReceiptWait_Abandoned(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	run := jm.running[rec.JobID]
	r := jobStopReceipt{manager: jm, jobID: rec.JobID, run: run}
	run.closeDoneAbandoned()
	err = r.wait()
	if err == nil {
		t.Fatal("abandoned completion should return error")
	}
	if !strings.Contains(err.Error(), "without durable completion") {
		t.Fatalf("error = %v, want durable completion error", err)
	}
}
