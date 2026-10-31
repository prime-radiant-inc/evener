package agent

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

func TestSubagentActivationReturn(t *testing.T) {
	t.Parallel()
	t.Run("siblings and same lease resumes", testActivationReturnSiblings)
	t.Run("synchronous yielded cancellation", func(t *testing.T) { testActivationReturnCancel(t, false) })
	t.Run("stale lease refusal", func(t *testing.T) { testActivationReturnCancel(t, true) })
	t.Run("scripted error", testActivationReturnError)
}

func testActivationReturnSiblings(t *testing.T) {
	a := newMessagingAdapter("RETURN-A", "RETURN-B")
	receipts := newActivationReturns()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	firstGate, yieldGate, secondGate, siblingGate := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(firstGate) })
	releaseYield := sync.OnceFunc(func() { close(yieldGate) })
	releaseSecond := sync.OnceFunc(func() { close(secondGate) })
	releaseSibling := sync.OnceFunc(func() { close(siblingGate) })
	defer releaseFirst()
	defer releaseYield()
	defer releaseSecond()
	defer releaseSibling()
	var jobID string
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("return-a", "RETURN-A") }, func(llm.Request) llm.Response { return delegateToolResponse("return-b", "RETURN-B") }, func(llm.Request) llm.Response { return finalResponse("root-return") })
	a.script("RETURN-A", func(llm.Request) llm.Response {
		awaitSignal(t, firstGate, "first provider release")
		return finalResponse("first-return")
	}, func(llm.Request) llm.Response {
		awaitSignal(t, yieldGate, "yield provider release")
		return incidentShellResponse("return-shell", fifo)
	}, func(req llm.Request) llm.Response {
		jobID = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		return waitResponseFor("return-wait", jobID)
	}, func(llm.Request) llm.Response {
		awaitSignal(t, secondGate, "second provider release")
		return waitResponseFor("return-wait-again", jobID)
	})
	a.script("RETURN-B", func(llm.Request) llm.Response {
		awaitSignal(t, siblingGate, "sibling release")
		return finalResponse("sibling-return")
	})
	root := newMessagingRoot(t, a, receipts.configure)
	t.Cleanup(func() { receipts.close(t, root) })
	if _, err := root.ProcessInput(t.Context(), "start", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, "RETURN-A", 1)
	a.waitForRequests(t, "RETURN-B", 1)
	var owner, sibling *Session
	a.mu.Lock()
	for id, key := range a.sessions {
		if key == "RETURN-A" {
			owner = root.getSub(id).sess
		}
		if key == "RETURN-B" {
			sibling = root.getSub(id).sess
		}
	}
	a.mu.Unlock()
	if owner == nil || sibling == nil {
		t.Fatal("missing scripted siblings")
	}
	old := receipts.take(t, owner.ID())
	b := receipts.take(t, sibling.ID())
	activationStillOpen(t, old.returned)
	activationStillOpen(t, b.returned)
	releaseFirst()
	waitForTestSignal(t, old.returned, "first terminal return")
	waitForTestSignal(t, old.generationDone, "first generation done")
	activationStillOpen(t, b.returned)
	a.script("root", func(llm.Request) llm.Response {
		return delegateSendResponse("return-next", owner.owningDelegateID, "next-generation", 0)
	}, func(llm.Request) llm.Response { return finalResponse("root-next") })
	if _, err := root.ProcessInput(t.Context(), "next", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, "RETURN-A", 2)
	first := receipts.take(t, owner.ID())
	if first.lease.generation != old.lease.generation+1 || first.generationDone == old.generationDone {
		t.Fatalf("generation identity: old=%+v new=%+v", old, first)
	}
	releaseYield()
	waitForTestSignal(t, first.returned, "yield return")
	activationStillOpen(t, first.generationDone)
	activationStillOpen(t, b.returned)
	if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "return-resume", Message: llm.UserMachinery("resume-3a40")}); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, "RETURN-A", 4)
	second := receipts.take(t, owner.ID())
	if first == second || first.returned == second.returned || first.lease != second.lease || first.generationDone != second.generationDone {
		t.Fatalf("same-lease activation identity: first=%+v second=%+v", first, second)
	}
	// These older witnesses are already closed while the actual resumed provider
	// remains held. Neither can stand in for this activation's receipt.
	waitForTestSignal(t, old.generationDone, "old done")
	waitForTestSignal(t, first.returned, "old receipt")
	activationStillOpen(t, second.returned)
	activationStillOpen(t, b.returned)
	releaseSecond()
	waitForTestSignal(t, second.returned, "resumed yield return")
	activationStillOpen(t, second.generationDone)
	activationStillOpen(t, b.returned)
	releaseSibling()
	waitForTestSignal(t, b.returned, "sibling return")
}

func testActivationReturnCancel(t *testing.T, stale bool) {
	a := newMessagingAdapter("RETURN-CANCEL")
	receipts := newActivationReturns()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("return-cancel", "RETURN-CANCEL") }, func(llm.Request) llm.Response { return finalResponse("root-cancel") })
	a.script("RETURN-CANCEL", func(llm.Request) llm.Response {
		awaitSignal(t, gate, "cancel provider release")
		return incidentShellResponse("cancel-shell", fifo)
	}, func(req llm.Request) llm.Response {
		return waitResponseFor("cancel-wait", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, a, receipts.configure)
	t.Cleanup(func() { receipts.close(t, root) })
	if _, err := root.ProcessInput(t.Context(), "start", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, "RETURN-CANCEL", 1)
	child := root.subagents.sessions()[0]
	first := receipts.take(t, child.ID())
	release()
	waitForTestSignal(t, first.returned, "cancel initial yield")
	activationStillOpen(t, first.generationDone)
	sub := root.getSub(child.ID())
	if stale {
		sub.cancelYieldedActivation(delegateLease{delegateID: first.lease.delegateID, generation: first.lease.generation + 1})
		receipts.collect()
		if len(receipts.pending) != 0 {
			t.Fatalf("stale refusal registered %d activations", len(receipts.pending))
		}
		activationStillOpen(t, first.generationDone)
	}
	if _, err := root.StopDelegateRun(first.lease.delegateID); err != nil {
		t.Fatal(err)
	}
	canceled := receipts.take(t, child.ID())
	if canceled == first || canceled.lease != first.lease {
		t.Fatalf("cancel identity: first=%+v canceled=%+v", first, canceled)
	}
	// Stop synchronously executes the canceled yielded tail, not a new provider.
	select {
	case <-canceled.returned:
	default:
		t.Fatal("synchronous cancel returned before its activation")
	}
	waitForTestSignal(t, first.generationDone, "canceled generation")
	if got := len(a.requestsFor("RETURN-CANCEL")); got != 2 {
		t.Fatalf("cancel provider requests=%d", got)
	}
	row := delegateAggregateSnapshot(t, root.delegateController, first.lease.delegateID)
	if row.CurrentRunOpen || row.LatestOutcome == nil || row.LatestOutcome.Status != delegatestore.OutcomeCancelled {
		t.Fatalf("cancel outcome=%+v", row)
	}
}

func testActivationReturnError(t *testing.T) {
	a := newMessagingAdapter("RETURN-ERROR")
	receipts := newActivationReturns()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	scriptedErr := errors.New("error-2859")
	a.finiteScript("root", func(context.Context, llm.Request) (llm.Response, error) {
		return delegateToolResponse("return-error", "RETURN-ERROR"), nil
	}, func(context.Context, llm.Request) (llm.Response, error) { return finalResponse("root-error"), nil })
	a.finiteScript("RETURN-ERROR", func(context.Context, llm.Request) (llm.Response, error) {
		awaitSignal(t, gate, "error provider release")
		return llm.Response{}, scriptedErr
	})
	root := newMessagingRoot(t, a, func(cfg *SessionConfig) {
		receipts.configure(cfg)
		cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 0}
	})
	t.Cleanup(func() { receipts.close(t, root) })
	if _, err := root.ProcessInput(t.Context(), "start", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, "RETURN-ERROR", 1)
	child := root.subagents.sessions()[0]
	receipt := receipts.take(t, child.ID())
	release()
	waitForTestSignal(t, receipt.returned, "scripted error return")
	waitForTestSignal(t, receipt.generationDone, "failed generation")
	sub := root.getSub(child.ID())
	sub.mu.Lock()
	runErr := sub.err
	sub.mu.Unlock()
	if !errors.Is(runErr, scriptedErr) {
		t.Fatalf("executor error=%v", runErr)
	}
	row := delegateAggregateSnapshot(t, root.delegateController, receipt.lease.delegateID)
	if row.CurrentRunOpen || row.LatestOutcome == nil || row.LatestOutcome.Status != delegatestore.OutcomeFailed {
		t.Fatalf("error outcome=%+v", row)
	}
}
