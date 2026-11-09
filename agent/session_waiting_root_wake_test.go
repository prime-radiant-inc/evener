package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// A root that ends its turn on communicate(end_turn=true,
// end_reason="waiting_on_work") while a delegate still runs sleeps idle with
// the delegate blocked, and the delegate's report wakes it into a turn that
// presents the report to the model. This is the contract the prompt's wait
// line promises: ending the turn on a running delegate loses nothing, because
// the report comes back.
//
// The child is held on reportGate until the root is provably idle, so the
// wake the test observes can only come from a report that arrived AFTER the
// wait; a child that answered during the first turn would prove nothing.
func TestWaitingRootIsWokenByItsDelegateReport(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WAITED-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("del_wait", "WAITED-CHILD: report the answer.")
		},
		func(llm.Request) llm.Response {
			return endReasonResponse("waiting on the child", "waiting_on_work")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "CHILD-ANSWER") {
				return finalResponse("woke without the child's report")
			}
			return finalResponse("got the answer")
		},
	)
	reportGate := make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	t.Cleanup(releaseReport)
	adapter.script("WAITED-CHILD", func(llm.Request) llm.Response {
		<-reportGate
		return finalResponse("CHILD-ANSWER")
	})
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.NonInteractive = false })
	wakes := make(chan struct{}, 8)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})

	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), stallHandshakeTimeout)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The root accepted the waiting turn end and rested: it is idle, it asks
	// nobody for attention, and the child has not answered yet.
	if got := root.State(); got != SessionIdle {
		t.Fatalf("root state after a waiting turn end = %q, want %q: a wait asks nobody for attention", got, SessionIdle)
	}
	if len(rootPendingAttention(root)) != 0 {
		t.Fatal("the child's report reached the root before the wait settled: the wake would not prove causality")
	}
	// Delegate creation launches the child in its own goroutine, so the root
	// finishing its turn says nothing about the child having reached the
	// provider yet. Wait for that request, then assert it is still the only
	// one: the child is parked on reportGate, not answering.
	if got := len(adapter.waitForRequests(t, "WAITED-CHILD", 1)); got != 1 {
		t.Fatalf("child requests = %d, want the single blocked request only", got)
	}

	// Release the child. Its report raises the wake a served root listens on.
	releaseReport()
	select {
	case <-wakes:
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("the child's report did not wake the waiting root: a wait would have stranded the result")
	}
	waitForCondition(t, stallHandshakeTimeout, "the child's report attention to arm", func() bool {
		return len(rootAttentionFold(t, root).pendingIDs()) > 0
	})

	// The wake starts the root's turn, which presents the report to the model.
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	requests := adapter.requestsFor("root")
	if len(requests) == 0 {
		t.Fatal("the root made no model request")
	}
	if !requestContainsText(requests[len(requests)-1], "CHILD-ANSWER") {
		t.Fatal("the root's notification turn did not present the child's report")
	}

	// The reported answer, not the wait line, is the root's latest turn end.
	reply, reported := root.communicatedReply()
	if !reported || strings.TrimSpace(reply) != "got the answer" {
		t.Fatalf("root's last report = %q (reported=%t), want the answer its wake produced", reply, reported)
	}
}
