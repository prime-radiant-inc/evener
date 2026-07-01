package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestW3Dlg_SendRunningInvalidTranscriptRef covers the decodeRef guard: a
// running-delegate steer whose rec carries a malformed transcript_ref is
// rejected before any subagent lookup.
func TestW3Dlg_SendRunningInvalidTranscriptRef(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	rec := &jobstore.JobRecord{
		JobID:         "job_bogus",
		DelegateID:    "dlg_bogus",
		Type:          jobstore.JobDelegate,
		TranscriptRef: "not-a-ref",
	}
	res := parent.sendRunningDelegateMessage("dlg_bogus", "hi", rec, false, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "invalid transcript_ref") {
		t.Fatalf("err = %v, want invalid transcript_ref", res.Err)
	}
}

// TestW3Dlg_SendRunningChildNotRetained covers the retained-runtime guard: a
// steer whose child session is not in the subagent map is rejected.
func TestW3Dlg_SendRunningChildNotRetained(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	rec := &jobstore.JobRecord{
		JobID:         "job_x",
		DelegateID:    "dlg_x",
		Type:          jobstore.JobDelegate,
		TranscriptRef: encodeRef("", "child-not-tracked"),
	}
	res := parent.sendRunningDelegateMessage("dlg_x", "hi", rec, false, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "is not retained") {
		t.Fatalf("err = %v, want not retained", res.Err)
	}
}

// TestW3Dlg_SendRunningFromWatchNilJobManager covers the fromWatch job-manager
// lookup guard: a watch-originated steer against a session whose job manager is
// unavailable is surfaced as a hard failure.
func TestW3Dlg_SendRunningFromWatchNilJobManager(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	rec := &jobstore.JobRecord{
		JobID:         "job_live",
		DelegateID:    "dlg_live",
		Type:          jobstore.JobDelegate,
		TranscriptRef: encodeRef("", child.ID()),
	}

	savedJM := parent.jobManager
	parent.jobManager = nil
	res := parent.sendRunningDelegateMessage("dlg_live", "hi", rec, true, nil)
	parent.jobManager = savedJM

	if res.Err == nil {
		t.Fatalf("err = nil, want job manager unavailable failure")
	}
}

// TestW3Dlg_SendRunningFromWatchRunningButRuntimeJobGone covers the fromWatch
// inconsistency guard: a child marked sub.running whose runtime job record has
// vanished (not in jm.running, not driving) is rejected as not-live.
func TestW3Dlg_SendRunningFromWatchRunningButRuntimeJobGone(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	rec := &jobstore.JobRecord{
		JobID:         "job_missing_runtime",
		DelegateID:    "dlg_live",
		Type:          jobstore.JobDelegate,
		TranscriptRef: encodeRef("", child.ID()),
	}
	res := parent.sendRunningDelegateMessage("dlg_live", "hi", rec, true, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "runtime job is not live") {
		t.Fatalf("err = %v, want runtime job is not live", res.Err)
	}
}

// TestW3Dlg_SendRunningNotAcceptingMessages covers the non-watch undelivered
// path: a running child that rejects the steer (closed session) surfaces a
// not_controllable hard failure rather than a retryable watch-busy class.
func TestW3Dlg_SendRunningNotAcceptingMessages(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	rec := &jobstore.JobRecord{
		JobID:         "job_closed_child",
		DelegateID:    "dlg_live",
		Type:          jobstore.JobDelegate,
		TranscriptRef: encodeRef("", child.ID()),
	}
	child.mu.Lock()
	child.state = SessionClosed
	child.mu.Unlock()

	res := parent.sendRunningDelegateMessage("dlg_live", "hi", rec, false, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "not accepting messages") {
		t.Fatalf("err = %v, want not accepting messages", res.Err)
	}
}
