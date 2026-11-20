package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/internal/testwait"
)

// waitSignalLabel is the label the deadline regression's child waits for. It is
// the test's own input, so the parent can look for it in the child's output
// without reading production copy.
const waitSignalLabel = "sentinel-waitsignal-7f3a"

// waitSignalDeadlineHelperEnv marks the re-exec'd child of
// TestWaitForTestSignalReportsBeforeTheBinaryDeadline, so the parent never
// re-enters the guard and the child never recurses.
const waitSignalDeadlineHelperEnv = "EVENER_WAIT_SIGNAL_DEADLINE_HELPER"

// onSessionEventKD drives the jobManager's session-event entry point with a kind
// and data, wrapping them in a SessionEvent envelope the way Session.emit does.
// Tests that need to set provenance on the event build a full events.SessionEvent
// literal and call jm.onSessionEvent directly instead.
func onSessionEventKD(jm *jobManager, kind events.EventKind, data events.EventData) {
	jm.onSessionEvent(events.SessionEvent{Kind: kind, SessionID: jm.sessionID, Data: data})
}

// drainAndAccept advances watch delivery the way the live loop does: one drain
// pass (delegate targets deliver + caller pendings re-token) followed by one
// notification accept (caller tokens render by key and settle). Use it in
// Session-based tests to drive a full delivery cycle; pure-jm tests assert on
// pending state instead (the new observable contract at the jobManager level).
func drainAndAccept(t *testing.T, s *Session) {
	t.Helper()
	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = s.acceptNotificationInput(context.Background(), "") // ok to no-op on empty queue
}

// installWatchBelowValidation installs a watch directly into jm.watches the way
// configureWatch does AFTER newWatchConfig succeeds, but WITHOUT the validation
// layer (target/send checks and the feedback-loop guard). It exists so tests can
// exercise the live firing+delivery path (onSessionEvent -> recordWatchSendsAnd
// Kick) for caller-self watch shapes that the create-path guard now rejects.
// newWatchConfig itself runs no loop guard, so this install is legal below
// validation. The install sequence mirrors configureWatch: build cfg, lock,
// initProgressStop, assign, unlock, startProgressTimer (the timer no-ops for
// events-only configs where progressIntervalMS == 0).
func installWatchBelowValidation(t *testing.T, jm *jobManager, a watchArgs) {
	t.Helper()
	if a.Send != nil {
		a.Send.To = strings.TrimSpace(a.Send.To)
	}
	cfg, err := newWatchConfig(a, jm.now(), "")
	if err != nil {
		t.Fatalf("newWatchConfig(%+v): %v", a, err)
	}
	sendTo := ""
	if a.Send != nil {
		sendTo = a.Send.To
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: a.Target, SendTo: sendTo}
	jm.mu.Lock()
	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
}

// waitForTestSignal waits for ch to close, failing with label if it does not.
//
// The guard's length comes from testwait.Budget, not a fixed 30s: when the test
// binary's own deadline is nearer than that budget, a fixed guard would lose to
// the deadline and the failure would arrive as a goroutine dump instead of this
// labelled report. Budget keeps the guard short of the deadline, using the same
// rule testwait.Until polls under, so the two waits cannot disagree about how
// much of the binary's time a hang guard may spend.
func waitForTestSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	// TRIPWIRE: callers signal in-process with no real I/O; the guard only
	// fires on a genuine hang.
	select {
	case <-ch:
	case <-time.After(testwait.Budget(t)):
		t.Fatalf("timed out waiting for %s", label)
	}
}

// Regression for #6788. The guard must report its own label before the test
// binary's deadline; with a fixed 30s it lost to a nearer deadline and the
// failure arrived as the runtime's goroutine dump instead.
//
// The deadline is the real one: a re-exec'd child of this binary runs under
// -test.timeout=5s and calls the helper on a channel that never closes. Before
// the fix the guard outlived the deadline, so the child died in the timeout
// panic and printed no label; after it the guard gives up halfway there, at
// 2.5s, and the child reports the label through t.Fatalf.
//
// The child's exit status says how it ended, so the diagnosis never rests on
// the runtime's own wording. A test binary exits 1 when a test fails through
// the framework, which is the guard's t.Fatalf; a panic, the deadline's
// included, exits 2, and so does a TestMain that cannot set up. An error that
// carries no exit status at all means no test binary ran.
func TestWaitForTestSignalReportsBeforeTheBinaryDeadline(t *testing.T) {
	t.Parallel()
	if os.Getenv(waitSignalDeadlineHelperEnv) == "1" {
		waitForTestSignal(t, make(chan struct{}), waitSignalLabel)
		t.Fatal("the guard returned on a channel that never closes")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWaitForTestSignalReportsBeforeTheBinaryDeadline$", "-test.timeout=5s")
	cmd.Env = append(os.Environ(), waitSignalDeadlineHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a guard that never sees its signal passed its test\n%s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the child test binary did not run: %v\n%s", err, out)
	}
	if code := exit.ExitCode(); code != 1 {
		t.Fatalf("the child ended with exit status %d, not a failed test: the guard lost to the binary's deadline, or the binary never reached the test\n%s", code, out)
	}
	if !strings.Contains(string(out), waitSignalLabel) {
		t.Fatalf("the child failed its test without reporting the guard's label %q\n%s", waitSignalLabel, out)
	}
}

func loadWatchSendRecord(t *testing.T, jm *jobManager) jobstore.WatchSendRecord {
	t.Helper()
	return jobstore.FoldWatchSends(loadJobStoreEvents(t, jm))
}

func restoredWatchSendPendingEvents(sessionID, watchedJobID, sendTo string, now time.Time) []jobstore.Event {
	return []jobstore.Event{{
		Kind: jobstore.EventWatchSendPending,
		TS:   now,
		WatchSend: &jobstore.WatchSendState{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        sessionID,
				WatchTarget:             watchedJobID,
				ResolvedWatchedIdentity: watchedJobID,
				ResolvedSendTo:          sendTo,
				WatchGeneration:         "watch_restore_generation",
			},
			DeliveryID:      "delivery_restore_pending",
			UpdateSeq:       1,
			Message:         "restored observe",
			Frame:           "restored observe\n\ndelivery_id: delivery_restore_pending",
			TriggerIdentity: watchedJobID,
			TriggerReason:   "output_match: ready",
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}}
}

func loadJobStoreEvents(t *testing.T, jm *jobManager) []jobstore.Event {
	t.Helper()
	b, err := os.ReadFile(jm.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("read jobs.jsonl: %v", err)
	}
	var events []jobstore.Event
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e jobstore.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse event %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func installCallerSendWatchWithPending(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	// This is the feedback-loop shape (caller target, communicate,
	// send.to=caller) that configureWatch now rejects (TestValidateWatchDeliveryLoop
	// asserts the rejection). Install below validation to exercise the caller-send
	// pending/busy-delivery mechanics this helper's callers depend on.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	onSessionEventKD(jm, events.EventCommunicate, nil)
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("installCallerSendWatchWithPending: watch config not found")
	}
	if len(cfg.pendingOrder) == 0 {
		t.Fatal("installCallerSendWatchWithPending: no pending send after busy delivery")
	}
	return cfg
}

// installCallerSendWatchWithCurrentFrame installs a caller-send watch, drives it
// to updateSeq 2 (one busy fire creates pending @1, a second coalesces to @2),
// then stamps a deterministic Frame on the single pending entry so render-by-key
// assertions can match exact frame text. Returns the cfg, the pending key, and
// the pending entry's DeliveryID.
func installCallerSendWatchWithCurrentFrame(t *testing.T, jm *jobManager, frame string) (*watchConfig, jobstore.WatchSendKey, string) {
	t.Helper()
	cfg := installCallerSendWatchWithPending(t, jm)
	onSessionEventKD(jm, events.EventCommunicate, nil) // bump updateSeq 1 -> 2
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(cfg.pendingOrder) != 1 {
		t.Fatalf("want exactly one pending entry, got %d", len(cfg.pendingOrder))
	}
	key := cfg.pendingOrder[0]
	state := cfg.pending[key]
	if state == nil {
		t.Fatal("pending entry missing for key")
	}
	if state.UpdateSeq != 2 {
		t.Fatalf("pending updateSeq = %d, want 2 after two fires", state.UpdateSeq)
	}
	state.Frame = frame
	return cfg, key, state.DeliveryID
}

// The v2 re-route this section once tested — the parent's drain re-tokening a
// child's caller-targeted pending onto the parent's rail — is deleted in T15.
// Its replacement, TestDrainDoesNotReRouteChildCallerPendings, lives in
// job_delegate_drivedown_test.go and pins the new behavior: a mid-owner caller
// send renders in the mid's own drive turn, never on the parent's rail.

// terminalShellWithOutput creates a shell job, writes output to it, finalizes it
// completed, and returns the (now store-only) job_id. After finalize the job has
// been removed from jm.running, so a watch attached afterward must resolve its
// terminal status from the store and scan retained output via grepOutput.
func terminalShellWithOutput(t *testing.T, jm *jobManager, output string) string {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	if output != "" {
		if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte(output)); err != nil {
			t.Fatalf("append output: %v", err)
		}
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	jm.mu.Lock()
	_, stillRunning := jm.running[rec.JobID]
	jm.mu.Unlock()
	if stillRunning {
		t.Fatalf("job %q still in jm.running after finalize; terminal catch-up tests assume store-only", rec.JobID)
	}
	return rec.JobID
}

// TestTerminalCatchupNoSendFiresNotification covers spec §7.1 "Terminal target":
// an output_match-only watch on a terminal job whose retained output already
// contains a match performs a one-shot catch-up — fires exactly one notification,
// installs no live watch, and reports terminal_catchup with the terminal status.
func onlyWatchConfigForTest(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("watch count = %d, want 1", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		return cfg
	}
	panic("unreachable")
}
