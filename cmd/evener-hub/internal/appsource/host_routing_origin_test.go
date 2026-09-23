package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// assertDispatchRefusal fails unless err is the typed InvalidParams refusal the
// host-routing origin guard returns, naming the origin.
func assertDispatchRefusal(t *testing.T, op string, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("%s error %T=%v, want WireError", op, err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("%s wire=%+v, want invalid params", op, wire)
	}
	if !strings.Contains(wire.Message, hostRoutingOrigin) {
		t.Fatalf("%s refusal %q does not name the origin %q", op, wire.Message, hostRoutingOrigin)
	}
}

// hostRoutingOrigin is the origin value these tests stamp, mirroring the hub's
// hostRoutingOriginBridge constant.
const hostRoutingOrigin = "bridge"

// The shared remote-dispatch seam refuses every remote-originated call the
// source serves, regardless of attach state: read, mutation, subscription,
// capability probe, and the admin forward all resolve their client through
// resolveClient, so a peer hub cannot ride an already-attached client to reach a
// second host (component 07, §"Host-routing origin guard"). The scripted client
// records what actually crossed the wire, so "nothing was sent" is asserted
// against the wire, not against the error alone.
func TestRemoteOriginatedDispatchIsRefusedAtTheClientSeam(t *testing.T) {
	client, calls := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("alpha", nil, func(context.Context, string) (*appwire.Client, error) {
		t.Error("a remote-originated dispatch reached the dialing connector")
		return nil, errors.New("the dispatch guard must refuse before any dial")
	})
	// Attached: exactly the state the dial guard does not cover.
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return client, true })

	ctx := WithHostRoutingOrigin(context.Background(), hostRoutingOrigin)

	_, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "alpha:t1"})
	assertDispatchRefusal(t, "ReadThread", err)

	_, err = source.StartThread(ctx, appwire.ThreadStartParams{CWD: "/tmp"})
	assertDispatchRefusal(t, "StartThread", err)

	err = source.SetThreadName(ctx, appwire.ThreadNameSetParams{Ref: "alpha:t1", Name: "renamed"})
	assertDispatchRefusal(t, "SetThreadName", err)

	err = source.ShutdownThread(ctx, appwire.ThreadShutdownParams{Ref: "alpha:t1"})
	assertDispatchRefusal(t, "ShutdownThread", err)

	_, err = source.HostCapabilities(ctx)
	assertDispatchRefusal(t, "HostCapabilities", err)

	_, err = source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "alpha:t1"})
	assertDispatchRefusal(t, "SubscribeThread", err)

	var out json.RawMessage
	err = source.AdminCall(ctx, appwire.MethodEvenerInstanceList, nil, &out)
	assertDispatchRefusal(t, "AdminCall", err)

	err = source.AdminMutationCall(ctx, appwire.MethodEvenerInstanceCreate, nil, &out)
	assertDispatchRefusal(t, "AdminMutationCall", err)

	// Only the harness's own initialize crossed the wire.
	if got := calls(); len(got) != 1 {
		t.Fatalf("remote-originated dispatch reached the remote: calls = %+v, want only initialize", got)
	}

	// A local (unmarked) request still dispatches over the same attached client.
	if _, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "alpha:t1"}); err != nil {
		t.Fatalf("local-originated ReadThread: %v", err)
	}
	if got := calls(); len(got) != 2 {
		t.Fatalf("local-originated read was not forwarded: calls = %+v, want initialize + thread/read", got)
	}
}
