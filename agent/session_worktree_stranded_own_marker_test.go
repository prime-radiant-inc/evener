package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
)

// withStrandedLaneRunner installs a git-runner seam for the test, chaining any
// seam already installed and falling back to the production runner otherwise.
// intercept may observe or perturb a command; when it reports handled true its
// result is used and the command is not run again, otherwise the command runs
// normally. It is how these tests reach the exact git call the residue pass
// issues against one lane without a second production seam.
func withStrandedLaneRunner(t *testing.T, r *wtRepo, intercept func(inner worktree.GitRunner, args []string) (out string, err error, handled bool)) {
	t.Helper()
	base := r.s.cfg.testOnly.worktreeGitRunner
	t.Cleanup(func() { r.s.cfg.testOnly.worktreeGitRunner = base })
	r.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := gitRunner(ctx, env)
		if base != nil {
			inner = base(ctx, env)
		}
		return func(args ...string) (string, error) {
			if out, err, handled := intercept(inner, args); handled {
				return out, err
			}
			return inner(args...)
		}
	}
}

// laneCommandIs reports whether args is the git command listTree issues against
// lanePath, i.e. `git -C <lane> <verb> ...`.
func laneCommandIs(args []string, lanePath, verb string) bool {
	return len(args) >= 3 && args[0] == "-C" && filepath.Clean(args[1]) == filepath.Clean(lanePath) && args[2] == verb
}

// These are REAL-git integration tests for the stranded-lane defect: a managed
// lane that still carries THIS session's own occupancy marker while the session
// occupies a different lane. That residue is what a process death inside a lane
// (followed by a resume that did not re-enter it) leaves behind, and the
// session's own marker is the one thing on the lane nobody else may release.
//
// They build on the wtRepo harness from session_tools_worktree_create_test.go.

// TestWorktreePrune_ReclaimsStrandedOwnMarker pins the fix for the stranded
// lane: prune skips a lane locked with ANY marker (the occupancy rule), so a
// lane carrying the pruning session's own stale marker could never be reclaimed
// by prune at all — the documented recovery (`git worktree unlock`, then prune)
// could not be run through the tool. A lane the session does NOT occupy but
// still marks as its own is crash residue, which `remove` and `switch` already
// treat as unlocked-for-us (spec §5); prune must agree.
//
// The session's own CURRENT lane must stay protected: prune must not unlock or
// collect the lane the session is standing in.
func TestWorktreePrune_ReclaimsStrandedOwnMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "stranded"})
	if err != nil {
		t.Fatalf("create stranded lane: %v", err)
	}
	strandedPath := first["path"].(string)
	second, err := r.create(t, map[string]any{"name": "current"})
	if err != nil {
		t.Fatalf("create current lane: %v", err)
	}
	currentPath := second["path"].(string)

	// create-away released the first lane, so re-lock it with this session's
	// marker to stand in for the residue a dead incarnation left behind.
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(r.s.id), strandedPath)
	if _, locked, _ := r.laneLocked(t, strandedPath); !locked {
		t.Fatal("stranded lane not locked by the test setup")
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	if e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "stranded"); e != nil {
		t.Errorf("stranded lane skipped by prune with reason %v; its own-session marker must not block reclamation", e["reason"])
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "stranded") == nil {
		t.Fatalf("stranded lane not reclaimed by prune: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, strandedPath); locked {
		t.Errorf("stranded lane still locked after prune: %q", reason)
	}
	if _, statErr := os.Stat(strandedPath); !os.IsNotExist(statErr) {
		t.Errorf("stranded lane worktree survived prune: err=%v", statErr)
	}

	// The lane the session actually occupies is not residue: prune protects the
	// current session's occupancy (spec §5 prune sweep 1).
	if _, locked, reason := r.laneLocked(t, currentPath); !locked {
		t.Errorf("prune released the session's own current-lane marker (%q)", reason)
	}
	if _, statErr := os.Stat(currentPath); statErr != nil {
		t.Errorf("prune removed the lane the session occupies: %v", statErr)
	}
}

// TestWorktreePrune_StrandedOwnMarkerOnASkippedLaneKeepsItsLock is the safety
// half of the fix, and the reason the release is not done during evaluation: a
// lane the pass then SKIPS must keep its lock. Letting eligibility treat the
// marker as absent and unlocking eagerly would leave a lane this session's own
// live work is rooted in unlocked, and another session's prune cannot see that
// work — the lock is the only thing stopping it from removing the lane.
//
// Both skip rungs that could strand a live lane are covered: the dirty-lane
// rung (unverifiable-by-anyone would be a different story; a dirty lane is
// simply not collectible) and the live-work rung.
func TestWorktreePrune_StrandedOwnMarkerOnASkippedLaneKeepsItsLock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, r *wtRepo, lanePath string)
	}{
		{
			name: "dirty lane",
			prepare: func(t *testing.T, r *wtRepo, lanePath string) {
				if err := os.WriteFile(filepath.Join(lanePath, "uncommitted.txt"), []byte("wip\n"), 0o644); err != nil {
					t.Fatalf("dirty the lane: %v", err)
				}
			},
		},
		{
			name: "live work under it",
			prepare: func(t *testing.T, r *wtRepo, lanePath string) {
				r.s.worktreeLiveWorkStub = func(string) []string { return []string{"live-real"} }
				t.Cleanup(func() { r.s.worktreeLiveWorkStub = nil })
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newWorktreeRepo(t)
			first, err := r.create(t, map[string]any{"name": "stranded"})
			if err != nil {
				t.Fatalf("create stranded lane: %v", err)
			}
			strandedPath := first["path"].(string)
			if _, err := r.create(t, map[string]any{"name": "current"}); err != nil {
				t.Fatalf("create current lane: %v", err)
			}
			wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(r.s.id), strandedPath)
			tc.prepare(t, r, strandedPath)

			out, err := r.pruneOp(t)
			if err != nil {
				t.Fatalf("prune: %v", err)
			}
			skipped := findPruneEntry(t, pruneEntries(t, out, "skipped"), "stranded")
			if skipped == nil {
				t.Fatalf("stranded lane was not skipped: %+v", out)
			}
			if _, locked, _ := r.laneLocked(t, strandedPath); !locked {
				t.Errorf("prune released the marker of a lane it skipped (%v); the next foreign prune could remove it", skipped["reason"])
			}
			if _, statErr := os.Stat(strandedPath); statErr != nil {
				t.Errorf("stranded lane worktree removed despite being skipped: %v", statErr)
			}
		})
	}
}

// TestWorktreePrune_LeavesAnotherSessionsMarkerAlone pins the bound on the
// fix: only the pruning session's OWN marker is residue it may clear. Another
// session's marker is not the sweeper's to release, dead or alive, so prune
// must still skip it (spec §5: occupancy protects other sessions with no
// liveness guess).
func TestWorktreePrune_LeavesAnotherSessionsMarkerAlone(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "other"})
	if err != nil {
		t.Fatalf("create other lane: %v", err)
	}
	otherPath := first["path"].(string)
	if _, err := r.create(t, map[string]any{"name": "keep"}); err != nil {
		t.Fatalf("create keep lane: %v", err)
	}
	// create-away released the first lane, so re-lock it with a DIFFERENT
	// session's marker while this session occupies the second lane.
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker("some_other_session"), otherPath)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "other")
	if e == nil {
		t.Fatalf("another session's lane was not skipped by prune: %+v", out)
	}
	if _, locked, _ := r.laneLocked(t, otherPath); !locked {
		t.Error("prune released another session's marker")
	}
}

// TestLaneResidueSweep_LeavesOwnMarkerAlone: the automatic residue sweep is
// delegate-only, so it never reaches the collect path for a plain session lane
// and never releases this session's own marker — a lane locked with it is left
// exactly as it was. That is what keeps the sweep out of the lock→record window
// of a create/switch: the session locks its target with its own marker before
// recording that lane as current, and the only operation that releases that
// marker is a prune the session itself runs (a tool call, serialized against
// its own create/switch), never the timer pass.
func TestLaneResidueSweep_LeavesOwnMarkerAlone(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "stranded"})
	if err != nil {
		t.Fatalf("create stranded lane: %v", err)
	}
	strandedPath := first["path"].(string)
	second, err := r.create(t, map[string]any{"name": "current"})
	if err != nil {
		t.Fatalf("create current lane: %v", err)
	}
	currentPath := second["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(r.s.id), strandedPath)

	r.s.runLaneResidueSweep(context.Background())

	if _, locked, reason := r.laneLocked(t, strandedPath); !locked {
		t.Errorf("the residue sweep released a plain session lane's own marker (%q); it must stay delegate-only", reason)
	}
	if _, locked, reason := r.laneLocked(t, currentPath); !locked {
		t.Errorf("the residue sweep released the session's own current-lane marker (%q)", reason)
	}
}

// TestWorktreePrune_FailedCollectionKeepsTheStrandedMarker: the stranded marker
// is released to make way for a collection. When that collection then FAILS with
// the worktree still in place, the marker has to go back: prune reports the lane
// it could not collect, and an unlocked lane is one another session's prune may
// remove without ever seeing this session's live work under it.
//
// The failure is injected at `git worktree remove`, which is exactly the
// transient refusal the release has to survive.
func TestWorktreePrune_FailedCollectionKeepsTheStrandedMarker(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "stranded"})
	if err != nil {
		t.Fatalf("create stranded lane: %v", err)
	}
	strandedPath := first["path"].(string)
	if _, err := r.create(t, map[string]any{"name": "current"}); err != nil {
		t.Fatalf("create current lane: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(r.s.id), strandedPath)

	withStrandedLaneRunner(t, r, func(inner worktree.GitRunner, args []string) (string, error, bool) {
		if len(args) >= 4 && args[0] == "worktree" && args[1] == "remove" &&
			filepath.Clean(args[len(args)-1]) == filepath.Clean(strandedPath) {
			return "", errors.New("injected transient remove failure"), true
		}
		return "", nil, false
	})

	if _, err := r.pruneOp(t); err == nil {
		t.Fatal("prune reported success despite the injected remove failure")
	}
	if _, statErr := os.Stat(strandedPath); statErr != nil {
		t.Fatalf("the lane should still exist after a refused remove: %v", statErr)
	}
	_, locked, reason := r.laneLocked(t, strandedPath)
	if !locked {
		t.Error("a failed collection left the lane unlocked; another session's prune could remove it")
	} else if want := worktree.FormatSessionMarker(r.s.id); reason != want {
		t.Errorf("lane lock reason = %q, want this session's own marker back (%q)", reason, want)
	}
}

// TestWorktreePrune_AbandonsTheReleaseWhenTheMarkerChanged: the residue verdict
// comes from one `worktree list` snapshot taken at the top of the pass, and the
// lane can change hands before its turn — the documented manual `git worktree
// unlock` recovery, or a second process on the same session id, releases the
// marker and another session takes the lane. Releasing on the snapshot's word
// would then unlock THAT session's marker and hand its lane to removal.
//
// The window is recreated at the lane's own disposability check (after the
// snapshot, before the release): the marker is released and another session
// locks the lane there.
func TestWorktreePrune_AbandonsTheReleaseWhenTheMarkerChanged(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	first, err := r.create(t, map[string]any{"name": "stranded"})
	if err != nil {
		t.Fatalf("create stranded lane: %v", err)
	}
	strandedPath := first["path"].(string)
	if _, err := r.create(t, map[string]any{"name": "current"}); err != nil {
		t.Fatalf("create current lane: %v", err)
	}
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(r.s.id), strandedPath)
	foreign := worktree.FormatSessionMarker("another_session")

	var swapped sync.Once
	withStrandedLaneRunner(t, r, func(inner worktree.GitRunner, args []string) (string, error, bool) {
		if laneCommandIs(args, strandedPath, "status") {
			swapped.Do(func() {
				_, _ = inner("worktree", "unlock", strandedPath)
				_, _ = inner("worktree", "lock", "--reason", foreign, strandedPath)
			})
		}
		return "", nil, false
	})

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), "stranded"); e != nil {
		t.Fatalf("prune collected a lane another session had taken: %+v", e)
	}
	if _, statErr := os.Stat(strandedPath); statErr != nil {
		t.Fatalf("the lane was removed despite the foreign marker: %v", statErr)
	}
	_, locked, reason := r.laneLocked(t, strandedPath)
	if !locked || reason != foreign {
		t.Errorf("lane lock = locked=%v reason=%q, want the other session's marker untouched (%q)", locked, reason, foreign)
	}
}
