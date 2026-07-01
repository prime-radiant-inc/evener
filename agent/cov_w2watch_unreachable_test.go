package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// w2watch_pendingReasons returns the reasons of the parent's currently enqueued
// job notifications.
func w2watch_pendingReasons(s *Session) []string {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	out := make([]string, 0, len(s.pendingJobNotifs))
	for _, n := range s.pendingJobNotifs {
		out = append(out, n.Reason)
	}
	return out
}

func w2watch_hasUnreachableEscalation(s *Session) bool {
	for _, r := range w2watch_pendingReasons(s) {
		if strings.HasPrefix(r, "child unreachable:") {
			return true
		}
	}
	return false
}

// A child owner that IS a live subagent this pass is driven, not rendered: both
// its forwarded terminal pending and its caller watch-send pending are skipped by
// renderUnreachableChildPendings.
func TestW2Watch_renderUnreachableChildPendingsSkipsLiveChild(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = parentJM.store.Close() })
	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	appendForwardedChildTerminalPending(t, parentJM, "job_live_work", "GONE")
	appendForwardedChildCallerWatchSendPending(t, parentJM, "GONE", "job_live_watch")

	parent.renderUnreachableChildPendings(map[string]bool{"GONE": true})

	if w2watch_hasUnreachableEscalation(parent) {
		t.Fatalf("live child escalated: %v", w2watch_pendingReasons(parent))
	}
	if pending := loadWatchSendRecord(t, parentJM).Pending; len(pending) != 1 {
		t.Fatalf("watch-send pending for a live child = %+v, want left pending (not dropped)", pending)
	}
}

// A child owner whose parent delegate record is still resumable is left for its
// own future drive/resume turn, not escalated: both rails skip it.
func TestW2Watch_renderUnreachableChildPendingsSkipsResumableChild(t *testing.T) {
	s := newDelegateRestorePreflightSession(t, nil)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef: %v", err)
	}
	// Guard: the fixture must actually project resumable, else this test would be
	// exercising the escalation arm instead of the resumable-skip arm.
	if !s.childResumable(childID) {
		t.Fatalf("seeded delegate for %q is not resumable; fixture drifted", childID)
	}

	appendForwardedChildTerminalPending(t, s.jobManager, "job_res_work", childID)
	appendForwardedChildCallerWatchSendPending(t, s.jobManager, childID, "job_res_watch")

	s.renderUnreachableChildPendings(nil)

	if w2watch_hasUnreachableEscalation(s) {
		t.Fatalf("resumable child escalated: %v", w2watch_pendingReasons(s))
	}
	if pending := loadWatchSendRecord(t, s.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("watch-send pending for a resumable child = %+v, want left pending (not dropped)", pending)
	}
}

// When the fallback drop-tombstone append fails for an unreachable child's
// caller watch-send, the render emits a warning and moves on (the pending is not
// silently lost — the failed append leaves the durable pending intact for retry).
func TestW2Watch_renderUnreachableChildPendingsDropAppendError(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() { _ = parentJM.store.Close() })
	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}

	appendForwardedChildCallerWatchSendPending(t, parentJM, "GONE", "job_gone_watch")

	// Fault the WatchSendDropped append that the fallback would write. GONE is not
	// live and not resumable, so the render reaches the drop.
	failAppendN(parentJM, jobstore.EventWatchSendDropped, 1)

	parent.renderUnreachableChildPendings(nil)

	// The durable pending must remain (the drop tombstone was not persisted).
	if pending := loadWatchSendRecord(t, parentJM).Pending; len(pending) != 1 {
		t.Fatalf("watch-send pending after failed drop append = %+v, want left intact for retry", pending)
	}
}
