package hub

import (
	"bytes"
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestHubRPCStartTurnPreservesImageAttachment(t *testing.T) {
	const sessionID = "01APPWIREIMAGE"
	imageBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	gotInput := make(chan []appwire.InputItem, 1)

	runDir := t.TempDir()
	daemon := startAppwireTestDaemonWithProtocol(t, runDir, sessionID, appwire.ProtocolVersion, func(server *appserver.Server) {
		appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			gotInput <- params.Input
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-image"}}, nil
		})
	})
	defer daemon.Close()

	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:       runDir,
		HubStateRoot: runDir,
		Roster:       roster,
		Past:         hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
		Ref:                "local:" + sessionID,
		ClientMutationID:   "mutation-image",
		ExpectedInstanceID: sessionID,
		Input: []appwire.InputItem{
			{Type: "text", Text: "inspect this"},
			{Type: "image", MediaType: "image/png", Data: imageBytes, Name: "attachment.png"},
		},
	})
	if err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	if resp.Turn.ID != "turn-image" {
		t.Fatalf("TurnStart response = %+v, want daemon response", resp)
	}

	select {
	case input := <-gotInput:
		if len(input) != 2 {
			t.Fatalf("daemon input = %+v, want text and image items", input)
		}
		got := input[1]
		if got.Type != "image" || got.MediaType != "image/png" || got.Name != "attachment.png" {
			t.Fatalf("daemon image item = %+v, want original metadata", got)
		}
		if !bytes.Equal(got.Data, imageBytes) {
			t.Fatalf("daemon image bytes = %x, want %x", got.Data, imageBytes)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not receive turn/start")
	}
}
