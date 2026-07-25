package agent

import (
	"strings"

	"primeradiant.com/serf/agent/internal/worktree"
)

// worktreeReadOnlyGitCommands are the git commands that cannot change what
// `git worktree list --porcelain` reports: they inspect refs, tree state, or
// ancestry without touching the worktree registry, HEAD, or any branch.
//
// This is an allowlist rather than a denylist of mutating commands, so an
// unrecognized command — including one added later — invalidates the cache.
// Being wrong in that direction costs one extra `git worktree list`; being
// wrong the other way means acting on a stale view of which lanes exist and
// which are locked.
var worktreeReadOnlyGitCommands = map[string]bool{
	"cat-file":         true,
	"check-ref-format": true,
	"config":           true,
	"diff":             true,
	"for-each-ref":     true,
	"log":              true,
	"ls-files":         true,
	"merge-base":       true,
	"rev-list":         true,
	"rev-parse":        true,
	"show":             true,
	"show-ref":         true,
	"status":           true,
	"symbolic-ref":     true, // read form; the write form is caught below
	"var":              true,
	"version":          true,
}

// cachingWorktreeRunner memoizes `git worktree list --porcelain` for the
// lifetime of one logical operation, invalidating on any command that could
// change the listing.
//
// A close or sweep pass evaluates each lane independently, and every lane's
// lock-state check re-ran the listing: a session with N lanes paid N full
// `git worktree list` subprocesses to read state that had not changed between
// them. Across the agent test suite that pattern accounted for 1066 of 3639 git
// invocations (29%), and a real session pays the same cost per close.
//
// The cache is deliberately scoped to a single runner instance — the unit
// callers already treat as one operation (see newWorktreeGitRunner) — so it
// cannot outlive the pass that created it or be shared across sessions. It is
// not safe for concurrent use by design: a GitRunner belongs to one operation
// on one goroutine, matching how every call site uses it.
//
// Returns nil for a nil runner so wrapping stays transparent.
func cachingWorktreeRunner(run worktree.GitRunner) worktree.GitRunner {
	if run == nil {
		return nil
	}
	var (
		cached string
		valid  bool
	)
	return func(args ...string) (string, error) {
		if isWorktreePorcelainList(args) {
			if valid {
				return cached, nil
			}
			out, err := run(args...)
			if err != nil {
				// Never cache a failure: the next read must retry, or one
				// transient git error would be replayed for the whole pass.
				return out, err
			}
			cached, valid = out, true
			return out, nil
		}
		if !isWorktreeListPreserving(args) {
			cached, valid = "", false
		}
		return run(args...)
	}
}

// isWorktreePorcelainList reports whether args is exactly the porcelain listing
// this cache memoizes. Any other `worktree list` spelling (different flags, so
// different output) falls through to the runner uncached.
func isWorktreePorcelainList(args []string) bool {
	return len(args) == 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain"
}

// isWorktreeListPreserving reports whether args is known not to change the
// porcelain listing. Unknown commands are treated as mutating.
func isWorktreeListPreserving(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "worktree":
		// `worktree list` is a read; add/remove/lock/unlock/prune/move/repair
		// all change the registry or its lock reasons.
		return len(args) >= 2 && args[1] == "list"
	case "symbolic-ref":
		// The write form takes a value: `symbolic-ref HEAD refs/heads/x`.
		// Reads pass only a name (plus flags).
		return countNonFlagArgs(args[1:]) <= 1
	}
	return worktreeReadOnlyGitCommands[args[0]]
}

func countNonFlagArgs(args []string) int {
	n := 0
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			n++
		}
	}
	return n
}
