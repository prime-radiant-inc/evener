package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestHubProtocolUpgradePreservesTranscriptAndRejectsUndeliverableMessages(t *testing.T) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: sessionID, SessionID: sessionID, Endpoint: "ws://127.0.0.1:1/rpc"},
		SessionID: sessionID, Status: "restartRequired",
	})
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past, Roster: roster})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	ref := "local:" + sessionID
	t.Run("saved transcript remains readable with explicit restart state", func(t *testing.T) {
		read, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, Subscribe: true})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.Status.Type != "restartRequired" {
			t.Errorf("status=%q", read.Thread.Status.Type)
		}
		if read.Thread.Evener.Capabilities.Send || read.Thread.Evener.Capabilities.Queue {
			t.Error("incompatible session advertises message delivery")
		}
		if len(read.Thread.Turns) != 2 {
			t.Errorf("saved turns=%d", len(read.Thread.Turns))
		}
	})
	for _, method := range []string{appwire.MethodTurnStart, appwire.MethodTurnQueue, appwire.MethodTurnSteer} {
		t.Run(method, func(t *testing.T) {
			var response any
			err := client.Request(context.Background(), method, map[string]any{"ref": ref, "clientMutationId": "upgrade-message", "expectedInstanceId": sessionID, "expectedTurnId": "turn-active", "input": []appwire.InputItem{{Type: "text", Text: "sentinel"}}}, &response)
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("error=%v", err)
			}
			data, ok := wire.Data.(map[string]any)
			if !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeNotAccepted) || data["clientMutationId"] != "upgrade-message" || data["cause"] != "daemonRestartRequired" {
				t.Fatalf("rejection=%+v", wire)
			}
		})
	}
	t.Run("resume refuses before replacement spawn", func(t *testing.T) {
		_, err := client.ThreadResume(context.Background(), appwire.ThreadResumeParams{Ref: ref})
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("error=%v", err)
		}
		data, ok := wire.Data.(map[string]any)
		if !ok || data["cause"] != "daemonRestartRequired" {
			t.Fatalf("rejection=%+v", wire)
		}
	})
}
