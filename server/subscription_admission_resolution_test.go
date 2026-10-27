package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// requestOutcome reduces a request error to the wire code the client saw, or 0
// for success, so the table below asserts dispositions and never copy.
func requestOutcome(err error) int {
	if err == nil {
		return 0
	}
	var wire appwire.WireError
	if errors.As(err, &wire) {
		return wire.Code
	}
	return -1
}

// The local subscription-admission resolver decides, per thread/read and
// thread/unsubscribe, whether the request is a subscribe against a thread this
// daemon serves. These cases pin what each input does to the request and to the
// subscription it may install.
func TestLocalSubscriptionAdmissionDispositions(t *testing.T) {
	cases := []struct {
		name           string
		method         string
		params         any
		wantOutcome    int
		wantSubscribed int
	}{
		{"subscribing read of the served thread installs a subscription", appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}, 0, 1},
		{"non-subscribing read installs nothing", appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}, 0, 0},
		{"subscribing read of an unknown thread is refused as unavailable", appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:other", Subscribe: true}, appwire.CodeUnavailable, 0},
		{"undecodable read params are an invalid request", appwire.MethodThreadRead, json.RawMessage(`"not an object"`), appwire.CodeInvalidParams, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_1")
			installProjectedMutationCallbacksForTest(srv, RetrySafeTurnFunctions{})
			client := dialServerAppWire(t, srv)
			err := client.Request(context.Background(), tc.method, tc.params, nil)
			if got := requestOutcome(err); got != tc.wantOutcome {
				t.Fatalf("outcome = %d (%v), want %d", got, err, tc.wantOutcome)
			}
			if got := srv.AppSubscriberCount("th_1"); got != tc.wantSubscribed {
				t.Fatalf("subscribers = %d, want %d", got, tc.wantSubscribed)
			}
		})
	}
}

func TestLocalSubscriptionAdmissionUnsubscribe(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	installProjectedMutationCallbacksForTest(srv, RetrySafeTurnFunctions{})
	client := dialServerAppWire(t, srv)
	ctx := context.Background()
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// An unsubscribe naming a thread this daemon does not serve succeeds and
	// leaves the live subscription alone.
	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:other"}); err != nil {
		t.Fatalf("unknown unsubscribe: %v", err)
	}
	if got := srv.AppSubscriberCount("th_1"); got != 1 {
		t.Fatalf("subscribers after unknown unsubscribe = %d, want 1", got)
	}

	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if got := srv.AppSubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscribers after unsubscribe = %d, want 0", got)
	}

	err := client.Request(ctx, appwire.MethodThreadUnsubscribe, json.RawMessage(`"not an object"`), nil)
	if got := requestOutcome(err); got != appwire.CodeInvalidParams {
		t.Fatalf("undecodable unsubscribe outcome = %d (%v), want invalid params", got, err)
	}
}
