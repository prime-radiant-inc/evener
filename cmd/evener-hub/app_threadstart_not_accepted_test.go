package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// threadStartRefusal is a failed thread/start's wire error and its standard
// data, as a client decodes them.
func threadStartRefusal(t *testing.T, err error) (appwire.WireError, appwire.ErrorData) {
	t.Helper()
	if err == nil {
		t.Fatal("thread/start succeeded, want a refusal")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("refusal %T is not a wire error: %v", err, err)
	}
	raw, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var data appwire.ErrorData
	if unmarshalErr := json.Unmarshal(raw, &data); unmarshalErr != nil {
		t.Fatalf("refusal data %s: %v", raw, unmarshalErr)
	}
	return wire, data
}

// A thread/start refused before the hub spawns anything says so: no session
// exists, so a client may start that draft again (#3184). The code, message and
// evenerErrorInfo stay what they were, so a client that doesn't read the
// outcome sees the same refusal.
func TestThreadStartRefusalsBeforeTheSpawnSayNotAccepted(t *testing.T) {
	t.Setenv("EVENER_MODEL", "") // no ambient model, so "model is required" is reachable
	cases := []struct {
		name    string
		spawner bool
		params  func(t *testing.T) appwire.ThreadStartParams
		code    int
		info    appwire.ErrorInfo
	}{
		{
			name:    "no model resolves",
			spawner: true,
			params:  func(t *testing.T) appwire.ThreadStartParams { return appwire.ThreadStartParams{CWD: t.TempDir()} },
			code:    appwire.CodeInvalidParams,
			info:    appwire.ErrorInvalidParams,
		},
		{
			name:    "the working directory is missing",
			spawner: true,
			params: func(t *testing.T) appwire.ThreadStartParams {
				return appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: filepath.Join(t.TempDir(), "missing")}
			},
			code: appwire.CodeInvalidParams,
			info: appwire.ErrorInvalidParams,
		},
		{
			name:    "the hub has no spawner",
			spawner: false,
			params: func(t *testing.T) appwire.ThreadStartParams {
				return appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: t.TempDir()}
			},
			code: appwire.CodeUnavailable,
			info: appwire.ErrorActionUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{
				RunDir:           t.TempDir(),
				HubStateRoot:     t.TempDir(),
				LaunchConfigRoot: t.TempDir(),
				Past:             hubcore.NewPastIndex(""),
			}
			if tc.spawner {
				cfg.Spawner = &fakeRPCModelContractSpawner{
					spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
						t.Fatal("a refused start must not spawn")
						return rendezvous.Entry{}, nil
					},
				}
			}
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}

			_, err := client.ThreadStart(context.Background(), tc.params(t))
			wire, data := threadStartRefusal(t, err)
			if wire.Code != tc.code || data.EvenerErrorInfo != tc.info {
				t.Fatalf("refusal code=%d info=%q, want %d %q: %v", wire.Code, data.EvenerErrorInfo, tc.code, tc.info, err)
			}
			if data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
				t.Fatalf("mutationOutcome=%q, want %q: %v", data.MutationOutcome, appwire.MutationOutcomeNotAccepted, err)
			}
		})
	}
}

// A thread/start refused after the spawn doesn't say not accepted: its session
// already exists (the initial input's skill check runs against the spawned
// session's own read), so a client must not start that draft again.
func TestThreadStartRefusalAfterTheSpawnLeavesItsOutcomeOpen(t *testing.T) {
	sessionID := hubtest.SessionID(t)
	daemon, _ := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true}, nil)
	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID:        404,
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + daemon.URL[len("http"):],
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Model:      "gpt-5",
	}
	spawned := 0
	spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawned++
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive}),
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialSkillInputHub(t, hub)
	defer client.Close()

	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   "/tmp",
		Input: skillInputHubSelection(),
	})
	wire, data := threadStartRefusal(t, err)
	if spawned != 1 {
		t.Fatalf("spawned=%d, want the refusal to come after the spawn", spawned)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("refusal code=%d, want invalidParams: %v", wire.Code, err)
	}
	if data.MutationOutcome != "" {
		t.Fatalf("mutationOutcome=%q, want none for a start whose session exists: %v", data.MutationOutcome, err)
	}
}
