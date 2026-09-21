package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// TestHostManageReconnectingDetachKeepsMidAttach pins the round-17 finding: a
// link drop does not end the in-progress attach. The supervisor's link-drop
// path emits StateReconnecting immediately before EventDetached carries the
// same state (sshconn's supervise: stateEvent(StateReconnecting) then
// detachEvent(StateReconnecting)) and then parks in its backoff retrying, so
// for the whole reconnect window the row must keep reporting midAttach —
// keeping the Connect control disabled instead of rendering the host plainly
// offline and inviting a redundant manual attempt the supervisor already
// duplicates. The ordinary detach — every non-reconnecting Detached the
// manager emits carries StateDisconnected — still ends the in-progress
// attach, and the truthful-status guarantee is unchanged: Attached renders
// only while the attached-only client lookup confirms a live channel.
func TestHostManageReconnectingDetachKeepsMidAttach(t *testing.T) {
	live := &appwire.Client{}
	sources := appsource.NewRegistry()
	online := false
	cfg := hubcore.WebConfig{
		RemoteHostOnline: func(string) bool { return online },
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host == "h" && online {
				return live, true
			}
			return nil, false
		},
	}
	hosts, err := hostreg.New([]hostreg.Host{{Name: "h", SSH: "h.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	m := newHubHostManager(sources, nil, cfg, "", hosts, nil)
	status := func() appwire.HostRow {
		t.Helper()
		resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "h"})
		if err != nil {
			t.Fatalf("Status = %v", err)
		}
		return resp.Host
	}

	// Attached: the attached-only lookup confirms the live channel.
	online = true
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventAttached})
	if row := status(); !row.Attached || row.MidAttach {
		t.Fatalf("attached row = %+v, want attached with no in-progress attach", row)
	}

	// The link drops. This is the supervisor's actual transition, in the
	// order its link-drop path emits: StateReconnecting first, then the
	// Detached carrying the same reconnecting state. The channel is already
	// gone from the manager's map, so the attached-only lookup confirms
	// nothing.
	online = false
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventState, State: sshconn.StateReconnecting})
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventDetached, State: sshconn.StateReconnecting})
	// The row across the backoff window: honestly offline — no live channel
	// exists to confirm — but still mid-attach, so the Connect control stays
	// disabled while the supervisor is already reconnecting.
	row := status()
	if row.Attached {
		t.Fatalf("reconnecting row = %+v, want offline: the dropped channel is gone from the manager's map", row)
	}
	if !row.MidAttach {
		t.Fatalf("reconnecting row = %+v, want midAttach: the reconnect in progress must keep the row reporting it", row)
	}

	// Recovery: the supervisor's next attempt publishes the replacement and
	// announces Attached, which supersedes the in-progress state.
	online = true
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventAttached})
	if row := status(); !row.Attached || row.MidAttach {
		t.Fatalf("re-attached row = %+v, want attached with no in-progress attach", row)
	}

	// The give-up: a terminal failure ends the reconnect in the order the
	// loop emits it — EventFailed, then the disconnected state — and the row
	// honestly reports the failure with no in-progress attach, so Connect
	// re-enables only once reconnecting is genuinely over.
	online = false
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventState, State: sshconn.StateReconnecting})
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventDetached, State: sshconn.StateReconnecting})
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventFailed, Err: errors.New("dial refused")})
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventState, State: sshconn.StateDisconnected})
	row = status()
	if row.Attached || row.MidAttach || row.LastAttachErr != "dial refused" {
		t.Fatalf("failed row = %+v, want offline, no in-progress attach, and lastAttachError", row)
	}

	// The ordinary detach still clears an in-progress attach: a mid-attach
	// host whose channel is torn down hears the Detached every
	// non-reconnecting path emits — StateDisconnected.
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventState, State: sshconn.StateAttaching})
	if row := status(); !row.MidAttach {
		t.Fatalf("attaching row = %+v, want midAttach", row)
	}
	m.observeEvent(sshconn.Event{Host: "h", Kind: sshconn.EventDetached, State: sshconn.StateDisconnected})
	if row := status(); row.Attached || row.MidAttach {
		t.Fatalf("detached row = %+v, want offline with no in-progress attach: the ordinary detach ends it", row)
	}
}
