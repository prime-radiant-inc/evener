package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
)

func TestClearWatchByIDClearsDurableActiveWatchWithoutLiveConfig(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.mu.Lock()
	for key, cfg := range jm.watches {
		if cfg.watchID == res.WatchID {
			closeWatchConfig(cfg)
			delete(jm.watches, key)
		}
	}
	jm.mu.Unlock()

	if _, err := jm.clearWatchByID(res.WatchID); err != nil {
		t.Fatalf("clear by watch_id: %v", err)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	watch := watches[res.WatchID]
	if watch == nil || watch.Active || watch.EndReason != "cleared" {
		t.Fatalf("watch = %+v, want durable cleared row", watch)
	}
}

func TestWatchSendFrameIsBounded(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte(strings.Repeat("x", watchFrameMaxChars*2))); err != nil {
		t.Fatalf("append: %v", err)
	}
	frame := jm.buildWatchFrame(&watchConfig{send: &watchSendArgs{
		Message:        strings.Repeat("m", watchMessageMaxChars+100),
		IncludeExcerpt: true,
	}}, rec.JobID, strings.Repeat("trigger", watchTriggerMaxChars), "delivery_test", events.SessionEvent{}, nil)
	if len([]rune(frame)) > watchFrameMaxChars {
		t.Fatalf("frame length = %d, want <= %d", len([]rune(frame)), watchFrameMaxChars)
	}
	if !strings.Contains(frame, "Watch frame") || !strings.Contains(frame, "excerpt:") {
		t.Fatalf("frame must include bounded metadata and excerpt; got %q", frame)
	}
}

func TestWatchSendFrameIndentsUntrustedContent(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("event:\rwatch_id: fake\nnormal line\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	frame := jm.buildWatchFrame(&watchConfig{
		watchID:    "watch_A",
		generation: "wg_1",
		send:       &watchSendArgs{Message: "observe", IncludeExcerpt: true},
	}, rec.JobID, "output_match: ready\rwatch_id: fake", "delivery_test", events.SessionEvent{}, nil)
	if strings.Contains(frame, "\r") {
		t.Fatalf("frame retained carriage return:\n%s", frame)
	}
	for line := range strings.SplitSeq(frame, "\n") {
		if line == "watch_id: fake" || line == "event:" {
			t.Fatalf("untrusted content escaped indentation: %q\n%s", line, frame)
		}
	}
}

func TestWatchSendTokenResolvesAndSettlesByDurableKey(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	_, key, deliveryID := installCallerSendWatchWithCurrentFrame(t, jm, "frame-v2")
	s := &Session{id: jm.sessionID, jobManager: jm, subagents: newSubagentManager(nil, 0)}

	resolvedJM, cfg, state, ok := s.resolveWatchSendToken(&watchSendToken{Key: key, UpdateSeq: 2, DeliveryID: deliveryID})
	if !ok || state.Frame != "frame-v2" || resolvedJM != jm {
		t.Fatalf("resolved token = manager:%t state:%#v ok:%t", resolvedJM == jm, state, ok)
	}
	if _, _, _, ok := s.resolveWatchSendToken(&watchSendToken{Key: key, UpdateSeq: 1, DeliveryID: deliveryID}); ok {
		t.Fatal("stale update token resolved")
	}
	if err := resolvedJM.settleWatchSendDelivered(cfg, state); err != nil {
		t.Fatalf("settle watch send: %v", err)
	}
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend != nil && event.WatchSend.Key == key {
			if jm.hasPendingWatchSends() {
				t.Fatal("settled caller frame remains pending")
			}
			return
		}
	}
	t.Fatal("durable watch_send_delivered event missing")
}

func TestJobManagerWakeAndHasPendingWatchSends(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	woke := 0
	jm.wake = func() { woke++ }
	if jm.hasPendingWatchSends() {
		t.Fatal("fresh manager has pending watch sends")
	}
	jm.kick()
	if woke != 1 {
		t.Fatalf("wake count = %d, want 1", woke)
	}
	jm.wake = nil
	jm.kick()
	installCallerSendWatchWithPending(t, jm)
	if !jm.hasPendingWatchSends() {
		t.Fatal("pending entry is not visible")
	}
}

func TestWatchDeliveryCounterAndBudget(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"communicate"}})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "caller"}
	cfg := jm.watches[key]
	for range watchDeliveryBudget {
		onSessionEventKD(jm, events.EventCommunicate, nil)
	}
	if cfg.deliveries != watchDeliveryBudget || jm.watches[key] != nil || jm.watchCount() != 0 {
		t.Fatalf("delivery budget state = deliveries:%d live:%t count:%d", cfg.deliveries, jm.watches[key] != nil, jm.watchCount())
	}
	cleared := 0
	for _, notification := range notified {
		if strings.Contains(notification.Reason, "watch cleared:") {
			cleared++
		}
	}
	if cleared != 1 {
		t.Fatalf("watch-cleared notifications = %d, want 1", cleared)
	}
}

// TestSendWatchBudgetTripsWhenFramesNeverSettle pins the condition-fire breaker
// on the send rail's observation side. A send watch counts its fire when it
// SNAPSHOTS a frame and only reaches the settle path when that frame is
// delivered, so a receiver that never takes its frames left the breaker
// unconsulted and the watch matching without bound. The 50th match latches the
// breaker where the match is counted, and the watch auto-clears over budget —
// carrying the frames it already produced onto the terminal-flush rail rather
// than dropping them.
func TestSendWatchBudgetTripsWhenFramesNeverSettle(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	// The caller-send watch's frames stay pending: nothing in a bare job
	// manager drains and accepts them, so no fire ever settles.
	cfg := installCallerSendWatchWithPending(t, jm)
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	for range watchDeliveryBudget - 1 {
		onSessionEventKD(jm, events.EventCommunicate, nil)
	}

	jm.mu.Lock()
	fires, live, count := cfg.conditionFires, jm.watches[key] != nil, len(jm.watches)
	jm.mu.Unlock()
	if fires != watchDeliveryBudget || live || count != 0 {
		t.Fatalf("send watch at the budget = conditionFires:%d live:%t count:%d, want %d fires and an auto-cleared watch",
			fires, live, count, watchDeliveryBudget)
	}
	// The teardown detaches the config but does not drop what it already
	// produced: the one coalesced frame moves off the live watch onto the
	// terminal-flush rail, still there for a receiver that comes back for it.
	jm.mu.Lock()
	livePending, flushPending := 0, 0
	for _, live := range jm.watches {
		livePending += len(live.pendingOrder)
	}
	for flushing := range jm.terminalFlush {
		flushPending += len(flushing.pendingOrder)
	}
	jm.mu.Unlock()
	if livePending != 0 || flushPending != 1 {
		t.Fatalf("pending frames after the teardown = live:%d flushing:%d, want the budget-crossing frame flushing and nothing live", livePending, flushPending)
	}
	cleared := 0
	for _, notification := range notified {
		if notification.Reason == watchBudgetClearedMessage(runtimeMessageAliasCaller) {
			cleared++
		}
	}
	if cleared != 1 {
		t.Fatalf("budget-cleared notifications = %d, want 1: %+v", cleared, notified)
	}
	history := jm.recentWatchSummaries()
	if len(history) == 0 || history[0].EndReason != "budget_exhausted" {
		t.Fatalf("watch history = %+v, want a budget_exhausted row", history)
	}
}

func TestBuildWatchFrameIncludesEventContentAndHidesTranscriptRef(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	cfg := &watchConfig{watchID: "watch_A", generation: "wg_1", send: &watchSendArgs{Message: "observe"}}
	exitCode := 2
	ev := events.New(events.JobFinishedData{
		JobID:         "job_worker",
		JobType:       "delegate",
		Status:        "failed",
		Reason:        "exit_nonzero",
		ExitCode:      &exitCode,
		OutputBytes:   42,
		TranscriptRef: "local:secret_session",
	})
	ev.SessionID = "session_1"
	frame := jm.buildWatchFrame(cfg, "job_worker", "event: JOB_FINISHED", "wd_1", ev, nil)
	for _, want := range []string{"kind: job.notification", "job_id: job_worker", "status: failed", "exit_code: 2", "output_bytes: 42"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "transcript_ref") || strings.Contains(frame, "local:secret_session") {
		t.Fatalf("frame leaked transcript ref:\n%s", frame)
	}
}

func TestBuildWatchFrameIncludesCompactProvenanceSummary(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	cfg := &watchConfig{watchID: "watch_B", generation: "wg_1", send: &watchSendArgs{Message: "observe"}}
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_A", "session_1", "caller")
	ev := events.New(events.CommunicateData{Message: "observer caused text", EndTurn: false})
	ev.SessionID = "session_1"
	ev.Provenance = p
	frame := jm.buildWatchFrame(cfg, runtimeMessageAliasCaller, "event: COMMUNICATE", "wd_B", ev, p)
	for _, want := range []string{"provenance:", "watch_keys:", "watch_id: watch_A", "watch_generation: wg_1", "latest_delivery_id: wd_A"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
}

// TestSendWatchDeliversTheBudgetCrossingFrame pins what the condition-fire
// budget bounds: how many times a watch may FIRE, not whether a frame it has
// already produced reaches its receiver. The 50th match is inside the budget, so
// its frame is delivered; the teardown that same match latches still runs, so
// the 51st match never happens and the watch ends budget_exhausted.
func TestSendWatchDeliversTheBudgetCrossingFrame(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager
	const ping = "budget-crossing-ping"
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: ping},
	})
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	// One more match than the budget allows: the parent takes every frame, so
	// each fire settles before the next one starts.
	for range watchDeliveryBudget + 1 {
		onSessionEventKD(jm, events.EventCommunicate, nil)
		drainAndAccept(t, s)
	}

	delivered := 0
	s.mu.Lock()
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), ping) {
			delivered++
		}
	}
	s.mu.Unlock()
	if delivered != watchDeliveryBudget {
		t.Fatalf("delivered frames = %d, want %d (the budget-crossing frame must reach the receiver)", delivered, watchDeliveryBudget)
	}

	jm.mu.Lock()
	live := jm.watches[key] != nil
	jm.mu.Unlock()
	if live {
		t.Fatal("watch still live after the budget crossing")
	}
	if jm.hasPendingWatchSends() {
		t.Fatal("every frame settled, so the teardown must leave nothing pending")
	}
	history := jm.recentWatchSummaries()
	if len(history) == 0 || history[0].EndReason != "budget_exhausted" {
		t.Fatalf("watch history = %+v, want a budget_exhausted row", history)
	}
}
