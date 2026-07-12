//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"testing"
	"time"
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

}
