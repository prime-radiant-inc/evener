package agent

import (
	"context"
	"testing"
	"time"
)

// TestFreshnessProbeDoesNotJoinAnotherRevisionsCheck pins that coalescing is
// scoped to ONE environment revision.
//
// The defect: the sampler joined whatever occupied the slot, waited out the
// caller's whole budget, and only then discarded the answer at the revision
// check. A probe stalled on the workspace the session LEFT therefore charged
// every request in the workspace it moved TO — twelve seconds each, for an
// answer about a workspace that no longer exists — and the installed workspace
// could not establish its own verdict while that read stayed stuck.
//
// The distinction is the same one the publication guard makes: a revision gates
// the whole answer, the join as much as the publish. Sharing work on one
// question is the point of the single-flight, and a check for another
// workspace's config is not that question.
func TestFreshnessProbeDoesNotJoinAnotherRevisionsCheck(t *testing.T) {
	t.Parallel()
	s := &Session{}

	// A worker for the OLD workspace is in flight and will never answer, which
	// is what a stalled mount looks like.
	staleRevision := s.cliMCPEnvRevision
	occupied := make(chan struct{})
	s.freshnessCheckMu.Lock()
	s.freshnessCheck = &exclusionFreshnessCheck{done: occupied, revision: staleRevision}
	s.freshnessCheckMu.Unlock()
	t.Cleanup(func() { close(occupied) })

	// The session switches worktrees, so the installed revision moves.
	s.invalidateCLIMCPClassification()
	installed := s.cliMCPEnvRevision
	if installed == staleRevision {
		t.Fatal("fixture is wrong: the swap did not move the revision, so there is nothing to distinguish")
	}

	// The probe must not wait on the stale worker. A join through the occupied
	// slot blocks here until the cleanup runs, which the tripwire reports as the
	// wait the finding names.
	returned := make(chan uint64, 1)
	go func() {
		_, settled, answerRevision := s.sampleExclusionFreshness(
			context.Background(),
			cliMCPClassificationEvidence{},
			nil, // EvidenceCurrent answers false for a nil env, so the worker settles
			installed,
		)
		if !settled {
			returned <- 0
			return
		}
		returned <- answerRevision
	}()

	select {
	case got := <-returned:
		if got != 0 && got != installed {
			t.Errorf("the probe answered for revision %d, want the installed %d", got, installed)
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: the join crossed revisions
		t.Fatal("a probe for the installed workspace waited on a check for the workspace the session left")
	}
}
