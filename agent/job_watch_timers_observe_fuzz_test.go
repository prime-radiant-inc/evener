//go:build evenerfuzz

package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// FuzzWatchTimersObserveProgram covers observation helpers with real managers
// and stores. The byte selects an independently
// reproducible scenario; the seeds ensure every scenario runs under -run.
func FuzzWatchTimersObserveProgram(f *testing.F) {
	for scenario := byte(0); scenario < 10; scenario++ {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario byte) {
		jm, err := newJobManager(t.TempDir(), "watch-observe", func(jobNotification) {})
		if err != nil {
			t.Fatalf("new job manager: %v", err)
		}
		t.Cleanup(func() { _ = jm.closeStoreOnly() })

		switch scenario % 10 {
		case 0:
			n := jm.watchNotificationFromWatch(nil, "job_nil", "nil config", nil)
			if n.JobID != "job_nil" || n.Reason != "nil config" || n.Provenance != nil {
				t.Fatalf("nil-config notification = %+v", n)
			}
		case 1:
			d := watchSendDelivery{message: "unchanged"}
			if got := jm.snapshotWatchSendFrame(d); got.message != d.message || got.frame != "" {
				t.Fatalf("nil-send snapshot changed delivery: %+v", got)
			}
		case 2:
			cfg, err := newWatchConfig(watchArgs{Target: "job_target", Send: &watchSendArgs{To: runtimeMessageAliasWatched}}, jm.now())
			if err != nil {
				t.Fatalf("new watch config: %v", err)
			}
			key := watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: runtimeMessageAliasWatched}
			jm.watches[key] = cfg
			delivery := jm.watchSendSnapshot(cfg, runtimeMessageAliasCaller, "fuzz", events.SessionEvent{SessionID: jm.sessionID})
			if !jm.isCurrentWatchSendDelivery(delivery) {
				t.Fatal("freshly installed delivery is not current")
			}
			_, _, ok, err := jm.recordWatchSend(delivery)
			if ok || (err != nil && !strings.Contains(err.Error(), "watched_unresolved")) {
				t.Fatalf("unresolved watched send = (ok=%v, err=%v)", ok, err)
			}
		case 3:
			cfg := &watchConfig{watchID: "watch", generation: "generation"}
			state := jm.watchSendState(watchSendDelivery{cfg: cfg}, runtimeMessageAliasCaller)
			if state.DeliveryID == "" {
				t.Fatal("watch send state did not mint delivery id")
			}
		case 4:
			notified := false
			cfg := &watchConfig{
				watchID:           "watch_receiver",
				generation:        "generation_receiver",
				receiverSessionID: "receiver",
				receiverNotify:    func(jobNotification) { notified = true },
			}
			n := jm.watchNotificationFromWatch(cfg, "job_receiver", "routed", nil)
			if n.receiverSessionID != "receiver" || n.receiverNotify == nil {
				t.Fatalf("receiver notification = %+v", n)
			}
			n.receiverNotify(n)
			if !notified {
				t.Fatal("receiver notification callback did not run")
			}
		case 5:
			got := jm.snapshotWatchSendFrame(watchSendDelivery{
				send: &watchSendArgs{Message: "observe"}, selfInfluence: true, gradientDepth: 2,
			})
			if !strings.HasPrefix(got.frame, "<system-reminder>") {
				t.Fatalf("self-influence notice missing from frame: %q", got.frame)
			}
		case 6:
			if _, _, ok, err := jm.recordWatchSend(watchSendDelivery{}); ok || err != nil {
				t.Fatalf("invalid delivery = (ok=%v, err=%v)", ok, err)
			}
		case 7:
			cfg, err := newWatchConfig(watchArgs{Target: "job_terminal", Send: &watchSendArgs{To: runtimeMessageAliasCaller}}, jm.now())
			if err != nil {
				t.Fatalf("new terminal config: %v", err)
			}
			jm.terminalFlush = make(map[*watchConfig]bool)
			jm.terminalFlush[cfg] = true
			d := jm.watchSendSnapshot(cfg, "job_terminal", "terminal", events.SessionEvent{SessionID: jm.sessionID})
			d.allowAfterTerminalExpiry = true
			appendFailure := errors.New("terminal pending append failure")
			jm.appendEvent = func(jobstore.Event) error { return appendFailure }
			if _, _, ok, err := jm.recordWatchSend(d); ok || !errors.Is(err, appendFailure) {
				t.Fatalf("terminal append failure = (ok=%v, err=%v)", ok, err)
			}
			if cfg.pending == nil || len(cfg.pending) != 1 {
				t.Fatalf("unpersisted terminal pending not retained: %+v", cfg.pending)
			}
		case 8:
			cfg, err := newWatchConfig(watchArgs{Target: "job_settled", Send: &watchSendArgs{To: runtimeMessageAliasCaller}}, jm.now())
			if err != nil {
				t.Fatalf("new settled config: %v", err)
			}
			key := watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: runtimeMessageAliasCaller}
			jm.watches[key] = cfg
			d := jm.watchSendSnapshot(cfg, "job_settled", "settled", events.SessionEvent{SessionID: jm.sessionID})
			state := jm.watchSendState(d, runtimeMessageAliasCaller)
			cfg.settledUpdateSeq = make(map[jobstore.WatchSendKey]uint64)
			cfg.settledUpdateSeq[state.Key] = d.updateSeq
			if _, _, ok, err := jm.recordWatchSend(d); ok || err != nil {
				t.Fatalf("settled delivery = (ok=%v, err=%v)", ok, err)
			}
		case 9:
			cfg, err := newWatchConfig(watchArgs{Target: "job_runaway", Send: &watchSendArgs{To: runtimeMessageAliasCaller}}, jm.now())
			if err != nil {
				t.Fatalf("new runaway config: %v", err)
			}
			key := watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: runtimeMessageAliasCaller}
			jm.watches[key] = cfg
			d := jm.watchSendSnapshot(cfg, "job_runaway", "runaway", events.SessionEvent{SessionID: jm.sessionID})
			d.fuseDepth = runawaySelfInfluenceDepth
			if _, _, ok, err := jm.recordWatchSend(d); ok || err != nil {
				t.Fatalf("runaway delivery = (ok=%v, err=%v)", ok, err)
			}
			found := false
			stored, err := jm.store.LoadEvents()
			if err != nil {
				t.Fatalf("load runaway events: %v", err)
			}
			for _, event := range stored {
				if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil && event.WatchSend.DiagnosticReason == "runaway" {
					found = true
				}
			}
			if !found {
				t.Fatal("runaway drop event not persisted")
			}
		}
	})
}
