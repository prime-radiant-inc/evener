package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
)

// TestW3Dlg_CreateDelegateTrackLaunchClosingSession covers the launch-failure
// cleanup arm of createDelegate: when the session is closing after the delegate
// job is attached, the launch fails, the started job is finalized, and a
// start_failed result is returned.
func TestW3Dlg_CreateDelegateTrackLaunchClosingSession(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	sess.mu.Lock()
	sess.closing = true
	sess.mu.Unlock()

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "closing session",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "session is closed") {
		t.Fatalf("err = %v, want session is closed", res.Err)
	}
	if res.Status != jobstore.StatusFailed || res.Reason != "start_failed" {
		t.Fatalf("result = %+v, want failed/start_failed", res)
	}
	if res.JobID == "" {
		t.Fatalf("result = %+v, want a started job id to report", res)
	}
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusFailed {
		t.Fatalf("attached job rec = %+v, want finalized failed", rec)
	}
}

// TestW3Dlg_SendCallerMarksWatchCallbackDelivered covers the watch-delivery
// bookkeeping arm of the caller alias: when a caller send is delivered during a
// watch-delivery turn, the current turn is marked as having delivered its
// callback.
func TestW3Dlg_SendCallerMarksWatchCallbackDelivered(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	sess.cfg.spawn.parentSteerDelivered = func(string, *provenance.Causal, string) bool { return true }
	sess.setActiveEntryKind(EntryWatchDelivery)

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "advisory during watch delivery",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if !res.Delivered || res.Action != "delivered" || res.MessageType != "runtime" {
		t.Fatalf("result = %+v, want delivered runtime alias", res)
	}
	if !sess.watchCallbackDeliveredForCurrentTurn() {
		t.Fatal("watch callback not marked delivered for current turn")
	}
}

// TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume covers the
// retained-running lookup failure path: when the retained child is marked
// running but findRunningDelegateByTranscriptRef fails, delegate_send falls
// through to the established resume path instead of hard-failing.
func TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("resumed complete") },
	}}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, sess)
	childID := rec.DelegateRestore.ChildSessionID

	liveChild := newDelegateTestSession(t, c)
	sess.subagents.track(&subagent{
		id:      childID,
		sess:    liveChild,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	})
	sess.cfg.testOnly.delegateSend.findRunning = func(*jobManager, string) (*jobstore.JobRecord, error) {
		return nil, errors.New("running lookup fault")
	}
	sess.cfg.testOnly.delegateSend.beforePostState = func(sub *subagent) {
		sub.mu.Lock()
		sub.running = false
		sub.mu.Unlock()
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  rec.DelegateID,
		Message: "resume",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "started" || res.StartedJobID == "" || res.StartedJobID == rec.JobID || res.DelegateID != rec.DelegateID || res.Status != jobstore.StatusRunning {
		t.Fatalf("result = %+v, want resumed delegate start", res)
	}
	waitForShellDone(t, sess.jobManager, res.StartedJobID)
	started := loadShellRecord(t, sess.jobManager, res.StartedJobID)
	output, _, _, err := sess.jobManager.readOutput(res.StartedJobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read resumed output: %v", err)
	}
	if started.Status != jobstore.StatusCompleted || !strings.Contains(output, "resumed complete") {
		t.Fatalf("started record = %+v, output should contain resumed completion", started)
	}
}

// TestW3Dlg_SendStoreRunningNotInRuntimeStaysNonResumable covers the
// observed-terminal reconciliation guard: a delegate whose latest job is
// Running in the store but absent from the live runtime finalizes to nothing
// (the run is gone) and is then rejected as still non-terminal/running.
func TestW3Dlg_SendStoreRunningNotInRuntimeStaysNonResumable(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateRestorePreflightSession(t, c)

	childID, _ := seedRetainedChildSessionWithWorkingDir(t, sess)
	delegateID := jobstore.NewDelegateID()
	generation := jobstore.NewDelegateGeneration()
	jobID := jobstore.NewJobID(sess.ID())
	now := time.Now().UTC()
	ref := encodeRef("", childID)
	if err := sess.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: delegateID,
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   childID,
			TranscriptRef:    ref,
			OwnerSessionID:   sess.ID(),
			VisibleSessionID: sess.ID(),
			Generation:       generation,
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate created: %v", err)
	}
	if err := sess.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		DelegateID:       delegateID,
		Type:             jobstore.JobDelegate,
		Task:             "observed running",
		OwnerSessionID:   sess.ID(),
		VisibleToSession: sess.ID(),
		StartedAt:        &now,
		TranscriptRef:    ref,
	}); err != nil {
		t.Fatalf("append delegate started: %v", err)
	}

	liveChild := newTestSession(t)
	sess.subagents.track(&subagent{
		id:     childID,
		sess:   liveChild,
		status: SubagentRunning,
		done:   make(chan struct{}),
	})

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  delegateID,
		Message: "steer",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "has status") {
		t.Fatalf("err = %v, want target_not_resumable has status running", res.Err)
	}
}
