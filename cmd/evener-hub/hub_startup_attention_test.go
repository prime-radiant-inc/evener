package hub

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// newAttentionPokeFixture builds the runtime's production navigation
// invalidation wiring for the poke tests. The attention watcher only recomputes
// on its own five-second tick unless something pokes it, so each store's change
// must reach the poke channel; the remote refresher has its own channel that a
// cache change must not feed.
func newAttentionPokeFixture(t *testing.T) (*hubRuntime, string) {
	t.Helper()
	root := t.TempDir()
	runDir := filepath.Join(root, "run")
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: "sess-poke-1", status: "idle"})
	r := &hubRuntime{
		hubStateRoot:  root,
		stderr:        io.Discard,
		roster:        roster,
		past:          hubcore.NewPastIndex(filepath.Join(root, "past")),
		archive:       hubcore.NewArchiveStore(filepath.Join(root, "archive.db")),
		favorite:      hubcore.NewFavoriteStore(filepath.Join(root, "favorite.db")),
		remoteCache:   &hubcore.RemoteThreadCache{},
		pinSections:   hubcore.NewPinSectionStore(filepath.Join(root, "pins.db")),
		sessionSeen:   hubcore.NewSessionSeenStore(filepath.Join(root, "seen.db")),
		inputs:        &hubcore.InputsVersion{},
		attentionPoke: make(chan struct{}, 1),
		remotePoke:    make(chan struct{}, 1),
	}
	r.wireNavigationInvalidation(newTestWebServer(t))
	return r, runDir
}

// TestRosterChangePokesTheAttentionWatcher pins the production wiring behind
// attention freshness for local changes: a roster change must poke the
// attention watcher. Without the poke this test blocks until its bound because
// nothing else in the fixture can deliver a poke.
func TestRosterChangePokesTheAttentionWatcher(t *testing.T) {
	t.Parallel()
	r, runDir := newAttentionPokeFixture(t)

	writeRendezvous(t, runDir, rendezvous.Entry{
		Protocol:   appwire.ProtocolVersion,
		PID:        os.Getpid(),
		Endpoint:   "ws://127.0.0.1:1",
		SessionID:  "sess-poke-1",
		ThreadID:   "sess-poke-1",
		InstanceID: "sess-poke-1",
	})
	r.roster.Refresh()

	waitNavigationSignal(t, r.attentionPoke, "a roster change to poke the attention watcher")
	requireNoRemotePoke(t, r.remotePoke, "a local roster change")
}

// TestRemoteCacheChangePokesTheAttentionWatcher pins the same freshness for
// remote changes: sessions on another host reach the hub only through the
// remote-thread cache, so a cache change must poke the attention watcher too.
// The refresher published, or a host removal pruned, that change, so it must
// not be poked back to re-walk every attached host for nothing.
func TestRemoteCacheChangePokesTheAttentionWatcher(t *testing.T) {
	t.Parallel()
	r, _ := newAttentionPokeFixture(t)

	r.remoteCache.Store([]appwire.Thread{{ID: "remote-thread", Source: "remote-host"}})

	waitNavigationSignal(t, r.attentionPoke, "a remote-cache change to poke the attention watcher")
	requireNoRemotePoke(t, r.remotePoke, "a remote-cache change")

	// RemoveSource has its own onChange call site, so a host removal must poke
	// the watcher too.
	r.remoteCache.RemoveSource("remote-host")
	waitNavigationSignal(t, r.attentionPoke, "a remote-cache removal to poke the attention watcher")
	requireNoRemotePoke(t, r.remotePoke, "a remote-cache removal")
}

// requireNoRemotePoke fails when a change wakes the remote-thread refresher.
func requireNoRemotePoke(t *testing.T, signal <-chan struct{}, change string) {
	t.Helper()
	select {
	case <-signal:
		t.Fatalf("%s must not wake the remote-thread refresher", change)
	default:
	}
}
