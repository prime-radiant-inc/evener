package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

func backgroundFromJournal(t *testing.T, jm *jobManager, id string) bool {
	t.Helper()
	events, err := jobstore.ReadEvents(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	record := jobstore.Fold(events)[id]
	if record == nil {
		t.Fatalf("missing durable job %s", id)
	}
	return record.Background
}

func TestRunShellImmediateBackgroundRetainsEligibility(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	result := runShell(t.Context(), jm, env, shellArgs{
		Command: "printf 'background evidence\\n'", Background: true,
	})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("background handoff: %+v", result)
	}
	if !backgroundFromJournal(t, jm, result.JobID) {
		t.Fatal("handoff acknowledged without durable background evidence")
	}
	waitForShellDone(t, jm, result.JobID)
	if !backgroundFromJournal(t, jm, result.JobID) {
		t.Fatal("completion lost background evidence")
	}
}

func TestRunShellForegroundRetentionIsNotBackground(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	result := runShell(t.Context(), jm, env, shellArgs{Command: "printf retained-output", BlockTimeoutMS: 5000})
	if result.RunningInBackground || result.Status != string(jobstore.StatusCompleted) || result.settle == nil {
		t.Fatalf("foreground result: %+v", result)
	}
	id := result.settle(true)
	if id == "" || backgroundFromJournal(t, jm, id) {
		t.Fatalf("retained foreground output is missing or claimed background evidence: %q", id)
	}
	output, _, _, err := jm.readOutput(id, 1024)
	if err != nil || output != "retained-output" {
		t.Fatalf("retained output = %q, error = %v", output, err)
	}
}

// awaitShellReady observes a real command's readiness file, written after its
// output. Waiting on process completion then drains that output before assertions.
func awaitShellReady(t *testing.T, env execenv.StreamingExecutor) {
	t.Helper()
	root := env.(*execenv.LocalExecutionEnvironment).WorkingDirectory()
	// TRIPWIRE: a local shell writes readiness before blocking on its gate; 30s detects a stalled launch, not readiness timing.
	waitForCondition(t, 30*time.Second, "real shell readiness file", func() bool {
		_, err := os.Stat(filepath.Join(root, "ready"))
		return err == nil
	})
}

func receiveShellResult(t *testing.T, results <-chan shellResult) shellResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	// TRIPWIRE: the staged clock or context transition has already happened.
	case <-time.After(30 * time.Second):
		t.Fatal("shell did not return after staged transition")
		return shellResult{}
	}
}

func TestRunShellForegroundRuntimeLimitIsNotBackground(t *testing.T) {
	t.Parallel()
	jm, env, clk := newFakeClockShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	results := make(chan shellResult, 1)
	go func() {
		results <- runShell(t.Context(), jm, env, shellArgs{
			Command: "printf runtime-output; touch ready; sleep 30", BlockTimeoutMS: 5000, MaxRuntimeMS: 500,
		})
	}()
	clk.BlockUntil(2)
	awaitShellReady(t, env)
	clk.Advance(500 * time.Millisecond)
	result := receiveShellResult(t, results)
	if result.Status != string(jobstore.StatusStopped) || result.Reason != "run_timeout" || result.RunningInBackground || result.JobID == "" {
		t.Fatalf("foreground runtime result: %+v", result)
	}
	if backgroundFromJournal(t, jm, result.JobID) {
		t.Fatal("foreground runtime limit claimed background evidence")
	}
	record := loadShellRecord(t, jm, result.JobID)
	if record.Status != jobstore.StatusStopped || record.Reason != "run_timeout" || result.Output != "runtime-output" {
		t.Fatalf("foreground runtime outcome/output changed: %+v, output %q", record, result.Output)
	}
}

func TestRunShellForegroundCancellationIsNotBackground(t *testing.T) {
	t.Parallel()
	jm, env, clk := newFakeClockShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	results := make(chan shellResult, 1)
	go func() {
		results <- runShell(ctx, jm, env, shellArgs{Command: "printf cancelled-output; touch ready; sleep 30", BlockTimeoutMS: 5000})
	}()
	clk.BlockUntil(1)
	awaitShellReady(t, env)
	cancel()
	result := receiveShellResult(t, results)
	if result.Status != string(jobstore.StatusStopped) || result.Reason != "cancelled" || result.RunningInBackground || result.JobID != "" || result.Output != "cancelled-output" {
		t.Fatalf("cancelled foreground result: %+v", result)
	}
	if records, err := jm.store.Load(); err != nil || len(records) != 0 {
		t.Fatalf("cancelled foreground has durable records: %+v, error %v", records, err)
	}
	assertNoShellJobArtifacts(t, jm)
}

func TestRunShellBackgroundOutcomesRetainEligibility(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, command, reason string
		status                jobstore.Status
		exit                  int
	}{
		{"exit zero", "printf exit-output", "exit_zero", jobstore.StatusCompleted, 0},
		{"exit seven", "printf exit-output; exit 7", "exit_nonzero", jobstore.StatusCommandExitedNonzero, 7},
		{"runtime limit", "printf exit-output; touch ready; sleep 30", "run_timeout", jobstore.StatusStopped, -1},
		{"parent stop", "printf exit-output; touch ready; sleep 30", "stopped_by_parent", jobstore.StatusCancelled, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			jm, env, clk := newFakeClockShellTestRig(t)
			t.Cleanup(func() { _ = jm.close() })
			args := shellArgs{Command: test.command, Background: true}
			if test.reason == "run_timeout" {
				args.MaxRuntimeMS = 500
			}
			result := runShell(t.Context(), jm, env, args)
			if result.JobID == "" || !result.RunningInBackground {
				t.Fatalf("background result: %+v", result)
			}
			if test.reason == "run_timeout" || test.reason == "stopped_by_parent" {
				awaitShellReady(t, env)
				if test.reason == "run_timeout" {
					clk.BlockUntil(1)
					clk.Advance(500 * time.Millisecond)
				} else if _, err := jm.stop(result.JobID); err != nil {
					t.Fatal(err)
				}
			}
			waitForShellDone(t, jm, result.JobID)
			record := loadShellRecord(t, jm, result.JobID)
			if record.Status != test.status || record.Reason != test.reason || record.ExitCode == nil || *record.ExitCode != test.exit {
				t.Fatalf("terminal record = %+v, want %s/%s/%d", record, test.status, test.reason, test.exit)
			}
			if !backgroundFromJournal(t, jm, result.JobID) {
				t.Fatal("terminal outcome lost durable background evidence")
			}
		})
	}
}

func TestRunShellBackgroundStartFailureDoesNotAcknowledge(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	attempts := failAppendN(jm, jobstore.EventJobStarted, 1)
	result := runShell(t.Context(), jm, env, shellArgs{Command: "sleep 30", Background: true})
	if result.Status != string(jobstore.StatusFailed) || result.Reason != "start_failed" || result.JobID != "" || result.RunningInBackground || attempts.Load() != 1 {
		t.Fatalf("start failure result: %+v, attempts %d", result, attempts.Load())
	}
	if records, err := jm.store.Load(); err != nil || len(records) != 0 {
		t.Fatalf("failed start retained a record: %+v, error %v", records, err)
	}
	assertNoShellJobArtifacts(t, jm)
}

func TestRunShellBackgroundForwardFailureDoesNotAcknowledge(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	jm.parentJobID = "parent"
	jm.forward = func(jobstore.Event) error { return errors.New("transport unavailable") }
	result := runShell(t.Context(), jm, env, shellArgs{Command: "sleep 30", Background: true})
	if result.Status != string(jobstore.StatusFailed) || result.Reason != "start_failed" || result.JobID != "" || result.RunningInBackground {
		t.Fatalf("forward failure acknowledged background handoff: %+v", result)
	}
	records, err := jm.store.Load()
	if err != nil || len(records) != 1 {
		t.Fatalf("failed forward records: %+v, error %v", records, err)
	}
	for id, record := range records {
		if record.Status != jobstore.StatusFailed || record.Reason != "forward_failed" || record.TerminalGen == "" || shellReceiptHeld(jm, id) {
			t.Fatalf("forward failure cleanup/outcome changed: %+v", record)
		}
		if !backgroundFromJournal(t, jm, id) {
			t.Fatal("local failed forward lost durable start evidence")
		}
	}
	jm.mu.Lock()
	live := len(jm.running)
	jm.mu.Unlock()
	if live != 0 {
		t.Fatalf("failed forward left %d live jobs", live)
	}
}

func TestRunShellBackgroundForwardedAndReopenedEvidence(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	parentPath := filepath.Join(t.TempDir(), "jobs.jsonl")
	parent, err := jobstore.OpenNoSync(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	jm.parentDelegateID = "parent-delegate"
	jm.forward = parent.Append
	result := runShell(t.Context(), jm, env, shellArgs{Command: "printf forwarded-output", Background: true})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("forwarded launch: %+v", result)
	}
	waitForShellDone(t, jm, result.JobID)
	owner := loadShellRecord(t, jm, result.JobID)
	forwardedEvents, err := jobstore.ReadEvents(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	forwarded := jobstore.Fold(forwardedEvents)[result.JobID]
	if forwarded == nil || !forwarded.Background || !backgroundFromJournal(t, jm, result.JobID) {
		t.Fatalf("forwarded journal lost background evidence: %+v", forwarded)
	}
	if forwarded.OwnerSessionID != jm.sessionID || forwarded.ParentDelegateID != "parent-delegate" ||
		forwarded.ParentJobID != owner.ParentJobID || forwarded.VisibleToSession != owner.VisibleToSession ||
		forwarded.OriginTurnID != owner.OriginTurnID || forwarded.OriginToolCallID != owner.OriginToolCallID ||
		forwarded.OriginItemID != owner.OriginItemID || forwarded.Status != owner.Status || forwarded.TerminalGen != owner.TerminalGen {
		t.Fatalf("forwarded identity or terminal generation differs: owner %+v, parent %+v", owner, forwarded)
	}
	if err := jm.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := jobstore.OpenNoSync(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	records, err := reopened.Load()
	if err != nil || records[result.JobID] == nil || !records[result.JobID].Background {
		t.Fatalf("reopened store lost evidence: %+v, error %v", records, err)
	}
	fresh, err := newJobManagerNoSync(filepath.Dir(filepath.Dir(jm.dir)), jm.sessionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.close() })
	record := loadShellRecord(t, fresh, result.JobID)
	if !record.Background || record.Status != jobstore.StatusCompleted || record.Reason != "exit_zero" || record.TerminalGen != owner.TerminalGen {
		t.Fatalf("fresh manager lost eligibility or terminal evidence: %+v", record)
	}
	output, _, _, err := fresh.readOutput(result.JobID, 1024)
	if err != nil || output != "forwarded-output" {
		t.Fatalf("fresh manager output = %q, error %v", output, err)
	}
}

func TestRunShellBackgroundTerminalRetryPreservesEligibility(t *testing.T) {
	t.Parallel()
	jm, env, clk := newFakeClockShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	attempts := failAppendN(jm, jobstore.EventJobFinished, 1)
	result := runShell(t.Context(), jm, env, shellArgs{Command: "printf retry-output", Background: true})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("background retry launch: %+v", result)
	}
	clk.BlockUntil(1)
	clk.Advance(time.Second)
	waitForShellDone(t, jm, result.JobID)
	events, err := jobstore.ReadEvents(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var terminalCount int
	for _, event := range events {
		if event.JobID == result.JobID && event.Kind == jobstore.EventJobFinished {
			terminalCount++
		}
	}
	record := jobstore.Fold(events)[result.JobID]
	if attempts.Load() != 2 || terminalCount != 1 || record == nil || record.TerminalGen == "" || record.Status != jobstore.StatusCompleted || record.Reason != "exit_zero" {
		t.Fatalf("terminal retry authority changed: attempts %d, terminals %d, record %+v", attempts.Load(), terminalCount, record)
	}
	if !record.Background {
		t.Fatal("terminal retry lost background evidence")
	}
}

func TestShellDetachedLeavesNoDurableBackgroundJob(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	if reporter, ok := s.env.(interface{ DetachSupported() bool }); !ok || !reporter.DetachSupported() {
		t.Skip("detached execution is unsupported in this environment")
	}
	result := s.reg.ExecuteCall(t.Context(), s.env, llm.ToolCallData{
		ID: "detached-boundary", Name: "shell",
		Arguments: json.RawMessage(`{"command":"exec true","mode":"detached"}`),
	})
	if result.IsError {
		t.Fatalf("detached shell: %s", result.Output)
	}
	var detached detachedShellToolResult
	if err := json.Unmarshal(toolResultJSON(result), &detached); err != nil {
		t.Fatal(err)
	}
	if detached.PID <= 0 || detached.Mode != "detached" {
		t.Fatalf("detached result: %+v", detached)
	}
	s.mu.Lock()
	done := s.detachedProcesses[0].done
	s.mu.Unlock()
	<-done
	if events, err := s.jobManager.store.LoadEvents(); err != nil || len(events) != 0 {
		t.Fatalf("detached command introduced managed journal events: %+v, error %v", events, err)
	}
}
