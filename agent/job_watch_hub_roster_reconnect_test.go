package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestHubRosterWatchRebindDiscoversWorkingWorker(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	jm := root.jobManager
	jm.enqueue = root.enqueueJobNotification
	result, err := jm.configureHubRosterWatch(t.Context(), "local:manager", "")
	if err != nil {
		t.Fatal(err)
	}
	a := appwire.RosterEvent{Ref: "local:a", Level: "working", Kind: "idle"}
	b := appwire.RosterEvent{Ref: "local:b", Level: "working", Kind: "idle"}
	if err := jm.onHubRoster(result.WatchID, rosterUpdate(10, true, a)); err != nil {
		t.Fatal(err)
	}
	initial, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(initial) != 1 {
		t.Fatalf("initial delivered=%d, want one", len(initial))
	}
	if entries := rosterFrameEntries(t, initial[0].notification.watchSendFrame); len(entries) != 1 || entries[0] != a {
		t.Fatalf("first snapshot changed broker event kind: %+v", entries)
	}
	if err := jm.settleWatchSendDelivered(initial[0].watchCfg, initial[0].watchState); err != nil {
		t.Fatal(err)
	}
	jm.resetHubRosterRevision(result.WatchID)
	if err := jm.onHubRoster(result.WatchID, rosterUpdate(1, true, a, b)); err != nil {
		t.Fatal(err)
	}
	delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(delivered) != 1 {
		t.Fatalf("rebind delivered=%d, want one delta", len(delivered))
	}
	frame := delivered[0].notification.watchSendFrame
	entries := rosterFrameEntries(t, frame)
	if len(entries) != 1 || entries[0].Ref != b.Ref || entries[0].Level != "working" || entries[0].Kind != "new" {
		t.Errorf("rebind entries=%+v, want only B as new", entries)
	}
	if rosterFrameField(frame, "roster_initial: ") != "" {
		t.Error("ordinary rebind delta carried authoritative initial marker")
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(result.WatchID)
	retained := ok && len(cfg.rosterBaseline) == 2 && cfg.rosterBaseline[a.Ref] == a.AttentionEntry && cfg.rosterBaseline[b.Ref] == b.AttentionEntry
	jm.mu.Unlock()
	if !retained {
		t.Error("rebind baseline lost A or newly discovered B")
	}
}
