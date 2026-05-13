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
