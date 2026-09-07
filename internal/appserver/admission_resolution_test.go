package appserver

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestResolvedAdmissionMismatchPreservesOwnership(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		s := NewServer(ServerConfig{})
		c := s.NewConnection("conn")
		s.registerConnection(c)
		t.Cleanup(func() { s.unregisterConnection(c) })
		ctx := context.WithValue(t.Context(), connectionContextKey{}, c)
		ctx = context.WithValue(ctx, requestIDContextKey{}, "keeper")
		if !CaptureSubscription(ctx, false, func() string { return "keeper" }, func() uint64 { return 0 }, func() bool { return true }) {
			t.Fatal("keeper capture")
		}
		keeper := c.hydrations["keeper"]
		ctx = context.WithValue(ctx, requestIDContextKey{}, "read")
		admission := c.beginSubscriptionAdmission("read", "old-owner")
		if immediate {
			err := SubscribeWithAdmission(ctx, "new-delivery", true, "new-owner")
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("immediate mismatch=%v", err)
			}
		} else {
			captured := CaptureSubscription(ctx, true, nil, func() uint64 { return 0 }, func() bool { t.Error("mismatch reached materialization"); return true }, func() SubscriptionTarget {
				if s.projectionMu.TryLock() {
					s.projectionMu.Unlock()
					t.Error("resolution outside projection gate")
				}
				if s.deliveryMu.TryLock() {
					s.deliveryMu.Unlock()
					t.Error("resolution outside delivery gate")
				}
				if !c.admissionMu.TryLock() {
					t.Error("resolution under admission lock")
				} else {
					c.admissionMu.Unlock()
				}
				return SubscriptionTarget{ThreadID: "new-delivery", LifecycleKey: "new-owner"}
			})
			if captured {
				t.Fatal("capture accepted mismatched owner")
			}
		}
		if c.hydrations["keeper"] != keeper || !s.subs.IsSubscribed(c.id, "keeper") {
			t.Fatal("mismatch superseded keeper hydration")
		}
		if s.subs.IsSubscribed(c.id, "new-delivery") {
			t.Fatal("mismatch installed delivery")
		}
		c.admissionMu.Lock()
		_, pending := c.pendingAdmissions[admission]
		unchanged := admission.threadID == "old-owner"
		c.admissionMu.Unlock()
		if !pending || !unchanged {
			t.Fatal("mismatch consumed or changed ingress admission")
		}
	}
}

func TestResolvedImmediateAdmissionOptionalCompatibility(t *testing.T) {
	s := NewServer(ServerConfig{})
	c := s.NewConnection("conn")
	s.registerConnection(c)
	defer s.unregisterConnection(c)
	ctx := context.WithValue(t.Context(), connectionContextKey{}, c)
	// No admission: explicit expected owner does not change the default A=D.
	if err := SubscribeWithAdmission(ctx, "delivery", false, "different"); err != nil {
		t.Fatal(err)
	}
	if got := s.subs.byConn[c.id]["delivery"].lifecycleKey; got != "delivery" {
		t.Fatalf("default owner=%s", got)
	}
	ctx = context.WithValue(ctx, requestIDContextKey{}, "read")
	admission := c.beginSubscriptionAdmission("read", "owner")
	if err := SubscribeWithAdmission(ctx, "alias", false); err != nil {
		t.Fatal(err)
	}
	if _, pending := c.pendingAdmissions[admission]; pending {
		t.Fatal("successful generic registration did not consume admission")
	}
	if got := s.subs.byConn[c.id]["alias"].lifecycleKey; got != "owner" {
		t.Fatalf("admission owner=%s", got)
	}
}
