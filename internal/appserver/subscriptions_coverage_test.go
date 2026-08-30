package appserver

import (
	"testing"
)

func TestSubscriptionsRouteBuffersBufferingAndReturnsLive(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")

	// Buffering subscription
	rollback := subs.beginBuffered("conn-1", "th_1", false, 1)
	if rollback.replace {
		t.Fatal("non-replace rollback should have replace=false")
	}

	rec := SequencedNotification{Seq: 1, ThreadID: "th_1"}
	live := subs.Route(rec)
	if len(live) != 0 {
		t.Fatalf("buffering subscription should not return live, got %v", live)
	}

	// Release should return the buffered record (above cut 0)
	released, ok := subs.Release("conn-1", "th_1", 1)
	if !ok {
		t.Fatal("Release should succeed")
	}
	if len(released) != 1 || released[0].Seq != 1 {
		t.Fatalf("expected 1 released record with seq 1, got %+v", released)
	}
}

func TestSubscriptionsReleaseWithCut(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.beginBuffered("conn-1", "th_1", false, 1)

	// Buffer records
	subs.Route(SequencedNotification{Seq: 1, ThreadID: "th_1"})
	subs.Route(SequencedNotification{Seq: 2, ThreadID: "th_1"})
	subs.Route(SequencedNotification{Seq: 3, ThreadID: "th_1"})

	// Set cut at 1: only records above 1 should be released
	if !subs.SetCut("conn-1", "th_1", 1, 1) {
		t.Fatal("SetCut should succeed")
	}

	released, ok := subs.Release("conn-1", "th_1", 1)
	if !ok {
		t.Fatal("Release should succeed")
	}
	if len(released) != 2 {
		t.Fatalf("expected 2 released records (seq > 1), got %d", len(released))
	}
}

func TestSubscriptionsReleaseWrongGeneration(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.beginBuffered("conn-1", "th_1", false, 1)

	_, ok := subs.Release("conn-1", "th_1", 99)
	if ok {
		t.Fatal("Release with wrong generation should fail")
	}
}

func TestSubscriptionsSetCutWrongGeneration(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.beginBuffered("conn-1", "th_1", false, 1)

	if subs.SetCut("conn-1", "th_1", 99, 5) {
		t.Fatal("SetCut with wrong generation should return false")
	}
}

func TestSubscriptionsSetCutNoSubscription(t *testing.T) {
	subs := NewSubscriptions()
	if subs.SetCut("conn-1", "th_1", 1, 5) {
		t.Fatal("SetCut with no subscription should return false")
	}
}

func TestSubscriptionsWithdrawBufferedNonReplace(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	rollback := subs.beginBuffered("conn-1", "th_1", false, 2)

	if !subs.withdrawBuffered("conn-1", "th_1", 2, rollback) {
		t.Fatal("withdrawBuffered should succeed")
	}
	// After withdraw, the original subscription should be restored
	if !subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("subscription should be restored after withdraw")
	}
}

func TestSubscriptionsWithdrawBufferedWrongGeneration(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	rollback := subs.beginBuffered("conn-1", "th_1", false, 1)

	if subs.withdrawBuffered("conn-1", "th_1", 99, rollback) {
		t.Fatal("withdrawBuffered with wrong generation should fail")
	}
}

func TestSubscriptionsWithdrawBufferedReplace(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-1", "th_2")
	rollback := subs.beginBuffered("conn-1", "th_3", true, 1)

	if !subs.withdrawBuffered("conn-1", "th_3", 1, rollback) {
		t.Fatal("withdrawBuffered with replace should succeed")
	}
	// After replace-withdraw, original subscriptions should be restored
	if !subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("th_1 should be restored after replace withdraw")
	}
	if !subs.IsSubscribed("conn-1", "th_2") {
		t.Fatal("th_2 should be restored after replace withdraw")
	}
}

// A mid-capture unsubscribe against the buffering entry of a replace-capture
// must not strand the capture's rollback snapshot: the abort still restores
// the connection's other subscriptions, minus exactly the thread the client
// dropped. Regression test for the shape where Unsubscribe removing the
// buffering entry made withdrawBuffered bail at current==nil and silently
// dropped every other subscription.
func TestSubscriptionsUnsubscribeDuringReplaceCaptureDefersToAbort(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-1", "th_2")
	rollback := subs.beginBuffered("conn-1", "th_3", true, 1)

	// The client unsubscribes the thread the capture is hydrating, mid-flight.
	subs.Unsubscribe("conn-1", "th_3")

	// The raw buffering entry survives until the capture resolves (IsSubscribed
	// already reports the withdrawal).
	if subs.byConn["conn-1"]["th_3"] == nil {
		t.Fatal("mid-capture unsubscribe removed the buffering entry")
	}
	// The capture aborts (its read failed): the displaced snapshot comes back
	// minus the dropped thread.
	if !subs.withdrawBuffered("conn-1", "th_3", 1, rollback) {
		t.Fatal("withdrawBuffered with replace should succeed after a mid-capture unsubscribe")
	}
	if !subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("th_1 should be restored after the aborted capture")
	}
	if !subs.IsSubscribed("conn-1", "th_2") {
		t.Fatal("th_2 should be restored after the aborted capture")
	}
	if subs.IsSubscribed("conn-1", "th_3") {
		t.Fatal("th_3 should stay dropped: the client explicitly unsubscribed it")
	}
	// A later capture's abort must not skip th_1/th_2 because of the spent
	// withdrawn record.
	subs.Subscribe("conn-1", "th_2")
	subs.Unsubscribe("conn-1", "th_2")
	if subs.IsSubscribed("conn-1", "th_2") {
		t.Fatal("th_2 should be unsubscribed normally once no capture holds it")
	}
}

// The non-replace counterpart: a mid-capture unsubscribe defers to the
// generation's abort, which restores the previous subscription for that
// thread minus the client's drop.
func TestSubscriptionsUnsubscribeDuringNonReplaceCaptureDefersToAbort(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	rollback := subs.beginBuffered("conn-1", "th_1", false, 3)

	subs.Unsubscribe("conn-1", "th_1")
	if subs.byConn["conn-1"]["th_1"] == nil {
		t.Fatal("mid-capture unsubscribe removed the buffering entry")
	}
	// Every live-interest query agrees the drop already happened.
	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("IsSubscribed still reports a thread the client unsubscribed mid-capture")
	}
	for _, thread := range subs.Threads("conn-1") {
		if thread == "th_1" {
			t.Fatal("Threads still enumerates a thread the client unsubscribed mid-capture")
		}
	}
	if !subs.withdrawBuffered("conn-1", "th_1", 3, rollback) {
		t.Fatal("withdrawBuffered should succeed after a mid-capture unsubscribe")
	}
	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("th_1 should stay dropped: the client explicitly unsubscribed it")
	}
}

// A committed capture honors the mid-capture unsubscribe: the client dropped
// the thread after the capture began, so the commit removes the entry instead
// of resurrecting a subscription the client no longer holds, releases none of
// the buffered records, and consumes the withdrawal so later captures behave
// normally. Regression test for the wire-level flake where a serial
// unsubscribe landing between a subscribed read's capture and its
// release-commit was silently discarded
// (TestServerAppWireThreadUnsubscribeResolvesStableRefAcrossSwap).
func TestSubscriptionsUnsubscribeDuringCaptureThenCommit(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-1", "th_2")
	subs.beginBuffered("conn-1", "th_3", true, 4)

	subs.Unsubscribe("conn-1", "th_3")
	// The drop is visible immediately: the lingering buffering entry is
	// capture bookkeeping, not a live interest, so it must not hold the
	// relay open while the commit is still pending.
	if got := subs.ConnectionCount("th_3"); got != 0 {
		t.Fatalf("connection count after mid-capture unsubscribe = %d, want 0", got)
	}
	subs.Route(SequencedNotification{Seq: 9, ThreadID: "th_3"})
	records, ok := subs.Release("conn-1", "th_3", 4)
	if !ok {
		t.Fatal("Release should succeed for the live generation")
	}
	if len(records) != 0 {
		t.Fatalf("released records = %#v, want none: the client unsubscribed mid-capture", records)
	}
	if subs.IsSubscribed("conn-1", "th_3") {
		t.Fatal("committed capture resurrected a subscription the client dropped mid-capture")
	}
	// The withdrawal is consumed: a re-subscribe works, and a LATER capture
	// that displaces th_3 must have it restored on its abort.
	subs.Subscribe("conn-1", "th_3")
	rollback2 := subs.beginBuffered("conn-1", "th_4", true, 5)
	subs.Unsubscribe("conn-1", "th_4")
	if !subs.withdrawBuffered("conn-1", "th_4", 5, rollback2) {
		t.Fatal("second capture's abort should succeed")
	}
	if !subs.IsSubscribed("conn-1", "th_3") {
		t.Fatal("the spent withdrawn record suppressed restoring th_3 on the later capture's abort")
	}
	if subs.IsSubscribed("conn-1", "th_4") {
		t.Fatal("th_4 should stay dropped: the client explicitly unsubscribed it mid-capture")
	}
}

func TestSubscriptionsReplaceConnectionSubscriptions(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-1", "th_2")

	subs.ReplaceConnectionSubscriptions("conn-1", "th_3")
	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("th_1 should be removed by replace")
	}
	if subs.IsSubscribed("conn-1", "th_2") {
		t.Fatal("th_2 should be removed by replace")
	}
	if !subs.IsSubscribed("conn-1", "th_3") {
		t.Fatal("th_3 should be subscribed after replace")
	}
}

func TestSubscriptionsReplaceConnectionSubscriptionsEmptyThread(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")

	subs.ReplaceConnectionSubscriptions("conn-1", "")
	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("th_1 should be removed by replace with empty thread")
	}
}

func TestSubscriptionsConnections(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-2", "th_1")
	conns := subs.Connections("th_1")
	if len(conns) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(conns))
	}
}

func TestSubscriptionsConnectionCount(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-2", "th_1")
	if count := subs.ConnectionCount("th_1"); count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
}

func TestSubscriptionsRouteLiveDelivery(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	live := subs.Route(SequencedNotification{Seq: 1, ThreadID: "th_1"})
	if len(live) != 1 || live[0] != "conn-1" {
		t.Fatalf("expected conn-1 in live, got %v", live)
	}
}

func TestSubscriptionsRouteNoSubscribers(t *testing.T) {
	subs := NewSubscriptions()
	live := subs.Route(SequencedNotification{Seq: 1, ThreadID: "th_1"})
	if len(live) != 0 {
		t.Fatalf("expected no live for unknown thread, got %v", live)
	}
}

func TestSubscriptionsThreadsEmpty(t *testing.T) {
	subs := NewSubscriptions()
	threads := subs.Threads("conn-1")
	if len(threads) != 0 {
		t.Fatalf("expected 0 threads for unknown connection, got %v", threads)
	}
}

func TestSubscriptionsIsSubscribedFalse(t *testing.T) {
	subs := NewSubscriptions()
	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatal("unknown subscription should return false")
	}
}
