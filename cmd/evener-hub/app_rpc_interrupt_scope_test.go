package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// TestHubRPCRelaysSessionScopedInterrupt covers the two hub-side rejections a
// session-scoped Stop has to survive before it reaches a daemon: the request
// handler's own precondition and the local-daemon source's. A browser talks to
// the hub, so an escape hatch the hub refuses is no escape hatch.
func TestHubRPCRelaysSessionScopedInterrupt(t *testing.T) {
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener:    appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	var seen []appwire.TurnInterruptParams
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		seen = append(seen, params)
		return appwire.TurnInterruptResponse{Receipt: appwire.MutationReceipt{
			ClientMutationID: params.ClientMutationID,
			Disposition:      appwire.MutationDispositionApplied,
			ThreadID:         "th_1",
			TurnID:           "turn_m1",
			ProjectionState:  appwire.MutationProjectionReflected,
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       os.Getpid(),
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var stopped appwire.TurnInterruptResponse
	if err := client.Request(context.Background(), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              "local:th_1",
		ClientMutationID: "interrupt-session-scoped",
	}, &stopped); err != nil {
		t.Fatalf("interrupt naming no turn: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("interrupt reached the daemon %d times, want 1", len(seen))
	}
	if stopped.Receipt.TurnID != "turn_m1" {
		t.Fatalf("interrupt receipt = %#v, want the turn the daemon cancelled", stopped.Receipt)
	}
}
