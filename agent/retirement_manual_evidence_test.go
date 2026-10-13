package agent

import (
	"sync"
	"testing"
	"time"
)

// A manual retire that arrives while evidence-only work holds the process
// beside the namer is refused at once: the namer cannot clear that work, so
// waiting it out would only delay a refusal the retire already owes (#4055).
// Here the evidence is an armed job notification, which holds no lease.
func TestManualRetireDoesNotWaitForEvidenceBesideTheNamer(t *testing.T) {
	t.Parallel()
	root, c, clk, _ := blockedNamerSession(t)
	root.pendingJobNotifsMu.Lock()
	root.pendingJobNotifs = append(root.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-4055-evidence",
		Status: "completed",
	})
	root.pendingJobNotifsMu.Unlock()

	done := startManualClaim(c)
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire with an armed notification beside the namer = claim %v, %v; want a refusal", got.claim, got.err)
		}
		if !hasRetirementBlockerFor(got.snapshot.Blockers, "notification", root.ID()) {
			t.Fatalf("refusal snapshot lost the armed notification: %+v", got.snapshot.Blockers)
		}
	// TRIPWIRE: the refusal returns at once; 30s only fires if the retire waited
	// on the fake clock for the namer.
	case <-time.After(30 * time.Second):
		t.Fatalf("manual retire waited for the namer despite an armed notification beside it")
	}
	if n := clk.BlockedCount(); n != 0 {
		t.Fatalf("manual retire parked %d timer(s) on the namer despite evidence beside it", n)
	}
}

// naming.pending and an active goal both report the "autonomous" category, so
// the retire's wait decision must drop only the namer's own contribution. An
// active goal beside the namer therefore refuses at once, not after the wait.
func TestManualRetireDoesNotWaitForAnActiveGoalBesideTheNamer(t *testing.T) {
	t.Parallel()
	root, c, clk, _ := blockedNamerSession(t)
	root.getOrCreateGoalStore().Set("finish the job", time.Now())

	done := startManualClaim(c)
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire with an active goal beside the namer = claim %v, %v; want a refusal", got.claim, got.err)
		}
	// TRIPWIRE: the refusal returns at once; 30s only fires if the retire waited
	// on the fake clock for the namer.
	case <-time.After(30 * time.Second):
		t.Fatalf("manual retire waited for the namer despite an active goal beside it")
	}
	if n := clk.BlockedCount(); n != 0 {
		t.Fatalf("manual retire parked %d timer(s) on the namer despite an active goal beside it", n)
	}
}

// The wait pre-check must not turn a stale read into a skip: namingExcludedEvidence
// runs the evidence read without the controller lock and then discards it if the
// root or generation moved, so evidence read from a root the controller no longer
// owns is never a reason to skip the wait (#4055).
func TestRetirementWaitPreCheckDiscardsSupersededRoot(t *testing.T) {
	t.Parallel()
	root, c, _, _ := blockedNamerSession(t)
	root.pendingJobNotifsMu.Lock()
	root.pendingJobNotifs = append(root.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-4055-stale",
		Status: "completed",
	})
	root.pendingJobNotifsMu.Unlock()

	c.mu.Lock()
	capturedRoot, capturedGeneration := c.root, c.generation
	c.mu.Unlock()

	// A swap after the capture supersedes that pair; the evidence read above
	// returns a real obligation on capturedRoot, which must not count.
	replacement := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
	if err := c.AttachRoot(replacement); err != nil {
		t.Fatal(err)
	}
	if c.namingExcludedEvidence(capturedRoot, capturedGeneration) {
		t.Fatal("wait pre-check kept a superseded root's evidence as a reason to skip the wait")
	}
}

// The shared tree's owner mutex is held across a durable append, so the wait
// pre-check cannot read the tree then. An unread tree is not evidence: the retire
// must keep today's wait rather than skip on the fail-closed "delegate"
// placeholder the decision snapshot uses (#4055).
func TestManualRetireKeepsWaitingWhenTheTreeCannotBeRead(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	tree := root.delegateController
	if tree == nil {
		t.Fatal("fixture has no shared delegate tree; the wait pre-check tree path is not exercised")
	}
	tree.mu.Lock()
	unlockTree := sync.OnceFunc(tree.mu.Unlock)
	defer unlockTree()
	done := startManualClaim(c)
	// TRIPWIRE: an unread tree must not skip the wait; the helper fails if the
	// retire answers before parking its timer on the namer.
	awaitManualClaimWaiting(t, clk, done)
	unlockTree()
	release()
	select {
	case got := <-done:
		if got.claim != nil {
			_ = c.Abort(got.claim, "")
		}
	// TRIPWIRE: once the tree is readable and the namer released, the retire must
	// return; 30s only fires on a hang.
	case <-time.After(30 * time.Second):
		t.Fatal("manual retire did not return after the tree became readable")
	}
}

// The pre-check runs its evidence read without the controller lock, and the
// evidenceGate seam lets a test move the controller identity inside that read.
// A root swapped there must be discarded, so the retire keeps waiting instead of
// skipping on the superseded root's evidence (#4055).
func TestManualRetireDiscardsASupersededReadWhenItWaits(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	root.pendingJobNotifsMu.Lock()
	root.pendingJobNotifs = append(root.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-4055-raced",
		Status: "completed",
	})
	root.pendingJobNotifsMu.Unlock()

	replacement := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
	c.evidenceGate = func() {
		if err := c.AttachRoot(replacement); err != nil {
			t.Errorf("attach during the pre-check read: %v", err)
		}
	}
	done := startManualClaim(c)
	// TRIPWIRE: the superseded read must not skip the wait; the helper fails if
	// the retire answers before parking its timer on the namer.
	awaitManualClaimWaiting(t, clk, done)
	release()
	select {
	case got := <-done:
		if got.claim != nil {
			_ = c.Abort(got.claim, "")
		}
	// TRIPWIRE: with the namer released the retire must return; 30s only fires on
	// a hang.
	case <-time.After(30 * time.Second):
		t.Fatal("manual retire did not return after the namer settled")
	}
}

// The root runtime is read without the tree's owner mutex, so tree contention
// must not hide the root's own obligations: with an armed notification on the
// root beside the namer, the retire refuses at once even while the tree lock is
// held, and the refusal names the notification (#4055).
func TestManualRetireSkipsTheWaitWhenTreeContentionHidesOnlyTreeEvidence(t *testing.T) {
	t.Parallel()
	root, c, clk, _ := blockedNamerSession(t)
	root.pendingJobNotifsMu.Lock()
	root.pendingJobNotifs = append(root.pendingJobNotifs, jobNotification{
		Kind:   jobNotificationKindTerminal,
		JobID:  "job-4055-contended",
		Status: "completed",
	})
	root.pendingJobNotifsMu.Unlock()

	tree := root.delegateController
	if tree == nil {
		t.Fatal("fixture has no shared delegate tree; the wait pre-check tree path is not exercised")
	}
	tree.mu.Lock()
	unlockTree := sync.OnceFunc(tree.mu.Unlock)
	defer unlockTree()
	done := startManualClaim(c)
	select {
	case got := <-done:
		unlockTree()
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire under tree contention beside a root notification = claim %v, %v; want a refusal", got.claim, got.err)
		}
		if !hasRetirementBlockerFor(got.snapshot.Blockers, "notification", root.ID()) {
			t.Fatalf("refusal snapshot lost the root notification under tree contention: %+v", got.snapshot.Blockers)
		}
		if n := clk.BlockedCount(); n != 0 {
			t.Fatalf("manual retire parked %d timer(s) under tree contention despite a root notification", n)
		}
	// TRIPWIRE: the refusal returns at once; 30s only fires if the retire waited
	// on the fake clock for the namer.
	case <-time.After(30 * time.Second):
		t.Fatalf("manual retire waited out the namer under tree contention beside a root notification")
	}
}
