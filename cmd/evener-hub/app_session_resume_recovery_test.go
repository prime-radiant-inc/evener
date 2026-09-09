package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestAutomaticResumePreservesRecoveryMutationError(t *testing.T) {
	for _, method := range []string{appwire.MethodTurnStart, appwire.MethodThreadClear} {
		t.Run(method, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "recovery-0000000000")
			sessionID := buildRPCParentSession(t, stateDir)
			ref := localAppRef(sessionID)
			past := hubcore.NewPastIndex(stateDir)
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			cfg := hubcore.WebConfig{Past: past, ResumeLocks: hubcore.NewResumeLocks()}
			attempted := make(chan struct{})
			failAttempt := make(chan struct{})
			daemon := appserver.NewServer(appserver.ServerConfig{SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Evener: appwire.EvenerThread{Ref: ref, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Clear: true, Send: true}}}}, nil
			})
			appserver.HandleTyped(daemon.Router(), method, func(context.Context, appwire.EmptyParams) (appwire.EmptyResponse, error) {
				close(attempted)
				<-failAttempt
				return appwire.EmptyResponse{}, appwire.SessionUnavailable("daemon exited")
			})
			peer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer peer.Close()
			entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, SessionID: sessionID, ThreadID: sessionID, SourceID: "local", Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http")}
			sources := appsource.NewRegistry()
			sources.Add(appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry { return []rendezvous.Entry{entry} }, peer.Client()))
			hub := newHubAppServer(cfg, sources)
			result := make(chan error, 1)
			go func() {
				_, err := exactDispatch(t.Context(), t, hub, method, map[string]any{"ref": ref, "clientMutationId": "pending-mutation", "expectedInstanceId": sessionID, "input": []appwire.InputItem{{Type: "text", Text: "pending input"}}})
				result <- err
			}()
			<-attempted
			finish := cfg.ResumeLocks.BeginForceStop([]string{sessionID})
			finish(true)
			close(failAttempt)
			err := <-result
			if !isSessionRecoveryAdmissionError(err) {
				t.Errorf("automatic resume lost recovery classification: %T %v", err, err)
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("expected recovery wire error, got %v", err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorActionUnavailable || data.ClientMutationID != "pending-mutation" || data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked || data.Cause == "persistenceUnavailable" {
				t.Errorf("automatic resume lost recovery mutation data: %+v", wire)
			}
			if !cfg.ResumeLocks.RecoveryState(sessionID).ResumeRequired {
				t.Fatal("automatic resume acknowledged explicit recovery requirement")
			}
		})
	}
}
