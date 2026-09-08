package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

func TestNavigationPreservesKnownFavoritesDuringOwnershipFailure(t *testing.T) {
	runDir := t.TempDir()
	id := hubtest.SessionID(t)
	writeRendezvous(t, runDir, rendezvous.Entry{PID: os.Getpid(), SessionID: id, ThreadID: id, WorkingDir: t.TempDir()})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: id, status: "idle"})
	roster.Refresh()
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", id, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	unresolved := hubtest.SessionID(t)
	if err := favorites.Set("session", unresolved, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Roster: roster, Favorite: favorites})
	source := webNavigationSource{web: web}
	check := func(rename bool) {
		t.Helper()
		snapshot, err := source.Capture(t.Context(), "generation", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Inputs.LiveEntries) != 1 || snapshot.Inputs.LiveEntries[0].SessionID != id {
			t.Fatalf("known live row lost: %+v", snapshot.Inputs.LiveEntries)
		}
		if !snapshot.Inputs.SessionFavorite[id] {
			t.Fatal("known favorite lost")
		}
		if got := snapshot.Inputs.Renameable[id]; got != rename {
			t.Fatalf("rename=%v want=%v", got, rename)
		}
	}
	check(true)
	bad := filepath.Join(runDir, "424242.json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if roster.OwnershipError() == nil || roster.DaemonOwnershipAbsent() {
		t.Fatal("incomplete discovery claimed absent ownership")
	}
	check(false)
	decisions, err := favorites.Favorites()
	if err != nil || !decisions[hubcore.ArchiveKey{Kind: "session", ID: id}] || !decisions[hubcore.ArchiveKey{Kind: "session", ID: unresolved}] {
		t.Fatalf("favorite decision changed: %v %v", decisions, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.Capture(ctx, "generation", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if roster.OwnershipError() != nil {
		t.Fatal(roster.OwnershipError())
	}
	check(true)
}

func assertNavigationOwnershipReadOnly(t *testing.T, web *WebServer) {
	t.Helper()
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatalf("navigation read unavailable during ownership uncertainty: %v", err)
	}
	for id, renameable := range snapshot.Inputs.Renameable {
		if isLocalRouteID(id) && renameable {
			t.Errorf("local rename enabled during ownership uncertainty: %s", id)
		}
	}
}
