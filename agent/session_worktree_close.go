package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// isolationLane names one isolation delegate lane a session created: its
// delegate id (== the worktree name and branch, native worktree tools spec §9
// lifecycle step 1) and the lane's on-disk path (DelegateRestore.WorkingDir).
type isolationLane struct {
	delegateID string
	path       string
}

// ownedIsolationLanes enumerates the isolation delegate lanes THIS session
// created (native worktree tools spec §9 step 4), from the folded job records.
// A lane qualifies when its delegate job carries an isolation="worktree"
// restore descriptor whose ParentSessionID is this session — the field a
// forwarded copy of a descendant's delegate preserves as the ORIGINAL creator,
// so an ancestor never disposes a lane it did not create. Deduped by delegate
// id, since a delegate resumed several times has several job records all
// pointing at the same lane.
func ownedIsolationLanes(recs map[string]*jobstore.JobRecord, sessionID string) []isolationLane {
	seen := make(map[string]bool)
	var lanes []isolationLane
	for _, r := range recs {
		d := r.DelegateRestore
		if d == nil || d.Isolation != "worktree" {
			continue
		}
		if d.ParentSessionID != sessionID {
			continue
		}
		path := strings.TrimSpace(d.WorkingDir)
		if path == "" || r.DelegateID == "" || seen[r.DelegateID] {
			continue
		}
		seen[r.DelegateID] = true
		lanes = append(lanes, isolationLane{delegateID: r.DelegateID, path: path})
	}
	return lanes
}

// disposeDelegateLanesAtClose disposes the isolation delegate lanes this
// session created, in this session's own close path (native worktree tools
// spec §9 step 4). It runs AFTER child sessions are closed and BEFORE the
// jobstore is closed, because the disposed mark (step 5) is a durable append:
// the store must still be open. Per lane, in the load-bearing order the spec
// pins (rev-7 rejected marking first): evaluate the shared unchanged predicate,
// then either UNLOCK+KEEP a changed lane (descriptor untouched, still
// resumable) or UNLOCK → `git worktree remove` (non-force) → mark disposed →
// delete branch+sidecar for an unchanged one. A late dirty write racing the
// clean check makes the non-force remove refuse, which downgrades the lane back
// to KEEP and re-locks it. Every step is best-effort: a lane it cannot cleanly
// dispose is left for `prune` / the crash net (step 5's WorkingDir stat), never
// force-removed. The kept lanes are surfaced as a close-time notice.
func (s *Session) disposeDelegateLanesAtClose() {
	if s.jobManager == nil || s.jobManager.store == nil {
		return
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return // env swapping / local git worktrees are a local-env-only feature
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return
	}
	lanes := ownedIsolationLanes(recs, s.id)
	if len(lanes) == 0 {
		return
	}

	var kept []string
	for _, lane := range lanes {
		if note, wasKept := s.disposeOneDelegateLane(local, lane); wasKept {
			kept = append(kept, note)
		}
	}
	if len(kept) > 0 {
		s.emit(events.EventWarning, events.WarningData{
			Message: "kept " + strconv.Itoa(len(kept)) + " isolation worktree lane(s) with unmerged work at session close: " + strings.Join(kept, "; "),
		})
	}
}

// disposeOneDelegateLane disposes a single isolation lane and reports whether
// it was KEPT (unlocked, descriptor untouched, still resumable) along with a
// human-readable note for the close-time listing. It returns kept=false for a
// removed (unchanged) lane and for any lane it declines to touch (already gone,
// no sidecar, or foreign/session-locked — not the disposer's serf:dlg: lock).
func (s *Session) disposeOneDelegateLane(local *execenv.LocalExecutionEnvironment, lane isolationLane) (note string, kept bool) {
	lanePath := filepath.Clean(lane.path)

	// The lane directory must still exist and be a real linked worktree; a
	// crash after remove (or a prior prune) leaves nothing to do — the crash
	// net (step 5's WorkingDir stat) already refuses revival into it.
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		return "", false
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return "", false
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := gitRunner(context.Background(), controlEnv)

	// The sidecar carries the recorded base SHA the unchanged predicate needs.
	// Without it (unknown provenance) the lane is not ours to judge — leave it.
	metaDir := filepath.Join(filepath.Dir(lanePath), ".meta")
	sc, scErr := worktree.ReadSidecar(metaDir, lane.delegateID)
	if scErr != nil {
		return "", false
	}

	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		return "", false
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, lane.delegateID)
	}

	// Step-4 predicate: unchanged == clean tree AND tip == recorded base SHA;
	// anything else (commits OR a dirty tree) is a CHANGED lane whose work must
	// be preserved.
	unchanged, uErr := worktree.Unchanged(run, lanePath, sc.BaseSHA)
	if uErr != nil {
		// Cannot evaluate cleanly. Fail safe toward preservation: release our
		// own lock (if any) and keep the lane resumable rather than risk
		// removing unexamined work.
		s.unlockLaneIfOwn(run, worktree.EvDisposeChanged, st, lanePath)
		return lane.delegateID + " at " + lanePath + " (state unverifiable)", true
	}

	if !unchanged {
		// CHANGED → unlock and keep; the descriptor is NEVER touched, so the
		// lane stays resumable and `prune` collects it once its branch merges.
		if !s.unlockLaneIfOwn(run, worktree.EvDisposeChanged, st, lanePath) {
			return "", false // foreign / session-locked — not the disposer's dlg lock
		}
		dirty := false
		if clean, _, cErr := worktree.CleanTree(run, lanePath); cErr == nil {
			dirty = !clean
		}
		ahead := 0
		if aheadOut, aErr := run("-C", lanePath, "rev-list", "--count", sc.BaseSHA+"..HEAD"); aErr == nil {
			ahead = countLines(aheadOut)
		}
		return fmt.Sprintf("%s at %s (branch %s, %d ahead, dirty=%t)", lane.delegateID, lanePath, lane.delegateID, ahead, dirty), true
	}

	// UNCHANGED → unlock, then `git worktree remove` (non-force), then mark the
	// descriptor disposed, then delete branch + sidecar.
	switch worktree.Decide(worktree.EvDisposeUnchanged, st) {
	case worktree.ActUnlock:
		if _, err := run("worktree", "unlock", lanePath); err != nil {
			return "", false // cannot release our lock; leave the lane for prune
		}
	case worktree.ActNone:
		// Crash residue: already unlocked. The unlock is vacuous (running it on
		// an unlocked tree is fatal on git), so proceed straight to remove.
	default:
		return "", false // ActRefuse: not the disposer's dlg lock — leave it
	}

	if s.worktreeDisposeBeforeRemove != nil {
		s.worktreeDisposeBeforeRemove(lanePath)
	}
	if _, err := run("worktree", "remove", "--", lanePath); err != nil {
		// A late dirty write raced the clean check and git refused the
		// non-force remove: downgrade back to KEEP and re-lock the lane so it
		// is protected and resumable again.
		marker := worktree.FormatDelegateMarker(lane.delegateID, s.id)
		_, _ = run("worktree", "lock", "--reason", marker, lanePath)
		return lane.delegateID + " at " + lanePath + " (dirty at removal; re-locked)", true
	}

	// The worktree is gone. Mark the descriptor disposed BEFORE deleting the
	// branch/sidecar. A crash between remove and this mark is covered by the
	// stat crash net; the mark is the fast, explicit refusal.
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         s.jobManager.now(),
		DelegateID: lane.delegateID,
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane disposal mark failed for %s: %v", lane.delegateID, err)})
	}

	// Delete the branch (unchanged lane: tip == base, no work lost) and the
	// sidecar. Best-effort — the lane is already unrevivable.
	if _, err := run("branch", "-D", lane.delegateID); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane branch delete failed for %s: %v", lane.delegateID, err)})
	}
	_ = worktree.DeleteSidecar(metaDir, lane.delegateID)
	return "", false
}

// unlockLaneIfOwn routes a changed-lane / unverifiable-lane unlock through the
// pure lock decision core (never hand-rolled reason parsing): it releases the
// lane's serf:dlg: lock when the observed state is the disposer's own marker,
// is a no-op for an already-unlocked (crash-residue) lane, and returns false
// when the state is foreign or a plain session marker (someone switched in) so
// the caller declines to touch it. Only ActUnlock and ActNone keep the lane.
func (s *Session) unlockLaneIfOwn(run worktree.GitRunner, ev worktree.LockEvent, st worktree.LockState, lanePath string) bool {
	switch worktree.Decide(ev, st) {
	case worktree.ActUnlock:
		if _, err := run("worktree", "unlock", lanePath); err != nil {
			return false
		}
		return true
	case worktree.ActNone:
		return true
	default:
		return false
	}
}

// countLines counts the non-empty lines of git output (used for the
// commits-ahead count in the close-time kept-lane listing).
func countLines(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// unlockOwnManagedWorktreeAtClose unlocks the session's OWN occupied managed
// worktree on a clean close (native worktree tools spec §5 close-unlock), so a
// close→resume round-trip re-enters an unlocked tree. This is distinct from
// delegate-lane disposal: the session's own managed worktree (tracked by
// worktreeCurrentManaged / worktreeCurrentPath) is unlocked on disk, never
// removed. The unlock routes through leaveCurrentWorktree, which applies the
// EvLeave lock rule (own session marker → unlock; unlocked / foreign / delegate
// → no-op).
func (s *Session) unlockOwnManagedWorktreeAtClose() {
	s.mu.Lock()
	path := s.worktreeCurrentPath
	managed := s.worktreeCurrentManaged
	s.mu.Unlock()
	if path == "" || !managed {
		return
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return
	}
	rootedAtPath := local.WithWorkingDirectory(path)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtPath, path)
	if mainRoot == "" {
		return
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := gitRunner(context.Background(), controlEnv)
	if err := s.leaveCurrentWorktree(run); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("unlocking own worktree %s at close failed: %v", path, err)})
	}
}
