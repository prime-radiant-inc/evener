package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestRetainedDelegateReservesSessionUntilOwnerReleases(t *testing.T) {
	root, client, _ := newDelegateResourceBootstrapSession(t)
	logger, err := llm.NewSessionAPILogger(root.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	root.cfg.AcquireSessionOwnership = logger.ReserveSession
	adapter := newTask6FrozenDescriptorAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRun)
	result := root.createDelegate(context.Background(), delegateArgs{Task: "finish the assigned work", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case <-adapter.entered:
	case <-time.After(10 * time.Second): // TRIPWIRE: the scripted provider must receive the child request.
		t.Fatal("child never reached provider")
	}
	child := root.subagents.get(result.ChildSessionID)
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	contender, err := llm.NewSessionAPILogger(root.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contender.Close() })
	if err := contender.ReserveSession(result.ChildSessionID); !errors.Is(err, llm.ErrAPILogTargetLocked) {
		t.Fatalf("running child ownership = %v", err)
	}
	adapter.releaseRun()
	drainDone := make(chan struct{})
	defer close(drainDone)
	go func() {
		for {
			select {
			case <-adapter.entered:
			case <-drainDone:
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: await the actual child completion.
		t.Fatal("child never completed")
	}
	if err := contender.ReserveSession(result.ChildSessionID); !errors.Is(err, llm.ErrAPILogTargetLocked) {
		t.Fatalf("retained child ownership = %v", err)
	}
	root.Close()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := contender.ReserveSession(result.ChildSessionID); err != nil {
		t.Fatalf("released child ownership = %v", err)
	}
}
