package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestManagerWatchResyncReReadsWithoutStatusSelector(t *testing.T) {
	for _, remoteTarget := range []bool{false, true} {
		name := "local"
		if remoteTarget {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool := &aliasRelayPool{}
			targetSource := &aliasRelaySource{pool: pool}
			sources := appsource.NewRegistry()
			ref := "local:target"
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
			if remoteTarget {
				remoteSources := appsource.NewRegistry()
				remoteSources.Add(targetSource)
				remoteServer := newHubAppServer(cfg, remoteSources)
				defer remoteServer.Shutdown(ctx)
				remoteHTTP := httptest.NewServer(http.HandlerFunc(remoteServer.ServeWebSocket))
				defer remoteHTTP.Close()
				client := dialHubRPC(t, remoteHTTP)
				defer client.Close()
				if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				sources.Add(appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil }))
				ref = "remote:target"
			} else {
				sources.Add(targetSource)
			}
			var stateReads atomic.Int32
			local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				stateReads.Add(1)
				return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, SourceID: "local", SessionID: "manager", InstanceID: "manager", WorkspaceRef: "local:manager"}, SessionID: "manager", Manager: true}}
			}, nil)
			server := appserver.NewServer(appserver.ServerConfig{})
			defer server.Shutdown(ctx)
			owner := &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)}
			var reads atomic.Int32
			readStarted, releaseRead := make(chan struct{}), make(chan struct{})
			appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				read, err := owner.Read(ctx, p)
				if err != nil {
					return read, err
				}
				read.Thread.Evener.InstanceID, read.BootGeneration = "instance", "1"
				if p.IncludeTurns {
					n := reads.Add(1)
					if n == 2 {
						close(readStarted)
						select {
						case <-releaseRead:
						case <-ctx.Done():
							return read, ctx.Err()
						}
						accepted := true
						read.Thread.Turns = []appwire.Turn{{ID: "watched-turn", Items: []appwire.ThreadItem{{ID: "disconnected-done", Observation: &appwire.SessionObservationMetadata{TurnID: "watched-turn", EntryID: "disconnected-done", EndTurn: true, EndReason: "done", TerminalAccepted: &accepted, CausalKnown: true}}}}}
					}
					if n >= 3 {
						read.BootGeneration = "2"
					}
				}
				return read, nil
			})
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
			if _, err := human.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
				t.Fatal(err)
			}
			bound := &boundSessionOperations{owner: owner, local: local, binding: appwire.SessionBinding{Ref: "local:manager", InstanceID: "manager", BindingID: "binding"}}
			updates, err := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Completion: true})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case u := <-updates:
				if u.Snapshot == nil {
					t.Fatal("missing first cut")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			emitResync := func() {
				raw, err := json.Marshal(appwire.ThreadResyncParams{Ref: "local:target", ThreadID: "target"})
				if err != nil {
					t.Fatal(err)
				}
				pool.emit(t, appwire.Notification{Method: appwire.NotifyEvenerThreadResync, Params: raw})
			}
			emitResync()
			select {
			case <-readStarted:
			case <-ctx.Done():
				t.Fatal("completion-only resync did not re-read")
			}
			beforeDrain := stateReads.Load()
			server.BroadcastAll("test/drain-during-resync", struct{}{})
			awaitActivityRelayCondition(ctx, t, func() bool { return stateReads.Load() > beforeDrain }, "watch stopped draining while the resync response was blocked")
			// A notification is queued while the snapshot response is held. The
			// existing subscription handoff must release it after the new snapshot.
			server.Broadcast(ref, appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{Ref: ref, ThreadID: "target"})
			close(releaseRead)
			awaitSnapshot := func(generation string) appwire.SessionWatchUpdate {
				t.Helper()
				for {
					select {
					case u, ok := <-updates:
						if !ok {
							t.Fatal("watch closed before authoritative cut")
						}
						if u.Snapshot != nil {
							if u.Metadata.GenerationID != generation {
								t.Fatalf("generation=%s want=%s", u.Metadata.GenerationID, generation)
							}
							return u
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}
			cut := awaitSnapshot("1")
			if len(cut.Snapshot.Thread.Turns) != 1 || cut.Snapshot.Thread.Turns[0].Items[0].Observation.TerminalAccepted == nil {
				t.Fatal("resync lost disconnected terminal evidence")
			}
			emitResync()
			awaitSnapshot("2")
			// Generation replacement is exposed as a new authoritative cut so the
			// runtime ends the old watch rather than silently rebinding it.
			server.Broadcast(ref, "test/human-survived", struct{}{})
			for {
				select {
				case n := <-human.Notifications():
					if n.Method == "test/human-survived" {
						return
					}
				case <-ctx.Done():
					t.Fatal("human subscription lost during resync")
				}
			}
		})
	}
}
