package agent

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	tooldefs "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provenance"
)

// TestSelfDeliveryWatchShapesInstall covers the self-delivery watch shapes the
// removed create-time forbid used to reject: any watch whose resolved event kinds
// include a self-generated kind (assistant.tool/communicate, including via "*")
// AND whose delivery returns to the generating session (send omitted or
// send.to=caller). configureWatch now ACCEPTS every one of these — the breaker
// bounds the loop at runtime (the depth fuse via recordWatchSend on the send
// path, the volume breaker watchDeliveryBudget on the no-send notification path)
// instead of a create-time forbid.
func TestSelfDeliveryWatchShapesInstall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(t *testing.T, jm *jobManager) watchArgs
	}{
		{
			name: "notify_self_assistant_tool",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.tool"}}
			},
		},
		{
			name: "send_to_self_incident_shape",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.tool"}, Send: &watchSendArgs{To: "caller"}}
			},
		},
		{
			name: "every_derived_single_kind_to_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"assistant.tool"}, Every: 2, Send: &watchSendArgs{To: "caller"}}
			},
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
		},
		{
			name: "wildcard_send_to_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"*"}, Send: &watchSendArgs{To: "caller"}}
			},
		},
		{
			name: "wildcard_notify_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"*"}}
			},
		},
		{
			name: "communicate_notify_self",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"communicate"}}
			},
		},
		{
			name: "job_notification_self_watch",
			build: func(*testing.T, *jobManager) watchArgs {
				return watchArgs{Target: "caller", Events: []string{"job.notification"}}
			},
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := newTestJM(t)
			t.Cleanup(func() { _ = jm.close() })
			jm.enqueue = func(jobNotification) {}
			args := tt.build(t, jm)
			res, err := jm.configureWatch(args)
			if err != nil {
				t.Fatalf("configureWatch(%+v) = %v, want install under the breaker policy", args, err)
			}
			if jm.watchCount() == 0 {
				t.Fatalf("watch was not installed; watchCount = 0, result = %+v", res)
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

// TestJobWatchSelfSourceSelfKindInstalls: watching your own assistant.tool events
// with delivery back to yourself is allowed under the breaker policy — the
// create-time loop guard is gone; the runtime breaker bounds the loop.
func TestJobWatchSelfSourceSelfKindInstalls(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	res, err := jobWatchTool(sess, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"assistant.tool"},
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobWatchTool: %v, want self-watch install under the breaker policy", err)
	}
	state := res.(tooldefs.StateResult).State.(jobWatchToolResult)
	if !state.Watching {
		t.Fatalf("watch state = %+v, want watching", state)
	}
}

// TestEventWatchDeliversWatchOriginatedSubagentEventsClassified covers the
// breaker policy on the job.notification rail: an event whose provenance already
// carries THIS watch's (watch_id, generation) key is the watch's own downstream
// echo — delivered (not suppressed) and classified self-influenced, recording a
// pending send like any other fire. The runaway fuse, proven elsewhere, bounds a
// runaway.
func TestEventWatchDeliversWatchOriginatedSubagentEventsClassified(t *testing.T) {
	t.Parallel()
	proof := newWatchCustodyProof(t)
	jm := proof.jm
	cfg := proof.install(t, watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
	})

	ownEcho := events.SessionEvent{
		Kind:       events.EventJobFinished,
		SessionID:  jm.sessionID,
		Data:       events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed"},
		Provenance: provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_echo", jm.sessionID, "caller"),
	}
	jm.mu.Lock()
	classification := jm.classifySelfInfluenceLocked(cfg, ownEcho.Provenance)
	jm.mu.Unlock()
	if !classification.self || classification.gradientDepth != 0 || classification.fuseDepth != 0 {
		t.Fatalf("undelivered prior classification = %+v", classification)
	}
	jm.onSessionEvent(ownEcho)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("self-echo job event must record one pending send (delivered + classified), got %d: %+v", len(pending), pending)
	}

	first := proof.pending(t, cfg, 1, 0)
	if !provenance.ContainsWatch(first.Provenance, cfg.watchID, cfg.generation) {
		t.Fatal("echo lost the current watch key")
	}

	// A second fire for the same watch key COALESCES with the pending echo
	// (superseded, not appended): still one pending entry, but the fire counter
	// advanced.
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_worker", JobType: "delegate", Status: "completed"})
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("ordinary job event must coalesce into the pending entry, got %d", len(pending))
	}
	if cfg.nextUpdateSeq != 2 {
		t.Fatalf("nextUpdateSeq = %d, want 2 (echo fire + ordinary fire)", cfg.nextUpdateSeq)
	}
	latest := proof.pending(t, cfg, 2, 0)
	if latest.CoalescedCount != 1 || latest.DeliveryID == first.DeliveryID || !provenance.ContainsWatch(latest.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("coalesced source = %+v", latest)
	}
	proof.observe(t, latest)
}

// TestNoSendWatchNotificationCarriesProvenanceForEchoClassification: the
// notification's provenance still carries the watch key (that is how a downstream
// event is RECOGNIZED as the watch's echo) — but under the breaker policy the
// echo re-notifies instead of being suppressed; the volume budget bounds it.
func TestNoSendWatchNotificationCarriesProvenanceForEchoClassification(t *testing.T) {
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
	if len(notified) != 2 {
		t.Fatalf("self-echo must re-notify once (delivered + classified); notifications = %d: %+v", len(notified), notified)
	}
	if cfg.deliveries != 2 {
		t.Fatalf("deliveries = %d, want both fires counted", cfg.deliveries)
	}
}

// TestWatchOriginDeliversDelegateLifecycleWatchSendsClassified covers the breaker
// policy on the delegate-lifecycle (job.notification on "*") rail: the JobFinished
// event stamped with the watch's own provenance key is delivered (not suppressed)
// and classified self-influenced, recording one pending send. JobStarted/running
// is not a watch-matchable job.notification and never fires.
func TestWatchOriginDeliversDelegateLifecycleWatchSendsClassified(t *testing.T) {
	t.Parallel()
	proof := newWatchCustodyProof(t)
	jm := proof.jm
	cfg := proof.install(t, watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
	})
	own := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_obs", jm.sessionID, "*")
	jm.mu.Lock()
	classification := jm.classifySelfInfluenceLocked(cfg, own)
	jm.mu.Unlock()
	if !classification.self || classification.gradientDepth != 0 || classification.fuseDepth != 0 {
		t.Fatalf("undelivered lifecycle prior classification = %+v", classification)
	}

	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventJobStarted,
		SessionID:  jm.sessionID,
		Data:       events.JobStartedData{JobID: "job_obs", JobType: "delegate", Status: "running"},
		Provenance: own,
	})
	if len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != 0 {
		t.Fatal("JobStarted produced a notification frame")
	}

	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventJobFinished,
		SessionID:  jm.sessionID,
		Data:       events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed"},
		Provenance: own,
	})

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("self-echo JobFinished must record one pending send (delivered + classified), got %d: %+v", len(pending), pending)
	}
	state := proof.pending(t, cfg, 1, 0)
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatal("lifecycle echo lost current watch provenance")
	}
	proof.observe(t, state)
}

// TestOutputMatchDeliversSameWatchProvenanceClassifiedSelfInfluenced proves that
// an output_match feed carrying this watch's own (watch_id, generation) key is the
// watch's echo: delivered (not suppressed) and classified self-influenced under the
// breaker policy, firing once. The runaway fuse, proven elsewhere, bounds a runaway.
func TestOutputMatchDeliversSameWatchProvenanceClassifiedSelfInfluenced(t *testing.T) {
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

	if len(notified) != 1 {
		t.Fatalf("self-echo output_match must fire once (delivered + classified); got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1 for self-echo output_match", cfg.deliveries)
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

// TestOutputMatchDeliversSameWatchProvenanceAcrossSplitLineClassified proves the
// breaker policy holds when the watch's key reaches the match via the FIRST chunk
// of a token split across two feeds: the match is the watch's echo — delivered
// and classified self-influenced, firing once.
func TestOutputMatchDeliversSameWatchProvenanceAcrossSplitLineClassified(t *testing.T) {
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

	if len(notified) != 1 {
		t.Fatalf("split self-echo output_match must fire once (delivered + classified); got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1 for self-echo split output_match", cfg.deliveries)
	}
}

// TestOutputMatchDeliversSameWatchProvenanceOnTerminalFlushClassified proves the
// breaker policy through terminal expiry. The legacy name predates byte-window
// matching: a split match now fires before finalize, without a line terminator.
// Expiry must preserve that exact frame, not refire it or create an end notice.
func TestOutputMatchDeliversSameWatchProvenanceOnTerminalFlushClassified(t *testing.T) {
	t.Parallel()
	proof := newWatchCustodyProof(t)
	jm := proof.jm

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	cfg := proof.install(t, watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
	})
	p := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, rec.JobID)

	jm.feedJobOutputWithProvenance(rec.JobID, []byte("re"), 2, p)
	if len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != 0 {
		t.Fatal("partial token produced a frame")
	}
	jm.feedJobOutputWithProvenance(rec.JobID, []byte("ady"), 5, nil)
	before := proof.pending(t, cfg, 1, 0)
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("terminal-flush self-echo output_match must persist one pending send (delivered + classified); got %+v", pending)
	}
	state := proof.pending(t, cfg, 1, 0)
	if state.DeliveryID != before.DeliveryID || state.EndNotice || jm.watchCount() != 0 {
		t.Fatal("terminal expiry replaced the split match or left the watch live")
	}
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatal("terminal flush lost split-chunk watch provenance")
	}
	proof.observe(t, state)
}

// TestProgressTickDeliversSameWatchProvenanceClassified proves the breaker policy
// on the progress-tick fire site: a tick whose target job provenance carries the
// watch's own key is delivered and classified self-influenced, firing once.
func TestProgressTickDeliversSameWatchProvenanceClassified(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cfg, err := newWatchConfig(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}, jm.now(), "")
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
	if len(notified) != 1 {
		t.Fatalf("self-echo progress tick must fire once (delivered + classified); got %d notifications: %+v", len(notified), notified)
	}
	if cfg.deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1 for self-echo progress tick", cfg.deliveries)
	}
}

func TestWatchWatchedAliasSendRejectsConcreteTarget(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	target, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create shell target: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      target.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: runtimeMessageAliasWatched, Message: "observe"},
	}); err == nil || !strings.Contains(err.Error(), "watched is not a v1 delivery target") {
		t.Fatalf("error = %v, want watched alias rejection", err)
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
	if len(notified) != 0 {
		t.Fatalf("rejected watched alias emitted diagnostics: %+v", notified)
	}
}

func TestJobWatchAllowsDirectChildConcreteJobSourceAndManagesIt(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, testRootSessionID)
	childJM := newWalkJobManager(t, testChildSessionID)
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

	child := &Session{id: testChildSessionID, jobManager: childJM, subagents: newSubagentManager(nil, 0)}
	root := &Session{id: testRootSessionID, jobManager: rootJM, subagents: newSubagentManager(nil, 0)}
	root.subagents.track(&subagent{id: testChildSessionID, sess: child, status: SubagentRunning})

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
	// The child may NOT clear the root's ancestor-owned watch: the clear is
	// refused with an explicit error (not a success-shaped no-op) and the
	// watch stays armed for its owner.
	if _, err := jobWatchTool(child, map[string]any{"operation": "clear", "watch_id": state.WatchID}, 20000); err == nil || !strings.Contains(err.Error(), "may not clear") {
		t.Fatalf("child jobWatchTool clear err = %v, want explicit refusal", err)
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
	registry, err := childJM.store.LoadWatches()
	if err != nil {
		t.Fatalf("load cleared watch: %v", err)
	}
	cleared := registry[state.WatchID]
	if cleared == nil || cleared.Active || cleared.EndReason != "cleared" || cleared.ReceiverSessionID != root.id {
		t.Fatalf("durable watch after clear = %+v, want receiver-owned cleared registration", cleared)
	}
	history := childJM.visibleWatchHistorySnapshots(root.id)
	if len(history) != 1 || history[0].id != state.WatchID || history[0].endReason != "cleared" || history[0].endedAt.IsZero() {
		t.Fatalf("receiver history after clear = %+v, want one ended cleared watch", history)
	}
	// Clearing registration must not discard the firing's outstanding source
	// custody. That drain-only residue must not hide the watch's ended history.
	pending := loadWatchSendRecord(t, childJM).Pending
	if len(pending) != 1 {
		t.Fatalf("pending source frames after clear = %d, want the retained firing", len(pending))
	}
	for _, frame := range pending {
		if frame.Key.WatchID != state.WatchID || frame.ReceiverSessionID != root.id {
			t.Fatalf("pending source frame after clear = %+v, want the receiver-owned firing", frame)
		}
	}
	recentOut, err := jobWatchTool(root, map[string]any{"operation": "inspect", "watch_id": state.WatchID}, 20000)
	if err != nil {
		t.Fatalf("root inspect recent: %v", err)
	}
	recent := recentOut.(tooldefs.StateResult).State.(jobWatchInspectToolResult)
	if recent.Watching || recent.Source != childRec.JobID || recent.EndReason != "cleared" || recent.EndedAt != history[0].endedAt.Format(time.RFC3339Nano) {
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
	rootJM := newWalkJobManager(t, testRootSessionID)
	coordJM := newWalkJobManager(t, testCoordinatorSessionID)
	workerJM := newWalkJobManager(t, testWorkerSessionID)
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

	worker := &Session{id: testWorkerSessionID, jobManager: workerJM, subagents: newSubagentManager(nil, 0)}
	coordinator := &Session{id: testCoordinatorSessionID, jobManager: coordJM, subagents: newSubagentManager(nil, 0)}
	coordinator.subagents.track(&subagent{id: testWorkerSessionID, sess: worker, status: SubagentRunning})
	root := &Session{id: testRootSessionID, jobManager: rootJM, subagents: newSubagentManager(nil, 0)}
	root.subagents.track(&subagent{id: testCoordinatorSessionID, sess: coordinator, status: SubagentRunning})

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

// TestJobWatchDeliversSameWatchProvenanceClassifiedSelfInfluenced proves the
// breaker policy on the session-event fire site: a communicate event carrying the
// watch's own (watch_id, generation) key is delivered (not suppressed) and
// classified self-influenced, recording one pending send. cfg.deliveries stays 0
// because send deliveries are not pre-counted at fire time (they settle on their
// own path). The runaway fuse, proven elsewhere, bounds a runaway.
func TestJobWatchDeliversSameWatchProvenanceClassifiedSelfInfluenced(t *testing.T) {
	// Not parallel: captureReceiverErrorLogs replaces the default logger.
	secondHold := errors.New("custody-second-hold-6e81")
	captureReceiverErrorLogs(t, secondHold)
	proof := newWatchCustodyProof(t)
	jm := proof.jm
	cfg := proof.install(t, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
	})

	ev := events.New(events.CommunicateData{Message: "PYTHON_QUOTE quote=Ni!", EndTurn: false})
	ev.SessionID = jm.sessionID
	ev.Provenance = provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", jm.sessionID, "caller")
	jm.mu.Lock()
	classification := jm.classifySelfInfluenceLocked(cfg, ev.Provenance)
	jm.mu.Unlock()
	if !classification.self || classification.gradientDepth != 0 || classification.fuseDepth != 0 {
		t.Fatalf("undelivered communicate prior classification = %+v", classification)
	}

	jm.onSessionEvent(ev)

	if cfg.nextUpdateSeq != 1 {
		t.Fatalf("nextUpdateSeq = %d, want 1 for self-echo event (delivered + classified, fires once)", cfg.nextUpdateSeq)
	}
	if len(cfg.pending) != 1 {
		t.Fatalf("pending sends = %d, want 1 for self-echo event", len(cfg.pending))
	}
	state := proof.pending(t, cfg, 1, 0)
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatal("communicate echo lost current watch provenance")
	}
	proof.observe(t, state)

	// Echo the actual settled delivery. Undelivered synthetic priors above must
	// not contribute depth, but this successful source acknowledgement must.
	proof.hold(secondHold)
	ev.Provenance = state.Provenance
	jm.mu.Lock()
	classification = jm.classifySelfInfluenceLocked(cfg, ev.Provenance)
	jm.mu.Unlock()
	if !classification.self || classification.gradientDepth != 1 || classification.fuseDepth != 1 {
		t.Fatalf("delivered communicate prior classification = %+v", classification)
	}
	jm.onSessionEvent(ev)
	echo := proof.pending(t, cfg, 2, 1)
	if echo.DeliveryID == state.DeliveryID {
		t.Fatal("new echo reused its delivered prior identity")
	}
	proof.observe(t, echo)
	logged := slog.Default().Handler().(*reclaimLogCapture)
	logged.mu.Lock()
	warnings := 0
	for _, record := range logged.records {
		if record.Level < slog.LevelWarn {
			continue
		}
		record.Attrs(func(attr slog.Attr) bool {
			if err, ok := attr.Value.Any().(error); ok && errors.Is(err, secondHold) {
				warnings++
			}
			return true
		})
	}
	logged.mu.Unlock()
	if warnings == 0 {
		t.Fatal("receiver emitted no typed warning with the injected second-epoch cause")
	}
	t.Logf("receiver_typed_warning epoch=2 count=%d cause_is_injected=true", warnings)
}

// TestJobWatchDifferentGenerationFiresUnclassified: an old generation's key does
// not classify the new generation's fire as self-influenced (gradient scope), so
// the watch fires normally.
func TestJobWatchDoesNotSuppressDifferentGeneration(t *testing.T) {
	t.Parallel()
	proof := newWatchCustodyProof(t)
	jm := proof.jm
	cfg := proof.install(t, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
	})
	oldGeneration := "wg_previous"

	ev := events.New(events.CommunicateData{Message: "actually alpha marker", EndTurn: false})
	ev.SessionID = jm.sessionID
	ev.Provenance = provenance.WithWatch(nil, cfg.watchID, oldGeneration, "wd_old", jm.sessionID, "caller")
	jm.mu.Lock()
	classification := jm.classifySelfInfluenceLocked(cfg, ev.Provenance)
	jm.mu.Unlock()
	if classification.self || classification.gradientDepth != 0 || classification.fuseDepth != 0 {
		t.Fatalf("old-generation trigger classification = %+v", classification)
	}

	jm.onSessionEvent(ev)

	if cfg.deliveries == 0 && len(cfg.pending) == 0 {
		t.Fatal("old generation provenance must not suppress new generation")
	}
	state := proof.pending(t, cfg, 1, 0)
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, oldGeneration) || !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("old and current generation must both survive: %+v", state.Provenance)
	}
	proof.observe(t, state)
}

func TestWatchSendFrameRendersTriggerProvenanceNotDeliveryProvenance(t *testing.T) {
	t.Parallel()
	proof := newWatchCustodyProof(t)
	jm := proof.jm
	cfg := proof.install(t, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
	})

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
	// Parse the frame's machine field, not its human-readable copy. The
	// external trigger must not be relabeled with the delivery's watch key.
	fields := map[string]string{}
	for line := range strings.SplitSeq(state.Frame, "\n") {
		if key, value, ok := strings.Cut(line, ": "); ok && !strings.HasPrefix(key, " ") {
			fields[key] = value
		}
	}
	if fields["provenance"] != "external" || fields["watch_id"] != cfg.watchID || fields["delivery_id"] != state.DeliveryID {
		t.Fatalf("frame machine fields = %v", fields)
	}
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("delivery provenance = %+v, want persisted watch key %s/%s", state.Provenance, cfg.watchID, cfg.generation)
	}
	proof.pending(t, cfg, 1, 0)
	proof.observe(t, *state)
}
