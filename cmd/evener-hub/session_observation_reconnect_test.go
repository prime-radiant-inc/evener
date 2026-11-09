package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestManagerWatchSharedDaemonReconnect(t *testing.T) {
	for _, remoteTarget := range []bool{false, true} {
		name := "local"
		if remoteTarget {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			f := newHubRelayProgressFixture(t, hubcore.RelayLifecycleHooks{})
			ctx := f.ctx
			sources := appsource.NewRegistry()
			target := rootRef
			if remoteTarget {
				remoteClient := f.open()
				sources.Add(appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil }))
				target = "remote:" + rootID
			} else {
				sources.Add(f.source)
			}
			local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", SessionID: "manager", InstanceID: "manager", WorkspaceRef: "local:manager"}, SessionID: "manager", Manager: true}}
			}, nil)
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
			server := newHubAppServer(cfg, sources)
			defer server.Shutdown(ctx)
			owner := &sessionOperations{cfg: cfg, sources: sources, server: server}
			humanTransport, err := server.NewInProcessTransport(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer humanTransport.Close()
			human := appwire.NewClient(humanTransport)
			human.Start(ctx)
			if _, err := human.Initialize(ctx, appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			if _, err := human.ThreadRead(ctx, appwire.ThreadReadParams{Ref: target, Subscribe: true}); err != nil {
				t.Fatal(err)
			}
			bound := &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: "local:manager", InstanceID: "manager", BindingID: "binding"}}
			updates, err := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: target, Completion: true})
			if err != nil {
				t.Fatal(err)
			}
			var initial appwire.SessionWatchUpdate
			select {
			case initial = <-updates:
				if initial.Snapshot == nil {
					t.Fatal("missing initial cut")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Close real hijacked daemon connections without stopping the server or
			// its canonical relay owners. No Manager-side reconnect scheduler exists.
			f.closeUpstream()
			interrupted := false
			for {
				select {
				case u, ok := <-updates:
					if !ok {
						t.Fatal("watch ended instead of recovering the shared daemon connection")
					}
					if u.Notification != nil && u.Notification.Method == appwire.NotifyEvenerThreadResync {
						interrupted = true
					}
					if u.Snapshot != nil {
						if !interrupted || !reflect.DeepEqual(u.Metadata, initial.Metadata) {
							t.Fatalf("recovery cut=%+v initial=%+v interrupted=%t", u.Metadata, initial.Metadata, interrupted)
						}
						goto recovered
					}
				case <-ctx.Done():
					t.Fatal("shared reconnect did not recover Manager snapshot")
				}
			}
		recovered:
			f.daemon.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: rootID, Data: events.NotesUpdatedData{HumanNote: "after-manager-reconnect"}})
			for {
				select {
				case n := <-human.Notifications():
					if n.Method == appwire.NotifyEvenerNotesUpdated {
						var params appwire.NotesUpdatedParams
						if err := json.Unmarshal(n.Params, &params); err != nil {
							t.Fatal(err)
						}
						if params.HumanNote == "after-manager-reconnect" {
							return
						}
					}
				case <-ctx.Done():
					t.Fatal("human lost shared subscription after daemon reconnect")
				}
			}
		})
	}
}
