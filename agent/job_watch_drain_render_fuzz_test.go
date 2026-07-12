//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJobWatchDrainRenderTail drives the durable drain, restored-target, and
// frame-rendering tail through a real job manager and on-disk job store. The
// byte selects an ordering only; every replay covers the complete branch table.
func FuzzJobWatchDrainRenderTail(f *testing.F) {
	f.Add(byte(0))
	f.Add(byte(1))
	f.Fuzz(func(t *testing.T, order byte) {
		jm, err := newJobManager(t.TempDir(), "S", func(jobNotification) {})
		if err != nil {
			t.Fatal(err)
		}
		freezeClock(jm)
		jm.clock = agenttest.NewFakeClock()
		t.Cleanup(func() { _ = jm.store.Close() })
		s := &Session{id: "S", jobManager: jm}

		steps := []func(){
			func() { jdrExerciseClassifier(t, s) },
			func() { jdrExercisePendingAndRender(t, s) },
			func() { jdrExerciseGuardsAndFailures(t, s) },
		}
		if order&1 != 0 {
			steps[0], steps[2] = steps[2], steps[0]
		}
		for _, step := range steps {
			step()
		}
	})
}

func jdrExerciseClassifier(t *testing.T, s *Session) {
	t.Helper()
	trueValue, falseValue := true, false
	base := watchSendTargetResolver{
		sessionID: "S", hasJobManager: true,
		loadDelegates: func() (map[string]*jobstore.DelegateRecord, error) { return nil, errors.New("load") },
		findJobRecord: func(string) (*jobstore.JobRecord, error) { return nil, errors.New("missing") },
		assessResumable: func(*jobstore.JobRecord) delegateResumability {
			return delegateResumability{Resumable: false, Reason: "denied"}
		},
	}
	targets := []string{"caller", "", "parent", "plain", "job_bad", "dlg_load"}
	for _, target := range targets {
		classifyWatchSendDeliveryTarget(target, base)
	}
	classifyWatchSendDeliveryTarget("plain", watchSendTargetResolver{})

	delegateCases := []struct {
		d *jobstore.DelegateRecord
		r *jobstore.JobRecord
	}{
		{d: nil},
		{d: &jobstore.DelegateRecord{OwnerSessionID: "OTHER"}},
		{d: &jobstore.DelegateRecord{Resumable: true}},
		{d: &jobstore.DelegateRecord{Resumable: true, LatestJobID: "latest"}},
		{d: &jobstore.DelegateRecord{Resumable: true, CurrentJobID: "cur"}, r: &jobstore.JobRecord{OwnerSessionID: "OTHER"}},
		{d: &jobstore.DelegateRecord{Resumable: true, CurrentJobID: "cur"}, r: &jobstore.JobRecord{Type: jobstore.JobShell}},
		{d: &jobstore.DelegateRecord{Status: jobstore.DelegateNotResumable, CurrentJobID: "cur"}, r: &jobstore.JobRecord{Type: jobstore.JobDelegate}},
		{d: &jobstore.DelegateRecord{Resumable: true, CurrentJobID: "cur"}, r: &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusRunning}},
		{d: &jobstore.DelegateRecord{Resumable: true, CurrentJobID: "cur"}, r: &jobstore.JobRecord{Type: jobstore.JobDelegate}},
		{d: &jobstore.DelegateRecord{Resumable: true, CurrentJobID: "cur"}, r: &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &falseValue}},
		{d: &jobstore.DelegateRecord{Resumable: true, CurrentJobID: "cur"}, r: &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &trueValue}},
	}
	for _, tc := range delegateCases {
		r := base
		r.loadDelegates = func() (map[string]*jobstore.DelegateRecord, error) {
			return map[string]*jobstore.DelegateRecord{"dlg_x": tc.d}, nil
		}
		r.findJobRecord = func(string) (*jobstore.JobRecord, error) {
			if tc.r == nil {
				return nil, errors.New("missing")
			}
			return tc.r, nil
		}
		classifyWatchSendDeliveryTarget("dlg_x", r)
	}

	plainRecords := []*jobstore.JobRecord{
		{Type: jobstore.JobShell},
		{Type: jobstore.JobDelegate, Status: jobstore.StatusRunning},
		{Type: jobstore.JobDelegate},
		{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &falseValue},
		{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &trueValue},
	}
	for _, rec := range plainRecords {
		r := base
		r.findJobRecord = func(string) (*jobstore.JobRecord, error) { return rec, nil }
		classifyWatchSendDeliveryTarget("legacy", r)
		r.assessResumable = func(*jobstore.JobRecord) delegateResumability { return delegateResumability{Resumable: true} }
		classifyWatchSendDeliveryTarget("legacy", r)
	}
	for _, resumable := range []bool{false, true} {
		r := base
		r.findJobRecord = func(string) (*jobstore.JobRecord, error) {
			return &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusStopped, Reason: "runtime_lost"}, nil
		}
		r.assessResumable = func(*jobstore.JobRecord) delegateResumability {
			return delegateResumability{Resumable: resumable, Reason: "lost"}
		}
		classifyWatchSendDeliveryTarget("legacy-lost", r)
	}
	_, _ = s.classifyRestoredWatchSendTarget("caller")
	_ = (&Session{}).watchSendTargetResolver()
}

func jdrExercisePendingAndRender(t *testing.T, s *Session) {
	t.Helper()
	jm := s.jobManager
	key := jobstore.WatchSendKey{ResolvedWatchedIdentity: "missing", ResolvedSendTo: runtimeMessageAliasCaller, VisibleSessionID: "S"}
	state := &jobstore.WatchSendState{Key: key, UpdateSeq: 1, TriggerReason: "trigger"}
	cfg := &watchConfig{watchID: "w", send: &watchSendArgs{Message: "hello", IncludeExcerpt: true}, pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: state}, pendingOrder: []jobstore.WatchSendKey{key, {}}}
	delegateKey := jobstore.WatchSendKey{ResolvedWatchedIdentity: "missing", ResolvedSendTo: "dlg_missing", VisibleSessionID: "S"}
	delegateState := &jobstore.WatchSendState{Key: delegateKey, UpdateSeq: 2, TriggerReason: "delegate"}
	delegateCfg := &watchConfig{watchID: "wd", send: &watchSendArgs{To: "dlg_missing"}, pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{delegateKey: delegateState}, pendingOrder: []jobstore.WatchSendKey{delegateKey}}
	jm.mu.Lock()
	jm.watches[watchKey{VisibleSessionID: "S", Target: "missing"}] = cfg
	jm.watches[watchKey{VisibleSessionID: "S", Target: "missing-delegate"}] = delegateCfg
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[cfg] = true
	jm.terminalFlush[delegateCfg] = true
	jm.mu.Unlock()
	jm.pendingWatchSendDeliveries(func(st *jobstore.WatchSendState) bool { return st.UpdateSeq == 1 })
	jm.pendingWatchSendDeliveries(func(*jobstore.WatchSendState) bool { return false })
	jm.hasPendingWatchSends()
	jm.enqueue = func(jobNotification) {}
	s.enqueueOwnCallerWatchSendTokens()
	failAppendN(jm, jobstore.EventWatchSendDropped, 1)
	_, _ = s.drainJobManagerWatchSends(context.Background(), jm, "child")
	_, _ = s.drainJobManagerWatchSends(context.Background(), jm, "")

	badKey := jobstore.WatchSendKey{ResolvedWatchedIdentity: "bad", ResolvedSendTo: "job_bad", VisibleSessionID: "S"}
	badState := &jobstore.WatchSendState{Key: badKey, UpdateSeq: 3}
	badCfg := &watchConfig{pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{badKey: badState}, pendingOrder: []jobstore.WatchSendKey{badKey}}
	jm.mu.Lock()
	jm.watches[watchKey{VisibleSessionID: "S", Target: "bad"}] = badCfg
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
	jm.enqueueWatchNotifications(nil)
	routed := false
	jm.enqueueWatchNotifications([]jobNotification{{receiverNotify: func(jobNotification) { routed = true }}})
	if !routed {
		t.Fatal("receiver notification was not routed")
	}
	jm.enqueueWatchNotifications([]jobNotification{{receiverSessionID: "other"}, {receiverSessionID: "S"}})
	jm.mu.Lock()
	jm.closing = true
	jm.mu.Unlock()
	jm.enqueueWatchNotifications([]jobNotification{{Reason: "closed"}})
	jm.mu.Lock()
	jm.closing = false
	delete(jm.watches, watchKey{VisibleSessionID: "S", Target: "missing"})
	delete(jm.watches, watchKey{VisibleSessionID: "S", Target: "missing-delegate"})
	delete(jm.watches, watchKey{VisibleSessionID: "S", Target: "bad"})
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

	closed, err := newJobManager(t.TempDir(), "C", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	freezeClock(closed)
	closed.clock = agenttest.NewFakeClock()
	if err := closed.store.Close(); err != nil {
		t.Fatal(err)
	}
	cs := &Session{id: "C", jobManager: closed}
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
	s.renderUnreachableChildPendings(map[string]bool{"child-live": true})

	now := s.jobManager.now()
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "job_stop_gate_tail", Type: jobstore.JobDelegate,
		OwnerSessionID: s.id, VisibleToSession: s.id, TranscriptRef: encodeRef("", "child-stop-gated"), StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: now, JobID: "job_stop_gate_tail", Status: jobstore.StatusCancelled,
		Reason: "stopped_by_parent", EndedAt: &now, TerminalGen: "gen-stop-gate-tail",
	}); err != nil {
		t.Fatal(err)
	}
	child := &Session{id: "child-stop-gated", jobManager: s.jobManager}
	stopped := &subagent{id: child.id, sess: child}
	if !s.childStopGated(child.id) {
		t.Fatal("synthetic stopped delegate did not close the child drive gate")
	}
	s.driveChildIfNotStopGated(stopped)
	s.subagents = newSubagentManager(nil)
	s.subagents.track(stopped)
	s.driveChildrenWithUndeliveredAttention()
}
