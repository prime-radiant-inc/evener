package hub

import (
	"context"
	"errors"
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

func TestHubAdmissionInventoryChangeRejected(t *testing.T) {
	testHubAdmissionInventoryChange(t, false)
}

func TestHubAdmissionStableOwnerChangeSucceeds(t *testing.T) {
	testHubAdmissionInventoryChange(t, true)
}

func testHubAdmissionInventoryChange(t *testing.T, sameOwner bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var swapped atomic.Bool
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		id := "current"
		if sameOwner && swapped.Load() {
			id = "next"
		}
		appserver.Subscribe(ctx, id)
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: id, SessionID: id, Evener: appwire.EvenerThread{Ref: "local:" + id}}}, nil
	})
	upstream := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer upstream.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var lookups atomic.Int32
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		// First inventory lookup is ResolveSubscriptionAdmission at ingress;
		// second is ResolveRelaySession in the concurrent read handler.
		if lookups.Add(1) == 2 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		stable := "old-workspace"
		current := "current"
		if swapped.Load() && !sameOwner {
			stable = "new-workspace"
		} else if swapped.Load() {
			current = "next"
		}
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{SourceID: "local", ThreadID: stable, SessionID: current, WorkspaceRef: "local:" + stable, Endpoint: upstream.URL, Protocol: appwire.ProtocolVersion}}}
	}, http.DefaultClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	server := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, sources)
	wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer wire.Close()
	client := dialHubRPC(t, wire)
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ref := "local:current"
	if sameOwner {
		ref = "local:old-workspace"
	}
	go func() {
		_, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("inventory barrier not reached")
	}
	swapped.Store(true)
	if !sameOwner {
		if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	select {
	case err := <-done:
		var wire appwire.WireError
		if sameOwner && err != nil {
			t.Fatalf("same-owner read rejected: %v", err)
		} else if !sameOwner && (!errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable) {
			t.Errorf("changed-owner error=%v, want unavailable", err)
		}
	case <-ctx.Done():
		t.Fatal("read blocked")
	}
	if sameOwner {
		if got := server.SubscriberCount(ref); got != 1 {
			t.Fatalf("same-owner subscribers=%d, want 1", got)
		}
		awaitLiveHubSubscriptions(t, server, 1)
		daemon.Broadcast("next", "test/same-owner", map[string]any{"threadId": "next"})
		daemon.Broadcast("next", "test/alias-barrier", map[string]any{"threadId": "next"})
		if got := aliasMethodCountUntilBarrier(t, client, "test/same-owner"); got != 1 {
			t.Fatalf("same-owner delivery=%d, want 1", got)
		}
		return
	}
	if got := server.SubscriberCount("local:current"); got != 0 {
		t.Errorf("stale registration=%d, want 0", got)
	}
	server.Broadcast("local:current", "test/stale-inventory", map[string]any{})
	server.BroadcastAll("test/alias-barrier", map[string]any{})
	if got := aliasMethodCountUntilBarrier(t, client, "test/stale-inventory"); got != 0 {
		t.Errorf("stale inventory delivery=%d, want 0", got)
	}
}

func TestHubUnsubscribeAcceptedExtraField(t *testing.T) {
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
	server := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex("")}, sources)
	wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer wire.Close()
	client := dialHubRPC(t, wire)
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"local:stable", "local:current"} {
		if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
	}
	var result appwire.EmptyResponse
	if err := client.Request(ctx, appwire.MethodThreadUnsubscribe, map[string]any{"ref": "local:stable", "clientTag": "accepted-extra"}, &result); err != nil {
		t.Fatalf("extra field unsubscribe rejected: %v", err)
	}
	for _, key := range []string{"local:stable", "local:current"} {
		if got := server.SubscriberCount(key); got != 0 {
			t.Errorf("accepted extra-field unsubscribe leaves %s=%d, want 0", key, got)
		}
		server.Broadcast(key, "test/stale-extra", map[string]any{})
	}
	server.BroadcastAll("test/alias-barrier", map[string]any{})
	if got := aliasMethodCountUntilBarrier(t, client, "test/stale-extra"); got != 0 {
		t.Errorf("stale extra-field delivery=%d, want 0", got)
	}
}
