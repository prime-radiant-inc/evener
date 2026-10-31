package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestManagerWatchAdmissionRequiresTargetSessionTools(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		request    appwire.SessionWatchRequest
		wantRefuse bool
		upgraded   bool
	}{
		{name: "old target completion", request: appwire.SessionWatchRequest{Ref: "local:target", Completion: true}, wantRefuse: true},
		{name: "old target communicate", request: appwire.SessionWatchRequest{Ref: "local:target", Events: []string{"communicate"}}, wantRefuse: true},
		{name: "old target mixed", request: appwire.SessionWatchRequest{Ref: "local:target", Events: []string{"status", "communicate"}}, wantRefuse: true},
		{name: "old target status", request: appwire.SessionWatchRequest{Ref: "local:target", Events: []string{"status"}}},
		{name: "upgraded target empty history", request: appwire.SessionWatchRequest{Ref: "local:target", Completion: true}, upgraded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			const managerRef, targetRef = "local:manager", "local:target"
			peer := newObservationAdmissionPeer(t, tc.upgraded)
			entries := []appsource.LocalDaemonEntry{
				{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", ThreadID: "manager", SessionID: "manager", WorkspaceRef: managerRef}, SessionID: "manager", Manager: true},
				{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: peer.URL, SourceID: "local", ThreadID: "target", SessionID: "target", WorkspaceRef: targetRef}, SessionID: "target"},
			}
			source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry { return entries }, nil)
			sources := appsource.NewRegistry()
			sources.Add(source)
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
			server := newHubAppServer(cfg, sources)
			defer server.Shutdown(ctx)
			owner := &sessionOperations{cfg: cfg, sources: sources, server: server}
			bound := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: managerRef, InstanceID: "manager", BindingID: "binding"}}
			updates, err := bound.Watch(ctx, tc.request)
			if tc.wantRefuse {
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidRequest {
					t.Fatalf("Watch error = %v, want WireError code %d", err, appwire.CodeInvalidRequest)
				}
				return
			}
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			if tc.upgraded {
				select {
				case initial := <-updates:
					if initial.Snapshot == nil || len(initial.Snapshot.Thread.Turns) != 0 {
						t.Fatalf("upgraded empty-history initial snapshot = %+v", initial)
					}
				case <-ctx.Done():
					t.Fatal("empty-history completion watch did not attach")
				}
				return
			}
			select {
			case initial := <-updates:
				if initial.Snapshot == nil {
					t.Fatalf("status watch initial cut = %+v", initial)
				}
			case <-ctx.Done():
				t.Fatal("status watch did not attach")
			}
			peer.server.Broadcast("target", appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{Ref: targetRef, ThreadID: "target"})
			select {
			case update := <-updates:
				if update.Event != "status" || update.Notification == nil || update.Metadata.Ref != targetRef {
					t.Fatalf("status update = %+v", update)
				}
			case <-ctx.Done():
				t.Fatal("scripted status notification was not delivered")
			}
		})
	}
}

// observationAdmissionPeer is an external scripted v7 daemon. Old peers return
// no SessionTools feature; both versions return history items without evidence.
type observationAdmissionPeer struct {
	URL    string
	server *appserver.Server
}

func newObservationAdmissionPeer(t *testing.T, features bool) observationAdmissionPeer {
	t.Helper()
	peer := observationAdmissionPeer{server: appserver.NewServer(appserver.ServerConfig{SourceID: "local", Features: appwire.FeatureSet{SessionTools: features}})}
	appserver.HandleTyped(peer.server.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if err := appserver.SubscribeWithAdmission(ctx, "target", false); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		turns := []appwire.Turn{{ID: "old-turn", ItemsView: appwire.TurnItemsViewFragment, Items: []appwire.ThreadItem{{ID: "without-observation", TranscriptKey: "old-entry", Position: &appwire.ThreadItemPosition{Entry: 1}}}}}
		if features {
			turns = nil
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "target", SessionID: "target", Evener: appwire.EvenerThread{Ref: "local:target", InstanceID: "target"}, Turns: turns}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(peer.server.ServeWebSocket))
	t.Cleanup(func() { _ = peer.server.Shutdown(t.Context()); httpServer.Close() })
	peer.URL = "ws" + strings.TrimPrefix(httpServer.URL, "http")
	return peer
}
