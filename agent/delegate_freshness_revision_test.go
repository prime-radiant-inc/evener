package agent

import (
	"context"
	"testing"
)

// TestFreshnessVerdictDoesNotSurviveAnEnvironmentSwap pins that an answer about
// one workspace cannot become the verdict for another.
//
// A swap installs the new environment and then clears the old verdict. A check
// already in flight, started against the workspace being left, finishes after
// that and would write its answer back — restoring a claim about a workspace the
// session no longer occupies. In the permissive direction that keeps the NEW
// workspace's fallback tool out of the roster, which is the defect this
// classification exists to prevent, arriving through the swap path.
//
// The assertion is on the revision guard, so the check never has to reach the
// filesystem: the verdict is published only for the environment it was computed
// for.
func TestFreshnessVerdictDoesNotSurviveAnEnvironmentSwap(t *testing.T) {
	t.Parallel()
	s := &Session{}

	// Hold the single-flight with a check for the CURRENT revision, so the waiter
	// under test occupies the join path rather than starting a new worker.
	done := make(chan struct{})
	s.freshnessCheckMu.Lock()
	startRevision := s.cliMCPEnvRevision
	s.freshnessCheck = &exclusionFreshnessCheck{done: done, revision: startRevision}
	s.freshnessCheckMu.Unlock()

	// The swap lands while that check is in flight: the revision moves and the old
	// verdict is dropped, exactly as invalidateCLIMCPClassification does.
	s.invalidateCLIMCPClassification()

	// The check now finishes, reporting a verdict about the OLD environment.
	s.freshnessCheckMu.Lock()
	s.freshnessCheck.fresh = true
	s.freshnessCheck = nil
	s.freshnessCheckMu.Unlock()
	close(done)

	// A refresh that read its revision before the swap and lands its result after
	// must not record it.
	if s.cliMCPEnvRevision == startRevision {
		t.Fatalf("fixture is wrong: the swap did not move the revision, so nothing would be discarded")
	}
	s.publishFreshnessForRevision(true, startRevision)

	s.delegateExclusionsMu.RLock()
	known := s.cliMCPEvidenceFreshKnown
	fresh := s.cliMCPEvidenceFresh
	s.delegateExclusionsMu.RUnlock()
	if known {
		t.Errorf("a verdict computed for a workspace the session left was published (fresh=%v); the new workspace must start unknown", fresh)
	}
}

// TestFreshnessVerdictIsPublishedForItsOwnRevision is the other direction: a
// result whose environment is still installed is recorded, so the guard is a
// revision check and not a refusal to ever publish.
func TestFreshnessVerdictIsPublishedForItsOwnRevision(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.publishFreshnessForRevision(true, s.cliMCPEnvRevision)

	s.delegateExclusionsMu.RLock()
	known := s.cliMCPEvidenceFreshKnown
	fresh := s.cliMCPEvidenceFresh
	s.delegateExclusionsMu.RUnlock()
	if !known || !fresh {
		t.Errorf("a verdict for the installed environment was discarded: known=%v fresh=%v", known, fresh)
	}
}

// TestJoinedFreshnessAnswerIsNotPublishedUnderAnotherRevision pins the JOINER's
// half of the same rule.
//
// A joiner reads the answer to a check it did not start, and that answer
// describes the environment its WORKER began in. A check can outlive a cancelled
// delegate call, and the session can switch worktrees before the next
// shared-workspace spawn: if the joiner published the answer under its own
// revision, a fresh verdict for the workspace the session left would strip a tool
// the new workspace provides from the next child's durable ceiling. That is the
// wrong direction — uncertainty must leave a name eligible, never remove it.
func TestJoinedFreshnessAnswerIsNotPublishedUnderAnotherRevision(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.cliMCPToolNames = map[string]bool{"svc__echo": true}

	// A check for the OLD workspace is still in flight. Its revision is the one
	// the session had before the swap below.
	workerRevision := s.cliMCPEnvRevision
	done := make(chan struct{})
	s.freshnessCheckMu.Lock()
	s.freshnessCheck = &exclusionFreshnessCheck{done: done, revision: workerRevision, fresh: true, settled: true}
	s.freshnessCheckMu.Unlock()

	// The session switches worktrees: a new revision, and the old verdict dropped.
	s.invalidateCLIMCPClassification()
	callerRevision := s.cliMCPEnvRevision
	if callerRevision == workerRevision {
		t.Fatal("fixture is wrong: the swap did not move the revision, so there is no mismatch to discard")
	}

	// A NEWER verdict the installed workspace recorded, which the stale answer
	// must leave standing rather than overwrite.
	s.publishFreshnessForRevision(false, callerRevision)

	// Let the old check finish, then JOIN it from the new revision. This is the
	// caller-side path: the answer is the worker's, not this caller's.
	close(done)
	s.refreshCLIEvidenceFreshnessContext(context.Background())

	s.delegateExclusionsMu.RLock()
	known := s.cliMCPEvidenceFreshKnown
	fresh := s.cliMCPEvidenceFresh
	s.delegateExclusionsMu.RUnlock()
	if !known {
		t.Error("the joined answer from the old workspace cleared the newer verdict the installed workspace had recorded")
	}
	if fresh {
		t.Error("the joined answer from the old workspace replaced the newer verdict: it was published under the caller's revision")
	}
}
