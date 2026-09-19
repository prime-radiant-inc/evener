package agent

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
)

// unlockOp drives the unlock operation through the registered tool surface,
// returning the structured result map.
func (r *wtRepo) unlockOp(t *testing.T, args map[string]any) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	full := map[string]any{"operation": "unlock"}
	maps.Copy(full, args)
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), full)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unlock result is %T, want map[string]any", out)
	}
	return m, nil
}

// lockedReasonOf reports the lock state git holds for path, read from the real
// `git worktree list --porcelain` registry.
func (r *wtRepo) lockedReasonOf(t *testing.T, path string) (locked bool, reason string) {
	t.Helper()
	porcelain := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	target := canonicalOrClean(path)
	for _, e := range worktree.ParsePorcelain(porcelain) {
		if canonicalOrClean(e.Path) == target {
			return e.Locked, e.LockReason
		}
	}
	return false, ""
}

// TestManageWorktreeUnlockReleasesDelegateLane is the issue #481 regression
// test: a parent must be able to release an unreachable delegate's
// evener:dlg: occupancy lock without retiring the delegate, so it can recover
// the lane's uncommitted work instead of bypassing manage_worktree with raw
// git. Before the fix the dispatch has no `unlock` operation at all and this
// fails with `unknown operation "unlock"`. After it, the lane is unlocked, the
// worktree and the delegate's resumability are untouched, and the parent can
// switch into it.
func TestManageWorktreeUnlockReleasesDelegateLane(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)

	locked, reason := r.lockedReasonOf(t, lanePath)
	if !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("seeded lane lock = (%t, %q), want the delegate marker %q", locked, reason, worktree.FormatDelegateMarker(id, r.s.id))
	}

	res, err := r.unlockOp(t, map[string]any{"id": id})
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if res["status"] != "unlocked" || res["id"] != id || res["path"] != lanePath {
		t.Fatalf("unlock result = %#v, want unlocked/%s/%s", res, id, lanePath)
	}

	if locked, reason := r.lockedReasonOf(t, lanePath); locked {
		t.Fatalf("lane still locked after unlock with %q", reason)
	}
	if !laneWorktreePresent(lanePath) {
		t.Fatal("unlock removed the delegate's worktree")
	}
	if !r.branchExists(t, id) {
		t.Fatal("unlock deleted the delegate's branch")
	}
	// The delegate must remain revivable: unlock releases the lock only.
	assertStableWorktreeResumable(t, r.s.delegateController, id, true)

	// The lane is now switchable like any managed worktree — the recovery the
	// issue asks for.
	if _, err := r.switchOp(t, map[string]any{"path": lanePath}); err != nil {
		t.Fatalf("switch into the unlocked delegate lane: %v", err)
	}
}

// TestManageWorktreeUnlockRefusesRunningDelegate preserves the safety property
// that a running delegate's lane cannot be stolen: while any work in the
// delegate's subtree is active (or a stop is outstanding), unlock refuses and
// leaves the marker in place.
func TestManageWorktreeUnlockRefusesRunningDelegate(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)

	// Start generation 2 and leave it open: the delegate is now running.
	r.s.delegateController.mu.Lock()
	_, err := r.s.delegateController.appendLocked(
		delegateControllerRunStartedEvent(id, 2, delegatestore.TriggerAttention, time.Unix(30, 0).UTC()),
	)
	r.s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatalf("open a run on the seeded delegate: %v", err)
	}

	_, err = r.unlockOp(t, map[string]any{"id": id})
	if err == nil {
		t.Fatal("unlock succeeded on a running delegate; a running delegate's lane must not be stealable")
	}
	if !strings.Contains(err.Error(), "running or unfinished work") {
		t.Fatalf("unlock error = %v, want the running-work refusal", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("running-delegate lane lock = (%t, %q), want the marker kept", locked, reason)
	}
	assertStableWorktreeResumable(t, r.s.delegateController, id, true)
}

// TestManageWorktreeUnlockRefusesForeignLock confirms unlock only ever releases
// the delegate's own evener:dlg: marker; a lock owned by anyone else is
// refused rather than overridden.
func TestManageWorktreeUnlockRefusesForeignLock(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "foreign-owner", lanePath)

	_, err := r.unlockOp(t, map[string]any{"id": id})
	if err == nil {
		t.Fatal("unlock released a foreign-owned lock")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("unlock error = %v, want the lock-state refusal", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != "foreign-owner" {
		t.Fatalf("foreign lock = (%t, %q), want it untouched", locked, reason)
	}
}

// TestManageWorktreeUnlockRejectsNonDelegateID confirms the id argument must be
// a delegate id, mirroring dispose.
func TestManageWorktreeUnlockRejectsNonDelegateID(t *testing.T) {
	r := newWorktreeRepo(t)

	_, err := r.unlockOp(t, map[string]any{"id": "not-a-delegate"})
	if err == nil || !strings.Contains(err.Error(), "not a delegate id") {
		t.Fatalf("unlock non-delegate id error = %v, want the invalid_request refusal", err)
	}
}

// TestManageWorktreeUnlockRefusesArmedWatch is the High-1 regression: unlock
// must clear the SAME quiescence ladder dispose clears, including an armed or
// pending watch send targeting the delegate. Before the shared helper, unlock
// checked only the running/open predicates and released the lock anyway.
func TestManageWorktreeUnlockRefusesArmedWatch(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)

	childSessionID := "child-" + id
	key := watchKey{
		VisibleSessionID:   r.s.id,
		Target:             "job_source",
		SendTo:             stableWatchReceiverTarget,
		ReceiverSessionID:  childSessionID,
		ReceiverDelegateID: id,
	}
	cfg := &watchConfig{
		id:                 "w1",
		watchID:            "watch-1",
		target:             "job_source",
		send:               &watchSendArgs{To: stableWatchReceiverTarget},
		receiverSessionID:  childSessionID,
		receiverDelegateID: id,
		stableReceiver:     true,
	}
	r.s.jobManager.mu.Lock()
	r.s.jobManager.watches[key] = cfg
	r.s.jobManager.mu.Unlock()

	_, err := r.unlockOp(t, map[string]any{"id": id})
	if err == nil {
		t.Fatal("unlock released a lane that is the target of an armed watch send")
	}
	if !strings.Contains(err.Error(), "is the target of an armed or pending watch send") {
		t.Fatalf("unlock error = %v, want the watch-gate refusal", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("watched lane lock = (%t, %q), want the marker kept", locked, reason)
	}
}

// TestManageWorktreeUnlockRefusesRacingDrive is the High-2 regression: a drive
// or resume that raced the quiescence check must win, not be overwritten by the
// unlock. With the child registered as running, trySetDisposeGate refuses and
// unlock must refuse too. Before the fix, unlock had no gate and released the
// lock under the running child.
func TestManageWorktreeUnlockRefusesRacingDrive(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	childID := "child-" + id
	r.s.subagents.mu.Lock()
	r.s.subagents.subs[childID] = &subagent{id: childID, running: true}
	r.s.subagents.mu.Unlock()

	_, err := r.unlockOp(t, map[string]any{"id": id})
	if err == nil {
		t.Fatal("unlock succeeded while the child was running; the gate must refuse")
	}
	if !strings.Contains(err.Error(), "became active while unlocking") {
		t.Fatalf("unlock error = %v, want the dispose-gate refusal", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("racing-drive lane lock = (%t, %q), want the marker kept", locked, reason)
	}
}

// TestManageWorktreeUnlockRefusesProvenanceMismatch is the Low-1 regression:
// unlock must refuse when the lane resolves to a different main root than its
// sidecar records, rather than running git from the recorded root.
func TestManageWorktreeUnlockRefusesProvenanceMismatch(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	metaDir := metaDirForLane(lanePath)
	if err := worktree.UpdateSidecar(metaDir, id, func(sc *worktree.Sidecar) {
		sc.OriginalRoot = filepath.Join(t.TempDir(), "moved-root")
	}); err != nil {
		t.Fatalf("rewrite sidecar original_root: %v", err)
	}

	_, err := r.unlockOp(t, map[string]any{"id": id})
	if err == nil {
		t.Fatal("unlock accepted a lane whose main root does not match its sidecar")
	}
	if !strings.Contains(err.Error(), "provenance mismatch") {
		t.Fatalf("unlock error = %v, want the provenance refusal", err)
	}
	if locked, _ := r.lockedReasonOf(t, lanePath); !locked {
		t.Fatal("provenance refusal released the lock")
	}
}

// TestManageWorktreeUnlockRefreshesSidecarGrace is the Low-2 regression: the
// residue sweeper treats "unlocked with a stale sidecar" as collectible, so a
// hand-off must refresh the sidecar mtime the way the close-time KEEP path
// does. Before the fix, the mtime stayed old.
func TestManageWorktreeUnlockRefreshesSidecarGrace(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	metaDir := metaDirForLane(lanePath)
	old := time.Now().Add(-2 * time.Hour)
	sidecarFile := filepath.Join(metaDir, worktree.EncodeSidecarName(id)+".json")
	if err := os.Chtimes(sidecarFile, old, old); err != nil {
		t.Fatalf("age the sidecar: %v", err)
	}

	if _, err := r.unlockOp(t, map[string]any{"id": id}); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	age, err := worktree.SidecarAge(metaDir, id)
	if err != nil {
		t.Fatalf("sidecar age after unlock: %v", err)
	}
	if age > time.Minute {
		t.Fatalf("sidecar age after unlock = %v, want a refreshed mtime (grace window)", age)
	}
}

// TestManageWorktreeUnlockTouchesSidecarBeforeRelease is the Medium-1
// regression: the sidecar mtime must be refreshed BEFORE the lock is released,
// matching the close-time unlock ordering, or a residue sweep can observe
// "unlocked with a stale sidecar" and collect the lane in the gap. Before the
// fix the touch ran only after the unlock, so the mtime observed at release
// time was still the aged one.
func TestManageWorktreeUnlockTouchesSidecarBeforeRelease(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	metaDir := metaDirForLane(lanePath)
	old := time.Now().Add(-2 * time.Hour)
	sidecarFile := filepath.Join(metaDir, worktree.EncodeSidecarName(id)+".json")
	if err := os.Chtimes(sidecarFile, old, old); err != nil {
		t.Fatalf("age the sidecar: %v", err)
	}

	observedAge := time.Duration(-1)
	r.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) == 3 && args[0] == "worktree" && args[1] == "unlock" {
				if age, err := worktree.SidecarAge(metaDir, id); err == nil {
					observedAge = age
				}
			}
			return inner(args...)
		}
	}

	if _, err := r.unlockOp(t, map[string]any{"id": id}); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if observedAge < 0 {
		t.Fatal("the runner never observed git worktree unlock")
	}
	if observedAge > time.Minute {
		t.Fatalf("sidecar age at unlock = %v, want the mtime refreshed before the lock is released", observedAge)
	}
}

// TestManageWorktreeUnlockFenceBlocksAConcurrentStart is the concurrency-fence
// regression (High, round 3). Unlock parks inside its critical section at the
// git unlock call; a delegate start reserved from another goroutine must be
// refused by the controller-level lane-handoff fence, so the lane cannot be
// freed out from under a start. Before the fence, ReserveStart was admitted
// while unlock was mid-release (the cold-delegate path had no barrier).
func TestManageWorktreeUnlockFenceBlocksAConcurrentStart(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	c := r.s.delegateController

	reached := make(chan struct{})
	release := make(chan struct{})
	r.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) == 3 && args[0] == "worktree" && args[1] == "unlock" {
				close(reached)
				<-release
			}
			return inner(args...)
		}
	}

	type outcome struct {
		result WorktreeUnlockResult
		err    error
	}
	unlocked := make(chan outcome, 1)
	go func() {
		res, err := r.s.worktreeUnlock(context.Background(), id)
		unlocked <- outcome{result: res, err: err}
	}()
	<-reached

	// Unlock is parked mid-critical-section: a concurrent start must refuse.
	_, reserveErr := c.ReserveStart(rootDelegateActor(c.rootSessionID), id)
	reserveRefused := errors.Is(reserveErr, errDelegateTargetBusy)
	close(release)
	got := <-unlocked

	if got.err != nil {
		t.Fatalf("unlock: %v", got.err)
	}
	if !reserveRefused {
		t.Fatalf("a delegate start was admitted while unlock held the lane (ReserveStart err = %v)", reserveErr)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); locked || reason != "" {
		t.Fatalf("lane after unlock = (%t, %q), want unlocked", locked, reason)
	}
}

// TestDelegateLaneHandoffFencesStart pins the controller fence directly: a
// second handoff is refused while one is held, ReserveStart refuses under a
// held fence, and the fence clears on release.
func TestDelegateLaneHandoffFencesStart(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedStableIsolationLane(t)
	c := r.s.delegateController

	if !c.beginLaneHandoff(id) {
		t.Fatal("first lane handoff should arm")
	}
	if c.beginLaneHandoff(id) {
		t.Fatal("a second lane handoff must refuse while one is in flight")
	}
	if _, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), id); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart under a lane handoff = %v, want errDelegateTargetBusy", err)
	}
	c.endLaneHandoff(id)
	reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), id)
	if err != nil {
		t.Fatalf("ReserveStart after the handoff cleared = %v, want success", err)
	}
	_ = c.AbortStart(reservation)
}

// TestReapplyIsolationLaneLockForSend pins the send-path re-lock (High, round
// 3): a send must re-establish the delegate's own marker on an unlocked lane,
// adopt an already-own lane, and refuse a lane a session has switched into.
func TestReapplyIsolationLaneLockForSend(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	descriptor := delegatestore.Descriptor{Isolation: "worktree", WorkingDir: lanePath}

	// Unlocked lane -> the send re-locks it with the delegate's own marker.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	if err := r.s.reapplyIsolationLaneLockForSend(descriptor); err != nil {
		t.Fatalf("re-lock an unlocked lane: %v", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("lane after send re-lock = (%t, %q), want the delegate marker", locked, reason)
	}

	// Own marker -> adopt (no error, still locked).
	if err := r.s.reapplyIsolationLaneLockForSend(descriptor); err != nil {
		t.Fatalf("adopt the own marker: %v", err)
	}

	// A session switched in -> refuse the send.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker("someone-else"), lanePath)
	if err := r.s.reapplyIsolationLaneLockForSend(descriptor); err == nil {
		t.Fatal("reapply accepted a lane another session had locked")
	}

	// A non-isolated delegate is a no-op.
	if err := r.s.reapplyIsolationLaneLockForSend(delegatestore.Descriptor{Isolation: "none"}); err != nil {
		t.Fatalf("non-isolated descriptor = %v, want nil", err)
	}
}

// TestManageWorktreeUnlockRefusesWhileClosing is the close-gate regression
// (Medium, round 3): like dispose, unlock must refuse a closing session so
// Close() never drains a lane an admission it did not join is mutating. Called
// directly on the op (below the dispatch's own close fence, which the reviewer
// named as insufficient: it is the envWork admission, not disposeWG). Before
// the fix unlock had no admission and released the lane.
func TestManageWorktreeUnlockRefusesWhileClosing(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	r.s.mu.Lock()
	r.s.closing = true
	r.s.mu.Unlock()

	_, err := r.s.worktreeUnlock(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "session is closing") {
		t.Fatalf("unlock while closing = %v, want the close-gate refusal", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("close-gate refusal lane lock = (%t, %q), want the marker kept", locked, reason)
	}
}

// TestRestoreIdleForSendRelocksLaneForResidentChild is the resident-child
// re-lock regression (High, round 4): the `existing != nil` early return in
// restoreIdleForSend launches an already-resident child, so it must re-establish
// the lane lock like the leader path. Before the fix the early return skipped
// the re-lock, so a send started a resident child in a lane an unlock had freed.
func TestRestoreIdleForSendRelocksLaneForResidentChild(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	childID := "child-" + id
	r.s.subagents.mu.Lock()
	r.s.subagents.subs[childID] = &subagent{id: childID}
	r.s.subagents.mu.Unlock()
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)

	started := delegateStartCommit{
		lease: delegateLease{delegateID: id, generation: 2},
		descriptor: delegatestore.Descriptor{
			ChildSessionID: childID,
			Isolation:      "worktree",
			WorkingDir:     lanePath,
		},
	}
	runtime := delegateRuntime{owner: r.s}
	sub, _, finish, err := runtime.restoreIdleForSend(started)
	finish(sub, err)
	if err != nil {
		t.Fatalf("restoreIdleForSend: %v", err)
	}
	if sub == nil {
		t.Fatal("restoreIdleForSend returned no resident child")
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("resident-child send lane = (%t, %q), want the delegate marker re-established", locked, reason)
	}
}

// TestReapplyIsolationLaneLockForSendRefusesEmptyOriginalRoot is the
// empty-root-guard regression (Medium, round 4): the send-path re-lock must
// fail closed on a sidecar with no original_root, like dispose and unlock do,
// rather than rooting the control env at "" and running git against the evener
// process CWD.
func TestReapplyIsolationLaneLockForSendRefusesEmptyOriginalRoot(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	if err := worktree.UpdateSidecar(metaDirForLane(lanePath), id, func(sc *worktree.Sidecar) {
		sc.OriginalRoot = ""
	}); err != nil {
		t.Fatalf("blank the sidecar original_root: %v", err)
	}

	err := r.s.reapplyIsolationLaneLockForSend(delegatestore.Descriptor{Isolation: "worktree", WorkingDir: lanePath})
	if err == nil || !strings.Contains(err.Error(), "no original_root") {
		t.Fatalf("reapply with an empty original_root = %v, want the empty-root refusal", err)
	}
}

// TestReapplyIsolationLaneLockForSendRefusesAbsentLane is the fail-closed
// regression (High 2, round 5): an isolated delegate whose lane directory is
// gone must refuse the start, not be treated as a successful no-op.
func TestReapplyIsolationLaneLockForSendRefusesAbsentLane(t *testing.T) {
	r := newWorktreeRepo(t)
	_, lanePath, _ := r.seedStableIsolationLane(t)
	if err := os.RemoveAll(lanePath); err != nil {
		t.Fatalf("remove the lane: %v", err)
	}
	err := r.s.reapplyIsolationLaneLockForSend(delegatestore.Descriptor{Isolation: "worktree", WorkingDir: lanePath})
	if err == nil {
		t.Fatal("reapply treated an absent isolation lane as a successful no-op")
	}
}

// TestReapplyIsolationLaneLockForSendRefusesProvenanceMismatch is the
// send-path provenance regression (Medium 1, round 5).
func TestReapplyIsolationLaneLockForSendRefusesProvenanceMismatch(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	if err := worktree.UpdateSidecar(metaDirForLane(lanePath), id, func(sc *worktree.Sidecar) {
		sc.OriginalRoot = filepath.Join(t.TempDir(), "moved-root")
	}); err != nil {
		t.Fatalf("rewrite sidecar original_root: %v", err)
	}
	err := r.s.reapplyIsolationLaneLockForSend(delegatestore.Descriptor{Isolation: "worktree", WorkingDir: lanePath})
	if err == nil || !strings.Contains(err.Error(), "provenance mismatch") {
		t.Fatalf("reapply with a mismatched main root = %v, want the provenance refusal", err)
	}
}

// TestIdleDelegateRestoreCommitRefusesUnderLaneHandoff is the cold-attention
// fence regression (High 1, round 5): the cold attention restore commit bypasses
// ReserveStart, so it must consult the lane-handoff fence itself.
func TestIdleDelegateRestoreCommitRefusesUnderLaneHandoff(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedStableIsolationLane(t)
	c := r.s.delegateController
	if !c.beginLaneHandoff(id) {
		t.Fatal("arm the lane handoff")
	}
	defer c.endLaneHandoff(id)
	if _, _, err := c.idleDelegateRestoreCommit(id); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("idleDelegateRestoreCommit under a lane handoff = %v, want errDelegateTargetBusy", err)
	}
}

// TestBeginLaneHandoffRefusesWhileColdRestoreInFlight is the commit->install
// fence regression (High, round 6): a cold attention restore registers an
// in-flight window at commit time, and beginLaneHandoff must refuse while it is
// open, so unlock cannot slip into the gap between the point-in-time check and
// the child's install.
func TestBeginLaneHandoffRefusesWhileColdRestoreInFlight(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedStableIsolationLane(t)
	c := r.s.delegateController
	if _, _, err := c.idleDelegateRestoreCommit(id); err != nil {
		t.Fatalf("idleDelegateRestoreCommit: %v", err)
	}
	if c.beginLaneHandoff(id) {
		t.Fatal("beginLaneHandoff armed while a cold restore window was open")
	}
	c.endLaneRestore(id)
	if !c.beginLaneHandoff(id) {
		t.Fatal("beginLaneHandoff should arm once the restore window closes")
	}
	c.endLaneHandoff(id)
}

// TestTrySetDisposeGateRefusesFinalizing is the finalization-race regression
// (Medium, round 6): the gate must refuse a finalizing child under the same
// mutex hold, so unlock cannot release/evict under an in-flight finalizer.
func TestTrySetDisposeGateRefusesFinalizing(t *testing.T) {
	sub := &subagent{id: "sub-finalizing", finalizing: true}
	if sub.trySetDisposeGate() {
		t.Fatal("dispose gate armed on a finalizing child")
	}
	if sub.disposeGated {
		t.Fatal("disposeGated set on a finalizing child")
	}
}

// TestReleaseDelegateRuntimePointerClearsLiveRuntime is the stale-binding
// regression (High, round 7): after unlock releases a resident runtime it must
// clear the controller's live pointer, or a later delegate_send's fresh restore
// collides with the released session in AttachRuntime.
func TestReleaseDelegateRuntimePointerClearsLiveRuntime(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedStableIsolationLane(t)
	c := r.s.delegateController
	released := &Session{}
	c.mu.Lock()
	c.live[id] = &delegateLiveState{runtime: released}
	before := c.evidenceVersion
	c.mu.Unlock()

	c.releaseDelegateRuntimePointer(id, released)

	c.mu.Lock()
	got := c.live[id].runtime
	bumped := c.evidenceVersion > before
	c.mu.Unlock()
	if got != nil {
		t.Fatal("live.runtime was not cleared for the released runtime")
	}
	if !bumped {
		t.Fatal("evidenceVersion was not bumped")
	}
}

// TestDelegateHasResidentDescendants covers the High where unlock released only
// the direct runtime while a nested subagent stayed resident (issue #481
// review). The bounded fix refuses unlock until no strict descendant has a
// resident runtime; this pins the check itself without a phantom session tree.
func TestDelegateHasResidentDescendants(t *testing.T) {
	c := &delegateTreeController{
		durable: map[string]*delegatestore.Aggregate{
			"parent": {Descriptor: delegatestore.Descriptor{}},
			"child":  {Descriptor: delegatestore.Descriptor{ParentDelegateID: "parent"}},
		},
		live: map[string]*delegateLiveState{},
	}
	if c.delegateHasResidentDescendants("parent") {
		t.Fatal("no live runtime should report no resident descendants")
	}
	c.live["child"] = &delegateLiveState{runtime: &Session{}}
	if !c.delegateHasResidentDescendants("parent") {
		t.Fatal("a resident descendant runtime should be reported")
	}
	// The check is strict: the delegate's own live runtime is not a descendant.
	if c.delegateHasResidentDescendants("child") {
		t.Fatal("a leaf with a runtime has no resident descendants")
	}
}

// TestManageWorktreeUnlockAbortsWhenSidecarRefreshFails is the Medium-9
// regression: a failed sidecar-mtime refresh must abort the unlock while the
// lane is still locked, not expose a lane the sweeper can collect.
func TestManageWorktreeUnlockAbortsWhenSidecarRefreshFails(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	installWorktreeSeams(t, r.s, worktreeTestSeams{
		updateSidecar: func(string, string, func(*worktree.Sidecar)) error {
			return errors.New("sidecar refresh failed")
		},
	})

	_, err := r.unlockOp(t, map[string]any{"id": id})
	if err == nil || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("unlock with a failed sidecar refresh = %v, want the abort", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("sidecar-refresh abort lane lock = (%t, %q), want the marker kept", locked, reason)
	}
}

// TestManageWorktreeUnlockAbortsWhenResidentReleaseFails is the High-9
// regression: when the resident runtime cannot be released, unlock must fail and
// restore the lane lock, retaining the child rather than stranding a partially
// released runtime.
func TestManageWorktreeUnlockAbortsWhenResidentReleaseFails(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	childID := "child-" + id
	child := newSession(t)
	r.s.subagents.mu.Lock()
	r.s.subagents.subs[childID] = &subagent{id: childID, sess: child}
	r.s.subagents.mu.Unlock()
	installWorktreeSeams(t, r.s, worktreeTestSeams{
		releaseChildRuntime: func(*Session) error { return errors.New("release failed") },
	})

	_, err := r.unlockOp(t, map[string]any{"id": id})
	if err == nil || !strings.Contains(err.Error(), "resident runtime failed") {
		t.Fatalf("unlock with a failed resident release = %v, want the abort", err)
	}
	if locked, reason := r.lockedReasonOf(t, lanePath); !locked || reason != worktree.FormatDelegateMarker(id, r.s.id) {
		t.Fatalf("release-failure lane lock = (%t, %q), want the marker re-taken", locked, reason)
	}
	r.s.subagents.mu.Lock()
	stillPresent := r.s.subagents.subs[childID] != nil
	r.s.subagents.mu.Unlock()
	if !stillPresent {
		t.Fatal("child was removed despite the failed release")
	}
}
