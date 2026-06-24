package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestConfigureWatchRequiresCondition(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "caller"})
	if err == nil {
		t.Fatal("a watch with no condition and clear=false must error")
	}
}

func TestConfigureWatchRejectsNegativeProgressInterval(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "job_does_not_exist", OutputMatch: "ready"})
	if err == nil {
		t.Fatal("an unknown concrete job target must error (target_not_found)")
	}
}

func TestConfigureWatchRejectsForwardedNestedTarget(t *testing.T) {
	t.Parallel()
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

// TestTerminalCatchupRejectsForwardedNestedTarget guards the cross-session watch
// boundary for terminal catch-up: validateWatchTarget rejects a nested-owned job
// with target_not_watchable (the owning session must attach the watch), but it
// checks terminality BEFORE ownership, so a terminal nested-owned job surfaces as
// target_terminal. terminalWatchTargetStatus must mirror the ownership rejection
// so catch-up does NOT scan or fire on a job the caller is forbidden to watch.
func TestTerminalCatchupRejectsForwardedNestedTarget(t *testing.T) {
	t.Parallel()
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
	endedAt := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:    jobstore.EventJobFinished,
		TS:      endedAt,
		JobID:   "job_nested",
		Status:  jobstore.StatusCompleted,
		Reason:  "exit_zero",
		EndedAt: &endedAt,
	}); err != nil {
		t.Fatalf("append nested finish: %v", err)
	}

	// The ownership rejection must hold even though the record is terminal: a
	// nested-owned target is not catch-up-eligible.
	if _, terminal, err := jm.terminalWatchTargetStatus("job_nested"); err != nil || terminal {
		t.Fatalf("terminalWatchTargetStatus(nested-owned terminal) = terminal:%v err:%v, want terminal:false", terminal, err)
	}

	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	res, err := jm.configureWatch(watchArgs{Target: "job_nested", OutputMatch: "ready"})
	if err == nil {
		t.Fatalf("nested-owned terminal output_match must error, got result %+v", res)
	}
	if res.TerminalCatchup || res.Fired {
		t.Fatalf("nested-owned terminal target must not catch-up: %+v", res)
	}
	if len(notified) != 0 {
		t.Fatalf("nested-owned terminal target must enqueue nothing; got %+v", notified)
	}
}

func TestConfigureWatchSendToMissingDelegateFailsTargetNotFound(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_missing_delegate", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchSendToOtherSessionDelegateFailsNotControllable(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	watched, err := jm.createShell(createShellOpts{Command: "watched"})
	if err != nil {
		t.Fatalf("create watched job: %v", err)
	}
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_other",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "child_other",
			TranscriptRef:    encodeRef("", "child_other"),
			OwnerSessionID:   "OTHER",
			VisibleSessionID: jm.sessionID,
			Generation:       "dg_other",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("seed other delegate: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            "job_other_delegate",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_other",
		OwnerSessionID:   "OTHER",
		VisibleToSession: jm.sessionID,
		TranscriptRef:    encodeRef("", "child_other"),
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("seed other delegate job: %v", err)
	}

	_, err = jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_other", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "not_controllable") {
		t.Fatalf("error = %v, want not_controllable", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after failed create = %+v, want none", grants)
	}
}

func TestConfigureWatchRejectsUnknownEventKinds(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.mesage"}})

	if err == nil || !strings.Contains(err.Error(), "unknown event kind") {
		t.Fatalf("error = %v, want unknown event kind", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchRejectsAssistantMessageEvent(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}})

	if err == nil || !strings.Contains(err.Error(), "assistant.message is not watchable") {
		t.Fatalf("error = %v, want assistant.message guidance", err)
	}
	if !strings.Contains(err.Error(), "use communicate") {
		t.Fatalf("error = %v, want communicate guidance", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchAliasTargetWithoutContextFailsTargetNotFound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "main alias", target: "main"},
		{name: "watched without context", target: "watched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jm := newTestJM(t)

			_, err := jm.configureWatch(watchArgs{Target: tc.target})

			if err == nil || !strings.Contains(err.Error(), "target_not_found") {
				t.Fatalf("error = %v, want target_not_found", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", jm.watchCount())
			}
		})
	}
}

func TestConfigureWatchRejectsOutputMatchOnSessionTargets(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for _, target := range []string{"caller", "*"} {
		t.Run(target, func(t *testing.T) {
			_, err := jm.configureWatch(watchArgs{Target: target, OutputMatch: "ready"})
			if err == nil {
				t.Fatal("session target output_match watch must error")
			}
			if !strings.Contains(err.Error(), "output_match watches concrete job output") {
				t.Fatalf("error = %v, want output_match concrete-target validation", err)
			}
		})
	}
}

func TestConfigureWatchOutputMatchOnCallerCommunicateGivesRepairShape(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "caller", OutputMatch: "APPROVAL_REQUEST", Events: []string{"communicate"}})

	if err == nil {
		t.Fatal("caller communicate output_match watch must error")
	}
	if !strings.Contains(err.Error(), `source="parent"`) ||
		!strings.Contains(err.Error(), `events ["communicate"]`) ||
		!strings.Contains(err.Error(), `communicate(end_turn=true)`) {
		t.Fatalf("error = %v, want communicate repair shape", err)
	}
}

func TestJobWatchSendToMainAliasFailsTargetNotFound(t *testing.T) {
	t.Parallel()
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

func TestJobWatchSendToKnownShellJobFailsJobIDGuidance(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	watched, _ := jm.createShell(createShellOpts{Command: "watched"})
	observer, _ := jm.createShell(createShellOpts{Command: "observer"})

	_, err := jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: observer.JobID, Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "job_id is a job/turn handle") {
		t.Fatalf("error = %v, want job_id guidance", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchSendToWatchedFailsV1TargetValidation(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	watched, _ := jm.createShell(createShellOpts{Command: "watched"})

	_, err := jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "watched is not a v1 delivery target") {
		t.Fatalf("error = %v, want watched alias rejection", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchSendToNonResumableDelegateFailsTargetNotResumable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		seed func(*testing.T, *jobManager, string)
	}{
		{
			name: "delegate_not_resumable",
			seed: func(t *testing.T, jm *jobManager, delegateID string) {
				t.Helper()
				resumable := false
				appendDelegateTargetEvents(t, jm, delegateID, &resumable)
			},
		},
		{
			name: "terminal_job_missing_resumable_marker",
			seed: func(t *testing.T, jm *jobManager, delegateID string) {
				t.Helper()
				appendDelegateTargetEvents(t, jm, delegateID, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			watched, _ := jm.createShell(createShellOpts{Command: "watched"})
			tc.seed(t, jm, "dlg_dead")

			_, err := jm.configureWatch(watchArgs{
				Target:      watched.JobID,
				OutputMatch: "ready",
				Send:        &watchSendArgs{To: "dlg_dead", Message: "observe"},
			})

			if err == nil || !strings.Contains(err.Error(), "target_not_resumable") {
				t.Fatalf("error = %v, want target_not_resumable", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", jm.watchCount())
			}
		})
	}
}

func TestConfigureWatchRejectsTerminalizingConcreteJob(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestConfigureWatchRejectsUnsupportedEventFilterShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args watchArgs
		want string
	}{
		{
			name: "without_events",
			args: watchArgs{Target: runtimeMessageAliasCaller, EventFilter: &watchEventFilter{ToolName: "read_file"}},
			want: "event_filter requires events",
		},
		{
			name: "wrong_event",
			args: watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, EventFilter: &watchEventFilter{ToolName: "read_file"}},
			want: `source="parent"`,
		},
		{
			name: "wildcard_event",
			args: watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"*"}, EventFilter: &watchEventFilter{Status: "ok"}},
			want: `event_filter matches assistant.tool events`,
		},
		{
			name: "bad_status",
			args: watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{Status: "pending"}},
			want: `event_filter.status must be "ok" or "error"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			_, err := jm.configureWatch(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", jm.watchCount())
			}
		})
	}
}

func TestConfigureWatchRejectsSessionEventWatchWithProgress(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{
		Target:             runtimeMessageAliasCaller,
		ProgressIntervalMS: minWatchProgressIntervalMS,
		Events:             []string{"assistant.tool"},
		EventFilter: &watchEventFilter{
			ToolName: "read_file",
			Status:   "ok",
		},
		ReceiverSessionID:  "child_session",
		ReceiverDelegateID: "dlg_child",
	})
	if err == nil {
		t.Fatal("session event watch with periodic progress must fail")
	}
	if !strings.Contains(err.Error(), "session event watches use events/event_filter/every") {
		t.Fatalf("error = %v, want session event/progress mode guidance", err)
	}
}

func TestConfigureWatchRejectsEveryWithMultipleEvents(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"communicate", "job.notification"},
		Every:  2,
	})
	if err == nil || !strings.Contains(err.Error(), "every requires exactly one watched event kind") {
		t.Fatalf("error = %v, want every requires exactly one watched event kind", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}

	// every>1 with zero events should also fail (no event to throttle).
	_, err = jm.configureWatch(watchArgs{Target: "caller", Every: 2})
	if err == nil || !strings.Contains(err.Error(), "every requires exactly one watched event kind") {
		t.Fatalf("bare every with no events: error = %v, want every requires exactly one watched event kind", err)
	}

	// every:1 reads as unset, so bare every:1 is a watch with no condition.
	_, err = jm.configureWatch(watchArgs{Target: "caller", Every: 1})
	if err == nil || !strings.Contains(err.Error(), "nothing to watch") {
		t.Fatalf("bare every:1: error = %v, want nothing to watch", err)
	}
}

// every:1 is the semantic default (fire on each occurrence), so it must read as
// unset rather than trip the single-kind requirement — models legitimately send
// every:1 alongside multiple event kinds.
func TestConfigureWatchEveryOneReadsAsUnset(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")

	res, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"communicate", "job.notification"},
		Every:  1,
		Send:   &watchSendArgs{To: "dlg_obs"},
	})
	if err != nil {
		t.Fatalf("every:1 with multiple event kinds must be accepted as the default gate: %v", err)
	}
	if !res.Watching {
		t.Fatalf("result = %+v, want watching", res)
	}

	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil {
		t.Fatal("watch must be installed")
	}
	if cfg.triggerEvery != 0 {
		t.Fatalf("triggerEvery = %d, want 0 (every:1 reads as unset)", cfg.triggerEvery)
	}
}

func TestConfigureWatchEveryOneAllowsWildcardEvents(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")

	_, err = jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"*"},
		Every:  1,
		Send:   &watchSendArgs{To: "dlg_obs"},
	})
	if err != nil {
		t.Fatalf("every:1 with wildcard events must be accepted as the default gate: %v", err)
	}
	if jm.watchCount() != 1 {
		t.Fatalf("watch count = %d, want 1", jm.watchCount())
	}
}

func TestConfigureWatchRejectsEveryWithWildcardEvent(t *testing.T) {
	t.Parallel()
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

func TestConfigureWatchRejectsIncludeExcerptOnSessionTargets(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for _, target := range []string{"caller", "*"} {
		t.Run(target, func(t *testing.T) {
			_, err := jm.configureWatch(watchArgs{
				Target: target,
				Events: []string{"job.notification"},
				Send:   &watchSendArgs{To: "dlg_obs", IncludeExcerpt: true},
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

func TestJobManagerCloseDurablyClearsActiveWatches(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	res, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: minWatchProgressIntervalMS})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	if err := jm.closeRuntimeState(); err != nil {
		t.Fatalf("close runtime state: %v", err)
	}
	t.Cleanup(func() { _ = jm.closeStoreOnly() })

	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("load watches: %v", err)
	}
	w := watches[res.WatchID]
	if w == nil {
		t.Fatalf("watch %q missing from durable registry", res.WatchID)
	}
	if w.Active {
		t.Fatalf("watch = %+v, want inactive after close", w)
	}
	if w.EndReason != "job_manager_closed" {
		t.Fatalf("end reason = %q, want job_manager_closed", w.EndReason)
	}
}

func TestClearWatchOnTerminalTargetIsIdempotent(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)

	// No watch is installed (a concrete watch auto-removes on terminal) and the
	// target is terminal: clearing must be an idempotent no-op success, not
	// target_terminal.
	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
	if err != nil {
		t.Fatalf("clear on terminal target: %v", err)
	}
	if res.Watching {
		t.Fatalf("clear result Watching = true, want false")
	}
	res2, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
	if err != nil {
		t.Fatalf("second clear on terminal target: %v", err)
	}
	if res2.Watching {
		t.Fatalf("second clear result Watching = true, want false")
	}
}

func TestWatchHistoryRecordsReplacement(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "sleep 30"})
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	active := jm.liveWatchSummaries()
	if len(active) != 1 || active[0].ID == "" {
		t.Fatalf("active watch must carry a non-empty id, got %+v", active)
	}
	firstID := active[0].ID

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("idempotent reconfigure: %v", err)
	}
	if again := jm.liveWatchSummaries(); again[0].ID != firstID {
		t.Fatalf("idempotent re-configure changed id: %s -> %s", firstID, again[0].ID)
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "done"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if active = jm.liveWatchSummaries(); active[0].ID == firstID {
		t.Fatalf("replacement must assign a new id, still %s", firstID)
	}
	hist := jm.recentWatchSummaries()
	if len(hist) != 1 || hist[0].ID != firstID || hist[0].EndReason != "replaced" {
		t.Fatalf("recent_watches = %+v, want one replaced entry with id %s", hist, firstID)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if old := watches[firstID]; old == nil || old.Active || old.EndReason != "replaced" {
		t.Fatalf("durable old watch %s = %+v, want inactive replaced", firstID, old)
	}
	newID := active[0].ID
	if current := watches[newID]; current == nil || !current.Active {
		t.Fatalf("durable current watch %s = %+v, want active", newID, current)
	}
}

func TestWatchIdempotentReconfigureWithDetachedPendingDoesNotRegisterNewWatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "sleep 30"})
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	args := watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}
	res, err := jm.configureWatch(args)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	firstID := res.WatchID

	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}
	jm.mu.Lock()
	existing := jm.watches[key]
	if existing == nil {
		jm.mu.Unlock()
		t.Fatal("installed watch not found")
	}
	pendingKey := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchTarget:             rec.JobID,
		ResolvedWatchedIdentity: rec.JobID,
		ResolvedSendTo:          "dlg_obs",
		WatchGeneration:         jobstore.NewWatchGeneration(),
	}
	detached := &watchConfig{
		target:     rec.JobID,
		send:       &watchSendArgs{To: "dlg_obs", Message: "observe"},
		generation: pendingKey.WatchGeneration,
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{
			pendingKey: {
				Key:           pendingKey,
				DeliveryID:    "watch_delivery_detached",
				UpdateSeq:     1,
				Message:       "observe",
				TriggerReason: "output_match: ready",
			},
		},
		pendingOrder: []jobstore.WatchSendKey{pendingKey},
	}
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[detached] = true
	jm.mu.Unlock()

	again, err := jm.configureWatch(args)
	if err != nil {
		t.Fatalf("idempotent reconfigure: %v", err)
	}
	if again.WatchID != firstID {
		t.Fatalf("idempotent reconfigure changed watch id: %s -> %s", firstID, again.WatchID)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if len(watches) != 1 {
		t.Fatalf("durable watches = %+v, want exactly one active watch", watches)
	}
	if current := watches[firstID]; current == nil || !current.Active {
		t.Fatalf("durable watch %s = %+v, want active", firstID, current)
	}
}

func TestWatchHistoryRecordsTerminalAutoRemoval(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch should auto-remove on terminal, count=%d", jm.watchCount())
	}
	hist := jm.recentWatchSummaries()
	if len(hist) != 1 || hist[0].EndReason != "auto_removed_terminal" || hist[0].Source != rec.JobID {
		t.Fatalf("recent_watches = %+v, want one auto_removed_terminal entry", hist)
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if w := watches[res.WatchID]; w == nil || w.Active || w.EndReason != "auto_removed_terminal" {
		t.Fatalf("durable watch %s = %+v, want inactive auto_removed_terminal", res.WatchID, w)
	}
}

func TestTerminalWatchAutoRemovalAppendFailureKeepsWatchReachable(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil {
		t.Fatal("watch config missing before terminal finalize")
	}

	realAppendEvents := jm.appendEvents
	appendErr := errors.New("append watch clear failed")
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchCleared {
				return appendErr
			}
		}
		if realAppendEvents != nil {
			return realAppendEvents(events)
		}
		for _, event := range events {
			if err := jm.appendEvent(event); err != nil {
				return err
			}
		}
		return nil
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, appendErr) {
		t.Fatalf("finalize error = %v, want append failure", err)
	}
	jm.mu.Lock()
	stillReachable := jm.watches[key] == cfg
	jm.mu.Unlock()
	if !stillReachable {
		t.Fatal("watch config was detached before clear event became durable")
	}
	if jm.watchCount() != 1 {
		t.Fatalf("watch count after failed clear append = %d, want 1", jm.watchCount())
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if w := watches[res.WatchID]; w == nil || !w.Active {
		t.Fatalf("durable watch %s = %+v, want still active after failed clear append", res.WatchID, w)
	}
	if hist := jm.recentWatchSummaries(); len(hist) != 0 {
		t.Fatalf("recent_watches after failed clear append = %+v, want empty", hist)
	}

	jm.appendEvents = realAppendEvents
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after retry = %d, want 0", jm.watchCount())
	}
}

func TestKeptSyncTerminalWatchAppendFailureKeepsRetryState(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if run == nil || cfg == nil {
		t.Fatalf("setup run=%v cfg=%v, want both present", run != nil, cfg != nil)
	}

	realAppendEvent := jm.appendEvent
	finishedAppends := 0
	jm.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventJobFinished {
			finishedAppends++
		}
		return realAppendEvent(event)
	}
	realAppendEvents := jm.appendEvents
	appendErr := errors.New("append watch clear failed")
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchCleared {
				return appendErr
			}
		}
		if realAppendEvents != nil {
			return realAppendEvents(events)
		}
		for _, event := range events {
			if err := jm.appendEvent(event); err != nil {
				return err
			}
		}
		return nil
	}

	code := 0
	if err := jm.finalizeKeptSync(run, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, appendErr) {
		t.Fatalf("finalizeKeptSync error = %v, want append failure", err)
	}
	jm.mu.Lock()
	stillRunning := jm.running[rec.JobID] == run
	stillWatching := jm.watches[key] == cfg
	jm.mu.Unlock()
	if !stillRunning {
		t.Fatal("kept-sync run was removed before watch clear became durable")
	}
	if !stillWatching {
		t.Fatal("watch config was detached before watch clear became durable")
	}

	jm.appendEvents = realAppendEvents
	if err := jm.finalizeKeptSync(run, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry finalizeKeptSync: %v", err)
	}
	if finishedAppends != 1 {
		t.Fatalf("job_finished appends = %d, want one durable terminal across retry", finishedAppends)
	}
	jm.mu.Lock()
	_, running := jm.running[rec.JobID]
	_, watching := jm.watches[key]
	jm.mu.Unlock()
	if running || watching {
		t.Fatalf("retry left running=%v watching=%v, want both removed", running, watching)
	}
}

func TestKeptSyncRetryDoesNotArmOwnerNotification(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	jm.mu.Unlock()
	if run == nil {
		t.Fatal("running job missing")
	}

	realAppendEvents := jm.appendEvents
	appendErr := errors.New("append watch clear failed")
	failed := false
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchCleared && !failed {
				failed = true
				return appendErr
			}
		}
		return realAppendEvents(events)
	}

	code := 0
	jm.finalizeKeptSyncUntilDurable(run, jobstore.StatusCompleted, "exit_zero", &code)
	if !failed {
		t.Fatal("test did not exercise the kept-sync retry path")
	}
	if len(notified) != 0 {
		t.Fatalf("kept-sync retry enqueued owner/watch notifications: %+v", notified)
	}
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventJobNotificationPending {
			t.Fatalf("kept-sync retry armed owner notification: %+v", event)
		}
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending watch sends = %+v, want none", pending)
	}
	jm.mu.Lock()
	_, running := jm.running[rec.JobID]
	jm.mu.Unlock()
	if running {
		t.Fatal("kept-sync retry left job running")
	}
}

func TestKeptSyncTerminalWatchAppendFailureRetainsBufferedMatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	jm.mu.Unlock()
	if run == nil {
		t.Fatal("running job missing")
	}
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}

	realAppendEvents := jm.appendEvents
	appendErr := errors.New("append watch clear failed")
	failed := false
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchCleared && !failed {
				failed = true
				return appendErr
			}
		}
		return realAppendEvents(events)
	}

	code := 0
	if err := jm.finalizeKeptSync(run, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, appendErr) {
		t.Fatalf("finalizeKeptSync error = %v, want append failure", err)
	}
	if len(notified) != 0 {
		t.Fatalf("failed durable clear emitted notifications: %+v", notified)
	}

	jm.appendEvents = realAppendEvents
	if err := jm.finalizeKeptSync(run, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry finalizeKeptSync: %v", err)
	}
	if len(notified) != 1 || !strings.Contains(notified[0].Reason, "server ready") {
		t.Fatalf("notifications = %+v, want retained terminal output_match", notified)
	}
}

func TestWatchHistoryRecordsClear(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "sleep 30"})
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	hist := jm.recentWatchSummaries()
	if len(hist) != 1 || hist[0].EndReason != "cleared" {
		t.Fatalf("recent_watches = %+v, want one cleared entry", hist)
	}
}

func TestWatchHistoryCountsTerminalFlushDeliveries(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// An unterminated final line is buffered by the matcher and only matches at the
	// terminal Flush — the fire that must still be counted in recent_watches.
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append: %v", err)
	}
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)

	hist := jm.recentWatchSummaries()
	if len(hist) != 1 || hist[0].EndReason != "auto_removed_terminal" {
		t.Fatalf("recent_watches = %+v, want one auto_removed_terminal entry", hist)
	}
	if hist[0].Deliveries < 1 {
		t.Fatalf("recent_watches deliveries = %d, want >=1 (terminal-flush match counted)", hist[0].Deliveries)
	}
}
