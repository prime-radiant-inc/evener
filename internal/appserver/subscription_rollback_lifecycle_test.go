package appserver

import (
	"context"
	"testing"
)

func TestSubscriptionLifecycleUnsubscribeRawFallback(t *testing.T) {
	s := NewServer(ServerConfig{})
	c := &Connection{id: "conn", server: s}
	s.conns[c.id] = c
	s.subs.subscribeOwned(c.id, "current", "stable", false)
	s.subs.subscribeOwned(c.id, "stable", "stable", false)
	ctx := context.WithValue(t.Context(), connectionContextKey{}, c)
	UnsubscribeLifecycle(ctx, "current")
	if s.subs.IsSubscribed(c.id, "current") || !s.subs.IsSubscribed(c.id, "stable") {
		t.Fatal("unresolved unsubscribe must retain raw-key semantics")
	}
	ctx = context.WithValue(ctx, subscriptionLifecycleContextKey{}, "stable")
	UnsubscribeLifecycle(ctx, "current")
	if s.subs.IsSubscribed(c.id, "stable") {
		t.Fatal("resolved unsubscribe ignored ingress lifecycle identity")
	}
}

// Unsubscribe during the snapshot (before a finalizer exists) must reach
// subscriptions displaced by a replace capture, not just the live registry.
func TestSubscriptionDisplacedUnsubscribeBeforeFinalizer(t *testing.T) {
	s := NewSubscriptions()
	s.Subscribe("conn", "root")
	s.Subscribe("conn", "child")
	rollback := s.beginBuffered("conn", "replacement", true, 1)
	s.Unsubscribe("conn", "root")
	if !s.withdrawBuffered("conn", "replacement", 1, rollback) {
		t.Fatal("withdraw failed")
	}
	if s.IsSubscribed("conn", "root") {
		t.Error("abort resurrected unsubscribed displaced root")
	}
	if !s.IsSubscribed("conn", "child") {
		t.Error("abort lost unrelated child")
	}
	s.Subscribe("conn", "root")
	if !s.IsSubscribed("conn", "root") {
		t.Error("later legitimate subscription rejected")
	}
}

func TestSubscriptionLifecycleWithdrawsDisplacedAliases(t *testing.T) {
	for _, replace := range []bool{false, true} {
		s := NewSubscriptions()
		s.subscribeOwned("conn", "stable", "root", false)
		s.subscribeOwned("conn", "current", "root", false)
		s.subscribeOwned("conn", "child", "child", false)
		s.subscribeOwned("other", "stable", "root", false)
		rollback := s.beginBuffered("conn", "current", replace, 1, "root")
		// No response finalizer has been installed: only the generation itself
		// owns the displaced subscriptions at this point.
		s.UnsubscribeLifecycle("conn", "root")
		if !s.withdrawBuffered("conn", "current", 1, rollback) {
			t.Fatal("withdraw failed")
		}
		for _, key := range []string{"stable", "current"} {
			if s.IsSubscribed("conn", key) {
				t.Fatalf("replace=%v restored canceled alias %s", replace, key)
			}
		}
		if !s.IsSubscribed("conn", "child") || !s.IsSubscribed("other", "stable") {
			t.Fatal("unsubscribe removed unrelated ownership")
		}
		s.subscribeOwned("conn", "current", "root", false)
		if !s.IsSubscribed("conn", "current") {
			t.Fatal("old withdrawal poisoned later registration")
		}
		if s.withdrawBuffered("conn", "current", 1, rollback) || !s.IsSubscribed("conn", "current") {
			t.Fatal("old generation modified new subscription")
		}
	}
}

func TestSubscriptionLifecycleAbortPreservesDifferentOwner(t *testing.T) {
	for _, replace := range []bool{false, true} {
		s := NewSubscriptions()
		s.subscribeOwned("conn", "delivery", "child", false)
		rollback := s.beginBuffered("conn", "delivery", replace, 1, "root")
		s.UnsubscribeLifecycle("conn", "root")
		if !s.withdrawBuffered("conn", "delivery", 1, rollback) {
			t.Fatal("withdraw failed")
		}
		if !s.IsSubscribed("conn", "delivery") {
			t.Errorf("replace=%v: abort lost displaced child ownership", replace)
		}
	}
}
