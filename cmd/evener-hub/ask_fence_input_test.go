package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

func askFenceHubFixture(t *testing.T, supported bool) (*appwire.Client, *hubSkillInputRecorder, string) {
	t.Helper()
	sessionID := hubtest.SessionID(t)
	daemon, recorder := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true, Steer: true, AskFence: supported}, nil)
	return askHubClientForDaemon(t, sessionID, daemon.URL), recorder, sessionID
}

func askHubClientForDaemon(t *testing.T, sessionID, daemonURL string) *appwire.Client {
	t.Helper()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 401, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonURL[len("http"):],
		SourceID: "local", ThreadID: sessionID, SessionID: sessionID, WorkingDir: "/tmp", Model: "gpt-5",
	})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive})
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir, Roster: roster, Past: hubcore.NewPastIndex(""), ResumeLocks: hubcore.NewResumeLocks(),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New("unexpected resume of live daemon")
		}},
	})
	t.Cleanup(hub.Close)
	client := dialSkillInputHub(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestAskFenceHubPreparesEveryInputMode(t *testing.T) {
	for _, supported := range []bool{false, true} {
		name := "unsupported"
		if supported {
			name = "supported"
		}
		t.Run(name, func(t *testing.T) {
			client, recorder, sessionID := askFenceHubFixture(t, supported)
			revision := uint64(0)
			for _, mode := range []string{"start", "queue", "steer"} {
				t.Run(mode, func(t *testing.T) {
					params := map[string]any{
						"ref": "local:" + sessionID, "clientMutationId": "ask-" + mode,
						"expectedInstanceId": sessionID, "input": []appwire.InputItem{{Type: "text", Text: "ANSWER_482"}},
						"expectedAskRevision": revision, "expectedAskBootGeneration": "boot-7",
					}
					var result any
					err := client.Request(t.Context(), "turn/"+mode, params, &result)
					if !supported {
						wire, ok := errors.AsType[appwire.WireError](err)
						data, _ := appwire.DecodedErrorData(wire.Data)
						if !ok || data.EvenerErrorInfo != appwire.ErrorAskFenceUnsupportedOnDaemon {
							t.Fatalf("unsupported target returned %v, want typed pre-dispatch refusal", err)
						}
						if recorder.startCount()+recorder.queueCount()+recorder.steerCount() != 0 {
							t.Fatal("fenced answer reached an unsupported daemon")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					var gotRevision *uint64
					var gotBoot string
					switch mode {
					case "start":
						got := recorder.lastStart()
						gotRevision, gotBoot = got.ExpectedAskRevision, got.ExpectedAskBootGeneration
					case "queue":
						got := recorder.lastQueue()
						gotRevision, gotBoot = got.ExpectedAskRevision, got.ExpectedAskBootGeneration
					case "steer":
						got := recorder.lastSteer()
						gotRevision, gotBoot = got.ExpectedAskRevision, got.ExpectedAskBootGeneration
					}
					if gotRevision == nil || *gotRevision != revision || gotBoot != "boot-7" {
						t.Fatalf("forwarded pair=(%v,%q), want supplied revision zero and boot", gotRevision, gotBoot)
					}
				})
			}
		})
	}
}

func TestAskFenceHubRefusesIncompletePairsBeforeDispatch(t *testing.T) {
	client, recorder, sessionID := askFenceHubFixture(t, true)
	for _, mode := range []string{"start", "queue", "steer"} {
		for _, key := range []string{"expectedAskRevision", "expectedAskBootGeneration"} {
			t.Run(mode+"/"+key, func(t *testing.T) {
				params := map[string]any{"ref": "local:" + sessionID, "clientMutationId": mode + key,
					"expectedInstanceId": sessionID, "input": []appwire.InputItem{{Type: "text", Text: "ANSWER_483"}}}
				if key == "expectedAskRevision" {
					params[key] = 0
				} else {
					params[key] = "boot-7"
				}
				var result any
				err := client.Request(t.Context(), "turn/"+mode, params, &result)
				wire, ok := errors.AsType[appwire.WireError](err)
				if !ok || wire.Code != appwire.CodeInvalidParams {
					t.Fatalf("incomplete pair returned %v, want invalid params", err)
				}
				if recorder.startCount()+recorder.queueCount()+recorder.steerCount() != 0 {
					t.Fatal("incomplete pair reached daemon mutation handler")
				}
			})
		}
	}
}
