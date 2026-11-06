package hub

import (
	"context"
	"os"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// effortRelayReadErrorSource is the one method ensureEffortRelay's
// preparation reads: a local source whose thread read answers the test's
// error. The embedded nil Source satisfies the rest of the interface because
// the failing read returns before any other method is reached.
type effortRelayReadErrorSource struct {
	appsource.Source
	readErr error
}

func (s *effortRelayReadErrorSource) ID() string { return "local" }

func (s *effortRelayReadErrorSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, s.readErr
}

// TestEnsureEffortRelayColdVsPrepFailure pins the error split the low review
// finding asked for: a cold session's failed read defers to the resume path's
// afterResume hook, while any other preparation failure is surfaced instead of
// letting the change apply with no listener for its frame.
func TestEnsureEffortRelayColdVsPrepFailure(t *testing.T) {
	newRelays := func(t *testing.T, readErr error) (*hubRelay, *appsource.Registry) {
		t.Helper()
		sources := appsource.NewRegistry()
		sources.Add(&effortRelayReadErrorSource{readErr: readErr})
		relays := newHubRelay(appserver.NewServer(appserver.ServerConfig{}), hubcore.WebConfig{}, sources)
		return relays, sources
	}

	t.Run("cold session defers to the resume path", func(t *testing.T) {
		relays, sources := newRelays(t, appwire.SessionUnavailable("daemon exited"))
		if err := ensureEffortRelay(context.Background(), hubcore.WebConfig{}, relays, sources, "local:th"); err != nil {
			t.Fatalf("cold session relay prep = %v, want nil so afterResume attaches it", err)
		}
	})

	t.Run("other preparation failure is surfaced", func(t *testing.T) {
		relays, sources := newRelays(t, appwire.Unavailable("relay admission refused"))
		if err := ensureEffortRelay(context.Background(), hubcore.WebConfig{}, relays, sources, "local:th"); err == nil {
			t.Fatal("preparation failure was swallowed; the change would apply with a stale subscriber footer")
		}
	})

	t.Run("no relay registry is a no-op", func(t *testing.T) {
		sources := appsource.NewRegistry()
		if err := ensureEffortRelay(context.Background(), hubcore.WebConfig{}, nil, sources, "local:th"); err != nil {
			t.Fatalf("nil relays = %v, want nil", err)
		}
	})
}

// TestEnsureEffortRelayRefreshesDiscovery pins the motivating race: another
// caller's daemon appears after the roster's initial scan, so the snapshot the
// hub started with cannot name it. Relay preparation must refresh discovery
// before resolving, or it treats a running session as cold and the change goes
// out with no listener.
func TestEnsureEffortRelayRefreshesDiscovery(t *testing.T) {
	runDir := t.TempDir()
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: "discovered"}).SetProcessAlive(func(int) bool { return true })
	roster.Refresh()
	if _, ok := roster.Find("discovered"); ok {
		t.Fatal("fixture: roster already knows the daemon")
	}
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:        os.Getpid(),
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws://127.0.0.1:1/discovered",
		SourceID:   "local",
		ThreadID:   "discovered",
		SessionID:  "discovered",
		InstanceID: "discovered",
	})

	sources := appsource.NewRegistry()
	sources.Add(&effortRelayReadErrorSource{readErr: appwire.SessionUnavailable("daemon exited")})
	cfg := hubcore.WebConfig{Roster: roster}
	relays := newHubRelay(appserver.NewServer(appserver.ServerConfig{}), cfg, sources)

	if err := ensureEffortRelay(context.Background(), cfg, relays, sources, "local:discovered"); err != nil {
		t.Fatalf("cold relay prep = %v, want nil", err)
	}
	if _, ok := roster.Find("discovered"); !ok {
		t.Fatal("ensureEffortRelay did not refresh daemon discovery; a just-started daemon stays invisible")
	}
}
