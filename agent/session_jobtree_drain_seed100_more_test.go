//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

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
	if err := waitDrainWake(context.Background(), wake, make(chan time.Time)); err != nil {
		t.Fatalf("queued wake: %v", err)
	}
	recheck := make(chan time.Time, 1)
	recheck <- frozenTestTime
	if err := waitDrainWake(context.Background(), make(chan struct{}), recheck); err != nil {
		t.Fatalf("queued recheck: %v", err)
	}
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	if err := waitDrainWake(waitCtx, make(chan struct{}), make(chan time.Time)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wake = %v", err)
	}

	root := newSession(t)
	child := newSession(t)
	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child}
	root.subagents.mu.Unlock()
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
	if result, err := closed.drainJobTree(context.Background(), make(chan time.Time)); result != "" || err == nil {
		t.Fatalf("closed-store drain = %q, %v; want empty, error", result, err)
	}

	want := errors.New("seed100 drain fault")
	faulted := newSession(t)
	if result, err := faulted.drainJobTreeWith(context.Background(), make(chan time.Time), func(context.Context) error {
		return want
	}, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		t.Fatal("processed after kick failure")
		return "", nil
	}); result != "" || !errors.Is(err, want) {
		t.Fatalf("kick-fault drain = %q, %v; want empty, fault", result, err)
	}

	faulted.enqueueJobNotification(jobNotification{Kind: jobNotificationKindWatch, Status: jobNotificationEventWatch})
	if result, err := faulted.drainJobTreeWith(context.Background(), make(chan time.Time), func(context.Context) error {
		return nil
	}, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		return "", want
	}); result != "" || !errors.Is(err, want) {
		t.Fatalf("process-fault drain = %q, %v; want empty, fault", result, err)
	}

	kickRoot := newSession(t)
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
	_, err := kickRoot.delegateController.appendLocked(
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
	kickChild.enqueueJobNotification(jobNotification{JobID: "seed100-gated-attention"})
	if outstanding, err := kickRoot.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("stop-gated tree outstanding = %v, %v; want false, nil", outstanding, err)
	}
}
