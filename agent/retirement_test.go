package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
)

// Removing the active-operation barrier would let a claim overtake admitted input.
func TestRetirementAdmissionWinsWithoutWaiting(t *testing.T) {
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	release, err := c.BeginMutation(root.ID(), "input")
	if err != nil {
		t.Fatal(err)
	}
	claim, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil || state.Phase != "resident" || len(state.Blockers) == 0 {
		t.Fatalf("admitted input was not a blocker: claim=%v state=%+v", claim, state)
	}
	release()
	release() // release is idempotent, including concurrent cancellation.
	claim, _, err = c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	if _, err := c.BeginMutation(root.ID(), "input"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("claim-first admission: %v", err)
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
	release, err = c.BeginMutation(root.ID(), "input")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func retirementTestController(t *testing.T) (*RetirementController, *Session) {
	t.Helper()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(root.Close)
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	return c, root
}

// A borrowed handler must finish before teardown, but must not delay Commit or
// allow new admission after Commit. Releasing twice must not drain other readers.
func TestRetirementBorrowedReadersDrainAfterCommit(t *testing.T) {
	t.Parallel()
	c, root := retirementTestController(t)
	release, err := c.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	readerHeld := make(chan struct{})
	finishRead := make(chan struct{})
	readerFinished := make(chan struct{})
	go func() {
		close(readerHeld)
		<-finishRead
		release()
		release()
		close(readerFinished)
	}()
	<-readerHeld
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim with reader: %v %v", claim, err)
	}
	preparingRelease, err := c.Borrow()
	if err != nil {
		t.Fatalf("preparing read: %v", err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginMutation(root.ID(), "input"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("mutation after commit: %v", err)
	}
	if _, err := c.Borrow(); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("borrow after commit: %v", err)
	}
	// An already-expired context proves the drain is still pending without a sleep.
	expired, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	if err := c.DrainReaders(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain held reader: %v", err)
	}
	close(finishRead)
	<-readerFinished
	if err := c.DrainReaders(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("duplicate release drained second reader: %v", err)
	}
	var releases sync.WaitGroup
	for range 8 {
		releases.Go(preparingRelease)
	}
	releases.Wait()
	drained := make(chan error, 1)
	go func() { drained <- c.DrainReaders(context.Background()) }()
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Phase; got != "retiring" {
		t.Fatalf("phase = %q", got)
	}
}

// Holding outer work while a claim attempts to win must not prevent nested
// admission. Duplicate blocker projection must not collapse the actual leases.
func TestRetirementNestedMutationsKeepIndependentLeases(t *testing.T) {
	t.Parallel()
	c, root := retirementTestController(t)
	outer, err := c.BeginMutation(root.ID(), "input")
	if err != nil {
		t.Fatal(err)
	}
	claimAttempted := make(chan struct{})
	go func() {
		defer close(claimAttempted)
		if claim, _, err := c.TryClaim(true); err != nil || claim != nil {
			t.Errorf("claim during outer lease: %v %v", claim, err)
		}
	}()
	<-claimAttempted
	inner, err := c.BeginMutation(root.ID(), "input")
	if err != nil {
		t.Fatal(err)
	}
	third, err := c.BeginMutation(root.ID(), "admission")
	if err != nil {
		t.Fatal(err)
	}
	want := []RetirementBlocker{{Category: "admission", SessionID: root.ID()}, {Category: "input", SessionID: root.ID()}}
	state := c.Snapshot()
	if !reflect.DeepEqual(state.Blockers, want) {
		t.Fatalf("sorted distinct blockers: got %+v want %+v", state.Blockers, want)
	}
	state.Blockers[0].Category = "corrupt"
	if got := c.Snapshot().Blockers; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot aliases controller: %+v", got)
	}
	var releases sync.WaitGroup
	for range 8 {
		releases.Go(outer)
	}
	releases.Wait()
	third()
	if claim, state, err := c.TryClaim(true); err != nil || claim != nil || len(state.Blockers) != 1 || state.Blockers[0].Category != "input" {
		t.Fatalf("outer release lost inner lease: %v %+v %v", claim, state, err)
	}
	inner()
	if claim, _, err := c.TryClaim(true); err != nil || claim == nil {
		t.Fatalf("claim after all releases: %v %v", claim, err)
	}
}

// TestRetirementSettledBlockerIsNotReportedAsCurrent is the regression test
// for the cached-blocker defect: a refused claim's blockers are evidence for
// that attempt, not controller state that outlives it. Once the obligation
// settles, a status snapshot must not keep presenting the old refusal as
// current evidence.
func TestRetirementSettledBlockerIsNotReportedAsCurrent(t *testing.T) {
	t.Parallel()
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)

	// A queued input is an evidence obligation that is not an admission lease:
	// it forces the full evidence path (rather than the len(c.active) early
	// return) without any live BeginMutation blocker to mask it.
	root.mu.Lock()
	root.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	root.mu.Unlock()

	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("queued input did not refuse the claim: claim=%v err=%v state=%+v", claim, err, state)
	}
	if !hasRetirementBlocker(state.Blockers, "input") {
		t.Fatalf("refusal lost the queued-input blocker: %+v", state)
	}

	// Settle the obligation.
	root.mu.Lock()
	root.inputQueue = nil
	root.mu.Unlock()

	if snap := c.Snapshot(); hasRetirementBlocker(snap.Blockers, "input") {
		t.Fatalf("settled input blocker still reported as current: %+v", snap.Blockers)
	}
}

func hasRetirementBlocker(blockers []RetirementBlocker, category string) bool {
	for _, blocker := range blockers {
		if blocker.Category == category {
			return true
		}
	}
	return false
}

func hasRetirementBlockerFor(blockers []RetirementBlocker, category, sessionID string) bool {
	for _, blocker := range blockers {
		if blocker.Category == category && blocker.SessionID == sessionID {
			return true
		}
	}
	return false
}

// TestRetirementEarlyReturnDoesNotPresentResolvedBlocker is the regression test
// for the stale early-return snapshot: a refused claim's blockers are evidence
// for that attempt, not controller state that a later attempt inherits. When a
// subsequent TryClaim early-returns while work is admitted, it must present the
// live obligation set — not an obligation that has since resolved. The Hub
// resident UI renders RetirementSnapshot.Blockers verbatim, so the observable
// consequence is asserted on the returned snapshot content.
func TestRetirementEarlyReturnDoesNotPresentResolvedBlocker(t *testing.T) {
	t.Parallel()
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)

	// A queued input is a real evidence obligation (not an admission lease), so
	// the first attempt runs the full evidence path and refuses.
	root.mu.Lock()
	root.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	root.mu.Unlock()
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("queued input did not refuse the claim: claim=%v err=%v state=%+v", claim, err, state)
	}
	if !hasRetirementBlocker(state.Blockers, "input") {
		t.Fatalf("refusal lost the queued-input blocker: %+v", state)
	}

	// The obligation resolves without any further claim attempt.
	root.mu.Lock()
	root.inputQueue = nil
	root.mu.Unlock()

	// Admitted work forces the next manual attempt down the early-return gate
	// (len(c.active) != 0) instead of re-running evidence.
	release, err := c.BeginMutation(root.ID(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, snapshot, err := c.TryClaim(true)
	if err != nil {
		t.Fatalf("early-return claim: %v", err)
	}
	if hasRetirementBlocker(snapshot.Blockers, "input") {
		t.Fatalf("early return re-presented a resolved obligation: %+v", snapshot.Blockers)
	}
	if !hasRetirementBlocker(snapshot.Blockers, "turn") {
		t.Fatalf("early return dropped the live obligation: %+v", snapshot.Blockers)
	}
}

// A refusal recorded against one root must not surface for a later root. Refusal
// evidence belongs to the attempt that produced it and to the generation it was
// read against; AttachRoot changes the generation, so a new root's snapshot must
// present only the new root's current obligations — never the previous root's
// stale refusal (roborev r12, the different-generation/root leak).
func TestRetirementEarlyReturnDoesNotLeakPreviousRootRefusal(t *testing.T) {
	t.Parallel()
	first := newQueuePersistTestSession(t, t.TempDir())
	defer first.Close()
	c := retirementEvidenceController(t, first)

	// A queued input is a real evidence obligation that is not an admission
	// lease, so the first attempt runs the full evidence path and refuses.
	first.mu.Lock()
	first.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	first.mu.Unlock()
	claim, state, err := c.TryClaim(true)
	if err != nil || claim != nil {
		t.Fatalf("queued input did not refuse the claim: claim=%v err=%v state=%+v", claim, err, state)
	}
	if !hasRetirementBlocker(state.Blockers, "input") {
		t.Fatalf("refusal lost the queued-input blocker: %+v", state)
	}

	// Publish a new root. The first root's refusal must not follow it.
	second := newQueuePersistTestSession(t, t.TempDir())
	defer second.Close()
	if err := c.AttachRoot(second); err != nil {
		t.Fatal(err)
	}

	// Admitted work on the new root forces the next manual attempt down the
	// early-return gate instead of re-running evidence.
	release, err := c.BeginMutation(second.ID(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, snapshot, err := c.TryClaim(true)
	if err != nil {
		t.Fatalf("early-return claim: %v", err)
	}
	if hasRetirementBlocker(snapshot.Blockers, "input") {
		t.Fatalf("previous root's refusal leaked into the new root: %+v", snapshot.Blockers)
	}
	if !hasRetirementBlocker(snapshot.Blockers, "turn") {
		t.Fatalf("new root's live obligation missing: %+v", snapshot.Blockers)
	}
	for _, blocker := range snapshot.Blockers {
		if blocker.SessionID == first.ID() {
			t.Fatalf("snapshot presents an obligation owned by the previous root: %+v", snapshot.Blockers)
		}
	}
}

// TestRetirementEarlyReturnReportsFreshNonLeaseObligation pins the property the
// two early-return regression tests above leave unpinned: they assert the absence
// of a stale "input" blocker plus the presence of "turn", but "turn" comes from
// the BeginMutation active lease, so they pass even if the fresh non-blocking
// read were removed. Here a queued input is a live non-lease obligation on the
// current root, held while admitted work forces the early-return gate: the
// snapshot must contain both that fresh "input" blocker and the live "turn" lease.
func TestRetirementEarlyReturnReportsFreshNonLeaseObligation(t *testing.T) {
	t.Parallel()
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)

	// Admitted work forces the next manual attempt down the early-return gate
	// (len(c.active) != 0) instead of re-running full evidence.
	release, err := c.BeginMutation(root.ID(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// A queued input is an obligation with no lease behind it: only the fresh
	// non-blocking read can surface it, and the lease alone cannot satisfy the
	// assertion below.
	root.mu.Lock()
	root.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	root.mu.Unlock()

	claim, snapshot, err := c.TryClaim(true)
	if err != nil {
		t.Fatalf("early-return claim: %v", err)
	}
	if claim != nil {
		t.Fatalf("early return admitted a claim: %+v", snapshot)
	}
	if !hasRetirementBlockerFor(snapshot.Blockers, "input", root.ID()) {
		t.Fatalf("early return dropped the fresh non-lease obligation: %+v", snapshot.Blockers)
	}
	if !hasRetirementBlockerFor(snapshot.Blockers, "turn", root.ID()) {
		t.Fatalf("early return dropped the live lease: %+v", snapshot.Blockers)
	}
}

// TestRetirementClaimSnapshotDoesNotMixSwappedRootEvidence drives the real
// root-swap race: TryClaim captures the root and releases c.mu for the
// non-blocking evidence read, and AttachRoot can replace the root and advance the
// generation inside that window. The snapshot must never combine a blocker read
// from the previous root with the new root's live leases. The gate holds the
// claim attempt inside its evidence read (after the capture, before the relock),
// so the swap is ordered strictly between them rather than raced; the assertion is
// on snapshot content, not on any call count.
func TestRetirementClaimSnapshotDoesNotMixSwappedRootEvidence(t *testing.T) {
	t.Parallel()
	first := newQueuePersistTestSession(t, t.TempDir())
	defer first.Close()
	c := retirementEvidenceController(t, first)
	second := newQueuePersistTestSession(t, t.TempDir())
	defer second.Close()

	// A queued input on the old root is a fresh non-lease obligation: the
	// evidence read returns an "input" blocker attributed to first.
	first.mu.Lock()
	first.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	first.mu.Unlock()

	// The old root's admitted lease forces the early-return gate.
	releaseOld, err := c.BeginMutation(first.ID(), "admission")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOld()

	entered := make(chan struct{})
	resume := make(chan struct{})
	c.evidenceGate = func() {
		close(entered)
		<-resume
	}

	type claimResult struct {
		snapshot RetirementSnapshot
		err      error
	}
	result := make(chan claimResult, 1)
	go func() {
		_, snapshot, err := c.TryClaim(true)
		result <- claimResult{snapshot, err}
	}()

	// The gate only runs after TryClaim has captured (root, generation) and
	// dropped c.mu, so once it is entered the swap below is ordered after the
	// capture. AttachRoot then succeeds because c.mu is free.
	<-entered
	if err := c.AttachRoot(second); err != nil {
		t.Fatalf("attach during evidence read: %v", err)
	}
	// A lease admitted on the new root after the swap is the new root's live work.
	releaseNew, err := c.BeginMutation(second.ID(), "turn")
	if err != nil {
		t.Fatalf("admit on new root: %v", err)
	}
	defer releaseNew()
	// Let the evidence read finish; it still reads the captured old root, then
	// claimSnapshotCurrent relocks and combines.
	close(resume)

	got := <-result
	if got.err != nil {
		t.Fatalf("early-return claim: %v", got.err)
	}
	var staleInput, newLease bool
	for _, blocker := range got.snapshot.Blockers {
		if blocker.Category == "input" && blocker.SessionID == first.ID() {
			staleInput = true
		}
		if blocker.Category == "turn" && blocker.SessionID == second.ID() {
			newLease = true
		}
	}
	if staleInput && newLease {
		t.Fatalf("snapshot mixed the previous root's evidence with the new root's leases: %+v", got.snapshot.Blockers)
	}
	if staleInput {
		t.Fatalf("snapshot presented evidence read from the replaced root: %+v", got.snapshot.Blockers)
	}
	if !newLease {
		t.Fatalf("snapshot dropped the new root's live lease: %+v", got.snapshot.Blockers)
	}
}

// TestRetirementNonBlockingEvidenceFailsClosedOnOwnerContention pins the
// fail-closed placeholder: when the owner mutex is held (as it is across a
// durable delegate-store append), the non-blocking read cannot inspect the owner
// state, which carries non-lease-backed obligations. It must report that it could
// not be read rather than reporting the tree clear. The placeholder must not name
// a delegate it never inspected.
func TestRetirementNonBlockingEvidenceFailsClosedOnOwnerContention(t *testing.T) {
	t.Parallel()
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()

	release, err := c.BeginMutation(root.ID(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// Hold the owner mutex for the duration of the attempt, the way an append
	// that owns c.mu does in production.
	tree.mu.Lock()
	_, snapshot, err := c.TryClaim(true)
	tree.mu.Unlock()
	if err != nil {
		t.Fatalf("early-return claim: %v", err)
	}
	if !hasRetirementBlockerFor(snapshot.Blockers, "delegate", root.ID()) {
		t.Fatalf("owner contention reported the tree clear: %+v", snapshot.Blockers)
	}
	for _, blocker := range snapshot.Blockers {
		if blocker.Category == "delegate" && blocker.DelegateID != "" {
			t.Fatalf("placeholder named a delegate it never read: %+v", snapshot.Blockers)
		}
	}
}

// A preparation belongs to one root generation, not just a preparing phase.
// Deliberately invalidate each private identity component to exercise the exact
// claim check independently of pointer equality (ordinary callers cannot edit it).
func TestRetirementRejectsInvalidClaimIdentity(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"commit", "abort"} {
		for _, invalid := range []string{"nil", "copy", "controller", "root", "generation", "committed", "finished"} {
			t.Run(operation+"/"+invalid, func(t *testing.T) {
				c, root := retirementTestController(t)
				claim, _, err := c.TryClaim(true)
				if err != nil || claim == nil {
					t.Fatalf("claim: %v %v", claim, err)
				}
				candidate := claim
				c.mu.Lock()
				switch invalid {
				case "nil":
					candidate = nil
				case "copy":
					copied := *claim
					candidate = &copied
				case "controller":
					claim.controller = nil
				case "root":
					claim.root = nil
				case "generation":
					claim.generation++
				case "committed":
					claim.committed = true
				case "finished":
					claim.finished = true
				}
				c.mu.Unlock()
				if operation == "commit" {
					err = c.Commit(candidate)
				} else {
					err = c.Abort(candidate, "prepare_failed")
				}
				if !errors.Is(err, ErrRetirementUnavailable) {
					t.Fatalf("accepted %s claim: %v", invalid, err)
				}
				if got := c.Snapshot().Phase; got != "preparing" {
					t.Fatalf("invalid claim changed phase: %q", got)
				}
				if _, err := c.BeginMutation(root.ID(), "input"); !errors.Is(err, ErrRetirementUnavailable) {
					t.Fatalf("invalid claim reopened admission: %v", err)
				}
			})
		}
	}
}

// Reusing an old claim after a root swap must not abort or commit a newer claim.
func TestRetirementRootSwapRejectsStaleClaims(t *testing.T) {
	t.Parallel()
	c, first := retirementTestController(t)
	old, _, err := c.TryClaim(true)
	if err != nil || old == nil {
		t.Fatalf("old claim: %v %v", old, err)
	}
	if err := c.Abort(old, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
	release, err := c.BeginMutation(first.ID(), "admission")
	if err != nil {
		t.Fatal(err)
	}
	second := newQueuePersistTestSession(t, t.TempDir())
	defer second.Close()
	if err := c.AttachRoot(second); err != nil {
		t.Fatal(err)
	}
	if second.retirementController.Load() != c {
		t.Fatal("new root missing controller")
	}
	if claim, _, err := c.TryClaim(true); err != nil || claim != nil {
		t.Fatalf("swap lease bypassed: %v %v", claim, err)
	}
	release()
	current, _, err := c.TryClaim(true)
	if err != nil || current == nil {
		t.Fatalf("current claim: %v %v", current, err)
	}
	if current.root != second || current.generation <= old.generation {
		t.Fatal("claim retained stale root generation")
	}
	if err := c.Commit(old); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("stale commit: %v", err)
	}
	if err := c.Abort(old, "prepare_failed"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("stale abort: %v", err)
	}
	if claim, _, err := c.TryClaim(true); claim != nil || !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("concurrent claim: %v %v", claim, err)
	}
	if err := c.AttachRoot(first); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("swap during preparation: %v", err)
	}
	if err := c.Commit(current); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(current); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("double commit: %v", err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatalf("zero-reader drain: %v", err)
	}
}

// A failed drain or release after Commit must never restore a mutable resident.
func TestRetirementFailureAfterCommitCannotAbort(t *testing.T) {
	t.Parallel()
	c, root := retirementTestController(t)
	release, err := c.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	if err := c.DrainReaders(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain: %v", err)
	}
	for _, failure := range []string{"reader_drain_failed", "release_failed", "prepare_failed"} {
		if err := c.Abort(claim, failure); !errors.Is(err, ErrRetirementUnavailable) {
			t.Fatalf("abort %s: %v", failure, err)
		}
	}
	state := c.Snapshot()
	if state.Phase != "retiring" || state.Failure != "reader_drain_failed" {
		t.Fatalf("failed drain state: %+v", state)
	}
	if _, err := c.BeginMutation(root.ID(), "input"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("mutation: %v", err)
	}
	if err := c.AttachRoot(root); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("attach: %v", err)
	}
	release()
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Phase; got != "retiring" {
		t.Fatalf("successful retry reopened phase: %s", got)
	}
}

// Raw preparation errors must never enter the observable machine-category field.
func TestRetirementAbortBoundsFailure(t *testing.T) {
	t.Parallel()
	c, _ := retirementTestController(t)
	for _, tc := range []struct{ input, want string }{
		{"prepare_failed", "prepare_failed"}, {"release_failed", "release_failed"},
		{"reader_drain_failed", "reader_drain_failed"}, {"", ""},
		{"private prompt at /private/path", "prepare_failed"},
	} {
		claim, _, err := c.TryClaim(true)
		if err != nil || claim == nil {
			t.Fatalf("claim: %v %v", claim, err)
		}
		if err := c.Abort(claim, tc.input); err != nil {
			t.Fatal(err)
		}
		state := c.Snapshot()
		if state.Phase != "resident" || state.Failure != tc.want || !state.EligibleSince.IsZero() || !state.Deadline.IsZero() {
			t.Fatalf("abort state: %+v want failure %q", state, tc.want)
		}
		if err := c.Abort(claim, tc.input); !errors.Is(err, ErrRetirementUnavailable) {
			t.Fatalf("double abort: %v", err)
		}
	}
}

// Missing roots cannot prove eligibility, and another process controller cannot
// steal admission authority from an already-attached Session.
func TestRetirementRootAttachmentFailsClosed(t *testing.T) {
	t.Parallel()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if claim, state, err := c.TryClaim(true); err != nil || claim != nil || state.Phase != "resident" ||
		!reflect.DeepEqual(state.Blockers, []RetirementBlocker{{Category: "unsupported"}}) {
		t.Fatalf("rootless claim: %v %+v %v", claim, state, err)
	}
	if err := c.AttachRoot(nil); err == nil {
		t.Fatal("accepted nil root")
	}
	owner, root := retirementTestController(t)
	if err := c.AttachRoot(root); err == nil {
		t.Fatal("stole another controller's root")
	}
	if root.retirementController.Load() != owner {
		t.Fatal("replaced root's admission authority")
	}
	if claim, _, err := c.TryClaim(true); claim != nil || err != nil {
		t.Fatalf("failed attachment published root: %v %v", claim, err)
	}
	if err := c.DrainReaders(context.Background()); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("drain before commit: %v", err)
	}
}

// Publication and claim creation must linearize together: a winning attach is
// captured by the claim; a winning claim leaves the rejected root untouched.
func TestRetirementAttachRootRacesClaim(t *testing.T) {
	t.Parallel()
	for range 16 {
		c, first := retirementTestController(t)
		second := newQueuePersistTestSession(t, t.TempDir())
		t.Cleanup(second.Close)
		start := make(chan struct{})
		attached := make(chan error, 1)
		go func() { <-start; attached <- c.AttachRoot(second) }()
		type result struct {
			claim *RetirementClaim
			err   error
		}
		claimed := make(chan result, 1)
		go func() { <-start; claim, _, err := c.TryClaim(true); claimed <- result{claim, err} }()
		close(start)
		attachErr, claimResult := <-attached, <-claimed
		if claimResult.err != nil || claimResult.claim == nil {
			t.Fatalf("claim: %+v", claimResult)
		}
		if attachErr == nil {
			if claimResult.claim.root != second || second.retirementController.Load() != c {
				t.Fatal("claim missed published root")
			}
		} else {
			if !errors.Is(attachErr, ErrRetirementUnavailable) {
				t.Fatal(attachErr)
			}
			if claimResult.claim.root != first || second.retirementController.Load() != nil {
				t.Fatal("rejected attachment changed root identity")
			}
		}
		if err := c.Commit(claimResult.claim); err != nil {
			t.Fatal(err)
		}
	}
}

// Zero disables automatic retirement even if an eligible interval was recorded;
// positive timeouts require an existing, due interval. Establishing proof of that
// interval belongs to the later predicate/timer task, not this primitive.
func TestRetirementAutomaticClaimRequiresDueInterval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		timeout   time.Duration
		eligible  bool
		advance   time.Duration
		wantClaim bool
	}{
		{"disabled", 0, true, time.Hour, false},
		{"unknown", time.Hour, false, time.Hour, false},
		{"not_due", time.Hour, true, time.Minute, false},
		{"due", time.Hour, true, time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := agenttest.NewFakeClock()
			c, err := NewRetirementController(tc.timeout, clk)
			if err != nil {
				t.Fatal(err)
			}
			root := newQueuePersistTestSession(t, t.TempDir())
			defer root.Close()
			if err := c.AttachRoot(root); err != nil {
				t.Fatal(err)
			}
			if tc.eligible {
				c.eligibleSince = clk.Now()
			}
			clk.Advance(tc.advance)
			claim, state, err := c.TryClaim(false)
			if err != nil || (claim != nil) != tc.wantClaim {
				t.Fatalf("automatic claim: %v %+v %v", claim, state, err)
			}
			if state.Timeout != tc.timeout {
				t.Fatalf("timeout = %s", state.Timeout)
			}
			if tc.timeout == 0 || !tc.eligible {
				if !state.Deadline.IsZero() {
					t.Fatalf("unexpected deadline: %v", state.Deadline)
				}
			} else if want := time.Unix(4600, 0).UTC(); !state.Deadline.Equal(want) {
				t.Fatalf("deadline = %v want %v", state.Deadline, want)
			}
		})
	}
}

// Invalid configuration must not silently enable an unusable timer/controller.
func TestRetirementRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		timeout time.Duration
		clk     RetirementClock
	}{
		{-time.Second, clock.Real()}, {time.Hour, nil},
	} {
		if c, err := NewRetirementController(tc.timeout, tc.clk); err == nil || c != nil {
			t.Fatalf("accepted config: %v %v", c, err)
		}
	}
}

// Unknown categories stay bounded, passive reads/notifications do not reset
// eligibility, and a new mutation does reset it and notifies without blocking.
func TestRetirementChangesAndEligibilityReset(t *testing.T) {
	t.Parallel()
	c, root := retirementTestController(t)
	for range 8 {
		c.Changed()
	}
	select {
	case <-c.changed:
	default:
		t.Fatal("missing change notification")
	}
	select {
	case <-c.changed:
		t.Fatal("notifications did not coalesce")
	default:
	}
	eligible := time.Unix(1000, 0).UTC()
	c.eligibleSince = eligible
	releaseRead, err := c.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	c.Changed()
	releaseRead()
	if state := c.Snapshot(); !state.EligibleSince.Equal(eligible) {
		t.Fatalf("passive read reset interval: %+v", state)
	}
	release, err := c.BeginMutation(root.ID(), "private input text")
	if err != nil {
		t.Fatal(err)
	}
	state := c.Snapshot()
	if !state.EligibleSince.IsZero() || !state.Deadline.IsZero() || !reflect.DeepEqual(state.Blockers, []RetirementBlocker{{Category: "unsupported", SessionID: root.ID()}}) {
		t.Fatalf("mutation state: %+v", state)
	}
	<-c.changed
	release()
	select {
	case <-c.changed:
	default:
		t.Fatal("missing release notification")
	}
	c.eligibleSince = eligible
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	if !c.Snapshot().EligibleSince.IsZero() {
		t.Fatal("root publication retained old interval")
	}
}

// Every category a caller actually passes must survive into the blocker; an
// unlisted value is reported as "unsupported", so a missing entry makes real
// work read as unknown. Unknown values stay bounded, which
// TestRetirementChangesAndEligibilityReset already pins with free text.
func TestRetirementBeginMutationPreservesCallerCategories(t *testing.T) {
	t.Parallel()
	c, root := retirementTestController(t)
	for _, category := range []string{
		"turn", "input", "autonomous", "question", "job", "watch", "delegate",
		"environment", "persistence", "admission", "notification",
		"delegate_drive", "delegate_delivery", "delegate_restore",
	} {
		release, err := c.BeginMutation(root.ID(), category)
		if err != nil {
			t.Fatalf("BeginMutation(%q): %v", category, err)
		}
		var found bool
		for _, blocker := range c.Snapshot().Blockers {
			if blocker.Category == category && blocker.SessionID == root.ID() {
				found = true
			}
		}
		release()
		if !found {
			t.Fatalf("category %q was reported as unsupported instead of the real obligation", category)
		}
	}
}
