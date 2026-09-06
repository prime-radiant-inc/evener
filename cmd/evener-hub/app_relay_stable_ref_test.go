package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRPCStableRefSubscriptionWithReplacementInstance(t *testing.T) {
	sessionID := "02wMz5Txv733WHFsVy66SR"
	instanceID := "replacement-instance"
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		appserver.Subscribe(ctx, sessionID)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: instanceID, SessionID: sessionID, Source: "local",
			Evener: appwire.EvenerThread{Ref: params.Ref},
		}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       21 * 1000,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	daemon.Broadcast(sessionID, appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: instanceID, Ref: "local:" + sessionID, TurnID: "turn", ItemID: "item", Delta: "marker",
	})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-client.Notifications():
			if got.Method != appwire.NotifyAgentMessageDelta {
				continue
			}
			var params appwire.AgentMessageDeltaParams
			if err := json.Unmarshal(got.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Ref != "local:"+sessionID || params.ThreadID != instanceID || params.Delta != "marker" {
				t.Fatalf("unexpected event: %+v", params)
			}
			return
		case <-deadline:
			t.Fatal("stable reference subscription did not receive replacement instance event")
		}
	}
}
