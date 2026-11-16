package appserver

import (
	"testing"
	"time"
)

// TestSubscriptionsDepartureWakesTheLastSubscriberDrop pins the signal a relay
// waits on instead of polling: the change that takes a thread's live interest
// to zero must wake its waiters, so an idle thread is noticed when it happens
// rather than on the next poll.
func TestSubscriptionsDepartureWakesTheLastSubscriberDrop(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")

	departed := subs.Departed()
	select {
	case <-departed:
		t.Fatal("a subscribe woke the departure signal")
	default:
	}

	subs.Unsubscribe("conn-1", "th_1")
	select {
	case <-departed:
	case <-time.After(time.Second):
		t.Fatal("the last subscriber's departure did not wake the departure signal")
	}
}

// A connection going away is a departure too, and so is a withdrawn entry left
// in place by a capture: both take the live count to zero, and a relay that
// only learned of removals would keep the captured thread's relay alive.
func TestSubscriptionsDepartureCoversEveryCountDrop(t *testing.T) {
	cases := []struct {
		name  string
		drop  func(subs *Subscriptions)
		setup func(subs *Subscriptions)
	}{
		{
			name: "connection removed",
			drop: func(subs *Subscriptions) { subs.RemoveConnection("conn-1") },
		},
		{
			name: "lifecycle withdrawn",
			drop: func(subs *Subscriptions) { subs.UnsubscribeLifecycle("conn-1", "life") },
			setup: func(subs *Subscriptions) {
				subs.subscribeOwned("conn-1", "th_1", "life", false)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subs := NewSubscriptions()
			if tc.setup != nil {
				tc.setup(subs)
			} else {
				subs.Subscribe("conn-1", "th_1")
			}
			if got := subs.ConnectionCount("th_1"); got != 1 {
				t.Fatalf("setup: live interest = %d, want 1", got)
			}
			departed := subs.Departed()
			tc.drop(subs)
			if got := subs.ConnectionCount("th_1"); got != 0 {
				t.Fatalf("after the drop: live interest = %d, want 0", got)
			}
			select {
			case <-departed:
			case <-time.After(time.Second):
				t.Fatal("the drop to zero live interest did not wake the departure signal")
			}
		})
	}
}

// One close serves every waiter, and a later departure wakes them again: a
// single shared channel that closed once would only ever signal the first
// departure.
func TestSubscriptionsDepartureSignalsEachDrop(t *testing.T) {
	subs := NewSubscriptions()
	subs.Subscribe("conn-1", "th_1")
	subs.Subscribe("conn-2", "th_2")

	first := subs.Departed()
	subs.Unsubscribe("conn-1", "th_1")
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("the first departure did not wake its waiter")
	}

	// A waiter reading after the first drop must still see the second.
	second := subs.Departed()
	select {
	case <-second:
		t.Fatal("a departure was signalled before it happened")
	default:
	}
	subs.Unsubscribe("conn-2", "th_2")
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("the second departure did not wake a waiter that arrived in between")
	}
}
