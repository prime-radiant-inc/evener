package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestInterruptCommandDoesNotWaitForATurnID is the TUI half of "control
// mutations are session-scoped" (appwire v3, kata vewa/5gdv).
//
// turn/interrupt names no turn. It stops whatever the session is doing, and the
// daemon's precondition is the session's own quiescence -- not an id the client
// happens to be holding. The TUI's /interrupt kept a second, client-side gate
// on m.detail.ActiveTurnID and refused before sending anything whenever that
// field was empty.
//
// That field is empty in states the wire really reaches -- a session holding
// queued work reports active with no turn running -- and gating the command on
// an id the REQUEST does not carry can only refuse a Stop the daemon would have
// taken. A session the user can see running was one they could not interrupt.
//
// The capability and the running status are the gate (interruptCommandAvailable:
// Interrupt advertises harness support, #1375, and the command applies the
// status itself). Whether a turn has announced its name does not.
func TestInterruptCommandDoesNotWaitForATurnID(t *testing.T) {
	command, ok := hubCommandByName("interrupt")
	if !ok {
		t.Fatal("no /interrupt command in the registry")
	}

	m := &hubModel{
		mode: hubModeSession,
		detail: hubSessionDetail{
			Ref:          "local:th_1",
			SessionID:    "th_1",
			ActiveTurnID: "",
			Capabilities: hubSessionCapabilities{Interrupt: true},
		},
		session: newModel(nil),
	}

	cmd := runHubCommandDefinition(m, command, "")

	for _, message := range m.session.messages {
		if strings.Contains(message.Text, "active turn") {
			t.Fatalf("/interrupt refused a session with no announced turn id: %q", message.Text)
		}
	}
	if cmd == nil {
		t.Fatal("/interrupt produced no command: nothing was sent to the daemon for a session the user can see working")
	}
}

// TestInterruptCommandAppliesTheSessionStatus is the client half of the
// unfolded capability (#1375): ThreadCapabilities.Interrupt means "this harness
// can stop a turn", not "a turn is running", so /interrupt applies the status
// itself -- offered only while a turn runs, the rule sessionControls's stop
// uses for Ctrl+C. The raw bit alone offered the command on an idle wired
// session, where the daemon's quiescence precondition answers "no active turn
// to interrupt".
//
// The same Available closure backs the palette row, /help's listing and typed
// dispatch, so this covers all three.
func TestInterruptCommandAppliesTheSessionStatus(t *testing.T) {
	command, ok := hubCommandByName("interrupt")
	if !ok {
		t.Fatal("no /interrupt command in the registry")
	}
	for _, tc := range []struct {
		name       string
		caps       hubSessionCapabilities
		state      string
		wantAvail  bool
		wantReason string
	}{
		{"running turn", hubSessionCapabilities{Interrupt: true}, appwire.ThreadStatusActive, true, ""},
		{"idle session", hubSessionCapabilities{Interrupt: true}, appwire.ThreadStatusIdle, false, "no active turn"},
		{"awaiting session", hubSessionCapabilities{Interrupt: true}, appwire.ThreadStatusAwaiting, false, "no active turn"},
		{"unwired harness", hubSessionCapabilities{}, appwire.ThreadStatusActive, false, "source does not advertise interrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := hubCommandContext{mode: hubModeSession, caps: tc.caps, live: true, state: tc.state}
			available, reason := hubCommandAvailable(command, ctx)
			if available != tc.wantAvail || reason != tc.wantReason {
				t.Fatalf("Available = %v %q, want %v %q", available, reason, tc.wantAvail, tc.wantReason)
			}
		})
	}

	// /help lists a command only while it is available, so /interrupt tracks
	// the status there too.
	if help := hubCommandHelpLive(hubSessionCapabilities{Interrupt: true}, true, appwire.ThreadStatusIdle); strings.Contains(help, "/interrupt") {
		t.Fatalf("idle /help offered /interrupt on a wired harness:\n%s", help)
	}
	if help := hubCommandHelpLive(hubSessionCapabilities{Interrupt: true}, true, appwire.ThreadStatusActive); !strings.Contains(help, "/interrupt") {
		t.Fatalf("mid-turn /help hid /interrupt:\n%s", help)
	}

	// Typed /interrupt refuses an idle session before sending anything, with
	// the status reason rather than the capability one.
	m := &hubModel{
		mode:    hubModeSession,
		detail:  hubSessionDetail{Ref: "local:th_1", SessionID: "th_1", State: appwire.ThreadStatusIdle, Capabilities: hubSessionCapabilities{Interrupt: true}},
		session: newModel(nil),
	}
	if cmd := m.runHubSlashCommand("interrupt", ""); cmd != nil {
		t.Fatal("typed /interrupt on an idle session produced a command; it must refuse before dispatch")
	}
	notice, ok := noticeByCategory(*m, "action-unavailable")
	if !ok || notice.Reason != "no active turn" {
		t.Fatalf("typed /interrupt refusal = %+v ok=%v, want the no-active-turn reason", notice, ok)
	}
}
