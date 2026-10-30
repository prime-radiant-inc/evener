package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// The daemon's read fails after Spawn returns, before the owner can answer.
// Both wire calls traverse the real hub router, which discards results on error.
func TestManagerCreatePostSpawnFailure(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote forwarding"
		}
		t.Run(name, func(t *testing.T) {
			session := hubtest.SessionID(t)
			instance := "instance-creation-1"
			daemon, recorder := startSkillInputHubDaemon(t, session, appwire.ThreadCapabilities{}, appwire.SessionUnavailable("read failed after spawn"))
			runDir := t.TempDir()
			spawned := 0
			entry := rendezvous.Entry{PID: 404, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemon.URL[len("http"):], SourceID: "local", ThreadID: session, SessionID: session, InstanceID: instance, WorkingDir: t.TempDir(), Model: "gpt-5"}
			owner := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir: runDir, Roster: hubcore.NewRoster(runDir, fakeProber{sessionID: session, status: appwire.ThreadStatusActive}),
				Spawner: &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
					spawned++
					writeRendezvous(t, runDir, entry)
					return entry, nil
				}}, Past: hubcore.NewPastIndex(""),
			})
			defer owner.Close()
			caller := owner
			params := appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: entry.WorkingDir, Input: skillInputHubSelection()}
			if remote {
				controller, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
				defer controller.Close()
				remoteClient := dialHubRPC(t, owner)
				defer remoteClient.Close()
				if _, err := remoteClient.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
					t.Fatal(err)
				}
				web.sources.Add(appsource.NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil }))
				caller = controller
				params.Source = "host"
			}
			client := dialHubRPC(t, caller)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			_, err := appwiretest.ThreadStart(t.Context(), client, params)
			wire, data := wireErrorData(t, err)
			if wire.Code != appwire.CodeInvalidParams || data.EvenerErrorInfo != appwire.ErrorInvalidParams || data.RetryDisposition == appwire.RetryDispositionAutomatic || data.MutationOutcome == appwire.MutationOutcomeNotAccepted {
				t.Fatalf("classification changed: %+v data=%+v", wire, data)
			}
			raw, marshalErr := json.Marshal(wire.Data)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var partial struct {
				CreatedRef        string `json:"createdRef"`
				CreatedInstanceID string `json:"createdInstanceId"`
			}
			if err := json.Unmarshal(raw, &partial); err != nil {
				t.Fatal(err)
			}
			wantRef := "local:" + session
			if remote {
				wantRef = "host:" + session
			}
			if partial.CreatedRef != wantRef || partial.CreatedInstanceID != instance || spawned != 1 || recorder.startCount() != 0 {
				t.Fatalf("known creation lost or repeated: partial=%+v wantRef=%q spawned=%d submitted=%d error=%v", partial, wantRef, spawned, recorder.startCount(), err)
			}
			if !errors.As(err, &wire) {
				t.Fatal("wire error classification lost")
			}
		})
	}
}

func TestManagerCreatePostSpawnFailureNoSpawnEvidence(t *testing.T) {
	spawned := 0
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Spawner: &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
			spawned++
			return rendezvous.Entry{}, errors.New("spawn refused")
		}},
		Past: hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	_, err := appwiretest.ThreadStart(t.Context(), client, appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: t.TempDir()})
	wire, data := wireErrorData(t, err)
	if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorHubLaunch || spawned != 1 {
		t.Fatalf("spawn refusal changed: wire=%+v data=%+v spawned=%d", wire, data, spawned)
	}
	raw, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var partial struct {
		CreatedRef        string `json:"createdRef"`
		CreatedInstanceID string `json:"createdInstanceId"`
	}
	if err := json.Unmarshal(raw, &partial); err != nil {
		t.Fatal(err)
	}
	if partial.CreatedRef != "" || partial.CreatedInstanceID != "" {
		t.Fatalf("failed spawn falsely claimed creation: %s", raw)
	}
}
