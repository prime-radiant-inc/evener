//go:build serffuzz

package agent

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzWatchTimersObserveProgram covers watchdog termination and the observation
// helpers with real managers and stores. The byte selects an independently
// reproducible scenario; the seeds ensure every scenario runs under -run.
func FuzzWatchTimersObserveProgram(f *testing.F) {
	for scenario := byte(0); scenario < 17; scenario++ {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario byte) {
		jm, err := newJobManager(t.TempDir(), "watch-observe", func(jobNotification) {})
		if err != nil {
			t.Fatalf("new job manager: %v", err)
		}
		t.Cleanup(func() { _ = jm.closeStoreOnly() })

		switch scenario % 17 {
		case 0:
			n := jm.watchNotificationFromWatch(nil, "job_nil", "nil config", nil)
			if n.JobID != "job_nil" || n.Reason != "nil config" || n.Provenance != nil {
				t.Fatalf("nil-config notification = %+v", n)
			}
		case 1:
			jm.startQuietWatchdog("unused", nil)
		case 2:
			clk := agenttest.NewFakeClock()
			jm.clock, jm.now = clk, clk.Now
			jm.quietCheckInterval = time.Second
			stop := make(chan struct{})
			jm.startQuietWatchdog("missing", stop)
			clk.BlockUntil(1)
			clk.Advance(time.Second)
			for spins := 0; clk.BlockedCount() != 0 && spins < 10_000; spins++ {
				runtime.Gosched()
			}
			if clk.BlockedCount() != 0 {
				t.Fatal("quiet watchdog did not stop after missing-job tick")
			}
		case 3:
			if jm.fireQuietWatchdogTick("missing", time.Minute) {
				t.Fatal("missing job kept watchdog alive")
			}
		case 4:
			d := watchSendDelivery{message: "unchanged"}
			if got := jm.snapshotWatchSendFrame(d); got.message != d.message || got.frame != "" {
				t.Fatalf("nil-send snapshot changed delivery: %+v", got)
			}
		case 5:
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
		case 6:
			cfg := &watchConfig{watchID: "watch", generation: "generation"}
			state := jm.watchSendState(watchSendDelivery{cfg: cfg}, runtimeMessageAliasCaller)
			if state.DeliveryID == "" {
				t.Fatal("watch send state did not mint delivery id")
			}
		case 7:
			if err := jm.store.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}
			if _, _, err := jm.watchReadGrantObserver("dlg_closed"); err == nil {
				t.Fatal("closed delegate store did not fail")
			}
		case 8:
			if err := jm.appendWatchReadGrant("observer", "job_gone"); err != nil {
				t.Fatalf("append grant: %v", err)
			}
			s := &Session{jobManager: jm}
			if got, ok := s.lookupGrantedJobRead("observer", "job_gone"); ok || got != nil {
				t.Fatalf("grant to missing job resolved: (%+v, %v)", got, ok)
			}
		case 9:
			cfg, err := newWatchConfig(watchArgs{Target: "job_target", Send: &watchSendArgs{To: "dlg_missing"}}, jm.now())
			if err != nil {
				t.Fatalf("new watch config: %v", err)
			}
			// An unresolvable delivery target mints nothing and marks nothing:
			// delivery itself reports the route failure, so the grant is simply
			// never claimed and the next fire is free to retry.
			if got := jm.mintWatchSendReadGrant(cfg, "dlg_missing", events.JobFinishedData{JobID: "job_target"}); got != "" || cfg.grantsMinted != nil {
				t.Fatalf("missing observer minted grant %q (dedup=%+v)", got, cfg.grantsMinted)
			}
		case 10:
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
		case 11:
			clk := agenttest.NewFakeClock()
			jm.clock, jm.now = clk, clk.Now
			last := clk.Now()
			jm.running["dlg_activity"] = &runningJob{rec: &jobstore.JobRecord{
				JobID: "dlg_activity", Type: jobstore.JobDelegate, StartedAt: last.Add(-time.Hour), LastActivity: &last,
			}}
			if !jm.fireQuietWatchdogTick("dlg_activity", time.Minute) {
				t.Fatal("running delegate stopped watchdog")
			}
			if jm.running["dlg_activity"].quietNotified {
				t.Fatal("fresh LastActivity was ignored")
			}
		case 12:
			got := jm.snapshotWatchSendFrame(watchSendDelivery{
				send: &watchSendArgs{Message: "observe"}, selfInfluence: true, gradientDepth: 2,
			})
			if !strings.HasPrefix(got.frame, "<system-reminder>") {
				t.Fatalf("self-influence notice missing from frame: %q", got.frame)
			}
		case 13:
			if _, _, ok, err := jm.recordWatchSend(watchSendDelivery{}); ok || err != nil {
				t.Fatalf("invalid delivery = (ok=%v, err=%v)", ok, err)
			}
		case 14:
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
		case 15:
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
		case 16:
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
