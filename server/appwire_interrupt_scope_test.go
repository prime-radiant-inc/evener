package server

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestServerAppWireSessionScopedInterruptNeedsNoTurnID walks the whole daemon
// request path — the flag-day validator on the router, then the handler's own
// precondition — because both refuse an interrupt that names no turn, and Stop
// only reaches the session when neither does.
func TestServerAppWireSessionScopedInterruptNeedsNoTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var seen []appwire.TurnInterruptParams
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Interrupt: func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			seen = append(seen, params)
			return appwire.TurnInterruptResponse{}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion},
	))

	targeted := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(2), appwire.MethodTurnInterrupt,
		appwire.TurnInterruptParams{ClientMutationID: "interrupt-targeted", Ref: "local:th_1"},
	))
	if targeted.Kind() != appwire.MessageError || targeted.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("targeted interrupt with no expectedTurnId = %+v, want invalid params", targeted)
	}
	if len(seen) != 0 {
		t.Fatalf("targeted interrupt with no expectedTurnId reached the session: %#v", seen)
	}

	scoped := conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(3), appwire.MethodTurnInterrupt,
		appwire.TurnInterruptParams{
			ClientMutationID:     "interrupt-session-scoped",
			Ref:                  "local:th_1",
			InterruptRunningTurn: true,
		},
	))
	if scoped.Kind() != appwire.MessageResponse {
		t.Fatalf("session-scoped interrupt = %+v, want a response", scoped.Error.Error)
	}
	if len(seen) != 1 {
		t.Fatalf("session-scoped interrupt reached the session %d times, want 1", len(seen))
	}
	if !seen[0].InterruptRunningTurn || seen[0].ExpectedTurnID != "" {
		t.Fatalf("session-scoped interrupt params = %#v", seen[0])
	}

	// The router's flag-day validator is skipped for adapter-native servers
	// (internal/appserver/server.go), so the handler's own precondition is the
	// only one on that path and has to be checked without it.
	if _, err := srv.handleAppTurnInterrupt(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-targeted-direct",
		Ref:              "local:th_1",
	}); err == nil || !strings.Contains(err.Error(), "expectedTurnId is required") {
		t.Fatalf("targeted interrupt straight to the handler = %v, want expectedTurnId is required", err)
	}
	if len(seen) != 1 {
		t.Fatalf("targeted interrupt straight to the handler reached the session: %#v", seen)
	}
}
