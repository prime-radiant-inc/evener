//go:build evenerfuzz

package agent

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// FuzzJobWatchEventsOutput exercises the event, output, terminal
// catch-up, and progress-timer rails against a real manager and store. The byte
// selects a deterministic scenario; the seeds keep every edge in the default
// fuzz corpus.
func FuzzJobWatchEventsOutput(f *testing.F) {
	for scenario := uint8(0); scenario < 21; scenario++ {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario uint8) {
		jm := newTestJM(t)
		t.Cleanup(func() {
			jm.abandonRunningJobs()
			_ = jm.close()
		})

		switch scenario % 21 {
		case 0:
			dec := evaluateWatchEvent(watchEventSnapshot{
				target: runtimeMessageAliasCaller, targetActive: true,
				eventKinds: map[events.EventKind]bool{events.EventError: true},
				hasSend:    true, sendTo: runtimeMessageAliasWatched,
			}, events.SessionEvent{Kind: events.EventError})
			if dec.matched || dec.send {
				t.Fatalf("send-to-watched session decision = %+v", dec)
			}
		case 1:
			jm.feedJobOutput("missing", nil, 0)
		case 2:
			rec := createWatchOutputJob(t, jm)
			cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, OutputMatch: "hit"}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			cfg.deliveries = watchDeliveryBudget - 1
			cfg.conditionFires = watchDeliveryBudget - 1 // the next fire crosses the budget
			jm.watches[watchKey{Target: rec.JobID}] = cfg
			jm.feedJobOutput(rec.JobID, []byte("hit\n"), 4)
		case 3:
			rec := createWatchOutputJob(t, jm)
			cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, OutputMatch: "hit"}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(rec.OutputPath); err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			_, _, gotErr := jm.prepareAttachScanLocked(cfg, jm.running[rec.JobID])
			jm.mu.Unlock()
			if gotErr == nil {
				t.Fatal("missing output did not fail attach preparation")
			}
		case 4:
			cfg, err := newWatchConfig(watchArgs{Target: "job_x", OutputMatch: "hit"}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			if jm.completeAttachScan(cfg, "job_x", nil, true, errors.New("read failed")) {
				t.Fatal("failed attach preparation fired")
			}
		case 5:
			cfg, err := newWatchConfig(watchArgs{Target: "job_x", OutputMatch: "hit"}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			cfg.deliveries = watchDeliveryBudget - 1
			cfg.conditionFires = watchDeliveryBudget - 1 // the next fire crosses the budget
			jm.watches[watchKey{Target: "job_x"}] = cfg
			if !jm.fireAttachScan(cfg, "job_x", []byte("hit\n")) {
				t.Fatal("attach scan did not fire")
			}
		case 6:
			_, err := jm.runTerminalCatchup(watchArgs{Target: "job_x", OutputMatch: "["}, watchKey{Target: "job_x"}, jobstore.StatusCompleted)
			if err == nil {
				t.Fatal("invalid terminal regexp accepted")
			}
		case 7:
			_, err := jm.runTerminalCatchup(watchArgs{Target: "job_missing", OutputMatch: "hit"}, watchKey{Target: "job_missing"}, jobstore.StatusCompleted)
			if err == nil {
				t.Fatal("missing terminal output accepted")
			}
		case 8:
			key := watchKey{Target: "job_x"}
			cfg := &watchConfig{target: "job_x"}
			jm.watches[key] = &watchConfig{}
			_, _ = jm.completeExpiredJobWatchesLocked("job_x", []expiredJobWatch{{key: key, cfg: cfg}}, nil)
			if jm.watches[key] == nil {
				t.Fatal("replacement watch removed")
			}
		case 9:
			if dec := decideProgressTick(progressTickSnapshot{stillRegistered: true, target: "job_gone"}); dec.keepAlive {
				t.Fatalf("dead concrete target kept progress timer alive: %+v", dec)
			}
		case 10:
			filter := &watchEventFilter{ToolName: "wanted", Status: "error"}
			cases := []events.SessionEvent{
				{Kind: events.EventError},
				{Kind: events.EventToolCallEnd, Data: (*events.ToolCallEndData)(nil)},
				{Kind: events.EventToolCallEnd, Data: events.ErrorData{}},
				{Kind: events.EventToolCallEnd, Data: &events.ToolCallEndData{ToolName: "wanted"}},
				{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "wanted", Error: "boom"}},
			}
			for _, ev := range cases {
				_ = watchEventFilterMatches(filter, ev)
			}
		case 11:
			for _, data := range []events.EventData{
				events.JobStartedData{JobID: "job_started"},
				events.JobFinishedData{JobID: "job_finished"},
				events.ErrorData{},
			} {
				_ = watchEventWatchedIdentity("*", data)
				_ = watchEventMatchesTarget("job_finished", data)
			}
			if got := watchEventWatchedIdentity("job_concrete", events.ErrorData{}); got != "job_concrete" {
				t.Fatal(got)
			}
		case 12:
			snap := watchEventSnapshot{target: "*", targetActive: true, eventKinds: map[events.EventKind]bool{events.EventError: true}, triggerKind: events.EventError, triggerEvery: 2}
			if dec := evaluateWatchEvent(snap, events.SessionEvent{Kind: events.EventError}); dec.matched || dec.eventCount != 1 {
				t.Fatalf("throttled decision = %+v", dec)
			}
			snap.triggerEvery = 0
			if dec := evaluateWatchEvent(snap, events.SessionEvent{Kind: events.EventError}); !dec.matched || dec.notifyJobID != "" {
				t.Fatalf("session notify decision = %+v", dec)
			}
		case 13:
			cfg, err := newWatchConfig(watchArgs{Target: "*", Events: []string{"communicate"}}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			cfg.deliveries = watchDeliveryBudget - 1
			cfg.conditionFires = watchDeliveryBudget - 1 // the next fire crosses the budget
			jm.watches[watchKey{Target: "*"}] = cfg
			jm.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate})
		case 14:
			jm.feedJobOutput("job_x", []byte("later"), 10)
			jm.feedJobOutput("job_x", []byte("earlier"), 5)
		case 15:
			cfg, err := newWatchConfig(watchArgs{Target: "job_x", OutputMatch: "hit"}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			_, scan, prepErr := jm.prepareAttachScanLocked(cfg, &runningJob{})
			jm.mu.Unlock()
			// A watchable running job with no output store is not a quiet
			// no-fire: it reports unable-to-scan via the error return (commit
			// fac0f049f, byte-window output_match scanner).
			if scan || prepErr == nil || !strings.Contains(prepErr.Error(), "no readable output store") {
				t.Fatalf("output-less run scan=%v err=%v", scan, prepErr)
			}
		case 16:
			rec := createWatchOutputJob(t, jm)
			res, err := jm.runTerminalCatchup(watchArgs{Target: rec.JobID, OutputMatch: "absent"}, watchKey{Target: rec.JobID}, jobstore.StatusCompleted)
			if err != nil || res.Fired {
				t.Fatalf("unmatched catchup = %+v, %v", res, err)
			}
		case 17:
			key := watchKey{Target: "job_x", SendTo: "dlg_x"}
			cfg, err := newWatchConfig(watchArgs{Target: "job_x", OutputMatch: "hit", Send: &watchSendArgs{To: "dlg_x"}}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			cfg.outputMatcher.Feed([]byte("hit"))
			jm.watches[key] = cfg
			_, deliveries := jm.completeExpiredJobWatchesLocked("job_x", []expiredJobWatch{{key: key, cfg: cfg}}, nil)
			if len(deliveries) != 1 {
				t.Fatalf("terminal flush deliveries = %d", len(deliveries))
			}
		case 18:
			key := watchKey{Target: "job_x", SendTo: "dlg_x"}
			cfg := &watchConfig{target: "job_x", watchID: "w", generation: "g", pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{{}: {}}}
			jm.watches[key] = cfg
			jm.completeExpiredJobWatchesLocked("job_x", []expiredJobWatch{{key: key, cfg: cfg}}, nil)
			if !jm.terminalFlush[cfg] {
				t.Fatal("pending watch not detached")
			}
		case 19:
			clk := agenttest.NewFakeClockAt(time.Unix(1000, 0).UTC())
			jm.clock = clk
			fired := make(chan struct{}, 1)
			jm.enqueue = func(jobNotification) { fired <- struct{}{} }
			key := watchKey{Target: runtimeMessageAliasCaller}
			stop := make(chan struct{})
			cfg := &watchConfig{target: runtimeMessageAliasCaller, progressIntervalMS: 1}
			jm.watches[key] = cfg
			jm.startProgressTimer(key, cfg, stop)
			clk.BlockUntil(1)
			clk.Advance(time.Millisecond)
			<-fired
			clk.Advance(time.Millisecond)
			<-fired

			// Hold the manager lock while the final tick becomes ready. After a
			// scheduler handoff the timer is queued on this mutex; the waiter
			// channel therefore closes only after fireProgressTick observes the
			// invalid registration, returns false, and releases the mutex.
			jm.mu.Lock()
			delete(jm.watches, key)
			clk.Advance(time.Millisecond)
			for i := 0; i < 10; i++ {
				runtime.Gosched()
			}
			done := make(chan struct{})
			go func() {
				jm.mu.Lock()
				jm.mu.Unlock()
				close(done)
			}()
			jm.mu.Unlock()
			<-done
			if jm.fireProgressTick(key, cfg) {
				t.Fatal("inactive progress watch stayed alive")
			}
			close(stop)
		case 20:
			// The send twin of case 5. The attach scan's send rail counts its
			// fire at the match and its delivery only when the frame settles,
			// which nothing here drains, so the breaker has to latch at the
			// match or the watch matches without bound.
			key := watchKey{Target: "job_x", SendTo: "dlg_x"}
			cfg, err := newWatchConfig(watchArgs{Target: "job_x", OutputMatch: "hit", Send: &watchSendArgs{To: "dlg_x"}}, jm.now(), "")
			if err != nil {
				t.Fatal(err)
			}
			cfg.conditionFires = watchDeliveryBudget - 1 // the next fire crosses the budget
			jm.watches[key] = cfg
			cleared := 0
			jm.enqueue = func(n jobNotification) {
				if n.Reason != watchBudgetClearedMessage("job_x") {
					return
				}
				cleared++
				// The auto-clear does durable I/O and re-takes jm.mu, so it must
				// be scheduled after the observation releases it (spec §3).
				if !jm.mu.TryLock() {
					t.Error("budget auto-clear notification delivered under jm.mu")
					return
				}
				jm.mu.Unlock()
			}
			if !jm.fireAttachScan(cfg, "job_x", []byte("hit\n")) {
				t.Fatal("attach scan did not fire")
			}
			if !cfg.budgetTripped {
				t.Fatal("attach-scan send at the budget did not latch the breaker")
			}
			if cleared != 1 {
				t.Fatalf("budget-cleared notifications = %d, want exactly 1", cleared)
			}
			if jm.watches[key] != nil {
				t.Fatal("attach-scan send over budget left its watch live")
			}
		}
	})
}

func createWatchOutputJob(t *testing.T, jm *jobManager) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "watch fuzz fixture"})
	if err != nil {
		t.Fatalf("create output job: %v", err)
	}
	return rec
}
