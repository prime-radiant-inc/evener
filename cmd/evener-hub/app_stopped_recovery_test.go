package hub

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// After exit is durably confirmed, the process may remove its rendezvous file.
// Repeated Stop must recognize the established outcome rather than demand a
// nonexistent process claim or launch a daemon just to stop it again.
func TestConfirmedStoppedSessionRepeatedShutdownWithoutProcess(t *testing.T) {
	for _, method := range []string{"thread/shutdown"} {
		t.Run(method, func(t *testing.T) {
			stateDir, runDir := t.TempDir(), t.TempDir()
			sessionID := buildRPCParentSession(t, stateDir)
			ref := "local:" + sessionID
			entry := rendezvous.Entry{
				PID: 4242, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: ref,
				StateDir: stateDir, Protocol: appwire.ProtocolVersion,
				Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
			}
			writeRendezvous(t, runDir, entry)
			locks := hubcore.NewResumeLocks()
			var events []string
			spawns := 0
			cfg := hubcore.WebConfig{
				RunDir: runDir, ResumeLocks: locks,
				DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
					events = append(events, "open")
					return &forceStopProcess{events: &events}, nil
				}),
				Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
					spawns++
					return rendezvous.Entry{}, errors.New("unexpected launcher call")
				}},
			}
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			var response appwire.EmptyResponse
			if err := client.Request(t.Context(), "evener/thread/forceStop", map[string]string{"ref": ref}, &response); err != nil {
				t.Fatal(err)
			}
			before := locks.RecoveryState(sessionID)
			if !before.ExitConfirmed || !before.ResumeRequired || before.Stopping != 0 {
				t.Fatalf("first stop did not establish confirmed recovery: %+v", before)
			}
			if err := rendezvous.Remove(runDir, entry.PID); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := client.Request(t.Context(), method, map[string]string{"ref": ref}, &response); err != nil {
					t.Fatalf("repeated stop rejected confirmed exit: %v", err)
				}
			}
			if spawns != 0 || !reflect.DeepEqual(events, []string{"open", "kill", "wait", "close"}) {
				t.Fatalf("repeated stop touched a process: spawns=%d events=%v", spawns, events)
			}
			after := locks.RecoveryState(sessionID)
			// Epoch is admission generation, not recovery authority: the
			// confirmed-stopped no-op advances it so a Resume registration that
			// was waiting for the alias cannot launch on a pre-decision
			// snapshot. The authority itself must stay unchanged.
			if !after.ExitConfirmed || !after.ResumeRequired || after.Stopping != 0 || after.ResumeSessionID != before.ResumeSessionID {
				t.Fatalf("idempotent stop changed recovery authority: before=%+v after=%+v", before, after)
			}
		})
	}
}
