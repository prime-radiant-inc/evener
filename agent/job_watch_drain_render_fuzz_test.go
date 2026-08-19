//go:build evenerfuzz

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// FuzzJobWatchDrainRenderTail drives the durable drain, restored-target, and
// frame-rendering tail through a real job manager and on-disk job store. The
// byte selects an ordering only; every replay covers the complete branch table.
func FuzzJobWatchDrainRenderTail(f *testing.F) {
	f.Add(byte(0))
	f.Add(byte(1))
	f.Fuzz(func(t *testing.T, order byte) {
		jm, err := newJobManager(t.TempDir(), testOwnerSessionID, func(jobNotification) {})
		if err != nil {
			t.Fatal(err)
		}
		freezeClock(jm)
		jm.clock = agenttest.NewFakeClock()
		t.Cleanup(func() { _ = jm.store.Close() })
		controller, _ := newDelegateControllerTestHarness(t, 2, 1)
		s := &Session{id: testOwnerSessionID, jobManager: jm, delegateController: controller, delegateRootSessionID: testOwnerSessionID}

		steps := []func(){
			func() { jdrExercisePendingAndRender(t, s) },
			func() { jdrExerciseGuardsAndFailures(t, s) },
			func() { jdrExerciseSuccessfulChildDrive(t) },
		}
		if order&1 != 0 {
			steps[0], steps[2] = steps[2], steps[0]
		}
		for _, step := range steps {
			step()
		}
	})
}

func jdrExerciseSuccessfulChildDrive(t *testing.T) {
	t.Helper()
	clk := agenttest.NewFakeClock()
	parent := safzNewParent(t, clk, 1, []int{1}, &agenttest.DenyEnv{WorkDir: t.TempDir()})
	res, err := parent.spawnAgent(context.Background(), "drain queued notification", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var spawned struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(res.(string)), &spawned); err != nil || spawned.AgentID == "" {
		t.Fatalf("spawn result malformed: %v (%q)", err, res)
	}
	sub := parent.getSub(spawned.AgentID)
	if sub == nil || sub.sess == nil {
		t.Fatal("spawned child is not tracked")
	}
	safzWaitDone(t, sub)
	sub.sess.enqueueJobNotification(jobNotification{JobID: "job_drive_tail", Reason: "drive tail"})
	parent.driveChildrenWithUndeliveredAttention()
	parent.sendersWG.Wait()
	if parent.treeCounter.n.Load() != 0 || parent.driveCounter.n.Load() != 0 {
		t.Fatalf("child drive counters = tree:%d drive:%d, want zero", parent.treeCounter.n.Load(), parent.driveCounter.n.Load())
	}

	// sessions() retains a real child Session even when its job manager has not
	// been initialized, covering the defensive drain-report guard.
	nilJMParent := &Session{subagents: newSubagentManager(nil, 0)}
	nilJMParent.subagents.track(&subagent{id: "child-no-jm", sess: &Session{id: "child-no-jm"}, closed: true})
	_, _ = nilJMParent.drainPendingWatchSendsReport(context.Background())
}

func jdrExercisePendingAndRender(t *testing.T, s *Session) {
	t.Helper()
	jm := s.jobManager
	key := jobstore.WatchSendKey{ResolvedWatchedIdentity: "missing", ResolvedSendTo: runtimeMessageAliasCaller, VisibleSessionID: testOwnerSessionID}
	state := &jobstore.WatchSendState{Key: key, UpdateSeq: 1, TriggerReason: "trigger"}
	cfg := &watchConfig{watchID: "w", send: &watchSendArgs{Message: "hello", IncludeExcerpt: true}, pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: state}, pendingOrder: []jobstore.WatchSendKey{key, {}}}
	jm.mu.Lock()
	jm.watches[watchKey{VisibleSessionID: testOwnerSessionID, Target: "missing"}] = cfg
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[cfg] = true
	jm.mu.Unlock()
	jm.pendingWatchSendDeliveries(func(st *jobstore.WatchSendState) bool { return st.UpdateSeq == 1 })
	jm.pendingWatchSendDeliveries(func(*jobstore.WatchSendState) bool { return false })
	jm.hasPendingWatchSends()
	jm.enqueue = func(jobNotification) {}
	s.enqueueOwnCallerWatchSendTokens()
	failAppendN(jm, jobstore.EventWatchSendDropped, 1)
	_, _ = s.drainJobManagerWatchSends(context.Background(), jm, "child")
	_, _ = s.drainJobManagerWatchSends(context.Background(), jm, "")

	badKey := jobstore.WatchSendKey{ResolvedWatchedIdentity: "bad", ResolvedSendTo: "job_bad", VisibleSessionID: testOwnerSessionID}
	badState := &jobstore.WatchSendState{Key: badKey, UpdateSeq: 3}
	badCfg := &watchConfig{pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{badKey: badState}, pendingOrder: []jobstore.WatchSendKey{badKey}}
	jm.mu.Lock()
	jm.watches[watchKey{VisibleSessionID: testOwnerSessionID, Target: "bad"}] = badCfg
	jm.terminalFlush[badCfg] = true
	jm.mu.Unlock()
	failAppendN(jm, jobstore.EventWatchSendDropped, 1)
	_ = s.retryRestoredPendingWatchSends(context.Background())

	if resolve, err := resolveWatchSendTarget(runtimeMessageAliasWatched, ""); resolve != "" || err == nil {
		t.Fatal("unresolved watched alias")
	}
	if _, err := resolveWatchSendTarget(runtimeMessageAliasWatched, runtimeMessageAliasCaller); err == nil {
		t.Fatal("session watched alias resolved")
	}
	if got, err := resolveWatchSendTarget(runtimeMessageAliasWatched, "job_x"); got != "job_x" || err != nil {
		t.Fatal("concrete watched alias")
	}

	if got := jm.buildWatchFrame(nil, "", "", "", events.SessionEvent{}, nil); got != "" {
		t.Fatal(got)
	}
	frame := jm.buildWatchFrame(cfg, "missing", "a\r\nb", "delivery", events.SessionEvent{}, nil)
	if !strings.Contains(frame, "output_read_error") {
		t.Fatalf("missing read error: %q", frame)
	}
	var b strings.Builder
	writeWatchFrameBoolField(&b, "x", true)
	writeWatchFrameBoolField(&b, "x", false)
	_ = limitWatchText("abcdef", 2)
	_ = limitWatchText("abcdef", 20)
	outputRec, err := jm.createShell(createShellOpts{Command: "output", Description: "truncated excerpt"})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	outputRun := jm.running[outputRec.JobID]
	jm.mu.Unlock()
	if outputRun == nil {
		t.Fatal("running output fixture disappeared")
	}
	if _, err := jm.appendJobOutput(outputRec.JobID, outputRun.output, []byte(strings.Repeat("x", watchExcerptTailBytes+100))); err != nil {
		t.Fatal(err)
	}
	truncatedFrame := jm.buildWatchFrame(cfg, outputRec.JobID, "output", "delivery-output", events.SessionEvent{}, nil)
	if !strings.Contains(truncatedFrame, "[excerpt truncated]") && !strings.HasSuffix(truncatedFrame, watchTruncatedIndicator) {
		t.Fatalf("missing truncated marker: %q", truncatedFrame)
	}
	jm.enqueueWatchNotifications(nil)
	routed := false
	jm.enqueueWatchNotifications([]jobNotification{{receiverNotify: func(jobNotification) { routed = true }}})
	if !routed {
		t.Fatal("receiver notification was not routed")
	}
	jm.enqueueWatchNotifications([]jobNotification{{receiverSessionID: "other"}, {receiverSessionID: testOwnerSessionID}})
	jm.mu.Lock()
	jm.closing = true
	jm.mu.Unlock()
	jm.enqueueWatchNotifications([]jobNotification{{Reason: "closed"}})
	jm.mu.Lock()
	jm.closing = false
	delete(jm.watches, watchKey{VisibleSessionID: testOwnerSessionID, Target: "missing"})
	delete(jm.watches, watchKey{VisibleSessionID: testOwnerSessionID, Target: "bad"})
	jm.mu.Unlock()
	jm.hasPendingWatchSends()
}

func jdrExerciseGuardsAndFailures(t *testing.T, s *Session) {
	t.Helper()
	var nilSession *Session
	nilSession.renderUnreachableChildPendings(nil)
	_ = nilSession.childResumable("x")
	_ = nilSession.childStopGated("x")
	nilSession.settleDrivenChildForwardedPendings("x")
	nilSession.enqueueOwnCallerWatchSendTokens()
	_ = nilSession.retryRestoredPendingWatchSends(context.Background())
	s.driveChildIfNotStopGated(nil)
	s.driveChildIfNotStopGated(&subagent{})

	closed, err := newJobManager(t.TempDir(), testChildSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	freezeClock(closed)
	closed.clock = agenttest.NewFakeClock()
	if err := closed.store.Close(); err != nil {
		t.Fatal(err)
	}
	cs := &Session{id: testChildSessionID, jobManager: closed}
	cs.renderUnreachableChildPendings(nil)
	_ = cs.childResumable("child")
	_ = cs.childStopGated("child")
	cs.settleDrivenChildForwardedPendings("child")

	// Empty and nil records cover the skip arms without inventing a fake store.
	_ = s.childResumable("")
	if _, err := s.jobManager.createShell(createShellOpts{Command: "true", Description: "skip non-delegate"}); err != nil {
		t.Fatal(err)
	}
	_ = s.childResumable("no-child")
	_ = s.childStopGated("")
	s.settleDrivenChildForwardedPendings("")
	_ = s.drainPendingWatchSends(context.Background())
	appendForwardedChildTerminalPending(t, s.jobManager, "job_settle", "child-settle")
	failAppendN(s.jobManager, jobstore.EventJobNotificationDelivered, 1)
	s.settleDrivenChildForwardedPendings("child-settle")

	appendForwardedChildTerminalPending(t, s.jobManager, "job_live_tail", "child-live")
	appendForwardedChildTerminalPending(t, s.jobManager, "job_gone_tail", "child-gone")
	appendForwardedChildCallerWatchSendPending(t, s.jobManager, "child-live", "job_live_watch_tail")
	appendForwardedChildCallerWatchSendPending(t, s.jobManager, "child-gone", "job_gone_watch_tail")
	appendForwardedChildCallerWatchSendPending(t, s.jobManager, s.id, "job_same_session_watch_tail")
	appendForwardedChildCallerWatchSendPending(t, s.jobManager, "child-drop-error", "job_drop_error_watch_tail")
	failAppendN(s.jobManager, jobstore.EventWatchSendDropped, 1)
	s.renderUnreachableChildPendings(map[string]bool{"child-live": true})
	// Retry after the injected durable drop failure so the same pending is also
	// exercised through successful tombstoning and escalation.
	s.renderUnreachableChildPendings(map[string]bool{"child-live": true})

	now := s.jobManager.now()
	seedStableToolDelegate(t, s, "dlg_resumable_tail", "", now.Add(-2*time.Second), now.Add(-time.Second))
	resumableChildID := "child-dlg_resumable_tail"
	if !s.childResumable(resumableChildID) {
		t.Fatal("retained child fixture is not resumable")
	}
	appendForwardedChildTerminalPending(t, s.jobManager, "job_resumable_tail", resumableChildID)
	appendForwardedChildCallerWatchSendPending(t, s.jobManager, resumableChildID, "job_resumable_watch_tail")
	s.renderUnreachableChildPendings(nil)

	descriptor := stableToolDescriptor(s, "dlg_stop_gate_tail", "")
	lease := delegateLease{delegateID: "dlg_stop_gate_tail", generation: 1}
	packet := delegateStoppedTerminalPacket()
	s.delegateController.mu.Lock()
	_, err = s.delegateController.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: lease.delegateID, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		delegateControllerRunStartedEvent(lease.delegateID, lease.generation, delegatestore.TriggerInitial, now),
		delegatestore.Event{Kind: delegatestore.EventDelegateSubtreeStopRequested, DelegateID: lease.delegateID, SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: lease.delegateID}},
		delegateRunFinishedEvent(lease, delegatestore.OutcomeStopped, delegatestore.DispositionTerminalError, "stopped_by_parent", now, delegateDeliveryID(lease.delegateID, lease.generation), &packet),
	)
	s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	child := &Session{id: descriptor.ChildSessionID, jobManager: s.jobManager}
	stopped := &subagent{id: child.id, sess: child}
	if !s.childStopGated(child.id) {
		t.Fatal("synthetic stopped delegate did not close the child drive gate")
	}
	s.driveChildIfNotStopGated(stopped)
	s.subagents = newSubagentManager(nil, 0)
	s.subagents.track(stopped)
	s.driveChildrenWithUndeliveredAttention()
}

func appendForwardedChildTerminalPending(t *testing.T, jm *jobManager, jobID, childSessionID string) {
	t.Helper()
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		OwnerSessionID:   childSessionID,
		VisibleToSession: jm.sessionID,
		StartedAt:        &now,
		Command:          "true",
		Description:      "forwarded child shell",
	}); err != nil {
		t.Fatalf("append forwarded start %q: %v", jobID, err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "completed",
		EndedAt:     &now,
		TerminalGen: "gen-" + jobID,
	}); err != nil {
		t.Fatalf("append forwarded finish %q: %v", jobID, err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          now,
		JobID:       jobID,
		TerminalGen: "gen-" + jobID,
	}); err != nil {
		t.Fatalf("append forwarded pending %q: %v", jobID, err)
	}
}

func appendForwardedChildCallerWatchSendPending(t *testing.T, jm *jobManager, childSessionID, watchedJobID string) jobstore.WatchSendState {
	t.Helper()
	var state jobstore.WatchSendState
	for _, event := range restoredWatchSendPendingEvents(childSessionID, watchedJobID, runtimeMessageAliasCaller, jm.now()) {
		if event.WatchSend != nil {
			state = *event.WatchSend
		}
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("append forwarded caller watch-send pending %q: %v", watchedJobID, err)
		}
	}
	return state
}
