package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// drainAndAccept advances watch delivery the way the live loop does: one drain
// pass (delegate targets deliver + caller pendings re-token) followed by one
// notification accept (caller tokens render by key and settle). Use it in
// Session-based tests to drive a full delivery cycle; pure-jm tests assert on
// pending state instead (the new observable contract at the jobManager level).
func drainAndAccept(t *testing.T, s *Session) {
	t.Helper()
	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	s.acceptNotificationInput(context.Background()) // ok to no-op on empty queue
}

// drainWatchSendsVia delivers every pending non-caller watch send through the
// drain's delivery primitive (deliverPendingWatchSend), capturing the delivery
// args via the supplied sender — the way the live drain calls s.sendDelegateMessage.
// Caller-targeted sends route to notification tokens, not this primitive, so they
// are skipped (mirroring drainJobManagerWatchSends). Per-delivery errors are
// returned joined (the live drain logs them at the boundary and continues), so
// crash-recovery tests can assert the resulting state. Pure-jm tests use this to
// observe a delegate-targeted delivery after recording pending intent.
func drainWatchSendsVia(t *testing.T, jm *jobManager, send sendMessageFunc) error {
	t.Helper()
	var errs []error
	for _, d := range jm.pendingWatchSendDeliveries(nil) {
		if d.state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			continue
		}
		if err := jm.deliverPendingWatchSend(context.Background(), d.cfg, d.state, true, send); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deliverWatchSendVia records a fired delivery as pending and delivers it through
// the drain's primitive with the supplied sender. It mirrors the (now-deleted)
// jobManager.deliverWatchSend, but takes the sender as a parameter — the structural
// point of the mailbox design is that delivery is never reachable from a jobManager
// field, only from explicit loop-owned (or, in tests, explicit) delivery.
func deliverWatchSendVia(t *testing.T, jm *jobManager, d watchSendDelivery, send sendMessageFunc) error {
	t.Helper()
	state, cfg, ok, err := jm.recordWatchSend(d)
	if err != nil || !ok {
		return err
	}
	return jm.deliverPendingWatchSend(context.Background(), cfg, state, false, send)
}

// installWatchBelowValidation installs a watch directly into jm.watches the way
// configureWatch does AFTER newWatchConfig succeeds, but WITHOUT the validation
// layer (target/send checks and the feedback-loop guard). It exists so tests can
// exercise the live firing+delivery path (onSessionEvent -> recordWatchSendsAnd
// Kick) for caller-self watch shapes that the create-path guard now rejects.
// newWatchConfig itself runs no loop guard, so this install is legal below
// validation. The install sequence mirrors configureWatch: build cfg, lock,
// initProgressStop, assign, unlock, startProgressTimer (the timer no-ops for
// events-only configs where progressIntervalMS == 0).
func installWatchBelowValidation(t *testing.T, jm *jobManager, a watchArgs) {
	t.Helper()
	if a.Send != nil {
		a.Send.To = strings.TrimSpace(a.Send.To)
	}
	cfg, err := newWatchConfig(a, jm.now())
	if err != nil {
		t.Fatalf("newWatchConfig(%+v): %v", a, err)
	}
	sendTo := ""
	if a.Send != nil {
		sendTo = a.Send.To
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: a.Target, SendTo: sendTo}
	jm.mu.Lock()
	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
}

func TestConfigureWatchRequiresCondition(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "caller"})
	if err == nil {
		t.Fatal("a watch with no condition and clear=false must error")
	}
}

func TestConfigureWatchRejectsNegativeProgressInterval(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: -1})
	if err == nil {
		t.Fatal("negative progress interval must error")
	}
	if !strings.Contains(err.Error(), "progress_interval_ms must be non-negative") {
		t.Fatalf("error = %v, want progress_interval_ms validation", err)
	}
}

func TestConfigureWatchClampsProgressInterval(t *testing.T) {
	jm := newTestJM(t)
	t.Cleanup(func() { _ = jm.close() })
	res, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: 10})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if res.ProgressIntervalMS != minWatchProgressIntervalMS {
		t.Fatalf("progress interval = %d, want %d", res.ProgressIntervalMS, minWatchProgressIntervalMS)
	}
}

func TestConfigureWatchTargetNotFound(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "job_does_not_exist", OutputMatch: "ready"})
	if err == nil {
		t.Fatal("an unknown concrete job target must error (target_not_found)")
	}
}

func TestConfigureWatchRejectsForwardedNestedTarget(t *testing.T) {
	jm := newTestJM(t)
	startedAt := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_nested",
		Type:             jobstore.JobShell,
		Status:           jobstore.StatusRunning,
		OwnerSessionID:   "CHILD",
		VisibleToSession: jm.sessionID,
		ParentJobID:      "job_delegate",
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append nested start: %v", err)
	}

	_, err := jm.configureWatch(watchArgs{Target: "job_nested", OutputMatch: "ready"})
	if err == nil || !strings.Contains(err.Error(), "target_not_watchable") {
		t.Fatalf("error = %v, want target_not_watchable", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchSendToMissingJobFailsTargetNotFound(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_missing_delegate", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchRejectsUnknownEventKinds(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.mesage"}})

	if err == nil || !strings.Contains(err.Error(), "unknown event kind") {
		t.Fatalf("error = %v, want unknown event kind", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchMainAliasTargetFailsTargetNotFound(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "main"})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchWatchedTargetWithoutContextFails(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "watched"})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchRejectsOutputMatchOnSessionTargets(t *testing.T) {
	jm := newTestJM(t)
	for _, target := range []string{"caller", "*"} {
		t.Run(target, func(t *testing.T) {
			_, err := jm.configureWatch(watchArgs{Target: target, OutputMatch: "ready"})
			if err == nil {
				t.Fatal("session target output_match watch must error")
			}
			if !strings.Contains(err.Error(), "output_match requires a concrete job target") {
				t.Fatalf("error = %v, want output_match concrete-target validation", err)
			}
		})
	}
}

func TestJobWatchSendToMainAliasFailsTargetNotFound(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})

	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "main", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchSendToKnownShellJobFailsTargetNotMessageable(t *testing.T) {
	jm := newTestJM(t)
	watched, _ := jm.createShell(createShellOpts{Command: "watched"})
	observer, _ := jm.createShell(createShellOpts{Command: "observer"})

	_, err := jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: observer.JobID, Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_messageable") {
		t.Fatalf("error = %v, want target_not_messageable", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchSendToWatchedKnownShellJobFailsTargetNotMessageable(t *testing.T) {
	jm := newTestJM(t)
	watched, _ := jm.createShell(createShellOpts{Command: "watched"})

	_, err := jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_messageable") {
		t.Fatalf("error = %v, want target_not_messageable", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchRejectsTerminalizingConcreteJob(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})

	jm.mu.Lock()
	jm.running[rec.JobID].finalize = &finalizeAttempt{done: make(chan struct{})}
	jm.mu.Unlock()

	_, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err == nil {
		t.Fatal("a terminalizing concrete job must not accept new watches")
	}
	if !strings.Contains(err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("terminalizing job watch was registered; count = %d", jm.watchCount())
	}
}

func TestJobWatchRejectsConcreteJobWithoutRunningRuntime(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})

	jm.mu.Lock()
	delete(jm.running, rec.JobID)
	jm.mu.Unlock()

	_, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err == nil {
		t.Fatal("a concrete job without a running runtime must not accept new watches")
	}
	if !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("inert concrete job watch was registered; count = %d", jm.watchCount())
	}
}

func TestConfigureWatchIdempotentAndReplace(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	first, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if first.ReplacedExisting {
		t.Error("first watch must not report replaced_existing")
	}

	same, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if same.ReplacedExisting {
		t.Error("identical re-config must be idempotent, not a replacement")
	}

	diff, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "blocked"})
	if !diff.ReplacedExisting {
		t.Error("changed config on the same key must report replaced_existing")
	}
}

func TestClearWatchRemovesIt(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Errorf("clear must remove the watch; count = %d", jm.watchCount())
	}
}

func TestEventWatchFiresAndNotifiesCaller(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	jm.onSessionEvent(events.EventAssistantTextEnd, nil)

	if len(notified) != 1 {
		t.Fatalf("an assistant.message event must notify the caller once, got %d", len(notified))
	}
	if notified[0].JobID != "" {
		t.Fatalf("session event notification job_id = %q, want empty", notified[0].JobID)
	}
}

func TestWildcardEventWatchOnlyFiresSupportedEvents(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"*"}})
	jm.onSessionEvent(events.EventSteeringInjected, events.SteeringInjectedData{Text: "internal"})
	if len(notified) != 0 {
		t.Fatalf("internal event fired wildcard watch: %+v", notified)
	}

	jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	if len(notified) != 1 {
		t.Fatalf("supported event fires = %d, want 1", len(notified))
	}
}

func TestWildcardJobEventWatchNotifiesConcreteJob(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	if _, err := jm.configureWatch(watchArgs{Target: "*", Events: []string{"job.notification"}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_worker", JobType: "delegate", Status: "completed"})

	if len(notified) != 1 {
		t.Fatalf("job.notification event must notify the caller once, got %d", len(notified))
	}
	if notified[0].JobID != "job_worker" {
		t.Fatalf("job event notification job_id = %q, want concrete triggering job", notified[0].JobID)
	}
}

func TestEventWatchTriggerEveryNth(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }

	installWatchBelowValidation(t, jm, watchArgs{
		Target: "caller",
		Events: []string{"assistant.message"},
		Every:  3,
	})
	for i := 0; i < 7; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	}
	if fires != 2 {
		t.Errorf("every=3 over 7 events should fire twice, got %d", fires)
	}
}

func TestConfigureWatchRejectsEveryWithMultipleEvents(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"assistant.message", "job.notification"},
		Every:  2,
	})
	if err == nil || !strings.Contains(err.Error(), "every requires exactly one watched event kind") {
		t.Fatalf("error = %v, want every requires exactly one watched event kind", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}

	// every with zero events should also fail (no event to throttle).
	_, err = jm.configureWatch(watchArgs{Target: "caller", Every: 1})
	if err == nil || !strings.Contains(err.Error(), "every requires exactly one watched event kind") {
		t.Fatalf("bare every with no events: error = %v, want every requires exactly one watched event kind", err)
	}
}

func TestConfigureWatchRejectsEveryWithWildcardEvent(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"*"},
		Every:  3,
	})
	if err == nil || !strings.Contains(err.Error(), "concrete event kind") {
		t.Fatalf("error = %v, want rejection naming concrete event kind requirement", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestEventWatchIgnoresUnwatchedKind(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	jm.onSessionEvent(events.EventToolCallEnd, nil)
	if fires != 0 {
		t.Errorf("an unwatched event kind must not fire; fires = %d", fires)
	}
}

// TestValidateWatchDeliveryLoop covers the feedback-loop guard: configureWatch
// must reject any watch whose resolved event kinds include a self-generated kind
// (assistant.message/assistant.tool/communicate, including via "*") AND whose
// delivery returns to the generating session (send omitted or send.to=caller).
// The guard is target-independent: onSessionEvent matches kinds across all
// watches regardless of cfg.target, so a job-target watch with send.to=caller
// loops just as a caller-target one does.
func TestValidateWatchDeliveryLoop(t *testing.T) {
	tests := []struct {
		name    string
		build   func(t *testing.T, jm *jobManager) watchArgs
		wantErr bool
	}{
		{
			name: "notify_self_assistant_message",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.message"}}
			},
			wantErr: true,
		},
		{
			name: "send_to_self_incident_shape",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "caller"}}
			},
			wantErr: true,
		},
		{
			name: "every_derived_single_kind_to_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.message"}, Every: 2, Send: &watchSendArgs{To: "caller"}}
			},
			wantErr: true,
		},
		{
			name: "job_target_self_kind_to_caller",
			build: func(t *testing.T, jm *jobManager) watchArgs {
				rec, err := jm.createShell(createShellOpts{Command: "x"})
				if err != nil {
					t.Fatalf("createShell: %v", err)
				}
				return watchArgs{Target: rec.JobID, Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "caller"}}
			},
			wantErr: true,
		},
		{
			name: "wildcard_send_to_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"*"}, Send: &watchSendArgs{To: "caller"}}
			},
			wantErr: true,
		},
		{
			name: "wildcard_notify_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"*"}}
			},
			wantErr: true,
		},
		{
			name: "communicate_notify_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"communicate"}}
			},
			wantErr: true,
		},
		{
			name: "sidecar_delivery_to_delegate",
			build: func(t *testing.T, jm *jobManager) watchArgs {
				seedCommonWatchSendTargets(t, jm)
				return watchArgs{Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "job_obs"}}
			},
			wantErr: false,
		},
		{
			name: "job_notification_self_watch",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"job.notification"}}
			},
			wantErr: false,
		},
		{
			name: "output_match_only_to_caller",
			build: func(t *testing.T, jm *jobManager) watchArgs {
				rec, err := jm.createShell(createShellOpts{Command: "x"})
				if err != nil {
					t.Fatalf("createShell: %v", err)
				}
				return watchArgs{Target: rec.JobID, OutputMatch: "ready", Send: &watchSendArgs{To: "caller"}}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := newTestJM(t)
			t.Cleanup(func() { _ = jm.close() })
			jm.enqueue = func(jobNotification) {}
			args := tt.build(t, jm)
			_, err := jm.configureWatch(args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("configureWatch(%+v) = nil error, want feedback-loop rejection", args)
				}
				if !strings.Contains(err.Error(), "feedback loop") {
					t.Fatalf("error = %v, want message containing \"feedback loop\"", err)
				}
				if !strings.Contains(err.Error(), "invalid_request") {
					t.Fatalf("error = %v, want message containing \"invalid_request\"", err)
				}
				if jm.watchCount() != 0 {
					t.Fatalf("rejected watch must not be installed; watchCount = %d", jm.watchCount())
				}
				return
			}
			if err != nil {
				t.Fatalf("configureWatch(%+v) = %v, want no error", args, err)
			}
		})
	}
}

func TestEventWatchIgnoresWatchOriginatedSubagentEvents(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed", FromWatch: true})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("watch-originated job event retriggered watch send: %+v", pending)
	}

	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_worker", JobType: "delegate", Status: "completed"})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("ordinary job event must record one pending watch send, got %d", len(pending))
	}
}

func TestWatchOriginSuppressesDelegateLifecycleWatchSends(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	jm.onSessionEvent(events.EventJobStarted, events.JobStartedData{JobID: "job_obs", JobType: "delegate", Status: "running", FromWatch: true})
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed", FromWatch: true})

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("watch-originated delegate lifecycle events recorded watch sends: %+v", pending)
	}
}

func TestConcreteJobEventWatchSendsFrame(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"assistant.tool"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventToolCallEnd, nil)

	// Observation records the send as pending (frame snapshot included); delivery
	// is the loop-owned drain's job.
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("concrete job event watch must record one pending send, got %d", len(pending))
	}
	var state *jobstore.WatchSendState
	for _, p := range pending {
		state = p
	}
	if state.Key.ResolvedSendTo != "job_obs" {
		t.Fatalf("pending target = %q, want job_obs", state.Key.ResolvedSendTo)
	}
	if !strings.Contains(state.Frame, "observe") ||
		!strings.Contains(state.Frame, rec.JobID) ||
		!strings.Contains(state.Frame, "event: TOOL_CALL_END") {
		t.Fatalf("pending frame = %q, want configured message, job id, and trigger", state.Frame)
	}
}

func TestOutputMatchWatchFiresOnAppendedBytes(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("booting\nserver READY\n")); err != nil {
		t.Fatalf("append: %v", err)
	}

	if len(notified) != 1 {
		t.Fatalf("output_match must fire once on the matching appended line, got %d", len(notified))
	}
}

// TestOutputMatchHonorsScanOffsetThroughFeedPath proves the end offset threaded
// from the store reaches FeedAt in the matcher's lifetime-byte space: a chunk
// landing entirely below an attach-time scan offset must not fire, while a later
// chunk above it must. A stale matcher-local counter (the old Feed wrapper)
// would start at 0, sit below the scan offset, and silently drop both.
func TestOutputMatchHonorsScanOffsetThroughFeedPath(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: ""}]
	if cfg == nil || cfg.outputMatcher == nil {
		t.Fatal("output_match watch not installed")
	}

	// Mark the first 100 lifetime bytes as covered by an attach-time scan.
	const scanOffset = 100
	cfg.outputMatcher.SetScanOffset(scanOffset)

	// A chunk whose end offset is at or below the scan offset is already covered:
	// it must not fire.
	below := []byte("server ready\n")
	jm.feedJobOutput(rec.JobID, below, scanOffset)
	if len(notified) != 0 {
		t.Fatalf("a chunk ending at the scan offset must not fire; got %d", len(notified))
	}

	// A later chunk whose end offset is past the scan offset must fire.
	above := []byte("server ready\n")
	jm.feedJobOutput(rec.JobID, above, scanOffset+int64(len(above)))
	if len(notified) != 1 {
		t.Fatalf("a post-scan chunk must fire once; got %d", len(notified))
	}
}

// TestOutputMatchEndToEndOffsetThreadsFromStore reproduces the T2 attach flow that
// the store-derived end offset exists to make correct: a running job's output store
// already holds bytes the matcher was never fed (the watch did not exist while they
// were produced), attach sets the scan offset to that lifetime length, and live
// output is then appended via the real appendJobOutput -> feedJobOutput path.
//
// The byte counts are chosen so the two feed strategies diverge: the matcher-local
// 0-based counter the old Feed wrapper maintained would compute an end offset at or
// below the scan offset for the first live chunk and SILENTLY DISCARD it, never
// firing on live output; the store's lifetime Len() is already past the scan offset,
// so the production FeedAt path fires. Reverting feedJobOutput's FeedAt(chunk,
// endOffset) call to Feed(chunk) makes this test fail at the "first live chunk must
// fire" assertion.
func TestOutputMatchEndToEndOffsetThreadsFromStore(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := jm.running[rec.JobID].output

	// Pre-watch output: 100 lifetime bytes the matcher never sees. appendJobOutput
	// feeds only installed watches, and none exists yet, so the store advances while
	// any future matcher's internal counter stays at 0 -- the exact pre-attach state.
	preExisting := bytes.Repeat([]byte("x"), 100)
	if _, err := jm.appendJobOutput(rec.JobID, output, preExisting); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}
	if got := output.Len(); got != 100 {
		t.Fatalf("store lifetime length after pre-watch append = %d, want 100", got)
	}
	if len(notified) != 0 {
		t.Fatalf("pre-watch output must not fire (no watch installed); got %d", len(notified))
	}

	// Attach: install the watch (its matcher's feedOffset starts at 0, having seen
	// none of the 100 pre-existing bytes) and set the scan offset to the current
	// lifetime length, marking those 100 bytes as already covered.
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: ""}]
	if cfg == nil || cfg.outputMatcher == nil {
		t.Fatal("output_match watch not installed")
	}
	const scanOffset = 100
	if output.Len() != scanOffset {
		t.Fatalf("scan offset must equal current lifetime length; Len()=%d, scanOffset=%d", output.Len(), scanOffset)
	}
	cfg.outputMatcher.SetScanOffset(scanOffset)

	// First live chunk (13 bytes). Its store lifetime end is 113 > scanOffset, so
	// FeedAt fires. The stale 0-based counter would put its end at 13 <= scanOffset
	// and discard it -- this assertion is what catches a Feed regression.
	live := []byte("server READY\n")
	if len(live) > scanOffset {
		t.Fatalf("test setup: live chunk (%d bytes) must be <= scanOffset (%d) so a stale 0-based counter would wrongly discard it", len(live), scanOffset)
	}
	if _, err := jm.appendJobOutput(rec.JobID, output, live); err != nil {
		t.Fatalf("live append: %v", err)
	}
	if len(notified) != 1 {
		t.Fatalf("first live chunk past the scan offset must fire once via the real path; got %d", len(notified))
	}

	// A second live chunk fires again, confirming the offset keeps advancing with
	// the store across successive appends.
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("still READY\n")); err != nil {
		t.Fatalf("second live append: %v", err)
	}
	if len(notified) != 2 {
		t.Fatalf("second live chunk must fire again; got %d total", len(notified))
	}
}

// TestOutputMatchDropsOnEndOffsetRegression confirms the monotonicity guard: a
// chunk whose end offset regresses versus the last seen offset for the job is
// dropped (no match) and raises exactly one warning notification.
func TestOutputMatchDropsOnEndOffsetRegression(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// First feed at offset 200 fires and arms the last-seen offset.
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"), 200)
	if len(notified) != 1 {
		t.Fatalf("first feed must fire once; got %d", len(notified))
	}
	notified = notified[:0]

	// A regressed end offset (100 < 200) must drop the chunk before the matcher
	// and raise exactly one warning notification.
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"), 100)
	if len(notified) != 1 {
		t.Fatalf("a regressed feed must enqueue exactly one warning; got %d: %+v", len(notified), notified)
	}
	if !strings.Contains(notified[0].Reason, "offset") {
		t.Fatalf("regression warning reason = %q, want it to mention the offset regression", notified[0].Reason)
	}
}

// TestAttachScanFiresOnceForAlreadyPrintedToken proves the level-trigger: a
// watch attached to a running job whose retained output ALREADY contains the
// pattern on several lines fires exactly once at attach (not once per matching
// line), the fire carries the LAST matching line, and the create result reports
// fired=true (spec §7.1 "Running target" + "Attach-scan fire cardinality").
func TestAttachScanFiresOnceForAlreadyPrintedToken(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	// Three matching lines retained BEFORE the watch is configured. No watch exists,
	// so appendJobOutput advances the store but fires nothing.
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready\nready\nready\n")); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}
	if len(notified) != 0 {
		t.Fatalf("pre-watch output must not fire (no watch installed); got %d", len(notified))
	}

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !res.Fired {
		t.Fatal("create result must report fired=true when retained output already matches")
	}
	if len(notified) != 1 {
		t.Fatalf("attach scan must fire exactly once regardless of matching-line count; got %d", len(notified))
	}
	if notified[0].Reason != "output_match: ready" {
		t.Fatalf("attach fire reason = %q, want \"output_match: ready\" (the last matching line)", notified[0].Reason)
	}
}

// TestAttachScanSendArmFiresOnce is the send-arm counterpart: a sidecar watch
// (send.to=delegate) attached to a running job with already-printed output
// records exactly one pending send whose trigger carries the last matching line,
// and the create result reports fired=true.
func TestAttachScanSendArmFiresOnce(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	seedCommonWatchSendTargets(t, jm)

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready one\nready two\nready three\n")); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}

	res, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !res.Fired {
		t.Fatal("create result must report fired=true for a sidecar attach-scan match")
	}

	jm.mu.Lock()
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}]
	var pendingCount int
	var lastReason string
	if cfg != nil {
		pendingCount = len(cfg.pending)
		for _, state := range cfg.pending {
			lastReason = state.TriggerReason
		}
	}
	jm.mu.Unlock()
	if pendingCount != 1 {
		t.Fatalf("attach scan must record exactly one pending send; got %d", pendingCount)
	}
	if lastReason != "output_match: ready three" {
		t.Fatalf("pending send trigger = %q, want \"output_match: ready three\" (the last matching line)", lastReason)
	}
}

// TestAttachScanTokenStraddlingBoundaryFiresOnce drives the no-double-fire-across
// -the-seam case under -race: a partial token is retained at attach (no newline),
// the watch attaches (seeding the carry + scan offset), then the rest of the
// token arrives via the real appendJobOutput->feedJobOutput path. The token must
// fire EXACTLY once — the attach scan sees no complete matching line, and the
// live FeedAt completes the seeded carry into one match.
func TestAttachScanTokenStraddlingBoundaryFiresOnce(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := jm.running[rec.JobID].output
	// Partial token with no newline retained before attach.
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("rea")); err != nil {
		t.Fatalf("partial append: %v", err)
	}

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	// The retained tail "rea" is not a complete matching line, so the attach scan
	// fires nothing; the carry seed carries "rea" forward.
	if res.Fired {
		t.Fatal("create result must report fired=false: no complete matching line at attach")
	}
	if len(notified) != 0 {
		t.Fatalf("attach scan must not fire on an unterminated partial token; got %d", len(notified))
	}

	// The rest of the token arrives through the real append path; the seeded carry
	// completes "ready" and fires exactly once.
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("dy\n")); err != nil {
		t.Fatalf("completion append: %v", err)
	}
	if len(notified) != 1 {
		t.Fatalf("straddling token must fire exactly once via the carry+FeedAt seam; got %d", len(notified))
	}
	if notified[0].Reason != "output_match: ready" {
		t.Fatalf("seam fire reason = %q, want \"output_match: ready\"", notified[0].Reason)
	}
}

// TestAttachScanNoMatchThenLiveFires proves the empty case and that the live path
// still works after a no-fire attach: a running job with NO output gets an
// output_match watch (fired=false, no notification), then matching output appended
// through the real path fires once.
func TestAttachScanNoMatchThenLiveFires(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := jm.running[rec.JobID].output

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if res.Fired {
		t.Fatal("create result must report fired=false when nothing is retained")
	}
	if len(notified) != 0 {
		t.Fatalf("attach scan on empty output must not fire; got %d", len(notified))
	}

	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("server READY\n")); err != nil {
		t.Fatalf("live append: %v", err)
	}
	if len(notified) != 1 {
		t.Fatalf("live matching output must fire once after a no-match attach; got %d", len(notified))
	}
}

// TestAttachScanIdempotentReinstallDoesNotRefire pins that re-installing the
// identical watch (the idempotent no-op path) does NOT re-scan or re-fire: only a
// FRESH concrete-running output_match install scans.
func TestAttachScanIdempotentReinstallDoesNotRefire(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready\n")); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}

	first, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure first: %v", err)
	}
	if !first.Fired || len(notified) != 1 {
		t.Fatalf("first install must fire once; fired=%v notifications=%d", first.Fired, len(notified))
	}

	// Identical re-install is a no-op: it must not scan again.
	second, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure second: %v", err)
	}
	if second.Fired {
		t.Fatal("idempotent re-install must report fired=false (no fresh matcher installed)")
	}
	if len(notified) != 1 {
		t.Fatalf("idempotent re-install must not re-fire; total notifications = %d, want 1", len(notified))
	}
}

func TestConcreteWatchExpiresOnTerminal(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if jm.watchCount() != 1 {
		t.Fatalf("watch not registered")
	}
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 0 {
		t.Errorf("a concrete-job watch must expire when the job goes terminal; count = %d", jm.watchCount())
	}
}

func TestSessionWatchSurvivesAJobTerminal(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 1 {
		t.Errorf("a session-alias watch must survive a job going terminal; count = %d", jm.watchCount())
	}
}

func TestConcreteWatchFlushesBeforeTerminalNotification(t *testing.T) {
	jm := newTestJM(t)
	var order []string
	jm.enqueue = func(n jobNotification) {
		if n.Status == jobNotificationEventWatch {
			order = append(order, "watch")
		} else {
			order = append(order, "terminal")
		}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if strings.Join(order, ",") != "watch,terminal" {
		t.Fatalf("notification order = %v, want watch before terminal", order)
	}
}

func TestWatchSendDeliversFrameToTarget(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Observation records the send as pending; the loop-owned drain delivers it.
	feedJob(jm, rec.JobID, []byte("server READY\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("observation must record one pending send, got %d", len(pending))
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 {
		t.Fatalf("a send watch must deliver once, got %d", len(sent))
	}
	if sent[0].Target != "job_obs" {
		t.Errorf("delivery target = %q, want job_obs", sent[0].Target)
	}
	if !sent[0].Background || !sent[0].BackgroundSet || !sent[0].FromWatch {
		t.Errorf("delivery args = %+v, want background watch send", sent[0])
	}
	if !strings.Contains(sent[0].Message, "saw ready") {
		t.Errorf("delivery must carry the configured message + frame; got %q", sent[0].Message)
	}
	if !strings.Contains(sent[0].Message, "output_match: server READY") {
		t.Errorf("delivery frame must carry the match trigger; got %q", sent[0].Message)
	}
}

func TestWatchSendBatchContinuesAfterNonTerminalPersistenceFailure(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	seedWatchSendDelegateTarget(t, jm, "job_obs_a")
	seedWatchSendDelegateTarget(t, jm, "job_obs_b")
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs_a", Message: "observe a"},
	}); err != nil {
		t.Fatalf("configure first watch: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs_b", Message: "observe b"},
	}); err != nil {
		t.Fatalf("configure second watch: %v", err)
	}
	realAppend := jm.appendEvent
	appendErr := errors.New("pending append failed")
	var failedTarget string
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendPending &&
			e.WatchSend != nil &&
			failedTarget == "" {
			failedTarget = e.WatchSend.Key.ResolvedSendTo
			return appendErr
		}
		return realAppend(e)
	}

	// Observation records pending for both targets; one persist fails, the other
	// survives. The drain delivers only the survivor.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	if failedTarget == "" {
		t.Fatal("test did not intercept pending append")
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 || sent[0].Target == failedTarget {
		t.Fatalf("sent after partial batch failure = %+v, failed target %q; want only later independent target", sent, failedTarget)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after non-terminal partial failure = %+v, want none for delivered unrelated send", pending)
	}
}

func TestWatchSendBusyKeepsPendingAndEmitsNoDiagnostic(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return busyWatchSendResult()
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("observation must record one pending send, got %d", len(pending))
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 {
		t.Fatalf("send attempts = %d, want 1", len(sent))
	}
	if len(notified) != 0 {
		t.Fatalf("busy send emitted diagnostics: %+v", notified)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after busy send = %d, want 1: %+v", len(pending), pending)
	}
}

func TestWatchSendRetryAfterIdleDeliversLatestCoalescedFrame(t *testing.T) {
	jm := newTestJM(t)
	target := createRunningDelegateWatchTarget(t, jm)
	busy := true
	var delivered []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		if busy {
			return busyWatchSendResult()
		}
		delivered = append(delivered, a)
		return sendMessageResult{}
	}

	source, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      source.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: target.JobID, Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Two fires while the target is busy coalesce to a single latest-frame pending.
	feedJob(jm, source.JobID, []byte("first ready\n"))
	feedJob(jm, source.JobID, []byte("second ready\n"))
	drainWatchSendsVia(t, jm, send) // busy: delivery bounces, pending kept
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending before retry = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if state.CoalescedCount != 1 {
			t.Fatalf("coalesced_count = %d, want 1", state.CoalescedCount)
		}
		if !strings.Contains(state.Frame, "second ready") || strings.Contains(state.Frame, "first ready") {
			t.Fatalf("pending frame = %q, want latest coalesced frame only", state.Frame)
		}
	}

	// Once the target is idle, the next drain delivers the latest coalesced frame.
	busy = false
	drainWatchSendsVia(t, jm, send)

	if len(delivered) != 1 {
		t.Fatalf("retry delivered sends = %d, want 1", len(delivered))
	}
	if !strings.Contains(delivered[0].Message, "second ready") || strings.Contains(delivered[0].Message, "first ready") {
		t.Fatalf("retry message = %q, want latest coalesced frame", delivered[0].Message)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after retry = %+v, want none", pending)
	}
}

func TestWatchSendToResumedRunningDelegateSteersActiveRun(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first delegate = %+v, want completed", first)
	}
	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "resume and block",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed running delegate", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	source, _ := sess.jobManager.createShell(createShellOpts{Command: "x"})
	if _, err := sess.jobManager.configureWatch(watchArgs{
		Target:      source.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: first.JobID, Message: "observe original target"},
	}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}
	// Observation records the send as pending; the loop-owned drain steers it to
	// the resumed (running) delegate.
	feedJob(sess.jobManager, source.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending after observation = %+v, want one recorded send", pending)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	if queue := sub.sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue before drain = %+v, want empty (observation must not deliver)", queue)
	}

	if err := sess.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drainPendingWatchSends: %v", err)
	}
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after drain = %+v, want delivered", pending)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 {
		t.Fatalf("resumed delegate steering queue = %+v, want one watch send", queue)
	}
	if !strings.Contains(queue[0].Text, "observe original target") || !strings.Contains(queue[0].Text, "output_match: server ready") {
		t.Fatalf("resumed delegate steering message = %q, want watch message and frame", queue[0].Text)
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)

	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after resumed delegate finished = %+v, want none", pending)
	}
	for _, rec := range sess.jobManager.list(listFilter{Type: jobstore.JobDelegate}) {
		if rec.JobID != first.JobID && rec.JobID != second.JobID && rec.TranscriptRef == first.TranscriptRef {
			t.Fatalf("watch send created unexpected retry delegate job %+v", rec)
		}
	}
}

func TestWatchSendDeliveredAppendedOnlyAfterSendSucceeds(t *testing.T) {
	jm := newTestJM(t)
	var eventsBeforeSendReturn []jobstore.EventKind
	var eventKinds []jobstore.EventKind
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		eventKinds = append(eventKinds, e.Kind)
		return realAppend(e)
	}
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		eventsBeforeSendReturn = append(eventsBeforeSendReturn, eventKinds...)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Observation records pending; the drain delivers and then marks delivered.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	drainWatchSendsVia(t, jm, send)

	if containsEventKind(eventsBeforeSendReturn, jobstore.EventWatchSendDelivered) {
		t.Fatalf("delivered event was appended before send returned: %v", eventsBeforeSendReturn)
	}
	if !eventKindOrder(eventKinds, jobstore.EventWatchSendPending, jobstore.EventWatchSendDelivered) {
		t.Fatalf("event order = %v, want pending before delivered after send", eventKinds)
	}
}

func TestWatchSendCrashAfterSuccessBeforeDeliveredRetriesSameDeliveryID(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDelivered {
			return errors.New("crash before delivered marker")
		}
		return realAppend(e)
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// The send succeeds in the drain, but the delivered-marker append crashes, so
	// the pending survives for a post-restart retry.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	drainWatchSendsVia(t, jm, send)
	if len(sent) != 1 {
		t.Fatalf("initial sends = %d, want 1", len(sent))
	}
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after failed delivered marker = %d, want 1", len(pending))
	}
	var deliveryID string
	for _, state := range pending {
		deliveryID = state.DeliveryID
	}
	if deliveryID == "" || !strings.Contains(sent[0].Message, "delivery_id: "+deliveryID) {
		t.Fatalf("initial frame %q missing delivery_id %q", sent[0].Message, deliveryID)
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	defer reopened.store.Close()
	var retried []sendMessageArgs
	retriedSend := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		retried = append(retried, a)
		return sendMessageResult{}
	}
	// After restart, restoreWatchSendPending re-loaded the pending; the drain
	// retries delivery with the SAME delivery_id.
	drainWatchSendsVia(t, reopened, retriedSend)

	if len(retried) != 1 {
		t.Fatalf("retry sends = %d, want 1", len(retried))
	}
	if !strings.Contains(retried[0].Message, "delivery_id: "+deliveryID) {
		t.Fatalf("retry frame %q missing same delivery_id %q", retried[0].Message, deliveryID)
	}
}

// TestWatchSendRestoreRetokensPendingAndArmsTerminalNotification re-anchors the
// former ...RetriesPendingBeforeTerminalNotifications onto the drain/notification-rail model.
func TestWatchSendRestoreRetokensPendingAndArmsTerminalNotification(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "01KTESTWATCHRESTORE0000000000"
	jobID := "job_restore_idle"
	now := time.Unix(1000, 0).UTC()
	endedAt := now.Add(time.Second)
	resumable := true

	if err := os.MkdirAll(jobsDir(stateDir, sessionID), 0o755); err != nil {
		t.Fatalf("mkdir jobs dir: %v", err)
	}
	st, err := jobstore.Open(jobsDir(stateDir, sessionID) + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	for _, event := range []jobstore.Event{
		{
			Kind:             jobstore.EventJobStarted,
			TS:               now,
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			OwnerSessionID:   sessionID,
			VisibleToSession: sessionID,
			StartedAt:        &now,
		},
		{
			Kind:          jobstore.EventJobSessionAssigned,
			TS:            now,
			JobID:         jobID,
			TranscriptRef: encodeRef("", "child_restore_idle"),
			Resumable:     &resumable,
		},
		{
			Kind:        jobstore.EventJobFinished,
			TS:          endedAt,
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			TerminalGen: "term_restore_idle",
		},
		{
			Kind: jobstore.EventWatchSendPending,
			TS:   endedAt,
			WatchSend: &jobstore.WatchSendState{
				Key: jobstore.WatchSendKey{
					VisibleSessionID:        sessionID,
					WatchTarget:             jobID,
					ResolvedWatchedIdentity: jobID,
					ResolvedSendTo:          runtimeMessageAliasCaller,
					WatchGeneration:         "watch_restore_generation",
				},
				DeliveryID:      "delivery_restore_pending",
				UpdateSeq:       1,
				Message:         "restored observe",
				Frame:           "restored observe\n\ndelivery_id: delivery_restore_pending",
				TriggerIdentity: jobID,
				TriggerReason:   "output_match: ready",
				CreatedAt:       endedAt,
				UpdatedAt:       endedAt,
			},
		},
	} {
		if err := st.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close job store: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	defer restored.Close()

	// Restore re-tokens the caller pending onto the notification rail; nothing is
	// delivered until the loop-owned drain+accept renders it (spec §4.3).
	if queue := restored.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want caller send on the notification rail, not steering", queue)
	}
	drainAndAccept(t, restored)

	restoredFrame := waitForSteeringEntryContaining(t, restored, "delivery_id: delivery_restore_pending")
	if !strings.Contains(restoredFrame, "restored observe") {
		t.Fatalf("restored watch send text = %q, want stored frame with delivery id", restoredFrame)
	}
	if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after restore retry = %+v, want none", pending)
	}

	// The caller frame and the pre-existing terminal job both surface at the same
	// accept boundary. The durable watch_send_delivered (settled at accept) and the
	// terminal job_notification_pending (armed at restore) are both appended. The
	// old strict delivered<notification ordering no longer holds: caller sends moved
	// from between-rounds steering to between-inputs notifications, so the terminal
	// notification's pending is armed first and the watch send settles at the turn.
	events := loadJobStoreEvents(t, restored.jobManager)
	var sawDelivered, sawNotification bool
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventWatchSendDelivered:
			if event.WatchSend != nil && event.WatchSend.DeliveryID == "delivery_restore_pending" {
				sawDelivered = true
			}
		case jobstore.EventJobNotificationPending:
			sawNotification = true
		}
	}
	if !sawDelivered {
		t.Fatalf("restored caller watch send was not settled (no watch_send_delivered): %+v", events)
	}
	if !sawNotification {
		t.Fatalf("terminal job notification was not armed (no job_notification_pending): %+v", events)
	}
}

func TestWatchSendRestoreKeepsConcreteTerminalResumableDelegatePending(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(1000, 0).UTC()
	for _, event := range restoredWatchSendPendingEvents(s.ID(), rec.JobID, rec.JobID, now) {
		if err := s.jobManager.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	parentMeta := s.Meta()
	stateDir := s.stateDir
	requestsBeforeRestore := len(adapter.Requests())
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	defer restored.Close()

	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restore reconstructed child runtime = %+v, want none before explicit job_send_message", sub)
	}
	if requests := adapter.Requests(); len(requests) != requestsBeforeRestore {
		t.Fatalf("adapter requests after restore = %d, want unchanged %d", len(requests), requestsBeforeRestore)
	}
	if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending after restore retry = %+v, want retained watch send", pending)
	}
	events := loadJobStoreEvents(t, restored.jobManager)
	deliveredSeq := int64(0)
	notificationSeq := int64(0)
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventWatchSendDelivered:
			deliveredSeq = event.Seq
		case jobstore.EventJobNotificationPending:
			notificationSeq = event.Seq
		}
	}
	if deliveredSeq != 0 {
		t.Fatalf("delivered seq = %d, want no restore-time delivery", deliveredSeq)
	}
	if notificationSeq == 0 {
		t.Fatal("missing terminal notification")
	}
}

func TestWatchSendRestoreKeepsConcreteDelegateProductionSendPending(t *testing.T) {
	stateDir := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("watch follow-up complete")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "first task",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first delegate = %+v, want completed", first)
	}
	parentMeta := sess.Meta()
	sess.Close()

	now := time.Unix(2000, 0).UTC()
	st, err := jobstore.Open(jobsDir(stateDir, sess.ID()) + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	resumable := true
	if err := st.Append(jobstore.Event{
		Kind:          jobstore.EventJobSessionAssigned,
		TS:            now,
		JobID:         first.JobID,
		TranscriptRef: first.TranscriptRef,
		Resumable:     &resumable,
	}); err != nil {
		t.Fatalf("append resumable assignment: %v", err)
	}
	for _, event := range restoredWatchSendPendingEvents(sess.ID(), first.JobID, first.JobID, now) {
		if err := st.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close job store: %v", err)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	requestsBeforeRestore := len(adapter.Requests())

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	defer restored.Close()

	if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending after production restore retry = %+v, want retained watch send", pending)
	}
	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restore reconstructed child runtime = %+v, want none before explicit job_send_message", sub)
	}
	if requests := adapter.Requests(); len(requests) != requestsBeforeRestore {
		t.Fatalf("adapter requests after restore = %d, want unchanged %d", len(requests), requestsBeforeRestore)
	}
	events := loadJobStoreEvents(t, restored.jobManager)
	deliveredSeq := int64(0)
	notificationSeq := int64(0)
	var resumedJob string
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventWatchSendDelivered:
			deliveredSeq = event.Seq
		case jobstore.EventJobNotificationPending:
			notificationSeq = event.Seq
		case jobstore.EventJobStarted:
			if event.JobID != first.JobID && event.TranscriptRef == first.TranscriptRef {
				resumedJob = event.JobID
			}
		}
	}
	if deliveredSeq != 0 {
		t.Fatalf("watch_send_delivered seq = %d, want none during restore", deliveredSeq)
	}
	if notificationSeq == 0 {
		t.Fatal("missing terminal notification")
	}
	if resumedJob != "" {
		t.Fatalf("restore appended resumed delegate job %q for transcript %q", resumedJob, first.TranscriptRef)
	}
}

func TestWatchSendRestoreDoesNotAutoResumeRuntimeLostDelegate(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("restore retry must not run")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(3000, 0).UTC()
	for _, event := range restoredWatchSendPendingEvents(s.ID(), rec.JobID, rec.JobID, now) {
		if err := s.jobManager.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	parentMeta := s.Meta()
	stateDir := s.stateDir
	beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	defer restored.Close()

	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restore reconstructed child runtime = %+v, want none before explicit job_send_message", sub)
	}
	if jobs := restored.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != beforeJobs {
		t.Fatalf("delegate jobs after restore = %+v, want %d existing runtime_lost job only", jobs, beforeJobs)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during restore", requests)
	}
	if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending after restore retry = %+v, want watch send retained", pending)
	}
	for _, event := range loadJobStoreEvents(t, restored.jobManager) {
		if event.Kind == jobstore.EventWatchSendDelivered {
			t.Fatalf("restore delivered watch send to runtime-lost delegate: %+v", event)
		}
		if event.Kind == jobstore.EventJobStarted && event.JobID != rec.JobID && event.TranscriptRef == rec.TranscriptRef {
			t.Fatalf("restore appended resumed delegate job: %+v", event)
		}
	}
}

func TestWatchSendRestoreDropsDynamicallyNonResumableRuntimeLostDelegate(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("restore retry must not run")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	rec.DelegateRestore.LocalEnvPolicy = "not-a-policy"
	replaceStoredDelegateRecord(t, s, rec)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(3100, 0).UTC()
	for _, event := range restoredWatchSendPendingEvents(s.ID(), rec.JobID, rec.JobID, now) {
		if err := s.jobManager.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	parentMeta := s.Meta()
	stateDir := s.stateDir
	beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	defer restored.Close()

	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restore reconstructed child runtime = %+v, want none for non-resumable target", sub)
	}
	if jobs := restored.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != beforeJobs {
		t.Fatalf("delegate jobs after restore = %+v, want %d existing runtime_lost job only", jobs, beforeJobs)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during restore", requests)
	}
	if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after restore retry = %+v, want dropped watch send", pending)
	}
	var droppedReason string
	for _, event := range loadJobStoreEvents(t, restored.jobManager) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil {
			droppedReason = event.WatchSend.DiagnosticReason
		}
		if event.Kind == jobstore.EventJobStarted && event.JobID != rec.JobID && event.TranscriptRef == rec.TranscriptRef {
			t.Fatalf("restore appended resumed delegate job: %+v", event)
		}
	}
	if !strings.Contains(droppedReason, "target_not_resumable:parent_linkage_unavailable") {
		t.Fatalf("dropped reason = %q, want dynamic not-resumable reason", droppedReason)
	}
}

func TestWatchSendRestoreDropsDynamicallyNonResumableTerminalDelegate(t *testing.T) {
	cases := []struct {
		status jobstore.Status
		reason string
	}{
		{status: jobstore.StatusCompleted, reason: "exit_zero"},
		{status: jobstore.StatusCancelled, reason: "cancelled"},
		{status: jobstore.StatusFailed, reason: "failed"},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			adapter := &fakeAdapter{
				name: "openai",
				steps: []func(req llm.Request) llm.Response{
					func(req llm.Request) llm.Response {
						return communicateWithDefaultOutput("restore retry must not run")
					},
				},
			}
			c := llm.NewClient()
			c.Register(adapter)
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, tc.status, tc.reason)
			markStoredDelegateResumable(t, s, rec)
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			childID := rec.DelegateRestore.ChildSessionID
			removeChildSessionMeta(t, s, rec)
			now := time.Unix(3200, 0).UTC()
			for _, event := range restoredWatchSendPendingEvents(s.ID(), rec.JobID, rec.JobID, now) {
				if err := s.jobManager.appendEvent(event); err != nil {
					t.Fatalf("append %s: %v", event.Kind, err)
				}
			}
			parentMeta := s.Meta()
			stateDir := s.stateDir
			beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))
			s.Close()

			restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
			if err != nil {
				t.Fatalf("restore session: %v", err)
			}
			defer restored.Close()

			if sub := restored.subagents.get(childID); sub != nil {
				t.Fatalf("restore reconstructed child runtime = %+v, want none for non-resumable terminal target", sub)
			}
			if jobs := restored.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != beforeJobs {
				t.Fatalf("delegate jobs after restore = %+v, want %d existing terminal job only", jobs, beforeJobs)
			}
			if requests := adapter.Requests(); len(requests) != 0 {
				t.Fatalf("adapter requests = %+v, want no model calls during restore", requests)
			}
			if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 0 {
				t.Fatalf("pending after restore retry = %+v, want dropped watch send", pending)
			}
			var droppedReason string
			for _, event := range loadJobStoreEvents(t, restored.jobManager) {
				if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil {
					droppedReason = event.WatchSend.DiagnosticReason
				}
				if event.Kind == jobstore.EventJobStarted && event.JobID != rec.JobID && event.TranscriptRef == rec.TranscriptRef {
					t.Fatalf("restore appended resumed delegate job: %+v", event)
				}
			}
			if !strings.Contains(droppedReason, "target_not_resumable:missing_child_session_meta") {
				t.Fatalf("dropped reason = %q, want dynamic missing child meta reason", droppedReason)
			}
		})
	}
}

func TestWatchSendRestoreDropsTerminalResumableDelegateMissingRestoreDescriptor(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "S1"
	jobID := "job_restore_delegate"
	now := time.Unix(3300, 0).UTC()
	resumable := true

	jm, err := newJobManager(stateDir, sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	for _, event := range restoredWatchSendDelegateEvents(sessionID, jobID, now, &resumable, jobID) {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	reopened, err := newJobManager(stateDir, sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	defer reopened.store.Close()
	s := &Session{
		id:         sessionID,
		stateDir:   stateDir,
		jobManager: reopened,
		subagents:  newSubagentManager(nil),
	}

	if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("retry restored pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
		t.Fatalf("pending after restore retry = %+v, want dropped missing-restore-metadata watch send", pending)
	}
	var droppedReason string
	for _, event := range loadJobStoreEvents(t, reopened) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil {
			droppedReason = event.WatchSend.DiagnosticReason
		}
	}
	if !strings.Contains(droppedReason, "target_not_resumable:missing_delegate_resume_metadata") {
		t.Fatalf("dropped reason = %q, want missing delegate resume metadata", droppedReason)
	}
}

func TestWatchSendRestoreDropsHardFailureTargetsOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sendTo   string
		events   func(string, time.Time) []jobstore.Event
		wantText string
	}{
		{
			name:     "unknown",
			sendTo:   "job_missing",
			events:   func(string, time.Time) []jobstore.Event { return nil },
			wantText: "target_not_found",
		},
		{
			name:   "non_messageable",
			sendTo: "job_shell",
			events: func(sessionID string, now time.Time) []jobstore.Event {
				return []jobstore.Event{{
					Kind:             jobstore.EventJobStarted,
					TS:               now,
					JobID:            "job_shell",
					Type:             jobstore.JobShell,
					OwnerSessionID:   sessionID,
					VisibleToSession: sessionID,
					StartedAt:        &now,
				}}
			},
			wantText: "target_not_messageable",
		},
		{
			name:   "non_resumable",
			sendTo: "job_not_resumable",
			events: func(sessionID string, now time.Time) []jobstore.Event {
				resumable := false
				return restoredWatchSendDelegateEvents(sessionID, "job_not_resumable", now, &resumable, "job_not_resumable")[:3]
			},
			wantText: "target_not_resumable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			sessionID := "S1"
			now := time.Unix(1000, 0).UTC()
			var notified []jobNotification
			jm, err := newJobManager(stateDir, sessionID, func(n jobNotification) { notified = append(notified, n) })
			if err != nil {
				t.Fatalf("new job manager: %v", err)
			}
			for _, event := range tc.events(sessionID, now) {
				if err := jm.appendEvent(event); err != nil {
					t.Fatalf("append %s: %v", event.Kind, err)
				}
			}
			for _, event := range restoredWatchSendPendingEvents(sessionID, "job_watched", tc.sendTo, now) {
				if err := jm.appendEvent(event); err != nil {
					t.Fatalf("append %s: %v", event.Kind, err)
				}
			}
			if err := jm.store.Close(); err != nil {
				t.Fatalf("close seed store: %v", err)
			}
			reopened, err := newJobManager(stateDir, sessionID, func(n jobNotification) { notified = append(notified, n) })
			if err != nil {
				t.Fatalf("reopen job manager: %v", err)
			}
			defer reopened.store.Close()
			s := &Session{
				id:         sessionID,
				jobManager: reopened,
				subagents:  newSubagentManager(nil),
			}

			if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
				t.Fatalf("first retry restored pending: %v", err)
			}
			if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
				t.Fatalf("second retry restored pending: %v", err)
			}

			if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
				t.Fatalf("pending after hard failure retry = %+v, want none", pending)
			}
			if len(notified) != 1 {
				t.Fatalf("diagnostics = %d, want exactly 1: %+v", len(notified), notified)
			}
			if !strings.Contains(notified[0].Reason, "delivery_id=delivery_restore_pending") ||
				!strings.Contains(notified[0].Reason, tc.wantText) {
				t.Fatalf("diagnostic reason = %q, want delivery id and %q", notified[0].Reason, tc.wantText)
			}
		})
	}
}

func TestWatchSendHardFailureDropsPendingAndDiagnosesOnceAcrossRestores(t *testing.T) {
	stateDir := t.TempDir()
	var notified []jobNotification
	jm, err := newJobManager(stateDir, "S1", func(n jobNotification) { notified = append(notified, n) })
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return hardWatchSendResult(errors.New("target_not_messageable"))
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Observation records pending; the drain attempts delivery, hits a hard
	// failure, and drops the pending with a single diagnostic.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	_ = drainWatchSendsVia(t, jm, send)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after hard failure = %+v, want none", pending)
	}
	if len(notified) != 1 {
		t.Fatalf("diagnostics after hard failure = %d, want 1: %+v", len(notified), notified)
	}
	if !strings.Contains(notified[0].Reason, "delivery_id=") {
		t.Fatalf("diagnostic reason = %q, want delivery id", notified[0].Reason)
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// The drop is durable: a restart re-loads no pending, so a drain re-diagnoses
	// nothing — the diagnostic stays at exactly one across restores.
	reopened, err := newJobManager(stateDir, "S1", func(n jobNotification) { notified = append(notified, n) })
	if err != nil {
		t.Fatalf("first reopen: %v", err)
	}
	_ = drainWatchSendsVia(t, reopened, send)
	if err := reopened.store.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	second, err := newJobManager(stateDir, "S1", func(n jobNotification) { notified = append(notified, n) })
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer second.store.Close()
	_ = drainWatchSendsVia(t, second, send)

	if len(notified) != 1 {
		t.Fatalf("diagnostics across restores = %d, want exactly 1: %+v", len(notified), notified)
	}
}

func TestWatchSendTerminalOrderingSendsFinalFrameBeforeTerminalNotification(t *testing.T) {
	jm := newTestJM(t)
	var order []string
	seedCommonWatchSendTargets(t, jm)
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendPending {
			order = append(order, "record")
		}
		return realAppend(e)
	}
	jm.enqueue = func(n jobNotification) {
		if n.Status != jobNotificationEventWatch {
			order = append(order, "terminal")
		}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	// finalize persists the final watch frame as pending before arming the terminal
	// notification (observation order preserved; delivery is the drain's job).
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if strings.Join(order, ",") != "record,terminal" {
		t.Fatalf("order = %v, want final frame recorded before terminal", order)
	}
}

// TestWatchSendTerminalPendingPersistenceFailureRetainsFrameForDrain re-anchors
// the old ...RetriesFinalization. The old test asserted that a watch_send_pending
// persistence failure during finalize made finalize FAIL and leave the terminal
// notification un-armed, so a re-finalize retried the whole thing. The mailbox
// design decouples terminal arming from watch-send persistence (spec §4.1/§4.3):
// armFinalizedJob is persist-only and does not let a watch-send persist failure
// block arming; instead rememberUnpersistedTerminalPendingWatchSend retains the
// final frame in runtime terminalFlush, and the next drain re-persists + delivers
// it. The preserved guarantee — the final frame is not lost, it is retried — holds
// via the drain rather than via finalize-retry. (Crash in the persist-failure
// window is the documented at-least-once tradeoff; the OLD test never exercised it.)
func TestWatchSendTerminalPendingPersistenceFailureRetainsFrameForDrain(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	appendErr := errors.New("pending append failed")
	realAppend := jm.appendEvent
	blocked := true
	jm.appendEvent = func(e jobstore.Event) error {
		if blocked && e.Kind == jobstore.EventWatchSendPending {
			return appendErr
		}
		return realAppend(e)
	}

	// Finalize succeeds and arms despite the watch-send persist failure; the final
	// frame is retained in runtime terminalFlush, not lost.
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize err = %v, want success (persist failure does not block arming)", err)
	}
	if len(sent) != 0 {
		t.Fatalf("final watch send delivered during finalize: %#v", sent)
	}
	jobs := jm.list(listFilter{})
	job := findListedJob(jobs, rec.JobID)
	if job == nil || job.Status != jobstore.StatusCompleted {
		t.Fatalf("job state after finalization = %+v, want terminal retained", jobs)
	}
	if job.NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state after finalization = %q, want armed (decoupled from watch persist)", job.NotifyState)
	}
	jm.mu.Lock()
	var retainedFrame string
	for cfg := range jm.terminalFlush {
		for _, state := range cfg.pending {
			if state.Key.ResolvedSendTo == "job_obs" {
				retainedFrame = state.Frame
			}
		}
	}
	jm.mu.Unlock()
	if !strings.Contains(retainedFrame, "output_match: server ready") {
		t.Fatalf("final frame retained in terminalFlush = %q, want original final trigger", retainedFrame)
	}

	// The next drain re-persists and delivers the retained final frame.
	blocked = false
	_ = drainWatchSendsVia(t, jm, send)
	if len(sent) != 1 {
		t.Fatalf("final watch send after drain = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0].Message, "output_match: server ready") {
		t.Fatalf("retried final watch frame = %q, want original final trigger", sent[0].Message)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after drain = %+v, want settled", pending)
	}
}

func TestWatchSendTerminalFlushBatchContinuesAfterPersistenceFailure(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	seedWatchSendDelegateTarget(t, jm, "job_obs_a")
	seedWatchSendDelegateTarget(t, jm, "job_obs_b")
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs_a", Message: "observe a"},
	}); err != nil {
		t.Fatalf("configure first watch: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs_b", Message: "observe b"},
	}); err != nil {
		t.Fatalf("configure second watch: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	appendErr := errors.New("pending append failed")
	realAppend := jm.appendEvent
	blockFirst := true
	var failedTarget string
	jm.appendEvent = func(e jobstore.Event) error {
		if blockFirst &&
			e.Kind == jobstore.EventWatchSendPending &&
			e.WatchSend != nil &&
			failedTarget == "" {
			failedTarget = e.WatchSend.Key.ResolvedSendTo
			return appendErr
		}
		return realAppend(e)
	}

	// finalize persists the terminal batch as pending (delivery is the drain's
	// job). One pending persist fails; the failed target is retained in runtime
	// terminalFlush, the survivor persists, and arming is not blocked (spec §4.1).
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize err = %v, want success despite a partial-batch persist failure", err)
	}
	if failedTarget == "" {
		t.Fatal("test did not intercept pending append")
	}
	if len(sent) != 0 {
		t.Fatalf("watch sends delivered during finalize: %#v", sent)
	}
	jm.mu.Lock()
	var retainedFirst bool
	for cfg := range jm.terminalFlush {
		for _, state := range cfg.pending {
			if state.Key.ResolvedSendTo == failedTarget {
				retainedFirst = true
			}
		}
	}
	jm.mu.Unlock()
	if !retainedFirst {
		t.Fatal("failed terminal delivery was not retained for drain retry")
	}
	jobs := jm.list(listFilter{})
	job := findListedJob(jobs, rec.JobID)
	if job == nil || job.NotifyState != jobstore.NotifyPending {
		t.Fatalf("job state after partial terminal failure = %+v, want terminal notification armed", jobs)
	}

	// The drain delivers both the survivor and the retained failed target.
	blockFirst = false
	_ = drainWatchSendsVia(t, jm, send)
	if len(sent) != 2 {
		t.Fatalf("sent after drain = %+v, want both targets delivered once", sent)
	}
	var sentFailed bool
	for _, a := range sent {
		if a.Target == failedTarget {
			sentFailed = true
		}
	}
	if !sentFailed {
		t.Fatalf("drain did not deliver the failed target %q; sent = %+v", failedTarget, sent)
	}
}

func TestWatchSendToWatchedDeliversFrameToConcreteTarget(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec := createRunningDelegateWatchTarget(t, jm)
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "watched", Message: "saw ready"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("server READY\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("observation must record one pending send, got %d", len(pending))
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 {
		t.Fatalf("a send watch must deliver once, got %d", len(sent))
	}
	if sent[0].Target != rec.JobID {
		t.Fatalf("delivery target = %q, want watched job %q", sent[0].Target, rec.JobID)
	}
}

func TestWatchSendToWatchedWildcardJobNotificationDeliversConcreteTarget(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	_, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "watched", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_delegate", JobType: "delegate", Status: "completed"})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("observation must record one pending send, got %d", len(pending))
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 {
		t.Fatalf("wildcard job notification must deliver once, got %d", len(sent))
	}
	if sent[0].Target != "job_delegate" {
		t.Fatalf("delivery target = %q, want concrete watched job", sent[0].Target)
	}
}

func TestWatchSendPendingSnapshotCoalescesAndDoesNotRereadOutput(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready", IncludeExcerpt: true},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("first READY\ninitial excerpt\n")); err != nil {
		t.Fatalf("append first output: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("second READY\nlatest excerpt\n")); err != nil {
		t.Fatalf("append second output: %v", err)
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("do not reread\n")); err != nil {
		t.Fatalf("append later output: %v", err)
	}

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1: %+v", len(pending), pending)
	}
	var state *jobstore.WatchSendState
	for _, pendingState := range pending {
		state = pendingState
	}
	if state.CoalescedCount != 1 {
		t.Fatalf("coalesced_count = %d, want 1", state.CoalescedCount)
	}
	if !strings.Contains(state.Frame, "second READY") || !strings.Contains(state.Frame, "latest excerpt") {
		t.Fatalf("pending frame did not snapshot latest trigger/output: %q", state.Frame)
	}
	if strings.Contains(state.Frame, "do not reread") {
		t.Fatalf("pending frame reread later output: %q", state.Frame)
	}
}

func TestWatchSendPendingUsesTriggerTimeFrameSnapshot(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeExcerpt: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("server ready\ninitial excerpt\n")); err != nil {
		t.Fatalf("append trigger output: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: server ready")
	if _, err := jm.running[rec.JobID].output.Append([]byte("later output must not be snapshotted\n")); err != nil {
		t.Fatalf("append later output: %v", err)
	}

	_ = deliverWatchSendVia(t, jm, delivery, send)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "initial excerpt") {
			t.Fatalf("pending frame = %q, want trigger-time excerpt", state.Frame)
		}
		if strings.Contains(state.Frame, "later output must not be snapshotted") {
			t.Fatalf("pending frame reread output after trigger: %q", state.Frame)
		}
	}
}

func TestWatchSendGenerationChangesAfterRestoreAndReplacementDropsOldPending(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	seedCommonWatchSendTargets(t, jm)

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "first generation"},
	}); err != nil {
		t.Fatalf("configure first watch: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("first READY\n"))
	firstPending := loadWatchSendRecord(t, jm).Pending
	if len(firstPending) != 1 {
		t.Fatalf("first pending count = %d, want 1", len(firstPending))
	}
	var firstKey jobstore.WatchSendKey
	for key := range firstPending {
		firstKey = key
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	reopened.now = func() time.Time { return time.Unix(1001, 0).UTC() }
	output, err := jobstore.OpenOutput(reopened.outputPathForJob(rec, rec.JobID), maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("reopen output: %v", err)
	}
	reopened.running[rec.JobID] = &runningJob{rec: rec, output: output, done: make(chan struct{})}
	t.Cleanup(func() { _ = output.Close() })
	if _, err := reopened.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "second generation"},
	}); err != nil {
		t.Fatalf("configure second watch: %v", err)
	}
	feedJob(reopened, rec.JobID, []byte("second READY\n"))

	pending := loadWatchSendRecord(t, reopened).Pending
	if len(pending) != 1 {
		t.Fatalf("pending count after restore replacement = %d, want 1: %+v", len(pending), pending)
	}
	if _, ok := pending[firstKey]; ok {
		t.Fatalf("old restored pending key survived replacement cleanup: %+v", pending)
	}
	for key, state := range pending {
		if key.WatchGeneration == firstKey.WatchGeneration {
			t.Fatalf("watch generation reused after restore: %q", key.WatchGeneration)
		}
		if !strings.Contains(state.Frame, "second READY") {
			t.Fatalf("new pending frame = %q, want second trigger", state.Frame)
		}
		return
	}
	t.Fatal("new pending key not found")
}

func TestWatchSendRestoreLoadsPendingStateForFutureRetry(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeExcerpt: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready\nstored excerpt\n")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: server ready")
	_ = deliverWatchSendVia(t, jm, delivery, send)
	folded := loadWatchSendRecord(t, jm).Pending
	if len(folded) != 1 {
		t.Fatalf("folded pending before restore = %d, want 1", len(folded))
	}
	var wantFrame string
	for _, state := range folded {
		wantFrame = state.Frame
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })

	restored := runtimeWatchSendPending(t, reopened)
	if len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1: %+v", len(restored), restored)
	}
	for _, state := range restored {
		if state.Frame != wantFrame {
			t.Fatalf("restored frame = %q, want stored frame %q", state.Frame, wantFrame)
		}
		if !strings.Contains(state.Frame, "stored excerpt") {
			t.Fatalf("restored frame = %q, want stored payload", state.Frame)
		}
	}
}

func TestWatchSendRestoreClearDropsPendingState(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before restore = %d, want 1", len(pending))
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })
	if restored := runtimeWatchSendPending(t, reopened); len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1", len(restored))
	}

	if _, err := reopened.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear restored pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after restore clear = %+v, want none", pending)
	}
}

func TestWatchSendRestoreClearDropsWatchedTargetedPendingState(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	seedCommonWatchSendTargets(t, jm)
	rec := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before restore = %d, want 1", len(pending))
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })
	if restored := runtimeWatchSendPending(t, reopened); len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1", len(restored))
	}

	if _, err := reopened.configureWatch(watchArgs{Target: rec.JobID, Send: &watchSendArgs{To: "watched"}, Clear: true}); err != nil {
		t.Fatalf("clear restored watched pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after restore watched clear = %+v, want none", pending)
	}
}

func TestWatchSendRestoreReconfigureDropsWatchedPendingState(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	seedCommonWatchSendTargets(t, jm)
	rec := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure first watch: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	firstPending := loadWatchSendRecord(t, jm).Pending
	if len(firstPending) != 1 {
		t.Fatalf("pending before restore = %d, want 1", len(firstPending))
	}
	var firstKey jobstore.WatchSendKey
	for key := range firstPending {
		firstKey = key
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	reopened.now = func() time.Time { return time.Unix(1001, 0).UTC() }
	output, err := jobstore.OpenOutput(reopened.outputPathForJob(rec, rec.JobID), maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("reopen output: %v", err)
	}
	reopened.running[rec.JobID] = &runningJob{rec: rec, output: output, done: make(chan struct{}), durableStarted: true}
	t.Cleanup(func() { _ = output.Close() })
	t.Cleanup(func() { _ = reopened.store.Close() })
	if restored := runtimeWatchSendPending(t, reopened); len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1", len(restored))
	}

	if _, err := reopened.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "blocked",
		Send:        &watchSendArgs{To: "watched", Message: "replacement"},
	}); err != nil {
		t.Fatalf("reconfigure watched pending: %v", err)
	}

	pending := loadWatchSendRecord(t, reopened).Pending
	if _, ok := pending[firstKey]; ok {
		t.Fatalf("old restored watched pending survived replacement cleanup: %+v", pending)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after watched replacement = %+v, want none before new trigger", pending)
	}
}

func TestWatchSendClearDropsPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before clear = %d, want 1", len(pending))
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after clear = %+v, want none", pending)
	}
}

func TestWatchSendWatchedTargetPruneDropsPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before prune = %d, want 1", len(pending))
	}

	jm.abandonRunningJob(rec.JobID)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after watched-target prune = %+v, want none", pending)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after watched-target prune = %d, want 0", jm.watchCount())
	}
}

func TestWatchSendPruneAppendFailureKeepsPendingReachable(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready\n"))
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 1 {
		t.Fatalf("pending before prune = %+v, want one", cfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	jm.abandonRunningJob(rec.JobID)

	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed prune append = %d, want 1", got)
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	if !reachable && jm.terminalFlush != nil {
		reachable = jm.terminalFlush[cfg]
	}
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("pending watch config was unreachable after failed prune append")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed prune append = %d, want 1", len(pending))
	}

	jm.appendEvent = realAppend
	if err := jm.close(); err != nil {
		t.Fatalf("retry cleanup through close: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry cleanup = %d, want 0", len(pending))
	}
}

func TestWatchSendTerminalFlushPersistsAlreadyFiredPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after terminal flush = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "output_match: server ready") {
			t.Fatalf("pending frame = %q, want flushed trigger", state.Frame)
		}
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after terminal expiry = %d, want 0", jm.watchCount())
	}
}

func TestWatchSendTerminalFlushCloseDropsPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before close = %d, want 1", len(pending))
	}

	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after close = %+v, want none", pending)
	}
}

func TestWatchSendTerminalFlushConfigureClearDropsPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after terminal expiry = %d, want 0", jm.watchCount())
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before configure clear = %d, want 1", len(pending))
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err == nil || !strings.Contains(err.Error(), "target_terminal") {
		t.Fatalf("terminal concrete watch registration error = %v, want target_terminal", err)
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("configure clear terminal-flushed pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after configure clear = %+v, want none", pending)
	}
}

func TestWatchSendTerminalExpiryWithoutPendingDoesNotRetainDetachedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args watchArgs
	}{
		{
			name: "notification only",
			args: watchArgs{OutputMatch: "ready"},
		},
		{
			name: "send without flushed match",
			args: watchArgs{OutputMatch: "ready", Send: &watchSendArgs{To: "job_obs", Message: "observe"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			rec, _ := jm.createShell(createShellOpts{Command: "x"})
			seedWatchSendDelegateTarget(t, jm, "job_obs")
			tc.args.Target = rec.JobID
			if _, err := jm.configureWatch(tc.args); err != nil {
				t.Fatalf("configure: %v", err)
			}

			code := 0
			if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
				t.Fatalf("finalize: %v", err)
			}

			jm.mu.Lock()
			detached := len(jm.terminalFlush)
			jm.mu.Unlock()
			if detached != 0 {
				t.Fatalf("detached terminal flush configs = %d, want 0", detached)
			}
			if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err == nil || !strings.Contains(err.Error(), "target_terminal") {
				t.Fatalf("clear expired watch without pending = %v, want target_terminal", err)
			}
		})
	}
}

func TestWatchSendTerminalExpiryWithInflightSendRemainsClearable(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	jm.mu.Lock()
	detached := len(jm.terminalFlush)
	jm.mu.Unlock()
	if detached != 1 {
		t.Fatalf("detached terminal flush configs = %d, want 1", detached)
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Send: &watchSendArgs{To: "job_obs"}, Clear: true}); err != nil {
		t.Fatalf("clear terminal-flushed send: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after clear = %+v, want none", pending)
	}
}

func TestWatchSendClearNormalizesSendTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		configured  string
		clearTarget string
	}{
		{name: "configured untrimmed", configured: " job_obs ", clearTarget: "job_obs"},
		{name: "clear untrimmed", configured: "job_obs", clearTarget: " job_obs "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			rec, _ := jm.createShell(createShellOpts{Command: "x"})
			seedWatchSendDelegateTarget(t, jm, "job_obs")
			if _, err := jm.configureWatch(watchArgs{
				Target:      rec.JobID,
				OutputMatch: "ready",
				Send:        &watchSendArgs{To: tc.configured, Message: "observe"},
			}); err != nil {
				t.Fatalf("configure: %v", err)
			}
			if _, err := jm.configureWatch(watchArgs{
				Target: rec.JobID,
				Send:   &watchSendArgs{To: tc.clearTarget},
				Clear:  true,
			}); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count after clear = %d, want 0", jm.watchCount())
			}
		})
	}
}

func TestWatchSendTerminalFlushWatchedTargetedClearDropsPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after terminal flush = %d, want 1: %+v", len(pending), pending)
	}
	for key := range pending {
		if key.ResolvedSendTo != rec.JobID {
			t.Fatalf("pending resolved send target = %q, want watched job %q", key.ResolvedSendTo, rec.JobID)
		}
	}

	if _, err := jm.clearWatch(watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "watched"}); err != nil {
		t.Fatalf("clear terminal-flushed watched pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after terminal watched clear = %+v, want none", pending)
	}
}

func TestWatchSendTerminalFlushClearBeforeFailedSendDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cleared := false
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		if !cleared {
			cleared = true
			if _, err := jm.clearWatch(watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}); err != nil {
				t.Fatalf("clear terminal-flushed watch: %v", err)
			}
		}
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	// finalize records the terminal-flush pending; the drain's send clears the
	// watch mid-delivery, so the now-stale pending settles to nothing.
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	_ = drainWatchSendsVia(t, jm, send)
	if !cleared {
		t.Fatal("send callback did not clear watch")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after terminal flush clear = %+v, want none", pending)
	}
}

func TestWatchSendTerminalExpiryCloseDropsExistingPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before terminal expiry = %d, want 1", len(pending))
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after terminal expiry = %d, want 0", jm.watchCount())
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after terminal expiry = %d, want 1", len(pending))
	}

	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after close = %+v, want none", pending)
	}
}

func TestWatchSendStaleDeliveryClearedDuringSendDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
			t.Fatalf("clear during send: %v", err)
		}
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready")

	_ = deliverWatchSendVia(t, jm, delivery, send)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("stale delivery cleared during send persisted pending = %+v", pending)
	}
}

func TestWatchSendStaleDeliveryReplacedDuringSendDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		if _, err := jm.configureWatch(watchArgs{
			Target:      rec.JobID,
			OutputMatch: "blocked",
			Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
		}); err != nil {
			t.Fatalf("replace during send: %v", err)
		}
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready")

	_ = deliverWatchSendVia(t, jm, delivery, send)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("stale delivery replaced during send persisted pending = %+v", pending)
	}
}

func TestWatchSendPendingDeliveredRemovesBeforeNextFailure(t *testing.T) {
	jm := newTestJM(t)
	failSend := true
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		if failSend {
			return sendMessageResult{Err: errors.New("busy")}
		}
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	feedJob(jm, rec.JobID, []byte("ready one\n"))
	_ = drainWatchSendsVia(t, jm, send) // busy
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after first failure = %d, want 1", len(pending))
	}
	failSend = false
	feedJob(jm, rec.JobID, []byte("ready two\n"))
	_ = drainWatchSendsVia(t, jm, send) // delivered
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after delivered = %+v, want none", pending)
	}
	failSend = true
	feedJob(jm, rec.JobID, []byte("ready three\n"))
	_ = drainWatchSendsVia(t, jm, send) // busy again
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after second failure = %d, want 1", len(pending))
	}
	for _, state := range pending {
		if state.CoalescedCount != 0 {
			t.Fatalf("coalesced_count after delivered cleanup = %d, want 0", state.CoalescedCount)
		}
	}
}

func TestWatchSendOverlapOlderDeliveredDoesNotRemoveNewerPending(t *testing.T) {
	jm := newTestJM(t)
	sendErr := errors.New("busy")
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: sendErr}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: first ready")
	second := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: second ready")

	_ = deliverWatchSendVia(t, jm, second, send)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after second failure = %d, want 1", len(pending))
	}
	if got := len(second.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after second failure = %d, want 1", got)
	}

	sendErr = nil
	seedCommonWatchSendTargets(t, jm)
	send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}
	_ = deliverWatchSendVia(t, jm, first, send)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("folded pending after older delivered = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "second ready") {
			t.Fatalf("pending frame = %q, want newer trigger", state.Frame)
		}
	}
	if got := len(second.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after older delivered = %d, want newer pending retained", got)
	}
}

func TestWatchSendOverlapOlderFailedDoesNotOverwriteNewerPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: first ready")
	second := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: second ready")

	_ = deliverWatchSendVia(t, jm, second, send)
	_ = deliverWatchSendVia(t, jm, first, send)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("folded pending after older failed delivery = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "second ready") {
			t.Fatalf("pending frame = %q, want newer trigger", state.Frame)
		}
		if state.CoalescedCount != 0 {
			t.Fatalf("coalesced_count = %d, want 0 for ignored older delivery", state.CoalescedCount)
		}
	}
	if got := len(second.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after older failed delivery = %d, want 1", got)
	}
	for _, state := range second.cfg.pending {
		if !strings.Contains(state.Frame, "second ready") {
			t.Fatalf("in-memory pending frame = %q, want newer trigger", state.Frame)
		}
	}
}

func TestWatchSendStaleFailedDeliveryAfterNewerDeliveredDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: first ready")
	second := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: second ready")

	_ = deliverWatchSendVia(t, jm, second, send)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after newer delivered = %d, want 0", len(pending))
	}

	seedCommonWatchSendTargets(t, jm)
	send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	_ = deliverWatchSendVia(t, jm, first, send)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after stale failed delivery = %+v, want none", pending)
	}
	if got := len(first.cfg.pending); got != 0 {
		t.Fatalf("in-memory pending after stale failed delivery = %d, want 0", got)
	}
}

func TestWatchSendTeardownRejectsInFlightFailedDeliveryDuringDroppedAppend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *jobManager) (watchSendDelivery, func() error)
	}{
		{
			name: "clear",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				rec, delivery := setupConcretePendingWatchSend(t, jm)
				return delivery, func() error {
					_, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
					return err
				}
			},
		},
		{
			name: "replace",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				rec, delivery := setupConcretePendingWatchSend(t, jm)
				return delivery, func() error {
					_, err := jm.configureWatch(watchArgs{
						Target:      rec.JobID,
						OutputMatch: "blocked",
						Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
					})
					return err
				}
			},
		},
		{
			name: "prune",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				rec, delivery := setupConcretePendingWatchSend(t, jm)
				return delivery, func() error {
					jm.abandonRunningJob(rec.JobID)
					return nil
				}
			},
		},
		{
			name: "close",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				if _, err := jm.configureWatch(watchArgs{
					Target: "*",
					Events: []string{"job.notification"},
					Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
				}); err != nil {
					t.Fatalf("configure: %v", err)
				}
				jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})
				key := watchKey{VisibleSessionID: jm.sessionID, Target: "*", SendTo: "job_obs"}
				delivery := captureWatchSendDeliveryForKey(t, jm, key, "job_trigger_two", "job.notification")
				return delivery, jm.close
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			seedCommonWatchSendTargets(t, jm)
			send := func(context.Context, sendMessageArgs) sendMessageResult {
				return sendMessageResult{Err: errors.New("busy")}
			}
			delivery, teardown := tc.setup(t, jm)
			if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
				t.Fatalf("pending before teardown = %d, want 1", len(pending))
			}
			dropStarted := make(chan struct{})
			releaseDrop := make(chan struct{})
			realAppend := jm.appendEvent
			blocked := false
			jm.appendEvent = func(e jobstore.Event) error {
				if e.Kind == jobstore.EventWatchSendDropped && !blocked {
					blocked = true
					close(dropStarted)
					<-releaseDrop
				}
				return realAppend(e)
			}

			errCh := make(chan error, 1)
			go func() { errCh <- teardown() }()
			waitForTestSignal(t, dropStarted, "dropped append")

			_ = deliverWatchSendVia(t, jm, delivery, send)
			close(releaseDrop)
			if err := waitForTestError(t, errCh, "teardown"); err != nil {
				t.Fatalf("teardown: %v", err)
			}

			if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
				t.Fatalf("pending after teardown = %+v, want none", pending)
			}
			if got := len(delivery.cfg.pending); got != 0 {
				t.Fatalf("in-memory pending after teardown = %d, want 0", got)
			}
		})
	}
}

func TestWatchSendAppendFailureDuringClearKeepsPendingInMemory(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready\n"))
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 1 {
		t.Fatalf("pending before clear = %+v, want one", cfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err == nil {
		t.Fatal("clear succeeded, want append failure")
	}

	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed clear append = %d, want 1", got)
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("watch config with pending was detached after failed clear append")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed clear append = %d, want 1", len(pending))
	}

	jm.appendEvent = realAppend
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("retry clear: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry clear = %d, want 0", len(pending))
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after retry clear = %d, want 0", jm.watchCount())
	}
}

func TestWatchSendPartialDroppedAppendReconcilesSuccessfulPrefix(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_two", JobType: "delegate", Status: "completed"})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "*", SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 2 {
		t.Fatalf("pending before clear = %+v, want two", cfg)
	}
	realAppend := jm.appendEvent
	var dropped int
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			dropped++
			if dropped == 2 {
				return errors.New("append dropped failed")
			}
		}
		return realAppend(e)
	}

	if _, err := jm.configureWatch(watchArgs{Target: "*", Clear: true}); err == nil {
		t.Fatal("clear succeeded, want partial append failure")
	}
	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after partial dropped append = %d, want 1", got)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after partial dropped append = %d, want 1", len(pending))
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	rejecting := cfg.rejectingDelivery
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("watch config with remaining pending was detached after partial dropped append")
	}
	if rejecting {
		t.Fatal("watch config stayed rejecting after failed dropped append")
	}

	jm.appendEvent = realAppend
	if _, err := jm.configureWatch(watchArgs{Target: "*", Clear: true}); err != nil {
		t.Fatalf("retry clear: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry clear = %d, want 0", len(pending))
	}
}

func TestWatchSendAppendFailureDuringReplaceLeavesOldWatchReachable(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready\n"))
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}
	jm.mu.Lock()
	oldCfg := jm.watches[key]
	jm.mu.Unlock()
	if oldCfg == nil || len(oldCfg.pending) != 1 {
		t.Fatalf("pending before replace = %+v, want one", oldCfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "blocked",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err == nil {
		t.Fatal("replace succeeded, want append failure")
	}

	jm.mu.Lock()
	stillReachable := jm.watches[key] == oldCfg
	jm.mu.Unlock()
	if !stillReachable {
		t.Fatal("old watch config was replaced after failed drop append")
	}
	if got := len(oldCfg.pending); got != 1 {
		t.Fatalf("old pending after failed replace append = %d, want 1", got)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed replace append = %d, want 1", len(pending))
	}

	jm.appendEvent = realAppend
	res, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "blocked",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("retry replace: %v", err)
	}
	if !res.ReplacedExisting {
		t.Fatal("retry replace did not report replaced_existing")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry replace = %d, want 0", len(pending))
	}
}

func TestWatchSendAppendFailureDuringCloseReturnsErrorAndClosesStore(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger", JobType: "delegate", Status: "completed"})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "*", SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	if cfg != nil {
		cfg.progressStop = make(chan struct{})
	}
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 1 {
		t.Fatalf("pending before close = %+v, want one", cfg)
	}
	storePath := jm.dir + "/jobs.jsonl"
	realAppend := jm.appendEvent
	appendErr := errors.New("append dropped failed")
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return appendErr
		}
		return realAppend(e)
	}

	if err := jm.close(); !errors.Is(err, appendErr) {
		t.Fatalf("close error = %v, want append failure", err)
	}
	if _, err := jm.store.Load(); err != nil {
		if !errors.Is(err, jobstore.ErrStoreClosed) {
			t.Fatalf("store after close = %v, want closed", err)
		}
	} else {
		t.Fatal("store load after close succeeded, want closed store")
	}
	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed close append = %d, want 1", got)
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	if !reachable && jm.terminalFlush != nil {
		reachable = jm.terminalFlush[cfg]
	}
	progressArmed := cfg.progressStop != nil
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("pending watch config was unreachable after failed close append")
	}
	if progressArmed {
		t.Fatal("progress timer still armed after failed close append")
	}
	st, err := jobstore.Open(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	record, err := st.LoadWatchSends()
	if err != nil {
		t.Fatalf("load watch sends: %v", err)
	}
	if pending := record.Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed close append = %d, want 1", len(pending))
	}
	jm.mu.Lock()
	closing := jm.closing
	jm.mu.Unlock()
	if !closing {
		t.Fatal("job manager closing flag = false after close")
	}
}

func TestWatchSendAppendFailureDuringDeliveredKeepsPendingInMemory(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready one\n"))
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready two")
	if got := len(delivery.cfg.pending); got != 1 {
		t.Fatalf("pending before delivered = %d, want 1", got)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDelivered {
			return errors.New("append delivered failed")
		}
		return realAppend(e)
	}
	// The send succeeds but the delivered-marker append fails, so the pending must
	// stay in memory and in the durable fold for a later retry.
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}

	_ = deliverWatchSendVia(t, jm, delivery, send)

	if got := len(delivery.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed delivered append = %d, want 1", got)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed delivered append = %d, want 1", len(pending))
	}
}

func TestWatchSendSettledTombstonesAreBounded(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap+5; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}
	jm.mu.Lock()
	var cfg *watchConfig
	for _, watch := range jm.watches {
		cfg = watch
	}
	settled := len(cfg.settledUpdateSeq)
	jm.mu.Unlock()
	if settled > defaultWatchSendPendingCap {
		t.Fatalf("settled tombstones = %d, want <= %d", settled, defaultWatchSendPendingCap)
	}
}

func TestWatchSendAppendFailureDuringEvictionKeepsMemoryAndDurableConsistent(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}
	jm.mu.Lock()
	var cfg *watchConfig
	for _, watch := range jm.watches {
		cfg = watch
	}
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending before eviction = %+v, want cap", cfg)
	}

	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendEvicted {
			return errors.New("append evicted failed")
		}
		return realAppend(e)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_over_cap", JobType: "delegate", Status: "completed"})

	if got := len(cfg.pending); got != defaultWatchSendPendingCap+1 {
		t.Fatalf("in-memory pending after failed eviction append = %d, want %d", got, defaultWatchSendPendingCap+1)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != defaultWatchSendPendingCap+1 {
		t.Fatalf("folded pending after failed eviction append = %d, want %d", len(pending), defaultWatchSendPendingCap+1)
	} else {
		foundOverCap := false
		foundOldest := false
		for key := range pending {
			if key.ResolvedWatchedIdentity == "job_trigger_over_cap" {
				foundOverCap = true
			}
			if key.ResolvedWatchedIdentity == "job_trigger_A" {
				foundOldest = true
			}
		}
		if !foundOverCap || !foundOldest {
			t.Fatalf("folded pending after failed eviction = %+v, want new and not-yet-evicted oldest", pending)
		}
	}
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch send evicted") {
			t.Fatalf("eviction diagnostic emitted before durable evicted append succeeded: %+v", n)
		}
	}

	jm.appendEvent = realAppend
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_retry_cleanup", JobType: "delegate", Status: "completed"})
	if got := len(cfg.pending); got != defaultWatchSendPendingCap {
		t.Fatalf("in-memory pending after retry eviction = %d, want %d", got, defaultWatchSendPendingCap)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != defaultWatchSendPendingCap {
		t.Fatalf("folded pending after retry eviction = %d, want %d", len(pending), defaultWatchSendPendingCap)
	}
}

func TestWatchSendPendingAppendFailureBeforeEvictionKeepsExistingPending(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}
	jm.mu.Lock()
	var cfg *watchConfig
	for _, watch := range jm.watches {
		cfg = watch
	}
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending before failed append = %+v, want cap", cfg)
	}

	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendPending &&
			e.WatchSend != nil &&
			e.WatchSend.Key.ResolvedWatchedIdentity == "job_trigger_over_cap" {
			return errors.New("append pending failed")
		}
		return realAppend(e)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_over_cap", JobType: "delegate", Status: "completed"})

	if got := len(cfg.pending); got != defaultWatchSendPendingCap {
		t.Fatalf("in-memory pending after failed pending append = %d, want %d", got, defaultWatchSendPendingCap)
	}
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != defaultWatchSendPendingCap {
		t.Fatalf("folded pending after failed pending append = %d, want %d", len(pending), defaultWatchSendPendingCap)
	}
	foundOldest := false
	for key := range pending {
		if key.ResolvedWatchedIdentity == "job_trigger_over_cap" {
			t.Fatalf("failed pending append became durable: %+v", pending)
		}
		if key.ResolvedWatchedIdentity == "job_trigger_A" {
			foundOldest = true
		}
	}
	if !foundOldest {
		t.Fatalf("oldest pending was evicted after failed pending append: %+v", pending)
	}
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch send evicted") {
			t.Fatalf("eviction diagnostic emitted after failed pending append: %+v", n)
		}
	}
}

func captureWatchSendDelivery(t *testing.T, jm *jobManager, jobID, trigger string) watchSendDelivery {
	t.Helper()
	jm.mu.Lock()
	var delivery watchSendDelivery
	for _, cfg := range jm.watches {
		if cfg.target == jobID {
			delivery = jm.watchSendSnapshot(cfg, jobID, trigger)
			break
		}
	}
	jm.mu.Unlock()
	if delivery.cfg == nil {
		t.Fatalf("watch for %s not found", jobID)
	}
	return jm.snapshotWatchSendFrame(delivery)
}

func captureWatchSendDeliveryForKey(t *testing.T, jm *jobManager, key watchKey, watchedIdentity, trigger string) watchSendDelivery {
	t.Helper()
	jm.mu.Lock()
	cfg := jm.watches[key]
	var delivery watchSendDelivery
	if cfg != nil {
		delivery = jm.watchSendSnapshot(cfg, watchedIdentity, trigger)
	}
	jm.mu.Unlock()
	if delivery.cfg == nil {
		t.Fatalf("watch for %+v not found", key)
	}
	return jm.snapshotWatchSendFrame(delivery)
}

func setupConcretePendingWatchSend(t *testing.T, jm *jobManager) (*jobstore.JobRecord, watchSendDelivery) {
	t.Helper()
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready one\n"))
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready two")
	return rec, delivery
}

func waitForTestSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForTestError(t *testing.T, ch <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func TestWatchSendCapEvictsOldestPendingAndNotifies(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap+1; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending count = %d, want %d", len(pending), defaultWatchSendPendingCap)
	}
	for key := range pending {
		if key.ResolvedWatchedIdentity == "job_trigger_A" {
			t.Fatalf("oldest pending key was not evicted: %+v", pending)
		}
	}
	var evictionDiagnostics int
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch send evicted") {
			evictionDiagnostics++
			if !strings.Contains(n.Reason, "job_trigger_A") {
				t.Fatalf("diagnostic reason = %q, want evicted trigger", n.Reason)
			}
		}
	}
	if evictionDiagnostics != 1 {
		t.Fatalf("eviction diagnostic count = %d, want 1: %+v", evictionDiagnostics, notified)
	}
}

func createRunningDelegateWatchTarget(t *testing.T, jm *jobManager) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "delegate-output"})
	if err != nil {
		t.Fatalf("create watch target: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	run.rec.Type = jobstore.JobDelegate
	run.rec.TranscriptRef = encodeRef("", "child-"+rec.JobID)
	rec = cloneJobRecord(run.rec)
	jm.mu.Unlock()
	return rec
}

func loadWatchSendRecord(t *testing.T, jm *jobManager) jobstore.WatchSendRecord {
	t.Helper()
	return jobstore.FoldWatchSends(loadJobStoreEvents(t, jm))
}

func restoredWatchSendDelegateEvents(sessionID, jobID string, now time.Time, resumable *bool, sendTo string) []jobstore.Event {
	endedAt := now.Add(time.Second)
	events := []jobstore.Event{
		{
			Kind:             jobstore.EventJobStarted,
			TS:               now,
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			OwnerSessionID:   sessionID,
			VisibleToSession: sessionID,
			StartedAt:        &now,
		},
		{
			Kind:          jobstore.EventJobSessionAssigned,
			TS:            now,
			JobID:         jobID,
			TranscriptRef: encodeRef("", "child_"+jobID),
			Resumable:     resumable,
		},
		{
			Kind:        jobstore.EventJobFinished,
			TS:          endedAt,
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			TerminalGen: "term_" + jobID,
		},
	}
	return append(events, restoredWatchSendPendingEvents(sessionID, jobID, sendTo, endedAt)...)
}

func restoredWatchSendPendingEvents(sessionID, watchedJobID, sendTo string, now time.Time) []jobstore.Event {
	return []jobstore.Event{{
		Kind: jobstore.EventWatchSendPending,
		TS:   now,
		WatchSend: &jobstore.WatchSendState{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        sessionID,
				WatchTarget:             watchedJobID,
				ResolvedWatchedIdentity: watchedJobID,
				ResolvedSendTo:          sendTo,
				WatchGeneration:         "watch_restore_generation",
			},
			DeliveryID:      "delivery_restore_pending",
			UpdateSeq:       1,
			Message:         "restored observe",
			Frame:           "restored observe\n\ndelivery_id: delivery_restore_pending",
			TriggerIdentity: watchedJobID,
			TriggerReason:   "output_match: ready",
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}}
}

func loadJobStoreEvents(t *testing.T, jm *jobManager) []jobstore.Event {
	t.Helper()
	b, err := os.ReadFile(jm.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("read jobs.jsonl: %v", err)
	}
	var events []jobstore.Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e jobstore.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse event %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func runtimeWatchSendPending(t *testing.T, jm *jobManager) map[jobstore.WatchSendKey]*jobstore.WatchSendState {
	t.Helper()
	out := make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		for key, state := range cfg.pending {
			copied := *state
			out[key] = &copied
		}
	}
	for cfg := range jm.terminalFlush {
		for key, state := range cfg.pending {
			copied := *state
			out[key] = &copied
		}
	}
	return out
}

func TestWatchSendToWatchedRejectsSessionEventsWithoutConcreteTarget(t *testing.T) {
	for _, eventName := range []string{"assistant.message", "assistant.tool", "communicate"} {
		t.Run(eventName, func(t *testing.T) {
			jm := newTestJM(t)
			seedCommonWatchSendTargets(t, jm)

			_, err := jm.configureWatch(watchArgs{
				Target: "*",
				Events: []string{eventName},
				Send:   &watchSendArgs{To: "watched", Message: "observe"},
			})

			if err == nil || !strings.Contains(err.Error(), "target_not_found") {
				t.Fatalf("error = %v, want target_not_found", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", jm.watchCount())
			}
			if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
				t.Fatalf("rejected watched send recorded pending: %+v", pending)
			}
		})
	}
}

func TestWatchSendToWatchedAllowsWildcardJobNotificationTrigger(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{Delivered: true, Action: "sent"}
	}

	_, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"*"},
		Send:   &watchSendArgs{To: "watched", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configureWatch returned error: %v", err)
	}

	jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("assistant.message recorded pending = %#v, want no unresolved watched delivery", pending)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger", JobType: "delegate", Status: "completed"})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("job.notification must record one pending send, got %d", len(pending))
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one delivery", sent)
	}
	if sent[0].Target != "job_trigger" {
		t.Fatalf("send target = %q, want concrete watched job", sent[0].Target)
	}
}

func TestWatchSendToWatchedAllowsMixedEventsWithJobNotificationTrigger(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	seedCommonWatchSendTargets(t, jm)
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{Delivered: true, Action: "sent"}
	}

	_, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"assistant.message", "job.notification"},
		Send:   &watchSendArgs{To: "watched", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configureWatch returned error: %v", err)
	}

	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger", JobType: "delegate", Status: "completed"})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("job.notification must record one pending send, got %d", len(pending))
	}
	drainWatchSendsVia(t, jm, send)

	if len(sent) != 1 {
		t.Fatalf("sent = %#v, want one delivery", sent)
	}
	if sent[0].Target != "job_trigger" {
		t.Fatalf("send target = %q, want concrete watched job", sent[0].Target)
	}
}

func seedCommonWatchSendTargets(t *testing.T, jm *jobManager) {
	t.Helper()
	seedWatchSendDelegateTarget(t, jm, "job_obs")
}

func seedWatchSendDelegateTarget(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load jobs before seeding watch-send target: %v", err)
	}
	if rec := recs[jobID]; rec != nil {
		return
	}
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		Status:           jobstore.StatusRunning,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("seed watch-send delegate target %q: %v", jobID, err)
	}
}

func TestWatchSendFailureNotifiesCaller(t *testing.T) {
	jm := newTestJM(t)
	sendErr := errors.New("target_not_messageable: job_obs")
	seedCommonWatchSendTargets(t, jm)
	send := func(context.Context, sendMessageArgs) sendMessageResult {
		return hardWatchSendResult(sendErr)
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Observation records pending; the drain's hard delivery failure drops it and
	// notifies the caller once.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	_ = drainWatchSendsVia(t, jm, send)

	if len(notified) != 1 {
		t.Fatalf("failed watch send must notify caller once, got %d", len(notified))
	}
	if notified[0].Status != jobNotificationEventWatch {
		t.Fatalf("notification status = %q, want watch", notified[0].Status)
	}
	if !strings.Contains(notified[0].Reason, "watch send failed") ||
		!strings.Contains(notified[0].Reason, "target_not_messageable") {
		t.Fatalf("notification reason = %q, want bounded send failure", notified[0].Reason)
	}
}

func TestWatchSendFrameIsBounded(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte(strings.Repeat("x", watchFrameMaxChars*2))); err != nil {
		t.Fatalf("append: %v", err)
	}

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{
			Message:        strings.Repeat("m", watchMessageMaxChars+100),
			IncludeExcerpt: true,
		},
	}, rec.JobID, strings.Repeat("trigger", watchTriggerMaxChars), "delivery_test")

	if len([]rune(frame)) > watchFrameMaxChars {
		t.Fatalf("frame length = %d, want <= %d", len([]rune(frame)), watchFrameMaxChars)
	}
	if !strings.Contains(frame, "Watch frame") || !strings.Contains(frame, "excerpt:") {
		t.Fatalf("frame must include bounded metadata and excerpt; got %q", frame)
	}
}

func TestWatchSendExcerptIncludesFrameMetadata(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready excerpt\n")); err != nil {
		t.Fatalf("append: %v", err)
	}

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{
			Message:        "saw ready",
			IncludeExcerpt: true,
		},
	}, rec.JobID, "output_match: ready excerpt", "delivery_test")

	if !strings.Contains(frame, "saw ready") || !strings.Contains(frame, "ready excerpt") {
		t.Fatalf("excerpt delivery must include message and excerpt; got %q", frame)
	}
	if !strings.Contains(frame, "delivery_id: delivery_test") {
		t.Fatalf("excerpt delivery must include delivery id; got %q", frame)
	}
	if !strings.Contains(frame, "Watch frame") || !strings.Contains(frame, "trigger:") || !strings.Contains(frame, "job_id:") {
		t.Fatalf("excerpt delivery must include frame metadata; got %q", frame)
	}
}

func TestWatchSendMessageIncludesFrameMetadata(t *testing.T) {
	jm := newTestJM(t)

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{Message: "plain message"},
	}, "job_target", "output_match: ready", "delivery_message_only")

	if !strings.Contains(frame, "plain message") {
		t.Fatalf("message delivery must include message; got %q", frame)
	}
	if !strings.Contains(frame, "delivery_id: delivery_message_only") {
		t.Fatalf("message delivery must include delivery id; got %q", frame)
	}
	if !strings.Contains(frame, "Watch frame") || !strings.Contains(frame, "trigger:") || !strings.Contains(frame, "job_id:") {
		t.Fatalf("message delivery must include frame metadata; got %q", frame)
	}
}

func TestConfigureWatchRejectsIncludeExcerptOnSessionTargets(t *testing.T) {
	jm := newTestJM(t)
	delegate := createRunningDelegateWatchTarget(t, jm)
	for _, target := range []string{"caller", "*"} {
		t.Run(target, func(t *testing.T) {
			_, err := jm.configureWatch(watchArgs{
				Target: target,
				Events: []string{"job.notification"},
				Send:   &watchSendArgs{To: delegate.JobID, IncludeExcerpt: true},
			})
			if err == nil {
				t.Fatal("session target include_excerpt watch must error")
			}
			if !strings.Contains(err.Error(), "include_excerpt requires a concrete job target") {
				t.Fatalf("error = %v, want include_excerpt concrete-target validation", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", jm.watchCount())
			}
		})
	}
}

func TestWatchSessionTargetFrameOmitsExcerpt(t *testing.T) {
	jm := newTestJM(t)

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{Message: "session frame", IncludeExcerpt: true},
	}, "caller", "output_match: ready", "dlv")

	if strings.Contains(frame, "excerpt:") {
		t.Fatalf("session-target frame must not carry an excerpt; got %q", frame)
	}
	if strings.Contains(frame, "output_read_error") {
		t.Fatalf("session-target frame must not leak output_read_error; got %q", frame)
	}
}

func TestWatchSessionTargetFrameCarriesTranscriptRef(t *testing.T) {
	jm := newTestJM(t)
	want := "transcript_ref: " + encodeRef("", jm.sessionID)

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{Message: "session frame"},
	}, "caller", "output_match: ready", "dlv")

	if !strings.Contains(frame, want) {
		t.Fatalf("session-target frame must carry %q; got %q", want, frame)
	}
}

func TestWatchJobTargetFrameOmitsTranscriptRef(t *testing.T) {
	jm := newTestJM(t)

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{Message: "job frame"},
	}, "job_target", "output_match: ready", "dlv")

	if strings.Contains(frame, "transcript_ref") {
		t.Fatalf("job-target frame must not carry transcript_ref; got %q", frame)
	}
}

func busyWatchSendResult() sendMessageResult {
	return sendMessageResult{
		WatchSendDeliveryClass:    watchSendBusy,
		WatchSendDeliveryClassSet: true,
		Err:                       errors.New("busy"),
	}
}

func hardWatchSendResult(err error) sendMessageResult {
	return sendMessageResult{
		WatchSendDeliveryClass:    watchSendHardFailure,
		WatchSendDeliveryClassSet: true,
		Err:                       err,
	}
}

func containsEventKind(kinds []jobstore.EventKind, want jobstore.EventKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func eventKindOrder(kinds []jobstore.EventKind, before, after jobstore.EventKind) bool {
	beforeIndex := -1
	afterIndex := -1
	for i, kind := range kinds {
		if kind == before && beforeIndex == -1 {
			beforeIndex = i
		}
		if kind == after && afterIndex == -1 {
			afterIndex = i
		}
	}
	return beforeIndex >= 0 && afterIndex >= 0 && beforeIndex < afterIndex
}

func TestProgressTimerFiresPeriodically(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan struct{}, 4)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: 1000}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("progress timer did not fire within 3s")
	}
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
}

func TestProgressTimerStopsOnClose(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan jobNotification, 16)
	jm.enqueue = func(n jobNotification) { fired <- n }

	if _, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: minWatchProgressIntervalMS}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case n := <-fired:
		if n.JobID != "" {
			t.Fatalf("session progress notification job_id = %q, want empty", n.JobID)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("progress timer did not fire before close")
	}
	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for {
		select {
		case <-fired:
		default:
			goto drained
		}
	}

drained:
	if count := jm.watchCount(); count != 0 {
		t.Fatalf("close must remove watches; count = %d", count)
	}
	select {
	case <-fired:
		t.Fatal("progress timer fired after close")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchEventKindNamesResolve(t *testing.T) {
	if len(WatchEventKindNames) != len(modelEventKinds) {
		t.Fatalf("WatchEventKindNames has %d names, modelEventKinds has %d", len(WatchEventKindNames), len(modelEventKinds))
	}
	for _, name := range WatchEventKindNames {
		if _, ok := modelEventKinds[name]; !ok {
			t.Errorf("WatchEventKindNames includes unresolved event kind %q", name)
		}
	}
}

func installCallerSendWatchWithPending(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	// This is the feedback-loop shape (caller target, assistant.message,
	// send.to=caller) that configureWatch now rejects (TestValidateWatchDeliveryLoop
	// asserts the rejection). Install below validation to exercise the caller-send
	// pending/busy-delivery mechanics this helper's callers depend on.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.message"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("installCallerSendWatchWithPending: watch config not found")
	}
	if len(cfg.pendingOrder) == 0 {
		t.Fatal("installCallerSendWatchWithPending: no pending send after busy delivery")
	}
	return cfg
}

// installCallerSendWatchWithCurrentFrame installs a caller-send watch, drives it
// to updateSeq 2 (one busy fire creates pending @1, a second coalesces to @2),
// then stamps a deterministic Frame on the single pending entry so render-by-key
// assertions can match exact frame text. Returns the cfg, the pending key, and
// the pending entry's DeliveryID.
func installCallerSendWatchWithCurrentFrame(t *testing.T, jm *jobManager, frame string) (*watchConfig, jobstore.WatchSendKey, string) {
	t.Helper()
	cfg := installCallerSendWatchWithPending(t, jm)
	jm.onSessionEvent(events.EventAssistantTextEnd, nil) // bump updateSeq 1 -> 2
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(cfg.pendingOrder) != 1 {
		t.Fatalf("want exactly one pending entry, got %d", len(cfg.pendingOrder))
	}
	key := cfg.pendingOrder[0]
	state := cfg.pending[key]
	if state == nil {
		t.Fatal("pending entry missing for key")
	}
	if state.UpdateSeq != 2 {
		t.Fatalf("pending updateSeq = %d, want 2 after two fires", state.UpdateSeq)
	}
	state.Frame = frame
	return cfg, key, state.DeliveryID
}

func TestWatchSendTokenRenderByKey(t *testing.T) {
	jm := newTestJM(t)
	_, key, deliveryID := installCallerSendWatchWithCurrentFrame(t, jm, "frame-v2")
	s := &Session{id: jm.sessionID, jobManager: jm, subagents: newSubagentManager(nil)}

	current := &watchSendToken{Key: key, UpdateSeq: 2, DeliveryID: deliveryID}
	staleSeq := &watchSendToken{Key: key, UpdateSeq: 1, DeliveryID: deliveryID}
	clearedKey := key
	clearedKey.ResolvedWatchedIdentity = "no_such_target"
	staleCleared := &watchSendToken{Key: clearedKey, UpdateSeq: 2, DeliveryID: deliveryID}

	_, _, state, ok := s.resolveWatchSendToken(current)
	if !ok {
		t.Fatal("current token must resolve ok")
	}
	if state.Frame != "frame-v2" {
		t.Fatalf("current token frame = %q, want %q", state.Frame, "frame-v2")
	}

	if _, _, _, ok := s.resolveWatchSendToken(staleSeq); ok {
		t.Fatal("stale updateSeq token must resolve ok=false")
	}
	if _, _, _, ok := s.resolveWatchSendToken(staleCleared); ok {
		t.Fatal("token for a cleared key must resolve ok=false")
	}
}

func TestWatchSendTokenSettleAfterPersist(t *testing.T) {
	jm := newTestJM(t)
	_, key, deliveryID := installCallerSendWatchWithCurrentFrame(t, jm, "frame-v2")
	s := &Session{id: jm.sessionID, jobManager: jm, subagents: newSubagentManager(nil)}

	resolvedJM, cfg, state, ok := s.resolveWatchSendToken(&watchSendToken{Key: key, UpdateSeq: 2, DeliveryID: deliveryID})
	if !ok {
		t.Fatal("current token must resolve ok")
	}
	if resolvedJM != jm {
		t.Fatal("token must resolve to the owning jobManager")
	}

	if err := resolvedJM.settleWatchSendDelivered(cfg, state); err != nil {
		t.Fatalf("settleWatchSendDelivered: %v", err)
	}

	var delivered bool
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend != nil && event.WatchSend.Key == key {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("durable log must gain watch_send_delivered for the settled key")
	}
	if pending := runtimeWatchSendPending(t, jm); len(pending) != 0 {
		t.Fatalf("pending must be empty after settle, got %+v", pending)
	}
	if jm.hasPendingWatchSends() {
		t.Fatal("hasPendingWatchSends must be false after settle")
	}
}

func TestJobManagerWakeAndHasPendingWatchSends(t *testing.T) {
	jm := newTestJM(t)
	woke := 0
	jm.wake = func() { woke++ }

	if jm.hasPendingWatchSends() {
		t.Fatal("fresh manager must have no pending watch sends")
	}
	jm.kick()
	if woke != 1 {
		t.Fatalf("kick must call wake once, got %d", woke)
	}
	jm.wake = nil
	jm.kick() // must not panic with nil wake (test/restore managers pass nil)

	cfg := installCallerSendWatchWithPending(t, jm)
	_ = cfg
	if !jm.hasPendingWatchSends() {
		t.Fatal("pending entry must be visible to hasPendingWatchSends")
	}
}

// TestObservationRecordsIntentOnly is the spec §3 invariant: observation paths
// persist fired sends as pending, enqueue a wake token for caller-targeted
// sends, and kick the owner — but never deliver. A caller send and a delegate
// send both fire on assistant.message; afterward both must be pending, the
// caller send must have produced exactly one wake token, the owner must have
// been woken, and no delivery (no watch_send_delivered event, no jm.send call)
// must have occurred.
func TestObservationRecordsIntentOnly(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm) // running delegate "job_obs"

	woke := 0
	jm.wake = func() { woke++ }
	// Mirror production's enqueueJobNotificationAndNotify: enqueuing a
	// notification also wakes the owner. recordWatchSendsAndKick relies on the
	// caller token's enqueue waking (and falls back to kick() when nothing was
	// enqueued), so the capture must reproduce that wake to test the invariant.
	var enqueued []jobNotification
	jm.enqueue = func(n jobNotification) {
		enqueued = append(enqueued, n)
		jm.kick()
	}

	// The caller->caller watch is the feedback-loop shape configureWatch now
	// rejects; install it below validation so this test can still assert that
	// observation records intent for a caller send and a delegate send together.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.message"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "to-caller"},
	})
	if _, err := jm.configureWatch(watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.message"},
		Send:   &watchSendArgs{To: "job_obs", Message: "to-delegate"},
	}); err != nil {
		t.Fatalf("configure delegate-send watch: %v", err)
	}

	jm.onSessionEvent(events.EventAssistantTextEnd, nil)

	// Both sends are pending in jm state.
	if !jm.hasPendingWatchSends() {
		t.Fatal("observation must leave both sends pending")
	}
	pending := runtimeWatchSendPending(t, jm)
	if len(pending) != 2 {
		t.Fatalf("want 2 pending sends (caller + delegate), got %d: %+v", len(pending), pending)
	}

	// The caller send produced exactly one wake token.
	var tokens []jobNotification
	for _, n := range enqueued {
		if n.WatchSend != nil {
			tokens = append(tokens, n)
		}
	}
	if len(tokens) != 1 {
		t.Fatalf("caller send must enqueue exactly one wake token, got %d: %+v", len(tokens), enqueued)
	}
	if tokens[0].WatchSend.Key.ResolvedSendTo != runtimeMessageAliasCaller {
		t.Fatalf("wake token send-to = %q, want caller", tokens[0].WatchSend.Key.ResolvedSendTo)
	}

	// The owner was woken.
	if woke == 0 {
		t.Fatal("observation must wake the owner")
	}

	// No delivery happened: no watch_send_delivered event. (The jobManager no
	// longer has a send field at all — non-delivery is structural; the durable
	// log is the observable proof.)
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchSendDelivered {
			t.Fatalf("observation must not deliver: found watch_send_delivered event %+v", event)
		}
	}
}

// TestDrainDeliversDelegateTargetedSends proves the loop-owned drain is the
// executor of delegate-targeted watch sends. A running delegate is resumed,
// feedJobOutput records (but does not deliver) a pending send to it, and
// s.drainPendingWatchSends appends the frame to the child's steering queue and
// settles the pending with a watch_send_delivered event.
func TestDrainDeliversDelegateTargetedSends(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first delegate = %+v, want completed", first)
	}
	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "resume and block",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed running delegate", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	source, _ := sess.jobManager.createShell(createShellOpts{Command: "x"})
	if _, err := sess.jobManager.configureWatch(watchArgs{
		Target:      source.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: first.JobID, Message: "observe original target"},
	}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}

	// Observation records the send as pending without delivering it (spec §3).
	feedJob(sess.jobManager, source.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending after observation = %+v, want one recorded send", pending)
	}

	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	if queue := sub.sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue before drain = %+v, want empty (observation must not deliver)", queue)
	}

	// The loop-owned drain is the only executor of delegate-targeted delivery.
	if err := sess.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drainPendingWatchSends: %v", err)
	}

	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 {
		t.Fatalf("resumed delegate steering queue after drain = %+v, want one watch send", queue)
	}
	if !strings.Contains(queue[0].Text, "observe original target") || !strings.Contains(queue[0].Text, "output_match: server ready") {
		t.Fatalf("steering message = %q, want watch message and frame", queue[0].Text)
	}
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after drain = %+v, want settled", pending)
	}
	sawDelivered := false
	for _, event := range loadJobStoreEvents(t, sess.jobManager) {
		if event.Kind == jobstore.EventWatchSendDelivered {
			sawDelivered = true
		}
	}
	if !sawDelivered {
		t.Fatal("drain must append watch_send_delivered after delivery")
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)
}

// TestDrainResumesTerminalResumableTarget proves spec §4.2's explicit behavior
// change: every drain delivers to a resumable terminal delegate, resuming it.
// A foreground delegate completes (terminal + resumable + retained), a pending
// send targets it, and the drain resumes the child — observed via the adapter's
// second run hook firing.
func TestDrainResumesTerminalResumableTarget(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first delegate = %+v, want terminal completed", first)
	}

	source, _ := sess.jobManager.createShell(createShellOpts{Command: "x"})
	if _, err := sess.jobManager.configureWatch(watchArgs{
		Target:      source.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: first.JobID, Message: "resume the terminal delegate"},
	}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}

	// Observation records the send as pending; the target is terminal, so the
	// resume only happens when the loop-owned drain delivers.
	feedJob(sess.jobManager, source.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending after observation = %+v, want one recorded send", pending)
	}
	select {
	case <-adapter.secondStarted:
		t.Fatal("observation must not resume the terminal delegate")
	case <-time.After(100 * time.Millisecond):
	}

	if err := sess.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drainPendingWatchSends: %v", err)
	}

	// The drain resumed the terminal delegate (spec §4.2 explicit behavior change).
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not resume the terminal resumable delegate")
	}

	var resumedJob string
	for _, rec := range sess.jobManager.list(listFilter{Type: jobstore.JobDelegate}) {
		if rec.JobID != first.JobID && rec.TranscriptRef == first.TranscriptRef {
			resumedJob = rec.JobID
		}
	}
	if resumedJob == "" {
		t.Fatal("drain must append a resumed delegate job for the terminal target")
	}

	_, _ = sess.jobManager.stop(resumedJob)
	waitForShellDone(t, sess.jobManager, resumedJob)
}

// TestDrainEnqueuesTokensForChildCallerPendings proves the root drain iterates
// child jobManagers: a child holds a caller-targeted pending (restored shape),
// and the parent's drain re-tokens it onto the PARENT's notification rail with
// ChildSessionID set — nothing rides the parent steering queue.
func TestDrainEnqueuesTokensForChildCallerPendings(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	var enqueued []jobNotification
	parentJM.enqueue = func(n jobNotification) { enqueued = append(enqueued, n) }

	now := time.Unix(4000, 0).UTC()
	for _, event := range restoredWatchSendPendingEvents("CHILD", "job_child_watched", runtimeMessageAliasCaller, now) {
		if err := childJM.appendEvent(event); err != nil {
			t.Fatalf("append child pending: %v", err)
		}
	}
	if err := childJM.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore child pending: %v", err)
	}

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil),
	}
	child := &Session{id: "CHILD", jobManager: childJM}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   child,
		status: SubagentRunning,
	})

	if err := parent.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drainPendingWatchSends: %v", err)
	}

	var tokens []jobNotification
	for _, n := range enqueued {
		if n.WatchSend != nil {
			tokens = append(tokens, n)
		}
	}
	if len(tokens) != 1 {
		t.Fatalf("parent rail tokens = %d, want exactly one child caller token: %+v", len(tokens), enqueued)
	}
	if tokens[0].WatchSend.ChildSessionID != "CHILD" {
		t.Fatalf("token ChildSessionID = %q, want CHILD", tokens[0].WatchSend.ChildSessionID)
	}
	if tokens[0].WatchSend.Key.ResolvedSendTo != runtimeMessageAliasCaller {
		t.Fatalf("token send-to = %q, want caller", tokens[0].WatchSend.Key.ResolvedSendTo)
	}
	if queue := parent.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("parent steering queue = %+v, want empty (caller sends ride the notification rail)", queue)
	}
}

func TestWatchDeliveryCounterIncrementsPerNotification(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: "caller"}]
	if cfg == nil {
		t.Fatal("no-send caller watch not installed")
	}

	for i := 1; i <= 3; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
		jm.mu.Lock()
		got := cfg.deliveries
		jm.mu.Unlock()
		if got != i {
			t.Fatalf("after %d fires, deliveries = %d, want %d", i, got, i)
		}
	}
}

func TestWatchDeliveryCounterCountsSidecarSend(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	seedCommonWatchSendTargets(t, jm)
	var sent []sendMessageArgs
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}]
	if cfg == nil {
		t.Fatal("sidecar send watch not installed")
	}

	// Observation only records pending intent; no delivery, no count yet.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	jm.mu.Lock()
	beforeDrain := cfg.deliveries
	jm.mu.Unlock()
	if beforeDrain != 0 {
		t.Fatalf("observation must not count a delivery; deliveries = %d", beforeDrain)
	}

	// The loop-owned drain delivers and settles; the settle is the model-facing
	// delivery completion that counts.
	drainWatchSendsVia(t, jm, send)
	if len(sent) != 1 {
		t.Fatalf("drain must deliver once, got %d", len(sent))
	}
	jm.mu.Lock()
	afterDrain := cfg.deliveries
	jm.mu.Unlock()
	if afterDrain != 1 {
		t.Fatalf("a settled sidecar send must count one delivery, deliveries = %d", afterDrain)
	}
}

func TestWatchDeliveryCounterCountsCallerSettle(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	cfg, key, deliveryID := installCallerSendWatchWithCurrentFrame(t, jm, "frame-v2")
	s := &Session{id: jm.sessionID, jobManager: jm, subagents: newSubagentManager(nil)}

	resolvedJM, resolvedCfg, state, ok := s.resolveWatchSendToken(&watchSendToken{
		Key:        key,
		UpdateSeq:  2,
		DeliveryID: deliveryID,
	})
	if !ok {
		t.Fatal("current caller token must resolve ok")
	}

	if err := resolvedJM.settleWatchSendDelivered(resolvedCfg, state); err != nil {
		t.Fatalf("settleWatchSendDelivered: %v", err)
	}
	jm.mu.Lock()
	got := cfg.deliveries
	jm.mu.Unlock()
	if got != 1 {
		t.Fatalf("a settled caller frame must count one delivery, deliveries = %d", got)
	}
}

func TestWatchDeliveryBudgetAutoClearsWithOneFinalNotification(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "caller"}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("no-send caller watch not installed")
	}

	for i := 0; i < watchDeliveryBudget; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	}

	if jm.watches[key] != nil {
		t.Fatalf("watch must be auto-cleared at the delivery budget; still present")
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watchCount = %d, want 0 after auto-clear", jm.watchCount())
	}

	wantMsg := "watch cleared: caller delivered 50 times; re-arm with a tighter condition (higher every, narrower output_match, or longer progress_interval_ms)"
	var cleared []jobNotification
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch cleared:") {
			cleared = append(cleared, n)
		}
	}
	if len(cleared) != 1 {
		t.Fatalf("want exactly one cleared notification, got %d: %+v", len(cleared), cleared)
	}
	if cleared[0].Reason != wantMsg {
		t.Fatalf("cleared reason = %q, want %q", cleared[0].Reason, wantMsg)
	}
	block := formatJobNotificationBlock(cleared[0], "")
	if !strings.Contains(block, wantMsg) {
		t.Fatalf("rendered block must contain the full cleared message; got:\n%s", block)
	}

	// 50 regular notifications + exactly one cleared notification.
	if len(notified) != watchDeliveryBudget+1 {
		t.Fatalf("total notifications = %d, want %d", len(notified), watchDeliveryBudget+1)
	}

	// No further deliveries after clear: firing again produces nothing.
	before := len(notified)
	jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	if len(notified) != before {
		t.Fatalf("a cleared watch must not fire again; notifications grew from %d to %d", before, len(notified))
	}
}

func TestWatchDeliveryBudgetDoesNotDoubleClear(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "caller"}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("no-send caller watch not installed")
	}

	for i := 0; i < watchDeliveryBudget; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	}

	// Simulate an already-in-flight settle that crossed the budget again on a
	// cfg that is already detached: the auto-clear must be a no-op (no second
	// cleared notification).
	jm.mu.Lock()
	cfg.deliveries++ // 51: past the cap, never re-crosses
	jm.mu.Unlock()
	jm.autoClearWatchOverBudget(cfg)

	var cleared int
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch cleared:") {
			cleared++
		}
	}
	if cleared != 1 {
		t.Fatalf("auto-clear on an already-detached cfg must not re-notify; cleared count = %d", cleared)
	}
}

var _ = jobstore.JobShell
