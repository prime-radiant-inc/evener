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
