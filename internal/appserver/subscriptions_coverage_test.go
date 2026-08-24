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
