package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestHubRosterArchivedLiveSession(t *testing.T) {
	oldTicker := hubTicker
	hubTicker = func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}
	t.Cleanup(func() { hubTicker = oldTicker })

	for _, archivedInitially := range []bool{true, false} {
		name := "archive_after_subscribe"
		if archivedInitially {
			name = "archived_initial_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))
			live := hubcore.LiveEntry{
				Entry:     rendezvous.Entry{WorkspaceRef: "local:worker", SessionID: "worker"},
				SessionID: "worker", Status: "awaiting", PendingAsk: true,
			}
			hub, web := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
				Past: hubcore.NewPastIndex(""), Roster: hubcore.NewRosterWithEntries(live),
			})
			defer hub.Close()
			setArchived := func() {
				t.Helper()
				if err := archive.Set("", "session", live.SessionID, true, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			publish := func() {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				poke := make(chan struct{})
				done := make(chan struct{})
				go func() {
					watchHubAttention(ctx, poke, archive, web)
					close(done)
				}()
				// The unbuffered poke is accepted only after the initial run has
				// published. Wait for exit before inspecting broker state.
				poke <- struct{}{}
				cancel()
				<-done
			}
			if archivedInitially {
				setArchived()
			}
			publish()
			sub, err := web.navigation.roster.Subscribe(appwire.ManagerView{Ref: "local:manager", Manager: true})
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Cancel()
			initial := <-sub.Updates
			if !initial.Initial || len(initial.Events) != 1 || initial.Events[0].Ref != "local:worker" || initial.Events[0].Level != "needs_you" || !initial.Events[0].AskPending {
				t.Fatalf("live roster snapshot = %+v", initial)
			}
			if !archivedInitially {
				setArchived()
			}
			publish()
			publish()
			select {
			case update := <-sub.Updates:
				t.Fatalf("archiving a live session produced a roster delta: %+v", update)
			default:
			}
			if len(sub.baseline) != 1 || sub.baseline[live.SessionID].Ref != "local:worker" {
				t.Fatalf("archived live baseline = %+v", sub.baseline)
			}
			decisions, err := archive.Decisions()
			if err != nil {
				t.Fatal(err)
			}
			snapshot := web.navigationSnapshot(t.Context())
			attention, summary := hubcore.DeriveAttention(snapshot.metas, snapshot.live, decisions)
			if len(attention) != 0 || summary != (appwire.AttentionSummary{}) {
				t.Fatalf("archived attention map = %+v, summary = %+v", attention, summary)
			}
		})
	}
}
