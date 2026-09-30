package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"primeradiant.com/evener/agent/events"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSessionActivityPublicRoutes(t *testing.T) {
	for _, method := range []string{
		"evener/thread/activity/read", "evener/thread/delegates/list",
		"evener/thread/jobs/list", "evener/thread/watches/list",
	} {
		t.Run(method, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			s.SetAppIdentity("local", "root")
			_, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{
				Method: method, Params: json.RawMessage(`{"ref":"local:root"}`),
			})
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("registered read without a session hook = %v; want unavailable", err)
			}
		})
	}
}

func TestSessionActivityLogicalReceiverInvalidation(t *testing.T) {
	for _, descendant := range []bool{false, true} {
		t.Run(fmt.Sprint(descendant), func(t *testing.T) {
			s := NewServer(ServerConfig{})
			s.SetAppIdentity("local", "root")
			event := events.SessionEvent{Kind: events.EventSessionActivityChanged, SessionID: "root", Data: events.SessionActivityChangedData{ThreadID: "root", SessionID: "root", Ref: "local:root", Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceSummary, appwire.SessionActivityResourceWatches}}}
			if descendant {
				event.SessionID = "child"
				s.RecordDescendantAppEvent("root", event)
			} else {
				s.RecordAppEvent(event)
			}
			changes := []appwire.SessionActivityChangedParams{}
			for _, record := range s.AppNotificationsAfter(0, "root") {
				if record.Notification.Method == appwire.NotifyEvenerThreadActivityChanged {
					var params appwire.SessionActivityChangedParams
					if err := json.Unmarshal(record.Notification.Params, &params); err != nil {
						t.Fatal(err)
					}
					changes = append(changes, params)
				}
			}
			if len(changes) != 1 || changes[0].ThreadID != "root" || changes[0].Ref != "local:root" || changes[0].SessionID != "root" || len(changes[0].Resources) != 2 {
				t.Fatalf("logical receiver changes = %+v", changes)
			}
			for _, record := range s.AppNotificationsAfter(0, "child") {
				if record.Notification.Method == appwire.NotifyEvenerThreadActivityChanged {
					t.Fatal("receiver watch invalidation was also sent to physical observer")
				}
			}
			s.SetAppIdentity("local", "replacement")
			s.RecordDescendantAppEvent("root", event)
			for _, record := range s.AppNotificationsAfter(0, "replacement") {
				if record.Notification.Method == appwire.NotifyEvenerThreadActivityChanged {
					t.Fatal("replaced tree's activity reached new root")
				}
			}
		})
	}
}

func TestSessionActivityContextAndChildRef(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "root")
	type requestMarker struct{}
	marker := &struct{}{}
	ctx := context.WithValue(t.Context(), requestMarker{}, marker)
	s.SetThreadActivityReadFunc(func(got context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		if got.Value(requestMarker{}) != marker || params.Ref != "local:child" || params.Scope != appwire.SessionActivityScopeSubtree {
			t.Fatalf("hook context/ref = %v,%+v", got, params)
		}
		return appwire.SessionActivitySummary{Context: appwire.SessionActivityContext{SessionID: "child"}}, nil
	})
	result, err := s.AppServer().Router().Dispatch(ctx, appwire.Request{Method: appwire.MethodEvenerThreadActivityRead, Params: json.RawMessage(`{"ref":"local:child","scope":"subtree"}`)})
	if err != nil || result.(appwire.SessionActivitySummary).Context.SessionID != "child" {
		t.Fatalf("child route = %+v,%v", result, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.AppServer().Router().Dispatch(canceled, appwire.Request{Method: appwire.MethodEvenerThreadActivityRead, Params: json.RawMessage(`{"ref":"local:child"}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
}

func TestSessionActivitySubtreeInvalidationPreservesAffectedOwner(t *testing.T) {
	s := NewServer(ServerConfig{})
	prepared, err := PrepareAppIdentityForRef("local", "root", "local:workspace", "")
	if err != nil {
		t.Fatal(err)
	}
	s.ReplaceAppIdentity(prepared, nil)
	for _, target := range []string{"child", "root"} {
		s.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionActivityChanged, SessionID: "grandchild", Data: events.SessionActivityChangedData{ThreadID: target, Ref: "local:" + target, SessionID: "child", Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceSummary, appwire.SessionActivityResourceJobs}}})
	}
	for _, target := range []string{"child", "root"} {
		t.Run(target, func(t *testing.T) {
			changes := []appwire.SessionActivityChangedParams{}
			for _, record := range s.AppNotificationsAfter(0, target) {
				if record.Notification.Method == appwire.NotifyEvenerThreadActivityChanged {
					var change appwire.SessionActivityChangedParams
					if err := json.Unmarshal(record.Notification.Params, &change); err != nil {
						t.Fatal(err)
					}
					changes = append(changes, change)
				}
			}
			wantRef := "local:" + target
			if target == "root" {
				wantRef = "local:workspace"
			}
			if len(changes) != 1 || changes[0].ThreadID != target || changes[0].Ref != wantRef || changes[0].SessionID != "child" {
				t.Fatalf("%s subtree routing = %+v", target, changes)
			}
		})
	}
	var cut uint64
	for _, record := range s.AppNotificationsAfter(0, "root") {
		cut = max(cut, record.Seq)
	}
	prepared, err = PrepareAppIdentityForRef("local", "replacement", "local:workspace", "")
	if err != nil {
		t.Fatal(err)
	}
	s.ReplaceAppIdentity(prepared, nil)
	s.RecordDescendantAppEvent("root", events.SessionEvent{Kind: events.EventSessionActivityChanged, SessionID: "grandchild", Data: events.SessionActivityChangedData{ThreadID: "root", Ref: "local:root", SessionID: "child", Resources: []appwire.SessionActivityResource{appwire.SessionActivityResourceJobs}}})
	for _, record := range s.AppNotificationsAfter(cut, "replacement") {
		if record.Notification.Method == appwire.NotifyEvenerThreadActivityChanged {
			t.Fatal("old subtree event reached replacement workspace")
		}
	}
}
