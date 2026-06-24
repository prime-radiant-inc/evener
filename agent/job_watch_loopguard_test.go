package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provenance"
)

// TestValidateWatchDeliveryLoop covers the feedback-loop guard: configureWatch
// must reject any watch whose resolved event kinds include a self-generated kind
// (assistant.tool/communicate, including via "*") AND whose
// delivery returns to the generating session (send omitted or send.to=caller).
// The guard is target-independent: onSessionEvent matches kinds across all
// watches regardless of cfg.target, so a job-target watch with send.to=caller
// loops just as a caller-target one does.
func TestValidateWatchDeliveryLoop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		build   func(t *testing.T, jm *jobManager) watchArgs
		wantErr bool
	}{
		{
			name: "notify_self_assistant_tool",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.tool"}}
			},
			wantErr: true,
		},
		{
			name: "send_to_self_incident_shape",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.tool"}, Send: &watchSendArgs{To: "caller"}}
			},
			wantErr: true,
		},
		{
			name: "every_derived_single_kind_to_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.tool"}, Every: 2, Send: &watchSendArgs{To: "caller"}}
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
				return watchArgs{Target: rec.JobID, Events: []string{"assistant.tool"}, Send: &watchSendArgs{To: "caller"}}
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
				return watchArgs{Target: "caller", Events: []string{"communicate"}, Send: &watchSendArgs{To: "dlg_obs"}}
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

func TestJobWatchCreateSelfSourceFormatsSource(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	res, err := jobWatchTool(sess, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"job.notification"},
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool returned error: %v", err)
	}
	state := res.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != "self" {
		t.Fatalf("Source = %q, want self", state.Source)
	}
}

func TestJobWatchSelfSourceKeepsLoopGuard(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	_, err := jobWatchTool(sess, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"assistant.tool"},
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("jobWatchTool succeeded, want loop guard error")
	}
	if !strings.Contains(err.Error(), "feedback loop") {
		t.Fatalf("error = %v, want feedback loop", err)
	}
}

// TestEventWatchIgnoresWatchOriginatedSubagentEvents covers per-watch causal
// suppression on the job.notification rail: an event whose provenance already
// carries THIS watch's (watch_id, generation) key is dropped before any pending
// send is recorded (it is the watch's own downstream echo), while an event
// without that key records the send normally.
func TestEventWatchIgnoresWatchOriginatedSubagentEvents(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)

	ownEcho := events.SessionEvent{
		Kind:       events.EventJobFinished,
		SessionID:  jm.sessionID,
		Data:       events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed"},
		Provenance: provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_echo", jm.sessionID, "caller"),
	}
	jm.onSessionEvent(ownEcho)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("watch-originated job event retriggered watch send: %+v", pending)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_worker", JobType: "delegate", Status: "completed"})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("ordinary job event must record one pending watch send, got %d", len(pending))
	}
}

func TestNoSendWatchNotificationCarriesProvenanceForEchoSuppression(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	if _, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)

	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: jm.sessionID,
		Data:      events.JobFinishedData{JobID: "job_first", JobType: "delegate", Status: "completed"},
	})
	if len(notified) != 1 {
		t.Fatalf("first watch notification count = %d, want 1", len(notified))
	}
	if !provenance.ContainsWatch(notified[0].Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("notification provenance = %+v, want %s/%s", notified[0].Provenance, cfg.watchID, cfg.generation)
	}

	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventJobFinished,
		SessionID:  jm.sessionID,
		Data:       events.JobFinishedData{JobID: "job_echo", JobType: "delegate", Status: "completed"},
		Provenance: notified[0].Provenance,
	})
	if len(notified) != 1 {
		t.Fatalf("same-watch echo retriggered notification: %+v", notified)
	}
	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want only the first fire counted", cfg.deliveries)
	}
}

// TestWatchOriginSuppressesDelegateLifecycleWatchSends covers the same per-watch
// causal suppression for the delegate-lifecycle (job.notification on "*") rail:
// JobStarted/JobFinished events stamped with the watch's own provenance key are
// dropped before any pending send is recorded.
func TestWatchOriginSuppressesDelegateLifecycleWatchSends(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)
	own := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_obs", jm.sessionID, "*")

	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventJobStarted,
		SessionID:  jm.sessionID,
		Data:       events.JobStartedData{JobID: "job_obs", JobType: "delegate", Status: "running"},
		Provenance: own,
	})
	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventJobFinished,
		SessionID:  jm.sessionID,
		Data:       events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed"},
		Provenance: own,
	})

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("watch-originated delegate lifecycle events recorded watch sends: %+v", pending)
	}
}

func TestOutputMatchSuppressesSameWatchProvenance(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)
	p := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, rec.JobID)

	chunk := []byte("server READY\n")
	jm.feedJobOutputWithProvenance(rec.JobID, chunk, int64(len(chunk)), p)

	if len(notified) != 0 {
		t.Fatalf("same-watch output_match must be suppressed; got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0 for suppressed output_match", cfg.deliveries)
	}
}

func TestOutputMatchAllowsDifferentWatchProvenance(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)
	p := provenance.WithWatch(nil, "watch_other", "wg_other", "wd_other", jm.sessionID, rec.JobID)

	chunk := []byte("server READY\n")
	jm.feedJobOutputWithProvenance(rec.JobID, chunk, int64(len(chunk)), p)

	if len(notified) != 1 {
		t.Fatalf("different-watch output_match must fire once; got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1 for different-watch output_match", cfg.deliveries)
	}
}

func TestOutputMatchSuppressesSameWatchProvenanceAcrossSplitLine(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)
	p := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, rec.JobID)

	jm.feedJobOutputWithProvenance(rec.JobID, []byte("re"), 2, p)
	jm.feedJobOutputWithProvenance(rec.JobID, []byte("ady\n"), 6, nil)

	if len(notified) != 0 {
		t.Fatalf("split same-watch output_match must be suppressed; got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0 for suppressed split output_match", cfg.deliveries)
	}
}

func TestOutputMatchSuppressesSameWatchProvenanceOnTerminalFlush(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)
	p := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, rec.JobID)

	jm.feedJobOutputWithProvenance(rec.JobID, []byte("re"), 2, p)
	jm.feedJobOutputWithProvenance(rec.JobID, []byte("ady"), 5, nil)
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("terminal flush split same-watch output_match persisted pending = %+v, want none", pending)
	}
}

func TestProgressTickSuppressesSameWatchProvenance(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now())
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	p := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, rec.JobID)
	jm.mu.Lock()
	jm.watches[key] = cfg
	jm.running[rec.JobID].rec.Provenance = p
	jm.mu.Unlock()

	if !jm.fireProgressTick(key, cfg) {
		t.Fatal("progress tick returned false for live watch")
	}
	if len(notified) != 0 {
		t.Fatalf("same-watch progress tick must be suppressed; got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0 for suppressed progress tick", cfg.deliveries)
	}
}

func TestWatchCreateMintsObserverReadGrant(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	grants := loadGrantTable(t, jm)
	if !grants["child_job_obs"][rec.JobID] {
		t.Fatalf("grants after create = %+v, want child_job_obs -> %s", grants, rec.JobID)
	}
	if got := countWatchReadGrantEvents(t, jm); got != 1 {
		t.Fatalf("grant events after create = %d, want 1", got)
	}

	// The create-minted pair seeds the per-fire dedup: a live fire for the
	// same (observer, watched) pair must not append a second grant.
	feedJob(jm, rec.JobID, []byte("server ready\n"))
	if got := countWatchReadGrantEvents(t, jm); got != 1 {
		t.Fatalf("grant events after fire = %d, want 1 (create mint seeds dedup)", got)
	}
}

func TestWatchCreateMintsNoGrantForSessionTargetNotifyOnlyOrCaller(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	shellA, err := jm.createShell(createShellOpts{Command: "a"})
	if err != nil {
		t.Fatalf("create shellA: %v", err)
	}
	shellB, err := jm.createShell(createShellOpts{Command: "b"})
	if err != nil {
		t.Fatalf("create shellB: %v", err)
	}

	// Session-target watch with a delegate send target: the concrete watched
	// job is unknown until a fire, so creation mints nothing.
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure session-target watch: %v", err)
	}
	// Notify-only watch: no observer, no grant.
	if _, err := jm.configureWatch(watchArgs{
		Target:      shellA.JobID,
		OutputMatch: "ready",
	}); err != nil {
		t.Fatalf("configure notify-only watch: %v", err)
	}
	// Caller delivery: the caller reads through its own tools, never a grant.
	if _, err := jm.configureWatch(watchArgs{
		Target:      shellB.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
	}); err != nil {
		t.Fatalf("configure caller watch: %v", err)
	}

	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after creates = %+v, want none", grants)
	}
}

func TestWatchWildcardSendFireMintsGrantForResolvedWatchedJob(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := countWatchReadGrantEvents(t, jm); got != 0 {
		t.Fatalf("grant events before fire = %d, want 0", got)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})

	grants := loadGrantTable(t, jm)
	if !grants["child_job_obs"]["job_trigger_one"] {
		t.Fatalf("grants after fire = %+v, want child_job_obs -> job_trigger_one", grants)
	}
}

func TestWatchWildcardSendRepeatFireMintsGrantOnce(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "failed"})

	if got := countWatchReadGrantEvents(t, jm); got != 1 {
		t.Fatalf("grant events after repeat fire = %d, want 1 (per-config dedup)", got)
	}
	grants := loadGrantTable(t, jm)
	if !grants["child_job_obs"]["job_trigger_one"] {
		t.Fatalf("grants after repeat fire = %+v, want child_job_obs -> job_trigger_one", grants)
	}

	// A different watched job is a new (observer, job) pair: it mints its own.
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_two", JobType: "delegate", Status: "completed"})
	if got := countWatchReadGrantEvents(t, jm); got != 2 {
		t.Fatalf("grant events after second pair = %d, want 2", got)
	}
}

func TestWatchWatchedAliasSendRejectsConcreteTarget(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	target := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      target.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: runtimeMessageAliasWatched, Message: "observe"},
	}); err == nil || !strings.Contains(err.Error(), "watched is not a v1 delivery target") {
		t.Fatalf("error = %v, want watched alias rejection", err)
	}

	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after rejected watched alias = %+v, want none", grants)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("rejected watched alias recorded pending: %+v", pending)
	}
}

func TestWatchWatchedAliasSendRejectsWildcardJobNotification(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: runtimeMessageAliasWatched, Message: "observe"},
	}); err == nil || !strings.Contains(err.Error(), "watched is not a v1 delivery target") {
		t.Fatalf("error = %v, want watched alias rejection", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("rejected watched alias recorded pending: %+v", pending)
	}
	if got := countWatchReadGrantEvents(t, jm); got != 0 {
		t.Fatalf("grant events after rejected watched alias = %d, want 0", got)
	}
	if len(notified) != 0 {
		t.Fatalf("rejected watched alias emitted diagnostics: %+v", notified)
	}
}

// TestTerminalCatchupSendMintsObserverReadGrant pins the claim in
// mintWatchSendReadGrant's doc comment: a terminal catch-up send never had a
// create mint (runTerminalCatchup returns from configureWatch's terminal
// intercept before mintWatchCreateReadGrant runs, and its detached config is
// fresh), so the per-fire mint is the ONLY source of the observer's read
// grant. The single catch-up fire mints exactly one grant keyed on the
// observer delegate's child session id.
func TestTerminalCatchupSendMintsObserverReadGrant(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	jobID := terminalShellWithOutput(t, jm, "server ready\n")

	res, err := jm.configureWatch(watchArgs{
		Target:      jobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configureWatch terminal catch-up send: %v", err)
	}
	if !res.Fired || !res.TerminalCatchup {
		t.Fatalf("result = %+v, want fired+terminal_catchup", res)
	}

	grants := loadGrantTable(t, jm)
	if !grants["child_job_obs"][jobID] {
		t.Fatalf("grants after catch-up = %+v, want child_job_obs -> %s", grants, jobID)
	}
	if got := countWatchReadGrantEvents(t, jm); got != 1 {
		t.Fatalf("grant events after catch-up = %d, want 1 (single fire mints once)", got)
	}
}

func TestWatchCreateGrantAppendFailureFailsCreationLoudly(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	realAppend := jm.appendEvent
	grantErr := errors.New("grant append failed")
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchReadGrant {
			return grantErr
		}
		return realAppend(e)
	}

	_, err = jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	})
	if err == nil {
		t.Fatal("configureWatch succeeded, want grant append failure to fail creation")
	}
	if !strings.Contains(err.Error(), grantErr.Error()) {
		t.Fatalf("creation error = %v, want wrapped %v", err, grantErr)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch installed despite grant failure: count = %d", jm.watchCount())
	}
	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after failed create = %+v, want none", grants)
	}
}

func TestWatchPerFireGrantAppendFailureProceedsWithSend(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	realAppend := jm.appendEvent
	grantErr := errors.New("grant append failed")
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchReadGrant {
			return grantErr
		}
		return realAppend(e)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after grant failure = %d, want 1 (delivery > grant)", len(pending))
	}
	var grantDiagnostics int
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch read grant failed") {
			grantDiagnostics++
			if !strings.Contains(n.Reason, grantErr.Error()) {
				t.Fatalf("diagnostic reason = %q, want underlying append error", n.Reason)
			}
		}
	}
	if grantDiagnostics != 1 {
		t.Fatalf("grant diagnostics = %d, want 1: %+v", grantDiagnostics, notified)
	}
	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after failed fire mint = %+v, want none", grants)
	}

	// A failed mint must not poison the dedup set: once the store recovers,
	// the next fire for the same pair mints the grant.
	jm.appendEvent = realAppend
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "failed"})
	if !loadGrantTable(t, jm)["child_job_obs"]["job_trigger_one"] {
		t.Fatal("grant not re-minted after append recovered")
	}
}

// TestGrantedReadServesWatchedJobCrossStore is the spec §5.1 consumption
// direction: the observer delegate's job_read_output cannot resolve the
// watched job locally (it lives in the parent's store), so the read resolves
// through the parent-injected grant lookup — content, status, and grep all
// round-trip.
func TestGrantedReadServesWatchedJobCrossStore(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)

	out, err := observerReadOutput(t, fx.observer, map[string]any{"job_id": fx.watched, "tail_lines": 65536})
	if err != nil {
		t.Fatalf("granted read: %v", err)
	}
	if out.JobID != fx.watched || out.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("granted read = %+v, want completed record for %s", out, fx.watched)
	}
	if out.Reason == nil || *out.Reason != "exit_zero" {
		t.Fatalf("reason = %v, want exit_zero", out.Reason)
	}
	if out.Content != grantReadWatchedOutput {
		t.Fatalf("content = %q, want %q", out.Content, grantReadWatchedOutput)
	}
	if out.TotalBytes != int64(len(grantReadWatchedOutput)) {
		t.Fatalf("total_bytes = %d, want %d", out.TotalBytes, len(grantReadWatchedOutput))
	}
	if out.ExitCode == nil || *out.ExitCode != 0 {
		t.Fatalf("exit_code = %v, want 0", out.ExitCode)
	}

	grepped, err := observerReadOutput(t, fx.observer, map[string]any{"job_id": fx.watched, "grep": "ready"})
	if err != nil {
		t.Fatalf("granted grep read: %v", err)
	}
	if grepped.Grep != "ready" {
		t.Fatalf("grep echo = %q, want ready", grepped.Grep)
	}
	if len(grepped.Matches) != 1 || !strings.Contains(grepped.Matches[0].Line, "bravo ready") {
		t.Fatalf("grep matches = %+v, want one 'bravo ready' match", grepped.Matches)
	}
	if grepped.Matches[0].ByteOffset == nil {
		t.Fatalf("grep match byte_offset missing: %+v", grepped.Matches[0])
	}
}

// TestNonGrantedReadPreservesTargetNotFound pins the miss path on both axes:
// a granted observer reading an UNGRANTED parent job, and an ungranted child
// session reading the granted watched job. Both keep the original
// target_not_found error instead of leaking the parent store.
func TestNonGrantedReadPreservesTargetNotFound(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)
	ungranted, err := fx.parentJM.createShell(createShellOpts{Command: "other"})
	if err != nil {
		t.Fatalf("create ungranted job: %v", err)
	}

	if _, err := observerReadOutput(t, fx.observer, map[string]any{"job_id": ungranted.JobID}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ungranted job read error = %v, want original not-found", err)
	}

	strangerJM, err := newJobManager(t.TempDir(), "child_job_other", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new stranger jobManager: %v", err)
	}
	t.Cleanup(func() { _ = strangerJM.store.Close() })
	stranger := &Session{id: "child_job_other", jobManager: strangerJM}
	stranger.cfg.spawn.parentGrantedJobRead = fx.parent.lookupGrantedJobRead

	if _, err := observerReadOutput(t, stranger, map[string]any{"job_id": fx.watched}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("stranger read error = %v, want original not-found", err)
	}
}

// TestGrantSurvivesObserverResumeUnderNewJobID is the canonical fire ->
// resume -> read flow the session-id grant keying exists for: frame delivery
// to an idle observer resumes it under a NEW delegate job id, and the read
// must still resolve. The real resume path (resumeOrFindRunningDelegate ->
// attachDelegateJobFromWatch -> relinkDelegateChildToJob) needs a live
// subagent runtime, so this reproduces its store-visible effects: the old
// observer job goes terminal, a fresh job id starts for the SAME child
// session (same transcript ref), and the child is relinked to the new job.
func TestGrantSurvivesObserverResumeUnderNewJobID(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)
	ended := fx.parentJM.now().Add(time.Second)
	if err := fx.parentJM.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          ended,
		JobID:       "job_obs",
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &ended,
		TerminalGen: "term_job_obs",
	}); err != nil {
		t.Fatalf("finish observer job: %v", err)
	}
	if err := fx.parentJM.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               ended,
		JobID:            "job_obs_resumed",
		Type:             jobstore.JobDelegate,
		OwnerSessionID:   "PARENT",
		VisibleToSession: "PARENT",
		TranscriptRef:    encodeRef("", "child_job_obs"),
		StartedAt:        &ended,
	}); err != nil {
		t.Fatalf("start resumed observer job: %v", err)
	}
	relinkDelegateChildToJob(fx.observer, "job_obs_resumed")

	out, err := observerReadOutput(t, fx.observer, map[string]any{"job_id": fx.watched})
	if err != nil {
		t.Fatalf("granted read after resume: %v", err)
	}
	if out.JobID != fx.watched || out.Status != string(jobstore.StatusCompleted) || !strings.Contains(out.Content, "bravo ready") {
		t.Fatalf("read after resume = %+v, want watched job content", out)
	}
}

// TestGrantSurvivesWatchClearAndStoreReopen pins the durable half of spec
// §5.1: the grant is an append-only capability — clearing the watch does not
// revoke it, and a parent store reopen refolds it from jobs.jsonl. The
// re-wired seam mirrors restart, where delegate restore re-injects the lookup
// from the rebuilt parent session.
func TestGrantSurvivesWatchClearAndStoreReopen(t *testing.T) {
	t.Parallel()
	parentStateDir := t.TempDir()
	parentJM, err := newJobManager(parentStateDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	observerJM, err := newJobManager(t.TempDir(), "child_job_obs", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new observer jobManager: %v", err)
	}
	t.Cleanup(func() { _ = observerJM.store.Close() })

	seedCommonWatchSendTargets(t, parentJM)
	watched, err := parentJM.createShell(createShellOpts{Command: "server"})
	if err != nil {
		t.Fatalf("create watched job: %v", err)
	}
	if _, err := parentJM.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure sidecar watch: %v", err)
	}
	if _, err := parentJM.configureWatch(watchArgs{
		Target: watched.JobID,
		Send:   &watchSendArgs{To: "dlg_obs"},
		Clear:  true,
	}); err != nil {
		t.Fatalf("clear sidecar watch: %v", err)
	}
	if _, err := parentJM.appendJobOutput(watched.JobID, parentJM.running[watched.JobID].output, []byte(grantReadWatchedOutput)); err != nil {
		t.Fatalf("append watched output: %v", err)
	}
	code := 0
	if err := parentJM.finalize(watched.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize watched job: %v", err)
	}
	if err := parentJM.store.Close(); err != nil {
		t.Fatalf("close parent store: %v", err)
	}

	reopenedJM, err := newJobManager(parentStateDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = reopenedJM.store.Close() })
	grants, err := reopenedJM.store.LoadGrants()
	if err != nil {
		t.Fatalf("load grants after reopen: %v", err)
	}
	if !grants["child_job_obs"][watched.JobID] {
		t.Fatalf("grants after clear+reopen = %+v, want child_job_obs -> %s", grants, watched.JobID)
	}

	reopenedParent := &Session{id: "PARENT", jobManager: reopenedJM, subagents: newSubagentManager(nil)}
	observer := &Session{id: "child_job_obs", jobManager: observerJM}
	observer.cfg.spawn.parentGrantedJobRead = reopenedParent.lookupGrantedJobRead

	out, err := observerReadOutput(t, observer, map[string]any{"job_id": watched.JobID})
	if err != nil {
		t.Fatalf("granted read after clear+reopen: %v", err)
	}
	if out.Status != string(jobstore.StatusCompleted) || out.Content != grantReadWatchedOutput {
		t.Fatalf("read after clear+reopen = %+v, want full watched output", out)
	}
}

// TestGrantedReadAfterParentClosedPreservesTargetNotFound: a closed parent
// store makes the grant lookup unanswerable; the observer's read degrades to
// its original target_not_found instead of surfacing a parent-side failure or
// panicking.
func TestGrantedReadAfterParentClosedPreservesTargetNotFound(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)
	if err := fx.parentJM.close(); err != nil {
		t.Fatalf("close parent job manager: %v", err)
	}

	_, err := observerReadOutput(t, fx.observer, map[string]any{"job_id": fx.watched})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("read after parent close error = %v, want original not-found", err)
	}
}

// TestGrantedReadRejectsBlock pins the decision that granted cross-session
// reads are snapshot-only: max_wait_ms>0 fails loudly instead of silently
// degrading to a snapshot.
func TestGrantedReadRejectsBlock(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)

	for name, args := range map[string]map[string]any{
		"wait":      {"job_id": fx.watched, "max_wait_ms": 1000},
		"wait+grep": {"job_id": fx.watched, "max_wait_ms": 1000, "grep": "ready"},
	} {
		_, err := observerReadOutput(t, fx.observer, args)
		if err == nil || err.Error() != grantedReadBlockUnsupportedErr {
			t.Fatalf("%s error = %v, want %q", name, err, grantedReadBlockUnsupportedErr)
		}
	}
}

func TestJobWatchAllowsDirectChildConcreteJobSourceAndManagesIt(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, "ROOT")
	childJM := newWalkJobManager(t, "CHILD")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = rootJM.forwardEvent
	childJM.parentJobID = "job_root_delegate_child"

	childRec, err := childJM.createShell(createShellOpts{Command: "sleep 30", Description: "child job"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, childJM, childRec.JobID) })

	child := &Session{id: "CHILD", jobManager: childJM, subagents: newSubagentManager(nil)}
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "CHILD", sess: child, status: SubagentRunning})

	out, err := jobWatchTool(root, map[string]any{
		"operation":    "create",
		"source":       childRec.JobID,
		"output_match": "READY",
	}, 20000)
	if err != nil {
		t.Fatalf("jobWatchTool create: %v", err)
	}
	state := out.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != childRec.JobID || !state.Watching || state.Send != nil {
		t.Fatalf("watch state = %+v, want direct child source watching without public send", state)
	}

	listOut, err := jobWatchTool(root, map[string]any{"operation": "list"}, 20000)
	if err != nil {
		t.Fatalf("jobWatchTool list: %v", err)
	}
	list := listOut.(tooldefs.StateResult).State.(jobWatchListToolResult)
	if list.Count != 1 || len(list.Watches) != 1 {
		t.Fatalf("watch list = %+v, want one forwarded watch", list)
	}
	if list.Watches[0].WatchID != state.WatchID || list.Watches[0].Source != childRec.JobID || !list.Watches[0].Watching {
		t.Fatalf("watch list row = %+v, want forwarded watch %s on %s", list.Watches[0], state.WatchID, childRec.JobID)
	}

	inspectOut, err := jobWatchTool(root, map[string]any{"operation": "inspect", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("jobWatchTool inspect: %v", err)
	}
	inspect := inspectOut.(tooldefs.StateResult).State.(jobWatchInspectToolResult)
	if inspect.WatchID != state.WatchID || inspect.Source != childRec.JobID || !inspect.Watching {
		t.Fatalf("watch inspect = %+v, want forwarded watch", inspect)
	}

	childListOut, err := jobWatchTool(child, map[string]any{"operation": "list"}, 20000)
	if err != nil {
		t.Fatalf("child jobWatchTool list: %v", err)
	}
	childList := childListOut.(tooldefs.StateResult).State.(jobWatchListToolResult)
	if childList.Count != 0 || len(childList.Watches) != 0 || len(childList.RecentWatches) != 0 {
		t.Fatalf("child owner watch list = %+v, want no ancestor-owned watch", childList)
	}
	childJobListOut, err := jobListTool(child, map[string]any{}, 20000)
	if err != nil {
		t.Fatalf("child jobListTool: %v", err)
	}
	childJobList := childJobListOut.(tooldefs.StateResult).State.(jobListResult)
	if len(childJobList.Watches) != 0 || len(childJobList.RecentWatches) != 0 {
		t.Fatalf("child owner job_list watches = %+v recent=%+v, want no ancestor-owned watch", childJobList.Watches, childJobList.RecentWatches)
	}
	childInspectOut, err := jobWatchTool(child, map[string]any{"operation": "inspect", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("child jobWatchTool inspect: %v", err)
	}
	childInspect := childInspectOut.(tooldefs.StateResult).State.(jobWatchInspectToolResult)
	if childInspect.Watching || childInspect.Source != "" {
		t.Fatalf("child owner inspect = %+v, want not found", childInspect)
	}
	if _, err := jobWatchTool(child, map[string]any{"operation": "clear", "watch_id": state.WatchID}, 20000); err != nil {
		t.Fatalf("child jobWatchTool clear: %v", err)
	}
	if childJM.watchCount() != 1 {
		t.Fatalf("child watch count after owner clear = %d, want ancestor-owned watch intact", childJM.watchCount())
	}
	inspectOut, err = jobWatchTool(root, map[string]any{"operation": "inspect", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("root inspect after child clear: %v", err)
	}
	inspect = inspectOut.(tooldefs.StateResult).State.(jobWatchInspectToolResult)
	if inspect.WatchID != state.WatchID || inspect.Source != childRec.JobID || !inspect.Watching {
		t.Fatalf("root inspect after child clear = %+v, want forwarded watch still active", inspect)
	}

	feedJob(childJM, childRec.JobID, []byte("server READY\n"))
	if got := root.drainJobNotifications(); len(got) != 1 || got[0].JobID != childRec.JobID {
		t.Fatalf("root notifications after match = %+v, want one child job watch notification", got)
	}

	clearOut, err := jobWatchTool(root, map[string]any{"operation": "clear", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("jobWatchTool clear: %v", err)
	}
	clearState := clearOut.(tooldefs.StateResult).State.(jobWatchToolResult)
	if clearState.Watching || clearState.Source != childRec.JobID {
		t.Fatalf("clear state = %+v, want cleared forwarded watch", clearState)
	}
	if childJM.watchCount() != 0 {
		t.Fatalf("child watch count = %d, want cleared", childJM.watchCount())
	}
	recentOut, err := jobWatchTool(root, map[string]any{"operation": "inspect", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("root inspect recent: %v", err)
	}
	recent := recentOut.(tooldefs.StateResult).State.(jobWatchInspectToolResult)
	if recent.Watching || recent.Source != childRec.JobID || recent.EndReason != "cleared" {
		t.Fatalf("root recent inspect = %+v, want receiver-owned cleared history", recent)
	}
	childListOut, err = jobWatchTool(child, map[string]any{"operation": "list"}, 20000)
	if err != nil {
		t.Fatalf("child list after clear: %v", err)
	}
	childList = childListOut.(tooldefs.StateResult).State.(jobWatchListToolResult)
	if childList.Count != 0 || len(childList.Watches) != 0 || len(childList.RecentWatches) != 0 {
		t.Fatalf("child owner watch list after clear = %+v, want no receiver-owned history", childList)
	}
	childJobListOut, err = jobListTool(child, map[string]any{}, 20000)
	if err != nil {
		t.Fatalf("child jobListTool after clear: %v", err)
	}
	childJobList = childJobListOut.(tooldefs.StateResult).State.(jobListResult)
	if len(childJobList.Watches) != 0 || len(childJobList.RecentWatches) != 0 {
		t.Fatalf("child owner job_list watches after clear = %+v recent=%+v, want no receiver-owned history", childJobList.Watches, childJobList.RecentWatches)
	}
	childInspectOut, err = jobWatchTool(child, map[string]any{"operation": "inspect", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("child inspect after clear: %v", err)
	}
	childInspect = childInspectOut.(tooldefs.StateResult).State.(jobWatchInspectToolResult)
	if childInspect.Watching || childInspect.Source != "" || childInspect.EndReason != "" {
		t.Fatalf("child owner inspect after clear = %+v, want not found", childInspect)
	}

	feedJob(childJM, childRec.JobID, []byte("server READY again\n"))
	if got := root.drainJobNotifications(); len(got) != 0 {
		t.Fatalf("root notifications after clear = %+v, want none", got)
	}
}

func TestJobWatchAllowsDescendantConcreteJobSource(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"

	workerRec, err := workerJM.createShell(createShellOpts{Command: "sleep 30", Description: "worker job"})
	if err != nil {
		t.Fatalf("create worker shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, workerJM, workerRec.JobID) })

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	out, err := jobWatchTool(root, map[string]any{
		"operation":    "create",
		"source":       workerRec.JobID,
		"output_match": "READY",
	}, 20000)
	if err != nil {
		t.Fatalf("jobWatchTool: %v", err)
	}
	state := out.(tooldefs.StateResult).State.(jobWatchToolResult)
	if state.Source != workerRec.JobID || !state.Watching {
		t.Fatalf("watch state = %+v, want descendant concrete source watching", state)
	}
	if state.Send != nil {
		t.Fatalf("watch state exposed send routing: %+v", state.Send)
	}
	cfg := onlyWatchConfigForTest(t, workerJM)
	if cfg.receiverSessionID != root.ID() || cfg.receiverDelegateID != "" {
		t.Fatalf("watch receiver = %q/%q, want ancestor session %q with no delegate", cfg.receiverSessionID, cfg.receiverDelegateID, root.ID())
	}

	feedJob(workerJM, workerRec.JobID, []byte("worker READY\n"))
	rootNotified := root.drainJobNotifications()
	if len(rootNotified) != 1 {
		t.Fatalf("root notifications = %+v, want one output-match notification", rootNotified)
	}
	if rootNotified[0].JobID != workerRec.JobID {
		t.Fatalf("notification job_id = %q, want %q", rootNotified[0].JobID, workerRec.JobID)
	}
	if !strings.Contains(rootNotified[0].Reason, "READY") {
		t.Fatalf("notification reason = %q, want output match", rootNotified[0].Reason)
	}
	if len(worker.drainJobNotifications()) != 0 {
		t.Fatal("descendant owner received the watcher notification; want ancestor watcher only")
	}

	// A genuinely unknown job_id keeps the bare target_not_found (the guidance is
	// precise — it does not over-broaden to every miss).
	_, err = jobWatchTool(root, map[string]any{
		"operation": "create",
		"source":    "job_does_not_exist_anywhere",
		"events":    []any{"job.notification"},
	}, 20000)
	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("unknown job_id error = %v, want bare target_not_found", err)
	}
}

func TestJobWatchSuppressesSameWatchProvenanceBeforeDeliveryAccounting(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_1")
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: "dlg_1", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, jm)

	ev := events.New(events.CommunicateData{Message: "PYTHON_QUOTE quote=Ni!", EndTurn: false})
	ev.SessionID = jm.sessionID
	ev.Provenance = provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, "caller")

	jm.onSessionEvent(ev)

	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0 for suppressed event", cfg.deliveries)
	}
	if len(cfg.pending) != 0 {
		t.Fatalf("pending sends = %d, want 0 for suppressed event", len(cfg.pending))
	}
}

func TestJobWatchDoesNotSuppressDifferentGeneration(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_1")
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: "dlg_1", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, jm)
	oldGeneration := cfg.generation
	cfg.generation = "wg_recreated"

	ev := events.New(events.CommunicateData{Message: "actually alpha marker", EndTurn: false})
	ev.SessionID = jm.sessionID
	ev.Provenance = provenance.WithWatch(nil, cfg.watchID, oldGeneration, "wd_old", jm.sessionID, "caller")

	jm.onSessionEvent(ev)

	if cfg.deliveries == 0 && len(cfg.pending) == 0 {
		t.Fatal("old generation provenance must not suppress new generation")
	}
}

func TestWatchSendFrameRendersTriggerProvenanceNotDeliveryProvenance(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_1")
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: "dlg_1", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, jm)

	ev := events.New(events.CommunicateData{Message: "actually alpha marker", EndTurn: false})
	ev.SessionID = jm.sessionID
	jm.onSessionEvent(ev)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending sends = %+v, want one", pending)
	}
	var state *jobstore.WatchSendState
	for _, pendingState := range pending {
		state = pendingState
	}
	if !strings.Contains(state.Frame, "provenance: external") {
		t.Fatalf("frame provenance =\n%s\nwant external trigger provenance", state.Frame)
	}
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("delivery provenance = %+v, want persisted watch key %s/%s", state.Provenance, cfg.watchID, cfg.generation)
	}
}
