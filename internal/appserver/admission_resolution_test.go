package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSubscriptionAdmissionResolverPanicIsContained(t *testing.T) {
	s := NewServer(ServerConfig{SubscriptionAdmissionResolver: func(appwire.Message) (string, bool) {
		panic("resolver boom")
	}})
	c := s.NewConnection("panic")
	c.setInitialized()
	msg := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadRead, json.RawMessage(`{"subscribe":true}`))
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("resolver panic escaped ordered dispatch: %v", recovered)
		}
	}()
	c.executeOrdered(context.Background(), msg)
	response := <-c.send
	if response.Error == nil || response.Error.Error.Code != appwire.CodeInternalError {
		t.Fatalf("resolver panic response = %+v, want internal error", response)
	}
	if len(c.pendingAdmissions) != 0 || len(s.subs.byConn[c.id]) != 0 {
		t.Fatal("resolver panic caused admission or subscription side effects")
	}
}

func TestUnresolvedSubscribingReadFailsClosed(t *testing.T) {
	s := NewServer(ServerConfig{SubscriptionAdmissionResolver: func(appwire.Message) (string, bool) {
		return "", false
	}})
	c := s.NewConnection("unresolved")
	c.setInitialized()
	HandleTyped(s.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		Subscribe(ctx, "thread")
		return appwire.ThreadReadResponse{}, nil
	})
	msg := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadRead, json.RawMessage(`{"subscribe":true}`))
	c.executeOrdered(context.Background(), msg)
	response := <-c.send
	if response.Error == nil || response.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("unresolved subscribe response = %+v, want unavailable", response)
	}
	if s.subs.IsSubscribed(c.id, "thread") {
		t.Fatal("unresolved subscribe installed a subscription")
	}
}

func TestInvalidSubscribingReadPreservesInvalidParams(t *testing.T) {
	s := NewServer(ServerConfig{SubscriptionAdmissionResolver: func(appwire.Message) (string, bool) {
		return "", false
	}})
	c := s.NewConnection("invalid")
	c.setInitialized()
	HandleTyped(s.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, nil
	})
	msg := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadRead, json.RawMessage(`{"subscribe":true,"threadId":123}`))
	c.executeOrdered(context.Background(), msg)
	response := <-c.send
	if response.Error == nil || response.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("invalid subscribing read response = %+v, want invalid params", response)
	}
}

func TestUnresolvedSubscriptionUsesSessionUnavailableMetadata(t *testing.T) {
	s := NewServer(ServerConfig{SubscriptionAdmissionResolver: func(appwire.Message) (string, bool) { return "", false }})
	c := s.NewConnection("metadata")
	c.setInitialized()
	msg := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadRead, json.RawMessage(`{"subscribe":true}`))
	c.executeOrdered(context.Background(), msg)
	response := <-c.send
	if response.Error == nil || response.Error.Error.Data.(appwire.ErrorData).EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("unresolved metadata = %+v, want session unavailable", response)
	}
}

func TestUnresolvedUnsubscribeCancelsCanonicalAndDeliveryAdmissions(t *testing.T) {
	s := NewServer(ServerConfig{SubscriptionAdmissionResolverV2: func(appwire.Message) SubscriptionAdmissionResolution {
		return SubscriptionAdmissionResolution{Key: "local:delivery", SecondaryKey: "local:lifecycle", Intent: SubscriptionAdmissionUnresolved}
	}})
	c := s.NewConnection("dual")
	c.setInitialized()
	canonical := c.beginSubscriptionAdmission("read", "local:lifecycle")
	delivery := c.beginSubscriptionAdmission("other", "local:delivery")
	msg := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadUnsubscribe, json.RawMessage(`{"ref":"local:delivery"}`))
	c.executeOrdered(context.Background(), msg)
	c.admissionMu.Lock()
	canonicalCanceled := canonical.canceled
	deliveryCanceled := delivery.canceled
	c.admissionMu.Unlock()
	if !canonicalCanceled || !deliveryCanceled {
		t.Fatal("unresolved unsubscribe did not cancel admissions under either identity")
	}
}

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
