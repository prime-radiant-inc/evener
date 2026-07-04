package main

import (
	"testing"

	"primeradiant.com/serf/agent"
)

// TestHoldServeStateForAwaitingWake proves holdServeStateForAwaitingWake mirrors
// the session-level entry gate's refusal predicate (agent/session_lifecycle.go's
// `s.state == SessionAwaiting && kind != EntryUserInput`, spec §5.3): the input
// loop must hold its /status shadow write for exactly the (kind, state) pairs
// where ProcessInputKind will refuse before any state transition, and flip as
// before everywhere else.
func TestHoldServeStateForAwaitingWake(t *testing.T) {
	cases := []struct {
		name  string
		kind  agent.EntryKind
		state agent.SessionState
		want  bool
	}{
		{"notification wake while awaiting is held", agent.EntryNotification, agent.SessionAwaiting, true},
		{"continuation wake while awaiting is held", agent.EntryContinuation, agent.SessionAwaiting, true},
		{"watch delivery while awaiting is held", agent.EntryWatchDelivery, agent.SessionAwaiting, true},
		{"user input while awaiting is not held (resolves the question)", agent.EntryUserInput, agent.SessionAwaiting, false},
		{"notification wake while idle is not held", agent.EntryNotification, agent.SessionIdle, false},
		{"user input while idle is not held", agent.EntryUserInput, agent.SessionIdle, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdServeStateForAwaitingWake(tc.kind, tc.state); got != tc.want {
				t.Errorf("holdServeStateForAwaitingWake(%v, %v) = %v, want %v", tc.kind, tc.state, got, tc.want)
			}
		})
	}
}
