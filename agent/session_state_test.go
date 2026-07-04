package agent

import "testing"

func TestSessionAwaitingStringIsWireAwaiting(t *testing.T) {
	// The string is load-bearing: SessionProcessing is "active", and every
	// status switch on the wire journey defaults unknown strings to idle.
	if got := string(SessionAwaiting); got != "awaiting" {
		t.Fatalf("SessionAwaiting = %q, want %q", got, "awaiting")
	}
}
