package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// Stopping a subagent ends that subagent's own run and nothing else (S6,
// Jesse's ruling): its run context is cancelled, while a subagent it started
// keeps running under a context of its own.
func TestUserStopCancelsOnlyTheTargetsRun(t *testing.T) {
	root := newTestSession(t)
	parentSession := newTestSession(t)
	childSession := newTestSession(t)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	childCtx, cancelChild := context.WithCancel(context.Background())
	defer cancelChild()
	parent := &subagent{id: "child-session-a", sess: parentSession, running: true, cancel: cancelParent}
	child := &subagent{id: "child-session-b", sess: childSession, running: true, cancel: cancelChild}
	root.subagents.track(parent)
	parentSession.subagents.track(child)

	if found := root.subagentForChild("child-session-b"); found != child {
		t.Fatalf("subagentForChild found %p, want the nested subagent %p", found, child)
	}
	if _, got := root.subagentForChild("child-session-a").requestRunCancel(true); got != nil {
		t.Fatalf("requestRunCancel = %v, want the run stopping", got)
	}
	if parentCtx.Err() == nil {
		t.Fatal("the target's run context is still live")
	}
	if childCtx.Err() != nil {
		t.Fatal("stopping the target cancelled the subagent it started")
	}
	parent.mu.Lock()
	requested := parent.cancelRequested
	parent.mu.Unlock()
	child.mu.Lock()
	childRequested := child.cancelRequested
	child.mu.Unlock()
	if !requested || childRequested {
		t.Fatalf("cancelRequested target=%v child=%v, want only the target", requested, childRequested)
	}
}

// A run that is not running, or is already settling, has nothing to stop.
func TestUserStopRefusesARunThatIsNotRunning(t *testing.T) {
	idle := &subagent{id: "idle"}
	if _, err := idle.requestRunCancel(true); !errors.Is(err, errSubagentNotRunning) {
		t.Fatalf("idle requestRunCancel = %v, want errSubagentNotRunning", err)
	}
	settling := &subagent{id: "settling", running: true, settlementClaimed: true}
	if _, err := settling.requestRunCancel(true); !errors.Is(err, errSubagentSettling) {
		t.Fatalf("settling requestRunCancel = %v, want errSubagentSettling", err)
	}
	// A second stop while the first is still unwinding the run finds it
	// already stopping, so a retry answers notRunning rather than stopping.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping := &subagent{id: "stopping", running: true, cancel: cancel}
	if _, err := stopping.requestRunCancel(true); err != nil {
		t.Fatalf("first requestRunCancel = %v", err)
	}
	if _, err := stopping.requestRunCancel(true); !errors.Is(err, errSubagentSettling) {
		t.Fatalf("repeated requestRunCancel = %v, want errSubagentSettling", err)
	}
}

// requestRunCancel captures the run's own done channel under the same lock
// as its admission check, so a caller cannot be handed a channel a
// concurrent run reset invalidates between the check and the wait. Found by
// RoboRev on this PR: cancelAgent used to read sub.done separately, before
// this admission check, which can race a settle-and-resume in between.
func TestRequestRunCancelReturnsTheAdmittedRunsDoneChannelAtomically(t *testing.T) {
	original := make(chan struct{})
	sub := &subagent{id: "atomic", running: true, done: original, cancel: func() {}}
	done, err := sub.requestRunCancel(false)
	if err != nil {
		t.Fatalf("requestRunCancel = %v", err)
	}
	if done != original {
		t.Fatal("requestRunCancel returned a different channel than the run it admitted")
	}
	// Simulate a concurrent reset for a new run (resetSubagentForRunLocked):
	// the channel already handed back must stay this run's own, not track
	// whatever sub.done now holds.
	sub.mu.Lock()
	sub.done = make(chan struct{})
	sub.mu.Unlock()
	select {
	case <-done:
		t.Fatal("the returned channel must not be the reset run's channel")
	default:
	}
}

// A cancellation is credited to the user in the coordinator's packet only
// when the caller marks it a direct user stop (S6, StopDelegateRun):
// cancelAgent's own cancel (unwired in production today) leaves the run
// error's own text, so a future caller of cancelAgent is never misattributed
// as "Stopped by the user." Found by RoboRev on this PR.
func TestStableDelegateFinish_OnlyADirectUserStopReadsStoppedByTheUser(t *testing.T) {
	readPacketMessage := func(t *testing.T, finish delegateFinish) string {
		t.Helper()
		var message string
		if finish.packet == nil || json.Unmarshal(finish.packet.Message, &message) != nil {
			t.Fatalf("packet = %+v, want a decodable message", finish.packet)
		}
		return message
	}

	notUserStop := &subagent{id: "not-user-stop", sess: newTestSession(t), running: true, done: make(chan struct{}), cancel: func() {}}
	if _, err := notUserStop.requestRunCancel(false); err != nil {
		t.Fatalf("requestRunCancel(false) = %v", err)
	}
	if got := readPacketMessage(t, notUserStop.stableDelegateFinish("", context.Canceled)); got != context.Canceled.Error() {
		t.Fatalf("cancelled without a direct user stop = %q, want the run error's own text %q", got, context.Canceled.Error())
	}

	userStop := &subagent{id: "user-stop", sess: newTestSession(t), running: true, done: make(chan struct{}), cancel: func() {}}
	if _, err := userStop.requestRunCancel(true); err != nil {
		t.Fatalf("requestRunCancel(true) = %v", err)
	}
	if got := readPacketMessage(t, userStop.stableDelegateFinish("", context.Canceled)); got != delegateUserStopMessage {
		t.Fatalf("cancelled with a direct user stop = %q, want %q", got, delegateUserStopMessage)
	}
}

// End to end on a stable delegate: the stop ends its run as cancelled, with
// no durable subtree stop, the delegate idle and still resumable, and the
// coordinator told the user stopped it. A second stop finds nothing running,
// and an unknown delegate is an error.
func TestStopDelegateRunEndsTheRunAsCancelledByTheUser(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	root, delegateID := harness.root, harness.fixture.delegateID

	got, err := root.StopDelegateRun(delegateID)
	if err != nil || got != appwire.DelegateStopStopping {
		t.Fatalf("StopDelegateRun = %q, %v; want stopping", got, err)
	}
	harness.release()
	// TRIPWIRE: an in-process scripted adapter; settling takes milliseconds.
	waitForCondition(t, 30*time.Second, "the stopped run to settle", func() bool {
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		aggregate := root.delegateController.durable[delegateID]
		return aggregate != nil && !aggregate.CurrentRunOpen && aggregate.LatestOutcome != nil
	})

	root.delegateController.mu.Lock()
	aggregate := *root.delegateController.durable[delegateID]
	root.delegateController.mu.Unlock()
	if aggregate.LatestOutcome.Status != delegatestore.OutcomeCancelled || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.PendingStopSeq != 0 {
		t.Fatalf("stopped delegate = outcome %+v phase %q resumable %v pending stop %d; want cancelled, idle, resumable, no stop fence",
			aggregate.LatestOutcome, aggregate.Phase, aggregate.Resumable, aggregate.PendingStopSeq)
	}
	var message string
	if aggregate.LatestPacket == nil || json.Unmarshal(aggregate.LatestPacket.Message, &message) != nil || message != delegateUserStopMessage {
		t.Fatalf("the coordinator's packet = %+v, want %q", aggregate.LatestPacket, delegateUserStopMessage)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.SubtreeStopRequested != nil {
			t.Fatalf("a user stop wrote a durable subtree stop: %+v", event)
		}
	}

	if again, err := root.StopDelegateRun(delegateID); err != nil || again != appwire.DelegateStopNotRunning {
		t.Fatalf("second StopDelegateRun = %q, %v; want notRunning", again, err)
	}
	if _, err := root.StopDelegateRun("dlg_unknown"); !errors.Is(err, ErrUnknownDelegate) {
		t.Fatalf("unknown delegate = %v, want ErrUnknownDelegate", err)
	}
}
