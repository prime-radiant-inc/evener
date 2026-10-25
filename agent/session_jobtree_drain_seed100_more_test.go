//go:build evenerfuzz

package agent

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

func TestJobTreeDrainSeed100More(t *testing.T) {
	seed100JobtreeDrainMore(t)
}

func seed100JobtreeDrainMore(t *testing.T) {
	t.Helper()

	wake, notify := newDrainWake()
	notify()
	notify()
	select {
	case <-wake:
	default:
		t.Fatal("drain wake was not queued")
	}
	notify()
	if !takeDrainWake(wake) {
		t.Fatal("queued wake was not consumed")
	}
	if takeDrainWake(wake) || takeDrainWake(nil) {
		t.Fatal("empty wake channel reported work")
	}

	root := newSession(t)
	child := newSession(t)
	sub := &subagent{id: child.ID(), sess: child, finalizing: true}
	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = sub
	root.subagents.mu.Unlock()
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	if result, err := root.drainJobTreeWith(waitCtx, make(chan struct{}), make(chan time.Time), func(context.Context) error {
		return nil
	}, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		t.Fatal("cancelled drain processed without notification work")
		return "", nil
	}); result != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled drain = %q, %v", result, err)
	}
	sub.mu.Lock()
	sub.finalizing = false
	sub.mu.Unlock()
	if err := child.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	if outstanding, err := root.treeHasOutstandingWork(); outstanding || err == nil {
		t.Fatalf("child store fault = %v, %v; want false, error", outstanding, err)
	}

	closed := newSession(t)
	if err := closed.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := closed.drainJobTree(context.Background(), drainWakeForTest(closed), make(chan time.Time)); result != "" || err == nil {
		t.Fatalf("closed-store drain = %q, %v; want empty, error", result, err)
	}

	want := errors.New("seed100 drain fault")
	faulted := newSession(t)
	if result, err := faulted.drainJobTreeWith(context.Background(), drainWakeForTest(faulted), make(chan time.Time), func(context.Context) error {
		return want
	}, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		t.Fatal("processed after kick failure")
		return "", nil
	}); result != "" || !errors.Is(err, want) {
		t.Fatalf("kick-fault drain = %q, %v; want empty, fault", result, err)
	}

	faulted.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, Status: jobNotificationEventWatch})
	if result, err := faulted.drainJobTreeWith(context.Background(), drainWakeForTest(faulted), make(chan time.Time), func(context.Context) error {
		return nil
	}, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		return "", want
	}); result != "" || !errors.Is(err, want) {
		t.Fatalf("process-fault drain = %q, %v; want empty, fault", result, err)
	}

	kickRoot := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1}), withSteps(func(req llm.Request) llm.Response {
		if !requestContainsText(req, "dlg_seed100_stop_gate") {
			t.Error("root request omitted its owed Stop result")
		}
		return finalResponse("stop-result-observed-59")
	}))
	if err := kickRoot.kickDriveTreeWith(context.Background(), func(*Session, context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("root kick fault = %v", err)
	}
	kickChild := newSession(t)
	kickRoot.subagents.mu.Lock()
	kickRoot.subagents.subs[kickChild.ID()] = &subagent{id: kickChild.ID(), sess: kickChild}
	kickRoot.subagents.mu.Unlock()
	if err := kickRoot.kickDriveTreeWith(context.Background(), func(sess *Session, _ context.Context) error {
		if sess == kickChild {
			return want
		}
		return nil
	}); !errors.Is(err, want) {
		t.Fatalf("child kick fault = %v", err)
	}

	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	descriptor := stableToolDescriptor(kickRoot, "dlg_seed100_stop_gate", "")
	descriptor.ChildSessionID = kickChild.ID()
	lease := delegateLease{delegateID: "dlg_seed100_stop_gate", generation: 1}
	packet := delegateStoppedTerminalPacket()
	kickRoot.delegateController.mu.Lock()
	appended, err := kickRoot.delegateController.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: lease.delegateID, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		delegateControllerRunStartedEvent(lease.delegateID, lease.generation, delegatestore.TriggerInitial, started),
		delegatestore.Event{Kind: delegatestore.EventDelegateSubtreeStopRequested, DelegateID: lease.delegateID, SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: lease.delegateID}},
		delegateRunFinishedEvent(lease, delegatestore.OutcomeStopped, delegatestore.DispositionTerminalError, "stopped_by_parent", ended, delegateDeliveryID(lease.delegateID, lease.generation), &packet),
	)
	kickRoot.delegateController.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := kickRoot.kickDriveTreeWith(context.Background(), func(sess *Session, _ context.Context) error {
		if sess == kickChild {
			t.Fatal("drained stop-gated child")
		}
		return nil
	}); err != nil {
		t.Fatalf("stop-gated kick: %v", err)
	}
	if err := kickChild.enqueueNotification(notification{ID: "seed100-gated-attention", Message: llm.User("gated-payload-59")}); err != nil {
		t.Fatal(err)
	}
	if outstanding, err := kickRoot.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("owed Stop result outstanding = %v, %v; want true, nil before source custody", outstanding, err)
	}
	// Complete the durable Stop before transporting its result. The child's
	// latest stopped outcome still fences execution and preserves its message.
	kickRoot.delegateController.mu.Lock()
	_, err = kickRoot.delegateController.appendLocked(delegatestore.Event{
		Kind: delegatestore.EventDelegateSubtreeStopCompleted, DelegateID: lease.delegateID,
		SubtreeStopCompleted: &delegatestore.SubtreeStopCompleted{RequestSeq: appended[2].Seq},
	})
	kickRoot.delegateController.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := kickRoot.replayDelegateDeliveries(); err != nil {
		t.Fatal(err)
	}
	if ids := pendingDeliveryIDs(kickRoot.delegateController, lease.delegateID); len(ids) != 0 {
		t.Fatalf("Stop result source not acknowledged after custody: %v", ids)
	}
	id := delegateAttentionID(delegateDeliveryID(lease.delegateID, lease.generation))
	assertAttentionResponseReceipts(t, kickRoot, id, 0)
	if outstanding, err := kickRoot.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("unobserved root Stop result outstanding = %v, %v; want true, nil", outstanding, err)
	}
	if _, err := kickRoot.ProcessInput(context.Background(), "observe-stop-59", nil); err != nil {
		t.Fatal(err)
	}
	assertAttentionResponseReceipts(t, kickRoot, id, 1)
	if pending, err := kickChild.pendingNotificationIDs(); err != nil || !slices.Equal(pending, []string{"seed100-gated-attention"}) {
		t.Fatalf("stop-gated child message was not preserved: %v, %v", pending, err)
	}
	if outstanding, err := kickRoot.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("stop-gated tree outstanding = %v, %v; want false, nil", outstanding, err)
	}
}
