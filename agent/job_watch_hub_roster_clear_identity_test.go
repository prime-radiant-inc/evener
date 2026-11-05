package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestHubRosterWatchRebindClearSameRef(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"local", "remote"} {
		t.Run(source, func(t *testing.T) {
			before := appwire.RosterEvent{Ref: source + ":a", ID: "before-clear", Level: "idle", Kind: "idle"}
			after := before
			after.ID = "after-clear"
			if source == "remote" {
				before.ID, after.ID = before.Ref, after.Ref
				before.RosterInstanceID, after.RosterInstanceID = "before-clear", "after-clear"
			}
			testHubRosterWatchRebindClearSameRef(t, before, after)
		})
	}
}

func testHubRosterWatchRebindClearSameRef(t *testing.T, before, after appwire.RosterEvent) {
	t.Helper()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	jm := root.jobManager
	jm.enqueue = root.enqueueJobNotification
	result, err := jm.configureHubRosterWatch(t.Context(), "local:manager")
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubRoster(result.WatchID, rosterUpdate(10, true, before)); err != nil {
		t.Fatal(err)
	}
	initial, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(initial) != 1 {
		t.Fatalf("initial delivered=%d, want one", len(initial))
	}
	if err := jm.settleWatchSendDelivered(initial[0].watchCfg, initial[0].watchState); err != nil {
		t.Fatal(err)
	}
	jm.resetHubRosterRevision(result.WatchID)
	encoded, err := json.Marshal(rosterUpdate(1, true, after))
	if err != nil {
		t.Fatal(err)
	}
	var reconnect appwire.SessionWatchUpdate
	if err := json.Unmarshal(encoded, &reconnect); err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubRoster(result.WatchID, reconnect); err != nil {
		t.Fatal(err)
	}
	delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
	if len(delivered) != 1 {
		t.Fatalf("rebind delivered=%d, want one delta", len(delivered))
	}
	frame := delivered[0].notification.watchSendFrame
	entries := rosterFrameEntries(t, frame)
	if len(entries) != 1 || entries[0].Ref != after.Ref || entries[0].Level != "idle" || entries[0].Kind != "new" {
		t.Errorf("rebind Clear entries=%+v, want only the replacement as new", entries)
	}
	if rosterFrameField(frame, "roster_initial: ") != "" {
		t.Error("ordinary rebind delta carried authoritative initial marker")
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(result.WatchID)
	retained := ok && len(cfg.rosterBaseline) == 1 && cfg.rosterBaseline[after.Ref] == after.AttentionEntry
	jm.mu.Unlock()
	if !retained {
		t.Error("rebind baseline did not retain the replacement session identity")
	}
}
