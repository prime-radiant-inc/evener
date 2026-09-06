package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRelayStableCompletedLifecycle(t *testing.T) {
	for _, mode := range []string{"local:stable", "local:current"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				appserver.Subscribe(ctx, "current")
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "current", SessionID: "current", Evener: appwire.EvenerThread{Ref: "local:current"}}}, nil
			})
			upstream := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
			defer upstream.Close()
			source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
				return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable", Endpoint: upstream.URL, Protocol: appwire.ProtocolVersion}}}
			}, http.DefaultClient)
			sources := appsource.NewRegistry()
			sources.Add(source)
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
			server := newHubAppServer(cfg, sources)
			wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
			defer wire.Close()
			client := dialHubRPC(t, wire)
			defer client.Close()
			if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			response, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:stable", Subscribe: true})
			if err != nil {
				t.Fatal(err)
			}
			awaitLiveHubSubscriptions(t, server, 1)
			t.Logf("request=local:stable response=%s subscribers stable=%d current=%d", response.Thread.ID, server.SubscriberCount("local:stable"), server.SubscriberCount("local:current"))
			if got := server.SubscriberCount("local:stable"); got != 1 {
				t.Errorf("relay delivery/idle owner stable count=%d want 1", got)
			}
			if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:current", Subscribe: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: mode}); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"local:stable", "local:current"} {
				if got := server.SubscriberCount(key); got != 0 {
					t.Errorf("unsubscribe %s leaves %s subscribers=%d", mode, key, got)
				}
				server.Broadcast(key, "test/stale-completed", map[string]any{})
			}
			server.BroadcastAll("test/alias-barrier", map[string]any{})
			if got := aliasMethodCountUntilBarrier(t, client, "test/stale-completed"); got != 0 {
				t.Errorf("stale deliveries=%d", got)
			}

		})
	}
}

func TestHubRelayNonAtomicUnsubscribeGap(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "initial"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			source := &relayBroadcastSource{thread: appwire.Thread{ID: "probe", SessionID: "probe", Source: "codex", Evener: appwire.EvenerThread{Ref: "codex:probe"}}, notifications: make(chan appwire.Notification), subscribed: make(chan struct{}, 2), canceled: make(chan struct{}, 2)}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var gateOnce sync.Once
			gate := func(string) {
				gateOnce.Do(func() { close(entered) })
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}
			if existing {
				cfg.RelayHooks.BeforeExistingRegistration = gate
			} else {
				cfg.RelayHooks.AfterPlaceholder = gate
			}
			sources := appsource.NewRegistry()
			sources.Add(source)
			server := newHubAppServer(cfg, sources)
			wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
			defer wire.Close()
			var verifyKeeper func()
			if existing {
				keeper := dialHubRPC(t, wire)
				defer keeper.Close()
				if _, err := keeper.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
					t.Fatal(err)
				}
				if _, err := keeper.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "codex:probe", Subscribe: true}); err != nil {
					t.Fatal(err)
				}
				verifyKeeper = func() {
					for _, method := range []string{"test/keeper-alive", "test/alias-barrier"} {
						select {
						case source.notifications <- appwire.Notification{Method: method, Params: []byte(`{"threadId":"probe"}`)}:
						case <-ctx.Done():
							t.Fatal("keeper transport stopped")
						}
					}
					if got := aliasMethodCountUntilBarrier(t, keeper, "test/keeper-alive"); got != 1 {
						t.Fatalf("keeper deliveries=%d, want 1", got)
					}
				}
			}
			client := dialHubRPC(t, wire)
			defer client.Close()
			if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "codex:probe", Subscribe: true})
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("registration barrier not reached")
			}
			if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "codex:probe"}); err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case err := <-done:
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
					t.Fatalf("canceled admission error=%v, want unavailable", err)
				}
				if data, ok := wire.Data.(map[string]any); !ok || data["evenerErrorInfo"] != string(appwire.ErrorSessionUnavailable) {
					t.Fatalf("canceled admission data=%#v, want sessionUnavailable", wire.Data)
				}
			case <-ctx.Done():
				t.Fatal("read did not finish")
			}
			want := 0
			if existing {
				want = 1
			}
			if got := server.SubscriberCount("codex:probe"); got != want {
				t.Errorf("%s registration resurrected canceled admission: subscribers=%d want=%d", name, got, want)
			}
			if verifyKeeper != nil {
				verifyKeeper()
			} else {
				select {
				case <-source.canceled:
				case <-ctx.Done():
					t.Fatal("rejected initial transport was not canceled")
				}
			}
			server.Broadcast("codex:probe", "test/stale-nonatomic", map[string]any{})
			server.BroadcastAll("test/alias-barrier", map[string]any{})
			if got := aliasMethodCountUntilBarrier(t, client, "test/stale-nonatomic"); got != 0 {
				t.Errorf("canceled non-atomic request delivered %d stale frames; want 0", got)
			}
			if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "codex:probe", Subscribe: true}); err != nil {
				t.Fatalf("later legitimate read failed (stale placeholder): %v", err)
			}
			if got := server.SubscriberCount("codex:probe"); got != want+1 {
				t.Fatalf("later subscribers=%d, want %d", got, want+1)
			}
		})
	}
}
