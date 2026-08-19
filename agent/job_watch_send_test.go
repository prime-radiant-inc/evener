package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
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
