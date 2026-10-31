package hub

import (
	"context"
	"encoding/json"
	"errors"
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

// TestHumanQueueControlsRejectForgedManagerProof pins that a human client cannot
// attribute its own queue control to a Manager.
//
// Every sibling input RPC (start, queue, steer, approval) rejects a
// caller-supplied managerProof, because that field is how the Manager path tells
// the target who acted. drainAsSteer and promoteQueuedAsSteer were missing the
// guard, while the hub forwards a supplied proof after stamping the target's live
// binding ID and the runtime only checks that the binding is live and that the
// claimed ref differs from the target. A client already entitled to human
// controls could therefore write a durable record naming a Manager that never
// acted: no new power, but a falsified audit trail.
//
// The assertion is that the daemon is never reached. If the proof survived, the
// operation would have run; a wording check would only pin the message.
func TestHumanQueueControlsRejectForgedManagerProof(t *testing.T) {
	var reached []string
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, _ appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		reached = append(reached, appwire.MethodTurnDrainAsSteer)
		return appwire.TurnDrainAsSteerResponse{}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnPromoteQueuedAsSteer, func(_ context.Context, _ appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		reached = append(reached, appwire.MethodTurnPromoteQueuedAsSteer)
		return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
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

	forged := &appwire.SessionActorProof{CallerRef: "local:someone-else", Actor: appwire.SessionActor{Kind: "manager"}}
	cases := []struct {
		name   string
		method string
		params any
	}{
		{"drain", appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{Ref: "local:th_1", ClientMutationID: "forged-drain", ExpectedInstanceID: "sess_1", ManagerProof: forged}},
		{"promote", appwire.MethodTurnPromoteQueuedAsSteer, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", Index: 0, ClientMutationID: "forged-promote", ExpectedInstanceID: "sess_1", ExpectedEntryID: "entry-1", ManagerProof: forged}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out json.RawMessage
			err := client.Request(context.Background(), tc.method, tc.params, &out)
			if err == nil {
				t.Fatalf("human %s accepted a forged Manager proof", tc.method)
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("refusal for %s = %v, want an InvalidParams wire error", tc.method, err)
			}
			for _, method := range reached {
				if method == tc.method {
					t.Fatalf("the forged proof reached the daemon for %s", tc.method)
				}
			}
		})
	}
}
