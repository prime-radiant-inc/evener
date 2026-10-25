package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
)

// assertSessionUnavailableForHost fails unless err is the typed unavailable
// error the fleet view's auto-resume/refusal gates act on, naming host.
func assertSessionUnavailableForHost(t *testing.T, op, host string, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("%s error %T=%v, want WireError", op, err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("%s: wire=%+v", op, wire)
	}
	if !strings.Contains(wire.Message, host) {
		t.Fatalf("%s message %q does not name host %q", op, wire.Message, host)
	}
}

// A RemoteHubSource whose host is not attached must refuse every direct call
// with the typed unavailable error instead of resolving through the dialing
// connector: component 05, §"Every other remote call is non-dialing" and
// component 06 acceptance criterion 13 (Ensure called zero times, no transport
// opened). The attached-only lookup is the client resolver; the wired
// RemoteHubClientFunc is a fallback for tests only.
func TestRemoteHubSourceUnattachedCallIsUnavailableWithoutDialing(t *testing.T) {
	var dials atomic.Int64
	dial := func(context.Context, string) (*appwire.Client, error) {
		dials.Add(1)
		return nil, errors.New("the attached-only path must never dial")
	}
	source := NewRemoteHubSource("alpha", nil, dial)
	source.SetHostClientIfAttached(func(host string) (*appwire.Client, bool) {
		if host != "alpha" {
			t.Fatalf("attached-only lookup for %q, want alpha", host)
		}
		return nil, false
	})
	ctx := context.Background()

	_, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "alpha:t1"})
	assertSessionUnavailableForHost(t, "ReadThread", "alpha", err)

	_, err = source.StartThread(ctx, appwire.ThreadStartParams{CWD: "/tmp"})
	assertSessionUnavailableForHost(t, "StartThread", "alpha", err)

	// SubscribeThread is the notification rebind path: it must read the fresh
	// client through the attached-only lookup so a rebind on a detach edge never
	// re-dials a dormant host (component 05, §"One notification consumer per
	// client").
	_, err = source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "alpha:t1"})
	assertSessionUnavailableForHost(t, "SubscribeThread", "alpha", err)

	if got := dials.Load(); got != 0 {
		t.Fatalf("the dialing connector was called %d times; want 0", got)
	}
}

// When the host is attached, the same calls resolve through the attached-only
// client (the wired dialer stays untouched), so the lookup is a real
// replacement and not merely a refusal path.
func TestRemoteHubSourceAttachedCallUsesAttachedOnlyClient(t *testing.T) {
	var dials atomic.Int64
	dial := func(context.Context, string) (*appwire.Client, error) {
		dials.Add(1)
		return nil, errors.New("the attached-only path must never dial")
	}
	source := NewRemoteHubSource("alpha", nil, dial)
	live, calls := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "t1"}}}
	})
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return live, true })

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "alpha:t1"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if resp.Thread.ID != "t1" {
		t.Fatalf("ReadThread thread = %q, want t1", resp.Thread.ID)
	}
	var reads int
	for _, call := range calls() {
		if call.method == appwire.MethodThreadRead {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("attached client served %d thread/read calls, want 1", reads)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("the dialing connector was called %d times; want 0", got)
	}
}
