package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// The notification rail's restart silence rests on two gaps (8jqp). j6ys closed
// half of the first one — the registry now records WHO receives a
// cross-session watch — so this pins what is left, and pins it as executable
// fact rather than prose a future reader has to re-derive: the watch fired, the
// receiver is named durably, and NOTHING in the log says the fire happened.
//
// That last part is the live blocker. A restart reading this log sees a watch it
// must end runtime_lost and cannot tell "never matched" from "already matched",
// so any end notice it emitted would be a coin flip on whether it lies.
func TestNoSendReceiverWatchFiresWithoutLeavingDurableEvidence(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "npm run dev"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	var received []jobNotification
	installed, err := jm.configureWatch(watchArgs{
		Target:            rec.JobID,
		OutputMatch:       "ready",
		ReceiverSessionID: "S-parent",
		ReceiverNotify:    func(n jobNotification) { received = append(received, n) },
	})
	if err != nil {
		t.Fatalf("install receiver watch: %v", err)
	}

	feedJob(jm, rec.JobID, []byte("server ready\n"))

	// It matched, and only the live callback knows.
	if len(watchChannelNotices(received)) != 1 {
		t.Fatalf("receiver notifications = %+v, want the one output_match fire", received)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	watch := watches[installed.WatchID]
	if watch == nil {
		t.Fatalf("watch %q missing from the durable registry", installed.WatchID)
	}
	// The route half j6ys landed: the registry names the receiver, so a restart
	// can at least ask "whose watch was this".
	if watch.ReceiverSessionID != "S-parent" {
		t.Errorf("registry receiver = %q, want S-parent", watch.ReceiverSessionID)
	}
	// The evidence half, still open: no durable trace of the fire exists. The
	// registry rows are the only events this watch ever wrote.
	registryRows := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.WatchID != installed.WatchID {
			continue
		}
		switch event.Kind {
		case jobstore.EventWatchRegistered, jobstore.EventWatchCleared:
			registryRows++
		default:
			t.Errorf("unexpected durable event %q for a no-send watch — if the notification rail now leaves per-fire evidence, the restart-side notice in noticeUnrestoredWatchEnds must start using it (8jqp)", event.Kind)
		}
	}
	// Guards the guard: a scan that matched nothing would pass the loop above
	// while proving nothing at all.
	if registryRows == 0 {
		t.Fatalf("scanned no events for watch %q; the evidence check above is vacuous", installed.WatchID)
	}
}
