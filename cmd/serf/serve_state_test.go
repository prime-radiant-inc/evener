package main

import (
	"testing"

	"primeradiant.com/serf/agent"
)

// TestHoldServeStateForAwaitingWake proves holdServeStateForAwaitingWake mirrors
// the session-level entry gate's refusal predicate (agent/session_lifecycle.go's
// `len(s.askPending) > 0 && kind != EntryUserInput`, spec §5.3): the input loop
// must hold its /status shadow write for exactly the (kind, hasPendingAsk) pairs
// where ProcessInputKind will refuse before any state transition, and flip as
// before everywhere else. Keyed on hasPendingAsk rather than raw SessionState
// (attention-status-model v5 reconciliation): a session generally awaiting with
// no pending ask must NOT be held — async wakes re-arm by design there — only a
// genuine pending question is a stronger stop than the wake.
func TestHoldServeStateForAwaitingWake(t *testing.T) {
	cases := []struct {
		name          string
		kind          agent.EntryKind
		hasPendingAsk bool
		want          bool
	}{
		{"notification wake with a pending ask is held", agent.EntryNotification, true, true},
		{"continuation wake with a pending ask is held", agent.EntryContinuation, true, true},
		{"watch delivery with a pending ask is held", agent.EntryWatchDelivery, true, true},
		{"user input with a pending ask is not held (resolves the question)", agent.EntryUserInput, true, false},
		{"notification wake with no pending ask is not held (general awaiting re-arms freely)", agent.EntryNotification, false, false},
		{"user input with no pending ask is not held", agent.EntryUserInput, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdServeStateForAwaitingWake(tc.kind, tc.hasPendingAsk); got != tc.want {
				t.Errorf("holdServeStateForAwaitingWake(%v, %v) = %v, want %v", tc.kind, tc.hasPendingAsk, got, tc.want)
			}
		})
	}
}
