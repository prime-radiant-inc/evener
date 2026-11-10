package hub

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestAskFenceRemoteHubPreparesEveryInputMode(t *testing.T) {
	for _, supported := range []bool{false, true} {
		name := "omitted-bit"
		if supported {
			name = "supported"
		}
		t.Run(name, func(t *testing.T) {
			remote, recorder, sessionID := askFenceHubFixture(t, supported)
			source := appsource.NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return remote, nil })
			controller := httptest.NewUnstartedServer(nil)
			web := NewWebServer(hubcore.WebConfig{HubAddr: controller.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
			web.sources.Add(source)
			controller.Config.Handler = web.Handler()
			controller.Start()
			t.Cleanup(controller.Close)
			client := dialSkillInputHub(t, controller)
			t.Cleanup(func() { _ = client.Close() })
			ref := "host:" + sessionID
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if read.Thread.Evener.Capabilities.AskFence != supported {
				t.Fatalf("remote capability=%t want=%t", read.Thread.Evener.Capabilities.AskFence, supported)
			}
			for i, mode := range []string{"start", "queue", "steer"} {
				t.Run(mode, func(t *testing.T) {
					params := map[string]any{"ref": ref, "expectedInstanceId": sessionID,
						"clientMutationId": "remote-ask-" + mode, "input": []appwire.InputItem{{Type: "text", Text: "answer-token"}},
						"expectedAskRevision": uint64(0), "expectedAskBootGeneration": "boot-7"}
					var result any
					err := client.Request(t.Context(), "turn/"+mode, params, &result)
					count := recorder.startCount() + recorder.queueCount() + recorder.steerCount()
					if !supported {
						wire, ok := errors.AsType[appwire.WireError](err)
						data, _ := appwire.DecodedErrorData(wire.Data)
						if !ok || data.EvenerErrorInfo != appwire.ErrorAskFenceUnsupportedOnDaemon || count != 0 {
							t.Fatalf("unsupported remote result err=%v dispatched=%d", err, count)
						}
						return
					}
					if err != nil || count != i+1 {
						t.Fatalf("supported remote result err=%v dispatched=%d want=%d", err, count, i+1)
					}
					var revision *uint64
					var boot, forwardedRef string
					switch mode {
					case "start":
						got := recorder.lastStart()
						revision, boot, forwardedRef = got.ExpectedAskRevision, got.ExpectedAskBootGeneration, got.Ref
					case "queue":
						got := recorder.lastQueue()
						revision, boot, forwardedRef = got.ExpectedAskRevision, got.ExpectedAskBootGeneration, got.Ref
					case "steer":
						got := recorder.lastSteer()
						revision, boot, forwardedRef = got.ExpectedAskRevision, got.ExpectedAskBootGeneration, got.Ref
					}
					if revision == nil || *revision != 0 || boot != "boot-7" || forwardedRef != "local:"+sessionID {
						t.Fatalf("remote answer changed pair or ref: revision=%v boot=%q ref=%q", revision, boot, forwardedRef)
					}
				})
			}
		})
	}
}
