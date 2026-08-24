package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
)

// enqueueLeftoverNotification puts an in-memory notification for jobID on the
// session's own rail, mirroring what a job that finalized during the final
// round leaves behind when the model ends the turn anyway.
func enqueueLeftoverNotification(t *testing.T, sess *Session, jobID string) {
	t.Helper()
	recs, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load job store: %v", err)
	}
	rec := recs[jobID]
	if rec == nil {
		t.Fatalf("job %s not in store", jobID)
	}
	sess.enqueueJobNotification(jobNotificationFromRecord(rec))
}

// TestTerminalDrainDiscardsLeftoverNotificationsWithoutAProviderCall is the
// #329 sanitize-git-repo regression. The model sent the terminal communicate
// (end_turn under TurnEndsProcess) with an undelivered job notification still
// pending. There is no model left to deliver to — the turn that ends the
// process is by definition the last turn — so the drain must discard the
// leftover (settling its durable record as consumed) and exit WITHOUT another
// provider call. The field failure made that call, got an empty response, and
// the retry path injected "please continue" instead of exiting.
func TestTerminalDrainDiscardsLeftoverNotificationsWithoutAProviderCall(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("the answer") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))

	// TRIPWIRE: scripted in-process adapter plus in-memory job fixtures, no
	// real I/O; 30s only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.ProcessInput(ctx, "do the task", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if res != "the answer" {
		t.Fatalf("ProcessInput result = %q, want %q", res, "the answer")
	}

	// The leftover: a job that finalized before the terminal communicate was
	// accepted, its notification durable-pending AND queued but never delivered.
	seedOwnedDurablePending(t, sess.jobManager, "job-leftover", jobstore.JobShell)
	enqueueLeftoverNotification(t, sess, "job-leftover")

	drained, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree: %v", err)
	}
	if drained != "" {
		t.Fatalf("drain result = %q; no drain turn may run after the terminal communicate for a leftover", drained)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider calls = %d, want 1: the terminal communicate must be the last provider call", got)
	}
	requireNotificationState(t, sess.jobManager, "job-leftover", jobstore.NotifyConsumed)
	if p := sess.peekNotifications(); p != 0 {
		t.Fatalf("queued notifications after terminal drain = %d, want 0 (discarded)", p)
	}
	warnings := collectStallWarnings(sess)
	found := false
	for _, ev := range warnings {
		if d, ok := ev.Data.(events.WarningData); ok &&
			strings.Contains(d.Message, "job-leftover") && strings.Contains(d.Message, "discard") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the discarded leftover job-leftover, got %v", warnings)
	}
}

// seedWatchSendResidue leaves the job manager in the wedged-settlement state
// the #329 hang trials showed around delegate-lane (dlg_*) watches: a stable
// watch settlement claim that nothing in this process will ever finish. It
// keeps hasPendingWatchSends true, which counts as BOTH outstanding work
// (treeHasOutstandingWork) and a live/deliverable component
// (subtreeHasLiveComponent) — so the drain neither quiesces nor ever reaches
// the stall watchdog's verdict.
func seedWatchSendResidue(t *testing.T, sess *Session) {
	t.Helper()
	jm := sess.jobManager
	jm.mu.Lock()
	jm.stableWatchSettlementRetrying = true
	jm.mu.Unlock()

	outstanding, err := sess.treeHasOutstandingWork()
	if err != nil || !outstanding {
		t.Fatalf("precondition: residue must count as outstanding work, got %v err=%v", outstanding, err)
	}
	live, err := sess.subtreeHasLiveComponent()
	if err != nil || !live {
		t.Fatalf("precondition: residue must suppress the stall watchdog, got live=%v err=%v", live, err)
	}
	stalled, err := sess.drainSubtreeIsStalled()
	if err != nil || stalled {
		t.Fatalf("precondition: residue must NOT read as a genuine stall, got stalled=%v err=%v", stalled, err)
	}
}

// TestTerminalDrainExitsDespiteWatchSendResidue is the #329 dead-hang
// regression (train-fasttext, count-dataset-tokens, filter-js-from-html): the
// model ended the process-ending turn, no live work remains anywhere, but
// watch-send residue keeps the drain outstanding AND keeps the stall watchdog
// suppressed — a livelock that previously held the process open until the
// harness killed it. Once the terminal communicate is accepted, residue with
// no live work behind it must be abandoned so the process can exit.
func TestTerminalDrainExitsDespiteWatchSendResidue(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("all done") },
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))

	// TRIPWIRE: scripted adapter, in-memory residue, no real I/O. The fixed
	// drain returns on its first pass; 10s only fires on the #329 livelock.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "do the task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	seedWatchSendResidue(t, sess)

	drained, err := sess.DrainJobTree(ctx)
	if err != nil {
		t.Fatalf("DrainJobTree must exit cleanly past terminal residue, got: %v", err)
	}
	if drained != "" {
		t.Fatalf("drain result = %q, want empty (no drain turn ran)", drained)
	}
	if ctx.Err() != nil {
		t.Fatal("drain only returned because the context expired: this is the #329 hang")
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

// TestPostTerminalNotificationTurnEmptyResponseFinishesIdle is the other half
// of the #329 sanitize-git-repo regression. A notification turn that
// legitimately runs after the terminal communicate (a live job finished during
// the drain) can meet a model that has nothing left to say — an empty
// response. The empty-response retry path must not inject "please continue"
// and resurrect a run the model already declared over: the turn finishes idle
// on the first empty response, with no retry provider call.
func TestPostTerminalNotificationTurnEmptyResponseFinishesIdle(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("the answer") },
		// The post-terminal notification turn: truly empty (no text, no tool
		// calls, no phase metadata) — the model glitch shape the retry budget
		// exists for, which after a terminal communicate must finish idle.
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
		},
	}}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))

	// TRIPWIRE: scripted in-process adapter, no real I/O; 30s only fires on a
	// genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "do the task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// A fresh completion arriving AFTER the terminal communicate (live-work
	// shape): durable pending plus its queued notification.
	seedOwnedDurablePending(t, sess.jobManager, "job-late", jobstore.JobShell)
	enqueueLeftoverNotification(t, sess, "job-late")

	out, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if out != "" {
		t.Fatalf("notification turn result = %q, want empty", out)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider calls = %d, want 2: an empty post-terminal response must finish idle, not retry", got)
	}
}

// TestNormalDrainStillWaitsOnWatchSendResidue pins the #329 design boundary:
// WITHOUT a terminal communicate (the turn ended some other way), residue is
// treated exactly as before — the drain keeps waiting for the machinery to
// convert it, and only the caller's context bounds the wait. The discard rule
// is gated on the model having explicitly ended the process-ending turn.
func TestNormalDrainStillWaitsOnWatchSendResidue(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		TurnEndsProcess:  true,
	}))
	seedWatchSendResidue(t, sess)

	// TRIPWIRE: this bound is the assertion, not a hang guard — the correct
	// behavior here IS to still be waiting when it expires (the drain must not
	// discard residue without a terminal communicate), and the caller's
	// context is the only thing that ends the wait. One second sits far above
	// the drain's 250ms recheck cadence, so a wrongly-exiting drain returns
	// well before it.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := sess.DrainJobTree(ctx)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("without a terminal communicate the drain must keep waiting on residue until the caller cancels; got err=%v ctxErr=%v", err, ctx.Err())
	}
}
