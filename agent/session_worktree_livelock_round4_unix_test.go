//go:build unix

package agent

// Round-4 regressions for the livelock gate and the dead-owner reclamation
// (issue #5471):
//
//   - the gate's boundedness (finding 0): a turn-facing lane operation waits
//     at most its budget, and a cancelled context unblocks it — no acquisition
//     pathology can stall agent work indefinitely. The end-to-end pin is the
//     env-rollback suite's same-id restore (TestRejectedInputDoesNotPersistIts
//     ProvisionalTurn and its neighbors), which hung the round-3 head.
//   - the reclamation claim's scoping (finding 2): a lane the eligibility
//     rungs skip must give the dead owner root's flock back before the pass
//     moves on, so the root is immediately acquirable again.
//   - the failed resume (finding 3): a construction that fails after its
//     acquisition cleared a retired root's park marker puts the marker back
//     and releases, so the failed daemon's exit leaves the root parked, never
//     dead, and a foreign prune keeps its preserved lanes.
//   - the park's durability (finding 4): a retirement whose park marker
//     cannot be made durable ABORTS, keeping the root resident and its flock
//     held, instead of committing a park the machine can forget.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// TestLivelockGateWaitIsBoundedAndCancellable (review round 4, finding 0):
// the gate a delegate operation waits on is a bounded, cancellable wait, not
// an open-ended one. A livelock held for the session's whole lifetime (the
// same-id shape the round-3 head hung on) must refuse the operation within
// the budget, and a cancelled turn context must unblock the wait at once.
func TestLivelockGateWaitIsBoundedAndCancellable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sid := identifier.MustNewSessionID()
	if err := os.MkdirAll(schema.SessionsDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	holder := holdLivelock(t, dir, sid)
	defer holder()
	s := newSession(t, withStateDir(dir), withConfig(retiredOwnerSessionConfig(sid)))
	if s.ID() != sid {
		t.Fatalf("fixture session id = %q, want the pre-selected %q", s.ID(), sid)
	}

	// The holder keeps the gate pending, and the wait refuses within its
	// budget instead of stalling: the round-3 head sat here forever.
	started := time.Now()
	err := s.waitLivelockAcquired(context.Background(), 100*time.Millisecond)
	if !errors.Is(err, errLivelockGatePending) {
		t.Fatalf("budgeted wait error = %v, want the pending gate sentinel", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("budgeted wait took %s, want it bounded near its 100ms budget", elapsed)
	}

	// A cancelled turn context unblocks the wait at once.
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- s.waitLivelockAcquired(ctx, livelockGateWaitBudget) }()
	var waitErr error
	waitForCondition(t, livelockContentionWaitBudget, "the gate wait to sit on the pending gate", func() bool {
		select {
		case waitErr = <-result:
			return true
		default:
			cancel()
			return false
		}
	})
	if waitErr == nil {
		t.Fatal("the cancelled gate wait returned nil, want the context error")
	}
	if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("cancelled wait error = %v, want context.Canceled", waitErr)
	}
}

// TestWorktreePruneSkippedLaneReleasesDeadOwnerClaim (review round 4, finding
// 2): the reclamation claim is scoped to the lane it authorizes. A lane the
// eligibility rungs skip — dirty here — must give the dead owner root's flock
// back before the pass moves on: a claim held past its lane reads as a live
// root to every later probe and blocks a legitimate resume's acquisition for
// as long as the pruning session lives.
func TestWorktreePruneSkippedLaneReleasesDeadOwnerClaim(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	// The disposability gate's skip rung: an uncommitted change keeps the
	// lane, whatever its owner's liveness.
	if err := os.WriteFile(filepath.Join(path, "uncommitted.txt"), []byte("dirty lane"), 0o644); err != nil {
		t.Fatalf("dirty the lane: %v", err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("prune collected a dirty lane: %+v", e)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}

	// The claim the skip must have released: the root's flock is immediately
	// acquirable again — a replacement process's non-blocking take succeeds
	// where a leaked claim would contend it.
	release := holdLivelock(t, r.stateDir, deadOwnerRootSID)
	release()

	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the skipped dirty lane did not survive the pass: %v", statErr)
	}
}

// TestFailedResumeRestoresParkedLivelockState (review round 4, finding 3): a
// retired root's park marker is cleared by a resume's livelock acquisition;
// when that construction then fails, the cleanup must put the marker back
// under the still-held flock and release — so the failed daemon's exit leaves
// the root parked-for-resume, never dead, and a foreign prune keeps the lanes
// its resumable delegates still require.
func TestFailedResumeRestoresParkedLivelockState(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	sid := identifier.MustNewSessionID()
	seedOwnerSessionMeta(t, r.stateDir, sid, false, "")
	seedDeadLivelock(t, r.stateDir, sid)
	if err := schema.MarkSessionLivelockParked(r.stateDir, sid, time.Now()); err != nil {
		t.Fatalf("seed the retired root's park marker: %v", err)
	}
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, sid)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + sid

	// The resume whose construction fails: the acquisition clears the park
	// marker, and the fault at the first post-acquisition init point fails
	// the construction after it.
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	cfg := SessionConfig{
		StateDir:         r.stateDir,
		MaxSubagentDepth: 1,
		spawn:            spawnConfig{sessionID: sid},
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sessionInitFault: func(point string) error {
				if point == "builtin_agents" {
					return errors.New("injected construction failure")
				}
				return nil
			},
		},
	}
	constructionFailed := errors.New("injected construction failure")
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		t.Fatal("the faulted construction unexpectedly succeeded; the test is not in the state it means to be")
	}
	if !strings.Contains(err.Error(), constructionFailed.Error()) {
		t.Fatalf("construction error = %v, want the injected post-acquisition fault: the test must fail the construction AFTER the livelock take", err)
	}

	// The failed construction left the root parked and unheld, exactly as the
	// retirement committed it: the marker restored under the flock, the flock
	// released, the root answerable as parked — never as a dead one.
	parked, err := schema.SessionLivelockParked(r.stateDir, sid)
	if err != nil || !parked {
		t.Fatalf("park marker after the failed resume = (present=%v, err=%v), want restored", parked, err)
	}
	held, exists, err := schema.SessionLivelockHeld(r.stateDir, sid)
	if err != nil || !exists || held {
		t.Fatalf("livelock after the failed resume = (held=%v, exists=%v, err=%v), want released", held, exists, err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune after the failed resume: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("foreign prune collected a failed resume's parked root lane: %+v", e)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("the parked root's preserved lane lost its lock")
	}
}

// TestRetirementAbortsWhenParkIsNotDurable (review round 4, finding 4): the
// park marker must be durable BEFORE the retirement commits, while a
// persistence failure can still abort it. A retirement that cannot write the
// marker keeps residency — the root's flock stays held, so the root reads
// live, its lanes protected — instead of releasing into a state the machine
// can forget and a later prune then reads as dead.
func TestRetirementAbortsWhenParkIsNotDurable(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	sid := identifier.MustNewSessionID()
	seedOwnerSessionMeta(t, r.stateDir, sid, false, "")
	own := newSession(t, withStateDir(r.stateDir), withConfig(retiredOwnerSessionConfig(sid)))

	// Make the park marker's durable publication impossible: the sessions
	// directory refuses new files.
	if err := os.Chmod(schema.SessionsDir(r.stateDir), 0o500); err != nil {
		t.Fatalf("chmod sessions dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(schema.SessionsDir(r.stateDir), 0o755)
	})

	if err := own.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); err == nil {
		t.Fatal("retirement whose park could not be made durable reported success")
	}

	// Residency is preserved: the root still holds its flock, and no park
	// marker claims otherwise.
	held, exists, err := schema.SessionLivelockHeld(r.stateDir, sid)
	if err != nil || !exists || !held {
		t.Fatalf("livelock after the aborted retirement = (held=%v, exists=%v, err=%v), want still held: residency is the abort's whole point", held, exists, err)
	}
	parked, err := schema.SessionLivelockParked(r.stateDir, sid)
	if err != nil || parked {
		t.Fatalf("park marker after the aborted retirement = (present=%v, err=%v), want absent: the park never committed", parked, err)
	}
}
