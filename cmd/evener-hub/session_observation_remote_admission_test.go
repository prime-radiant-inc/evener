package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestManagerRemoteWatchAdmissionUsesThreadReadTargetSupport(t *testing.T) {
	for _, tc := range []struct {
		name        string
		support     *bool
		completion  bool
		wantRefusal bool
	}{
		{name: "unsupported target", support: boolPointer(false), completion: true, wantRefusal: true},
		{name: "old hub omits target support", support: nil, completion: true, wantRefusal: true},
		{name: "supported target", support: boolPointer(true), completion: true},
		{name: "status remains available", support: boolPointer(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			managerRef := "local:manager"
			managerEntry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", ThreadID: "manager", SessionID: "manager", WorkspaceRef: managerRef}
			local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				return []appsource.LocalDaemonEntry{{Entry: managerEntry, SessionID: "manager", Manager: true}}
			}, nil)
			remoteServer := appserver.NewServer(appserver.ServerConfig{ServerName: "remote-hub", SourceID: "remote", Features: appwire.FeatureSet{SessionTools: true}})
			appserver.HandleTyped(remoteServer.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				if err := appserver.SubscribeWithAdmission(ctx, "target", false); err != nil {
					return appwire.ThreadReadResponse{}, err
				}
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "target", SessionID: "target", Evener: appwire.EvenerThread{Ref: "local:target", InstanceID: "target"}}, TargetSessionToolsSupported: tc.support}, nil
			})
			remoteTransport, err := remoteServer.NewInProcessTransport(ctx)
			if err != nil {
				t.Fatal(err)
			}
			remoteClient := appwire.NewClient(remoteTransport)
			remoteClient.Start(ctx)
			defer remoteClient.Close()
			if _, err := remoteClient.Initialize(ctx, appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			remote := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil })
			sources := appsource.NewRegistry()
			sources.Add(local)
			sources.Add(remote)
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
			server := newHubAppServer(cfg, sources)
			defer server.Shutdown(ctx)
			owner := &sessionOperations{cfg: cfg, sources: sources, server: server}
			bound := &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: managerRef, InstanceID: "manager", BindingID: "binding"}}
			updates, err := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: "remote:target", Completion: tc.completion, Events: []string{"status"}})
			if tc.wantRefusal {
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidRequest || wire.Message != "target cannot report recorded-observation support" {
					t.Fatalf("Watch error = %#v, want InvalidRequest refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			select {
			case initial := <-updates:
				if initial.Snapshot == nil {
					t.Fatalf("initial update = %+v, want snapshot", initial)
				}
			case <-ctx.Done():
				t.Fatal("remote watch did not attach")
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }
