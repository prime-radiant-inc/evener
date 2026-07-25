package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
)

// These tests cover porcelain-listing failure branches that are reached only
// after an earlier listing succeeded. A `git` shim cannot express that: the
// runner memoizes `worktree list --porcelain` (cachingWorktreeRunner), so
// "fail the Nth call" no longer maps onto "fail this particular read". They
// drive the branch through the scripted GitRunner seam instead, which names the
// read being failed rather than counting subprocesses.

// scriptedGitRunner installs a runner for s that delegates to real git except
// where script says otherwise. script receives the argv and returns
// (stdout, err, handled); handled=false falls through to real git.
//
// Delegating rather than faking everything keeps these tests honest: only the
// single command under test is synthetic, and the surrounding operation runs
// against the real repository exactly as before.
func scriptedGitRunner(s *Session, script func(args []string) (string, error, bool)) {
	s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		delegate := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			if out, err, handled := script(args); handled {
				return out, err
			}
			return delegate(args...)
		}
	}
}

// failNthPorcelainRead returns a script that fails the nth (1-based)
// `worktree list --porcelain` it sees and passes everything else through.
// Counting happens at the seam BELOW the cache, so n counts real reads — the
// reads the production code actually issues — not memo hits.
func failNthPorcelainRead(n int, stderr string) func(args []string) (string, error, bool) {
	seen := 0
	return func(args []string) (string, error, bool) {
		if !isWorktreePorcelainList(args) {
			return "", nil, false
		}
		seen++
		if seen == n {
			return "", &gitCmdError{code: 1, args: args, stderr: stderr}, true
		}
		return "", nil, false
	}
}

// worktreePruneSweep3's own registry-hygiene listing failing.
//
// Sweep 3 only issues a fresh read when an earlier sweep changed the registry —
// the runner memoizes the listing otherwise — so this seeds a collectible lane
// for sweep 1 to remove. That removal invalidates the memo, sweep 3 re-reads,
// and this fails that read.
func TestWorktreePrune_Sweep3_ListingFails(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	r.addManagedWorktreeFixture(t, "collectible-lane")
	scriptedGitRunner(r.s, failNthPorcelainRead(2, "forced sweep-3 listing failure"))

	_, err := r.pruneOp(t)
	if err == nil || !strings.Contains(err.Error(), "sweep 3 listing worktrees") {
		t.Fatalf("prune with sweep 3's listing failing: err = %v, want the sweep-3 listing error", err)
	}
}

// The paired case, so the two prune listings stay distinguishable: sweep 1's
// initial scan failing reports the plain listing error, not sweep 3's.
func TestWorktreePrune_InitialListingFails(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	scriptedGitRunner(r.s, failNthPorcelainRead(1, "forced initial listing failure"))

	_, err := r.pruneOp(t)
	if err == nil || !strings.Contains(err.Error(), "listing worktrees") {
		t.Fatalf("prune with the initial listing failing: err = %v, want the listing error", err)
	}
	if strings.Contains(err.Error(), "sweep 3") {
		t.Fatalf("initial-listing failure reported as sweep 3: %v", err)
	}
}

// The lock rule still applies on the happy path: a managed lane left unlocked by
// a clean close is re-locked with this session's marker on re-entry. This is the
// behavior the deleted branch sat next to, and it stays covered.
func TestResumeWorktreeReentry_ManagedUnlockedRelocksFromSharedListing(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("currentEnv WorkingDirectory = %q, want the re-entered lane %q", got, path)
	}
	entry := r.porcelainEntry(t, path)
	want := worktree.FormatSessionMarker(r.s.id)
	if !entry.Locked || entry.LockReason != want {
		t.Fatalf("lock = (%v,%q), want locked with %q", entry.Locked, entry.LockReason, want)
	}
}
