package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
)

func TestAskPageHubChecksCapabilityBeforeForwardingPage(t *testing.T) {
	for _, supported := range []bool{false, true} {
		name := "unsupported"
		if supported {
			name = "supported"
		}
		t.Run(name, func(t *testing.T) {
			sessionID := hubtest.SessionID(t)
			var pageReads, metadataReads atomic.Int32
			app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				resp := appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local",
					Evener: appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{AskFence: supported}}}}
				if params.ExpectedAskRevision == nil {
					metadataReads.Add(1)
					return resp, nil
				}
				pageReads.Add(1)
				if params.Subscribe || params.IncludeTurns || params.ExpectedAskBootGeneration != "boot-9" || *params.ExpectedAskRevision != 0 || params.Offset == nil || *params.Offset != 10 || params.Limit == nil || *params.Limit != 1 {
					t.Errorf("page request changed or subscribed: %+v", params)
				}
				resp.PendingAskPage = &appwire.PendingAskPage{Composites: []appwire.PendingAskCall{{TurnID: "turn-11", CallID: "call-11"}}, Total: 11, CalllessCount: 2}
				return resp, nil
			})
			daemon := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
			t.Cleanup(daemon.Close)
			client := askHubClientForDaemon(t, sessionID, daemon.URL)
			revision, offset, limit := uint64(0), 10, 1
			resp, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sessionID,
				ExpectedAskRevision: &revision, ExpectedAskBootGeneration: "boot-9", Offset: &offset, Limit: &limit})
			if !supported {
				wire, ok := errors.AsType[appwire.WireError](err)
				data, _ := appwire.DecodedErrorData(wire.Data)
				if !ok || data.EvenerErrorInfo != appwire.ErrorAskFenceUnsupportedOnDaemon || pageReads.Load() != 0 {
					t.Fatalf("unsupported page result err=%v dispatched=%d", err, pageReads.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pageReads.Load() != 1 || metadataReads.Load() != 1 || resp.PendingAskPage == nil || resp.PendingAskPage.Total != 11 || resp.PendingAskPage.CalllessCount != 2 || len(resp.PendingAskPage.Composites) != 1 || resp.PendingAskPage.Composites[0].CallID != "call-11" {
				t.Fatalf("page did not survive the gated relay: metadata=%d page=%d response=%+v", metadataReads.Load(), pageReads.Load(), resp.PendingAskPage)
			}
		})
	}
}
