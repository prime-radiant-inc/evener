package appserver

import "testing"

func TestSubscriptionsTrackThreadsByConnection(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-1", "th_2")

	threads := subs.Threads("conn-1")
	if len(threads) != 2 {
		t.Fatalf("threads=%+v", threads)
	}
	if !subs.IsSubscribed("conn-1", "th_1") || !subs.IsSubscribed("conn-1", "th_2") {
		t.Fatalf("subscription missing")
	}
}

func TestSubscriptionsRemoveConnection(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-2", "th_1")
	subs.RemoveConnection("conn-1")

	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatalf("conn-1 still subscribed")
	}
	if !subs.IsSubscribed("conn-2", "th_1") {
		t.Fatalf("conn-2 subscription removed")
	}
}

func TestSubscriptionsUnsubscribe(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-1", "th_2")
	subs.Subscribe("conn-2", "th_1")

	subs.Unsubscribe("conn-1", "th_1")

	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatalf("conn-1 still subscribed to th_1")
	}
	if !subs.IsSubscribed("conn-1", "th_2") {
		t.Fatalf("conn-1 lost its other thread")
	}
	if !subs.IsSubscribed("conn-2", "th_1") {
		t.Fatalf("conn-2 subscription removed")
	}
	if subs.ConnectionCount("th_1") != 1 {
		t.Fatalf("th_1 connection count = %d, want 1 (only conn-2)", subs.ConnectionCount("th_1"))
	}
}

func TestSubscriptionsUnsubscribeIdempotent(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Unsubscribe("conn-1", "th_1")
	subs.Unsubscribe("conn-1", "th_1") // already gone; must not panic or re-add
	subs.Unsubscribe("conn-2", "th_1") // never subscribed
	subs.Unsubscribe("conn-1", "th_x") // never subscribed

	if subs.IsSubscribed("conn-1", "th_1") {
		t.Fatalf("conn-1 still subscribed")
	}
	if subs.ConnectionCount("th_1") != 0 || subs.ConnectionCount("th_x") != 0 {
		t.Fatalf("thread maps kept entries: th_1=%d th_x=%d", subs.ConnectionCount("th_1"), subs.ConnectionCount("th_x"))
	}
	if got := subs.Threads("conn-1"); len(got) != 0 {
		t.Fatalf("conn map kept entries: %+v", got)
	}
}

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

// A capture's commit-side calls name the generation that began it; a call for
// any other generation, or for a thread the connection does not hold, is
// refused so a superseded capture cannot cut or release a newer one's buffer.
func TestSubscriptionsCaptureCallsRefuseAStaleGeneration(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(subs *Subscriptions) bool
	}{
		{"release of another generation", func(subs *Subscriptions) bool {
			_, ok := subs.Release("conn-1", "th_1", 99)
			return ok
		}},
		{"cut of another generation", func(subs *Subscriptions) bool { return subs.SetCut("conn-1", "th_1", 99, 5) }},
		{"cut of an unheld thread", func(subs *Subscriptions) bool { return subs.SetCut("conn-1", "th_2", 1, 5) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subs := NewSubscriptions()
			subs.Subscribe("conn-1", "th_1")
			subs.beginBuffered("conn-1", "th_1", false, 1)
			if tc.call(subs) {
				t.Fatal("call succeeded for a generation that does not hold the buffer")
			}
		})
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

func TestSubscriptionsUnsubscribeDuringCaptureThenCommitDropsEntry(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.beginBuffered("conn-1", "th_1", false, 7)

	// Routed before the drop, so the record sits in the entry's buffer past
	// its cut and only Release's withdrawal handling keeps it undelivered.
	subs.Route(SequencedNotification{Seq: 9, ThreadID: "th_1"})
	subs.Unsubscribe("conn-1", "th_1")
	records, ok := subs.Release("conn-1", "th_1", 7)
	if !ok {
		t.Fatal("Release should succeed for the live generation")
	}
	if len(records) != 0 {
		t.Fatalf("commit delivered buffered records for a withdrawn thread: %#v", records)
	}
	if subs.byConn["conn-1"]["th_1"] != nil {
		t.Fatal("commit left the withdrawn entry in the registry")
	}
	if got := subs.ConnectionCount("th_1"); got != 0 {
		t.Fatalf("subscriber count after commit = %d, want 0", got)
	}
}

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

func TestSubscriptionsConnectionCount(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-2", "th_1")
	if count := subs.ConnectionCount("th_1"); count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
}

func TestSubscriptionsRouteNoSubscribers(t *testing.T) {
	subs := NewSubscriptions()
	live := subs.Route(SequencedNotification{Seq: 1, ThreadID: "th_1"})
	if len(live) != 0 {
		t.Fatalf("expected no live for unknown thread, got %v", live)
	}
}
