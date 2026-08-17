package main

import (
	"strings"
	"testing"
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
// The capability stays the gate. Whether a turn has announced its name does
// not.
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
