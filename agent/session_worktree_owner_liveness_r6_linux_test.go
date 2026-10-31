//go:build linux

package agent

// Round-6 regressions for the owner-liveness verdict's rendezvous arm (issue
// #5471's reporting PR, RoboRev findings 1 and 2). Linux-only: the daemon
// identity check reads the process start time from /proc
// (processStartedAt), and the tests pin verdicts that depend on establishing
// or refuting that identity.
//
//   - finding 1: an independently resumed coordinator (serve --resume of a
//     subagent that kept its lineage) has its own rendezvous entry while its
//     original root's livelock sits free; the entry must be attributed to
//     the tree by resolving its session identity to the same root, or the
//     coordinator's nested lanes read dead-looking.
//   - finding 2: a crashed daemon's stale rendezvous entry can be worn by a
//     pid an unrelated process reused; signal-0 liveness alone must not read
//     that as owner-live. A refuted identity is residue (not live evidence);
//     an identity that cannot be established is doubt (owner-unverifiable).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// seedSubagentSession writes a subagent session meta whose parent chain leads
// to parent: the shape a coordinator resumed on its own id retains.
func seedSubagentSession(t *testing.T, stateDir, sid, parent string) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sid, IsSubagent: true, ParentSessionID: parent,
	}); err != nil {
		t.Fatalf("seed coordinator meta: %v", err)
	}
}

// seedRendezvousEntry writes one daemon entry naming sid under the session's
// rendezvous seam, with the daemon registration time a real entry carries.
func seedRendezvousEntry(t *testing.T, dir, sid string, pid int, startedAt time.Time) {
	t.Helper()
	entry, err := json.Marshal(map[string]any{
		"pid":        pid,
		"session_id": sid,
		"started_at": startedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal rendezvous entry: %v", err)
	}
	// List only reads files named <pid>.json — the file name carries the
	// entry's process identity.
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), entry, 0o644); err != nil {
		t.Fatalf("seed rendezvous entry: %v", err)
	}
}

// TestWorktreePruneReportsLiveOwnerViaIndependentlyResumedCoordinator
// (finding 1): the lane's delegate marker names a coordinator whose original
// root's process is gone — its livelock file is present-but-free — but the
// coordinator was resumed on its own id and registered a rendezvous entry.
// The entry must be attributed to the tree by its session identity's root,
// not by string-matching the walked root, or this live owner's lane reads
// dead-looking. The lane is still not collected.
func TestWorktreePruneReportsLiveOwnerViaIndependentlyResumedCoordinator(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	root := identifier.MustNewSessionID()
	coordinator := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, root)
	seedSubagentSession(t, r.stateDir, coordinator, root)
	// The original root's process is gone: present-but-free livelock.
	if err := os.WriteFile(schema.SessionLivelockPath(r.stateDir, root), nil, 0o600); err != nil {
		t.Fatalf("seed free livelock: %v", err)
	}
	// The resumed coordinator's own daemon entry: this test process is the
	// live daemon, registered now (after it started).
	seedRendezvousEntry(t, dir, coordinator, os.Getpid(), time.Now().UTC())

	marker := worktree.FormatDelegateMarker("dlg_reportcoord01", coordinator)
	path := lockLaneWithDelegateMarker(t, r, "coordinator-lane", "dlg_reportcoord01", coordinator)
	requireLaneReportedSkippedAndKept(t, r, "coordinator-lane", path, "locked ("+marker+"); owner-live")
}

// TestWorktreePruneRefutesReusedPidRendezvousEntry (finding 2): the entry's
// pid is alive — but it began long after the entry's daemon registered, so
// the pid was reused by an unrelated process and the entry is a dead daemon's
// residue. Signal-0 liveness alone would read owner-live and hide this
// abandoned lane behind the recipe; the identity check refutes it and the
// free livelock reports the tree dead-looking. Still not collected.
func TestWorktreePruneRefutesReusedPidRendezvousEntry(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	root := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, root)
	if err := os.WriteFile(schema.SessionLivelockPath(r.stateDir, root), nil, 0o600); err != nil {
		t.Fatalf("seed free livelock: %v", err)
	}
	// A stale entry: registered an hour ago by a daemon that has since died,
	// whose pid this test process now happens to reuse.
	seedRendezvousEntry(t, dir, root, os.Getpid(), time.Now().UTC().Add(-time.Hour))

	marker := worktree.FormatDelegateMarker("dlg_reportreuse01", root)
	path := lockLaneWithDelegateMarker(t, r, "reused-pid-lane", "dlg_reportreuse01", root)
	requireLaneReportedSkippedAndKept(t, r, "reused-pid-lane", path, "locked ("+marker+"); owner-dead-looking")
}

// TestWorktreePruneUnverifiableWhenDaemonIdentityCannotBeEstablished
// (finding 2): the entry carries no registration time, so whether its pid
// still names the daemon that wrote it cannot be established. The verdict is
// doubt — owner-unverifiable — even though the pid is alive and the root's
// livelock sits free; an unverifiable entry must never license a
// dead-looking report. Still not collected.
func TestWorktreePruneUnverifiableWhenDaemonIdentityCannotBeEstablished(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	root := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, root)
	if err := os.WriteFile(schema.SessionLivelockPath(r.stateDir, root), nil, 0o600); err != nil {
		t.Fatalf("seed free livelock: %v", err)
	}
	// An entry with no started_at: the identity comparison has nothing to
	// verify against.
	seedRendezvousEntry(t, dir, root, os.Getpid(), time.Time{})

	marker := worktree.FormatDelegateMarker("dlg_reportunest01", root)
	path := lockLaneWithDelegateMarker(t, r, "unestablishable-lane", "dlg_reportunest01", root)
	requireLaneReportedSkippedAndKept(t, r, "unestablishable-lane", path, "locked ("+marker+"); owner-unverifiable")
}

// TestWorktreePruneReportsLiveOwnerViaRendezvous (round 5, moved here): the
// rendezvous arm for a root that carries no livelock at all — a live
// rendezvous pid is the evidence that still proves it alive, now verified as
// the daemon that wrote the entry. The lane is still not collected.
func TestWorktreePruneReportsLiveOwnerViaRendezvous(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	owner := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, owner)
	seedRendezvousEntry(t, dir, owner, os.Getpid(), time.Now().UTC())

	marker := worktree.FormatDelegateMarker("dlg_reportrendez01", owner)
	path := lockLaneWithDelegateMarker(t, r, "rendezvous-owner-lane", "dlg_reportrendez01", owner)
	requireLaneReportedSkippedAndKept(t, r, "rendezvous-owner-lane", path, "locked ("+marker+"); owner-live")
}

// TestWorktreePruneHeldLivelockOutranksUnestablishableEntry (review round 7,
// finding 1): conclusive live evidence takes precedence over doubt. The
// tree's root process holds its livelock, and an attributed rendezvous entry
// whose daemon identity cannot be established (no registration time) is only
// doubt — it must not turn the verdict owner-unverifiable when the livelock
// is held. This is also the darwin shape (processStartedAt has no reader
// there): a served owner must never read unverifiable while its livelock is
// held. The lane is still not collected.
func TestWorktreePruneHeldLivelockOutranksUnestablishableEntry(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	root := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, root)
	release, applied, err := schema.AcquireSessionLivelock(r.stateDir, root)
	if err != nil || !applied || release == nil {
		t.Fatalf("hold root livelock: applied=%t err=%v", applied, err)
	}
	defer release()
	// An attributed entry whose identity cannot be established: a live pid
	// with no registration time.
	seedRendezvousEntry(t, dir, root, os.Getpid(), time.Time{})

	marker := worktree.FormatDelegateMarker("dlg_reportunestheld01", root)
	path := lockLaneWithDelegateMarker(t, r, "unestablishable-held-lane", "dlg_reportunestheld01", root)
	requireLaneReportedSkippedAndKept(t, r, "unestablishable-held-lane", path, "locked ("+marker+"); owner-live")
}

// TestWorktreePruneGonePidEntryDoesNotBlockDeadLooking (review round 7,
// finding 1): a demonstrably gone pid is residue, not doubt. A stale
// rendezvous entry whose daemon's process is gone (here a short-lived child
// that already exited and was reaped) must be discarded, not treated as an
// unavailable start time that defers the verdict — the free livelock's
// dead-looking report is exactly what recovery needs here. The lane is still
// not collected.
func TestWorktreePruneGonePidEntryDoesNotBlockDeadLooking(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	dir := hermeticRendezvous(t, r)
	root := identifier.MustNewSessionID()
	seedOwnerRootSession(t, r.stateDir, root)
	if err := os.WriteFile(schema.SessionLivelockPath(r.stateDir, root), nil, 0o600); err != nil {
		t.Fatalf("seed free livelock: %v", err)
	}
	// The dead daemon: a child that already exited and was reaped, so its
	// pid is demonstrably gone.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatalf("run the short-lived daemon child: %v", err)
	}
	seedRendezvousEntry(t, dir, root, gone.Process.Pid, time.Now().UTC())

	marker := worktree.FormatDelegateMarker("dlg_reportgonepid01", root)
	path := lockLaneWithDelegateMarker(t, r, "gone-pid-lane", "dlg_reportgonepid01", root)
	requireLaneReportedSkippedAndKept(t, r, "gone-pid-lane", path, "locked ("+marker+"); owner-dead-looking")
}
