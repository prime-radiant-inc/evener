package agent

// REAL git (see docs/developing-evener/testing.md): the reporting contract of
// issue #5471's fallback. A managed lane locked by a delegate marker whose
// owner session tree is NOT this session is still never collected by `prune`
// — but its skip entry now carries the owner-liveness verdict, so a session
// facing a pile of locked delegate lanes can tell the dead-looking owners
// (the issue's 58 trees, ~6.7 GB) from live ones before applying the
// documented manual unlock recipe. Nothing here may change collection: every
// lane below stays skipped, locked and on disk whatever the verdict says.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// lockLaneWithDelegateMarker creates a managed lane and locks it with a
// foreign delegate marker (evener:dlg:<dlg>:<owner>), the lock shape a
// delegate's isolation lane carries.
func lockLaneWithDelegateMarker(t *testing.T, r *wtRepo, lane, dlgID, ownerSID string) string {
	t.Helper()
	path := r.addManagedWorktreeFixture(t, lane)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(dlgID, ownerSID), path)
	return path
}

// hermeticRendezvous points the session's rendezvous seam at an empty temp
// directory, so the owner-liveness probe reads no platform state.
func hermeticRendezvous(t *testing.T, r *wtRepo) string {
	t.Helper()
	dir := t.TempDir()
	r.s.mu.Lock()
	r.s.worktreeRendezvousDir = dir
	r.s.mu.Unlock()
	return dir
}

// seedOwnerRootSession writes the owner tree's root session meta: the walk
// from a delegate marker's session id stops at a non-subagent meta.
func seedOwnerRootSession(t *testing.T, stateDir, sid string) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: sid}); err != nil {
		t.Fatalf("seed owner root meta: %v", err)
	}
}

// requireLaneReportedSkippedAndKept asserts the reporting contract's
// invariant: the lane is in prune's skipped list with exactly the want reason,
// absent from removed, and its worktree is still on disk.
func requireLaneReportedSkippedAndKept(t *testing.T, r *wtRepo, lane, path, wantReason string) {
	t.Helper()
	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	skipped := pruneEntries(t, out, "skipped")
	e := findPruneEntry(t, skipped, lane)
	if e == nil {
		t.Fatalf("%s not in skipped: %+v", lane, skipped)
	}
	if e["reason"] != wantReason {
		t.Errorf("%s reason = %v, want %q", lane, e["reason"], wantReason)
	}
	if collected := findPruneEntry(t, pruneEntries(t, out, "removed"), lane); collected != nil {
		t.Errorf("%s collected by prune on an owner verdict: %+v", lane, collected)
	}
	if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
		t.Errorf("%s worktree removed by prune: %v", lane, statErr)
	}
}

// TestWorktreePruneReportsDeadLookingDelegateLaneOwner: the issue's shape —
// an owner root whose process is long gone. Its livelock file exists but the
// flock is free (the OS released it at process death), and no rendezvous
// entry names a live pid, so the skip reason carries owner-dead-looking
// instead of a bare locked marker the model cannot act on. The lane is NOT
// collected: the verdict is reporting, and acting on it is the documented
// manual recipe's job.
func TestWorktreePruneReportsDeadLookingDelegateLaneOwner(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	hermeticRendezvous(t, r)
	owner := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, owner)
	// Present-but-free: the file without the flock.
	if err := os.MkdirAll(schema.SessionsDir(r.stateDir), 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(schema.SessionLivelockPath(r.stateDir, owner), nil, 0o600); err != nil {
		t.Fatalf("seed free livelock: %v", err)
	}

	marker := worktree.FormatDelegateMarker("dlg_reportdead01", owner)
	path := lockLaneWithDelegateMarker(t, r, "dead-owner-lane", "dlg_reportdead01", owner)
	requireLaneReportedSkippedAndKept(t, r, "dead-owner-lane", path, "locked ("+marker+"); owner-dead-looking")
}

// TestWorktreePruneReportsLiveDelegateLaneOwner: the preservation side — a
// held livelock is a live owner process, and the verdict says so. The lane is
// still not collected.
func TestWorktreePruneReportsLiveDelegateLaneOwner(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	hermeticRendezvous(t, r)
	owner := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, owner)
	release, applied, err := schema.AcquireSessionLivelock(r.stateDir, owner)
	if err != nil || !applied || release == nil {
		t.Fatalf("hold owner livelock: applied=%t err=%v", applied, err)
	}
	defer release()

	marker := worktree.FormatDelegateMarker("dlg_reportlive01", owner)
	path := lockLaneWithDelegateMarker(t, r, "live-owner-lane", "dlg_reportlive01", owner)
	requireLaneReportedSkippedAndKept(t, r, "live-owner-lane", path, "locked ("+marker+"); owner-live")
}

// TestWorktreePruneReportsUnverifiableDelegateLaneOwner: a marker whose owner
// session has no resolvable meta (a tree this state directory never knew, or
// a record already gone) answers owner-unverifiable — doubt is reported as
// doubt, never as death. The lane is still not collected.
func TestWorktreePruneReportsUnverifiableDelegateLaneOwner(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	hermeticRendezvous(t, r)
	missingOwner := identifier.MustNewSessionID() // no meta, no livelock

	marker := worktree.FormatDelegateMarker("dlg_reportunver01", missingOwner)
	path := lockLaneWithDelegateMarker(t, r, "unverifiable-owner-lane", "dlg_reportunver01", missingOwner)
	requireLaneReportedSkippedAndKept(t, r, "unverifiable-owner-lane", path, "locked ("+marker+"); owner-unverifiable")
}

// TestWorktreePruneReportsLiveOwnerViaRendezvous: the rendezvous arm — a
// serve daemon's root may carry no livelock at all (a session resumed before
// this primitive existed, or a state directory migrated), and a live
// rendezvous pid is the evidence that still proves it alive. Any live signal
// outranks a missing one.
func TestWorktreePruneReportsLiveOwnerViaRendezvous(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	owner := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, owner)
	entry, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(), // this test process: provably alive
		"session_id": owner,
	})
	if err != nil {
		t.Fatalf("marshal rendezvous entry: %v", err)
	}
	// List only reads files named <pid>.json — the file name carries the
	// entry's process identity.
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"), entry, 0o644); err != nil {
		t.Fatalf("seed rendezvous entry: %v", err)
	}

	marker := worktree.FormatDelegateMarker("dlg_reportrendez01", owner)
	path := lockLaneWithDelegateMarker(t, r, "rendezvous-owner-lane", "dlg_reportrendez01", owner)
	requireLaneReportedSkippedAndKept(t, r, "rendezvous-owner-lane", path, "locked ("+marker+"); owner-live")
}
