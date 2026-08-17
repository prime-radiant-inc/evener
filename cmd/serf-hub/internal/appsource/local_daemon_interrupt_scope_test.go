package appsource

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

// TestLocalDaemonInterruptTargetRequirement pins which interrupts this relay
// refuses before it dials. The endpoint below is unreachable on purpose: only a
// request the relay rejects on its own can fail without a connection error, so
// the error text is the proof the precondition ran and not the daemon.
func TestLocalDaemonInterruptTargetRequirement(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol: appwire.ProtocolVersion,
				Endpoint: "ws://127.0.0.1:1/rpc",
				SourceID: "local",
				ThreadID: "th_1",
			},
			SessionID: "th_1",
		}}
	}, nil)

	if _, err := source.InterruptTurn(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-targeted",
		Ref:              "local:th_1",
	}); err == nil || !strings.Contains(err.Error(), "expectedTurnId is required") {
		t.Fatalf("targeted interrupt with no expectedTurnId = %v, want expectedTurnId is required", err)
	}

	_, err := source.InterruptTurn(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID:     "interrupt-session-scoped",
		Ref:                  "local:th_1",
		InterruptRunningTurn: true,
	})
	if err == nil {
		t.Fatal("session-scoped interrupt reached an unreachable daemon")
	}
	if strings.Contains(err.Error(), "expectedTurnId is required") {
		t.Fatalf("session-scoped interrupt refused by the relay: %v", err)
	}
}
