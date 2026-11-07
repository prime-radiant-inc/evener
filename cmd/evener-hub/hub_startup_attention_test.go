package hub

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// TestRosterChangePokesTheAttentionWatcher pins the production wiring behind
// attention freshness: the attention watcher only recomputes on its own
// five-second tick unless something pokes it, so a roster change must poke it.
// Without the poke this test blocks until its bound because nothing else in the
// fixture can deliver a poke.
func TestRosterChangePokesTheAttentionWatcher(t *testing.T) {
	t.Parallel()
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

	writeRendezvous(t, runDir, rendezvous.Entry{
		Protocol:   appwire.ProtocolVersion,
		PID:        os.Getpid(),
		Endpoint:   "ws://127.0.0.1:1",
		SessionID:  "sess-poke-1",
		ThreadID:   "sess-poke-1",
		InstanceID: "sess-poke-1",
	})
	roster.Refresh()

	select {
	case <-r.attentionPoke:
	case <-time.After(2 * time.Second):
		t.Fatal("a roster change did not poke the attention watcher")
	}
	select {
	case <-r.remotePoke:
		t.Fatal("a local roster change must not wake the remote-thread refresher")
	default:
	}
}
