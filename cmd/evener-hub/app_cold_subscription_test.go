package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRPCColdSubscriptionOwnership(t *testing.T) {
	root := t.TempDir()
	first, second := hubtest.SessionID(t), hubtest.SessionID(t)
	for _, id := range []string{first, second} {
		if err := schema.SaveSessionMeta(filepath.Join(root, "projects", "project-0000000000"), schema.SessionMeta{ID: id, EnvInfo: schema.EnvironmentInfo{WorkingDir: t.TempDir()}}); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{Past: past, ResumeLocks: hubcore.NewResumeLocks()})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	ctx := context.Background()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	read := func(id string, subscribe, replace bool) {
		t.Helper()
		if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + id, IncludeTurns: false, Subscribe: subscribe, ReplaceSubscription: replace}); err != nil {
			t.Fatal(err)
		}
	}
	assertOwners := func(a, b int) {
		t.Helper()
		if got := web.appRPC.SubscriberCount("local:" + first); got != a {
			t.Fatalf("first subscribers=%d, want %d", got, a)
		}
		if got := web.appRPC.SubscriberCount("local:" + second); got != b {
			t.Fatalf("second subscribers=%d, want %d", got, b)
		}
	}
	read(first, false, false)
	assertOwners(0, 0)
	read(first, true, false)
	assertOwners(1, 0)
	read(second, true, false)
	assertOwners(1, 1)
	read(second, true, true)
	assertOwners(0, 1)
	// A failed replacement must leave the existing observer attached.
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:missing", Subscribe: true, ReplaceSubscription: true}); err == nil {
		t.Fatal("missing session read succeeded")
	}
	assertOwners(0, 1)
	if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:" + second}); err != nil {
		t.Fatal(err)
	}
	assertOwners(0, 0)
}

// A routable daemon may reject a read during its lifecycle transition. Returning
// an unsequenced saved snapshot then would mix it with the live relay stream.
func TestHubRPCColdFallbackRequiresAbsentLiveRoute(t *testing.T) {
	root := t.TempDir()
	sessionID := buildRPCParentSessionWithWorkingDir(t, filepath.Join(root, "projects", "project-0000000000"), t.TempDir())
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	daemon := appserver.NewServer(appserver.ServerConfig{SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, appwire.SessionUnavailable(threadNotFoundMessagePrefix + sessionID)
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 21000, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local", ThreadID: sessionID, SessionID: sessionID})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, Subscribe: true}); err == nil {
		t.Fatal("live route failure was hidden by an unsequenced saved snapshot")
	}
	if got := web.appRPC.SubscriberCount("local:" + sessionID); got != 0 {
		t.Fatalf("failed read retained %d subscribers", got)
	}
}
