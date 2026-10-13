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

	if len(m.session.messages) != 0 || len(m.notices) != 0 {
		t.Fatalf("/interrupt refused a session with no announced turn id: messages=%+v notices=%+v", m.session.messages, m.notices)
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
	availability := func(caps hubSessionCapabilities, state string) (bool, string) {
		return hubCommandAvailable(command, hubCommandContext{mode: hubModeSession, caps: caps, live: true, state: state})
	}
	if available, reason := availability(hubSessionCapabilities{Interrupt: true}, appwire.ThreadStatusActive); !available || reason != "" {
		t.Fatalf("running turn: Available = %v %q, want available", available, reason)
	}
	idleAvail, idleReason := availability(hubSessionCapabilities{Interrupt: true}, appwire.ThreadStatusIdle)
	awaitingAvail, awaitingReason := availability(hubSessionCapabilities{Interrupt: true}, appwire.ThreadStatusAwaiting)
	unwiredAvail, unwiredReason := availability(hubSessionCapabilities{}, appwire.ThreadStatusActive)
	if idleAvail || awaitingAvail || unwiredAvail {
		t.Fatalf("Available idle=%v awaiting=%v unwired=%v, want all refused", idleAvail, awaitingAvail, unwiredAvail)
	}
	// Idle and awaiting are refused by the status, the unwired harness by its
	// capability: one status reason, told apart from the capability's.
	if idleReason == "" || idleReason != awaitingReason || idleReason == unwiredReason || unwiredReason == "" {
		t.Fatalf("reasons idle=%q awaiting=%q unwired=%q, want one status reason told apart from the capability reason", idleReason, awaitingReason, unwiredReason)
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
	_, statusReason := hubCommandAvailable(command, hubCommandContext{mode: hubModeSession, caps: m.detail.Capabilities, live: true, state: appwire.ThreadStatusIdle})
	notice, ok := noticeByCategory(*m, "action-unavailable")
	if !ok || notice.Reason != statusReason {
		t.Fatalf("typed /interrupt refusal = %+v ok=%v, want the status reason %q", notice, ok, statusReason)
	}
}
