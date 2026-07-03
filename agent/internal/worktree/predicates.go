package worktree

import (
	"errors"
	"fmt"
	"strings"
)

// GitRunner executes a git subcommand rooted at the main repository (the
// control env's cwd, spec §5) and returns its stdout. A predicate that must
// inspect a specific worktree passes "-C", "<wtPath>" as the first two args
// — there is no ambient "current worktree" for the runner, and none of the
// functions in this file ever rely on the main repo's HEAD (rev-6 review:
// consulting HEAD is how `remove` step 9 destroyed an unmerged branch under
// detached-HEAD review).
//
// A non-zero git exit is reported as a non-nil err. When a caller needs to
// distinguish a negative result (e.g. `merge-base --is-ancestor` exiting 1
// for "not an ancestor") from a genuine failure, err must satisfy
// `interface{ ExitCode() int }` on the negative-result path — exactly what
// *exec.ExitError does, so a GitRunner built directly on os/exec needs no
// extra wrapping.
type GitRunner func(args ...string) (stdout string, err error)

// exitCoder is satisfied by *exec.ExitError; declared locally so this
// package never imports os/exec (predicates.go only talks to git through the
// injected GitRunner).
type exitCoder interface{ ExitCode() int }

// MergedResult is the outcome of the two-arm merged predicate (spec §5 prune
// sweep 1's "disposable" merged arm). Arm names which test recognized the
// merge ("ancestry" or "cherry"); it is "" when Merged is false or the
// target was unresolvable. TargetRef is the ref (refs/heads/... or
// refs/remotes/.../...) whose tip was judged against, for reporting.
// TargetUnknown is true when merge_target was empty or no matching ref
// exists anywhere (local or remote-tracking) — the merged arm is then
// disabled and only the unchanged arm applies (spec §5).
type MergedResult struct {
	Merged        bool
	Arm           string
	TargetRef     string
	TargetUnknown bool
}

// splitNonEmptyLines splits git output on "\n" and drops empty lines (the
// trailing newline git always emits, and the whole-string-empty case).
func splitNonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// CleanTree reports whether wtPath's tree is clean (spec §5 remove step 6 /
// prune sweep 1's clean predicate): no modified, staged, or untracked files.
// It runs `git -C <wtPath> status --porcelain=v1 --untracked-files=all`
// through run — a clean tree produces no output. When dirty, offending
// carries the raw porcelain lines (one per changed/untracked path) so a
// caller can report the files at stake, as spec §5 remove step 6 requires.
func CleanTree(run GitRunner, wtPath string) (bool, []string, error) {
	out, err := run("-C", wtPath, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return false, nil, fmt.Errorf("worktree: status %s: %w", wtPath, err)
	}
	offending := splitNonEmptyLines(out)
	return len(offending) == 0, offending, nil
}

// Unchanged reports whether wtPath has never diverged from its recorded
// base SHA (spec §5's closing paragraph): the tree is clean AND the
// worktree's HEAD equals baseSHA. It is the cheap, always-available
// disposal test — no merge_target needed — shared with delegate
// auto-disposal (§9).
func Unchanged(run GitRunner, wtPath, baseSHA string) (bool, error) {
	clean, _, err := CleanTree(run, wtPath)
	if err != nil {
		return false, err
	}
	if !clean {
		return false, nil
	}
	tip, err := run("-C", wtPath, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("worktree: rev-parse HEAD %s: %w", wtPath, err)
	}
	return strings.TrimSpace(tip) == baseSHA, nil
}

// resolveRef resolves ref to its commit SHA, reporting ok=false (not an
// error) when the ref does not exist — a missing ref is an expected input
// to the merged-target search, not a failure.
func resolveRef(run GitRunner, ref string) (sha string, ok bool) {
	out, err := run("rev-parse", "--verify", ref)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// refTip is one candidate merge-target tip: the ref it came from and its
// resolved commit SHA.
type refTip struct {
	ref string
	sha string
}

// remoteTrackingTips returns every `refs/remotes/*/<mergeTarget>` ref (spec
// §5: "any remote-tracking tip") via `git for-each-ref` — the `*` matches
// exactly one path component (the remote name), so this never crosses into
// mergeTarget's own slashes.
func remoteTrackingTips(run GitRunner, mergeTarget string) ([]refTip, error) {
	pattern := "refs/remotes/*/" + mergeTarget
	out, err := run("for-each-ref", "--format=%(refname) %(objectname)", pattern)
	if err != nil {
		return nil, fmt.Errorf("worktree: for-each-ref %s: %w", pattern, err)
	}
	var tips []refTip
	for _, line := range splitNonEmptyLines(out) {
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		tips = append(tips, refTip{ref: ref, sha: sha})
	}
	return tips, nil
}

// isAncestor runs `git merge-base --is-ancestor ancestor descendant`,
// distinguishing exit 1 ("not an ancestor" — a normal negative result) from
// any other non-zero exit (a genuine failure, e.g. an unknown object).
func isAncestor(run GitRunner, ancestor, descendant string) (bool, error) {
	_, err := run("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var ec exitCoder
	if errors.As(err, &ec) && ec.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("worktree: merge-base --is-ancestor %s %s: %w", ancestor, descendant, err)
}

// isBehind reports whether localSHA is a strict ancestor of remoteSHA — the
// "local branch is ... behind" condition spec §5 uses to admit a
// remote-tracking tip alongside (not instead of) an existing local branch.
func isBehind(run GitRunner, localSHA, remoteSHA string) (bool, error) {
	if localSHA == remoteSHA {
		return false, nil
	}
	return isAncestor(run, localSHA, remoteSHA)
}

// cherryEquivalent implements the patch-equivalence arm: every commit git
// reports for `git cherry <targetTip> <tip> <base>` (i.e. every commit in
// base..tip) must be patch-equivalent to some commit already in targetTip
// (a "-" line). Zero commits (nothing since base) is deliberately NOT
// treated as cherry-merged: that is the `Unchanged` predicate's case, and
// conflating them here would let a lane with no work look "merged" via the
// wrong arm — callers wanting the no-commits-since-base case must check
// Unchanged themselves. See the task report for the full rationale.
func cherryEquivalent(run GitRunner, targetTip, tip, base string) (bool, error) {
	out, err := run("cherry", targetTip, tip, base)
	if err != nil {
		return false, fmt.Errorf("worktree: cherry %s %s %s: %w", targetTip, tip, base, err)
	}
	lines := splitNonEmptyLines(out)
	if len(lines) == 0 {
		return false, nil
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "- ") {
			return false, nil
		}
	}
	return true, nil
}

// checkMerged runs both arms of the merged predicate against a single
// candidate target tip: ancestry first (true merges, fast-forwards), then
// patch-equivalence (rebase-merges, single-commit squashes).
func checkMerged(run GitRunner, tip, targetTip, base string) (merged bool, arm string, err error) {
	anc, err := isAncestor(run, tip, targetTip)
	if err != nil {
		return false, "", err
	}
	if anc {
		return true, "ancestry", nil
	}
	cherry, err := cherryEquivalent(run, targetTip, tip, base)
	if err != nil {
		return false, "", err
	}
	if cherry {
		return true, "cherry", nil
	}
	return false, "", nil
}

// Merged implements the two-arm merged predicate of spec §5 prune sweep 1,
// judged against the recorded merge_target's tip: the local branch
// (refs/heads/<mergeTarget>) when it exists and is not behind, otherwise
// any refs/remotes/*/<mergeTarget> tip that does contain the work — never
// the main root's HEAD (rev-6 review: HEAD is whatever the user happens to
// have checked out).
//
// An empty mergeTarget, or a mergeTarget with neither a local nor any
// remote-tracking ref, disables the merged arm entirely: TargetUnknown is
// true and Merged is false (the caller falls back to Unchanged).
func Merged(run GitRunner, tipSHA, mergeTarget, baseSHA string) (MergedResult, error) {
	if mergeTarget == "" {
		return MergedResult{TargetUnknown: true}, nil
	}

	localRef := "refs/heads/" + mergeTarget
	localSHA, localExists := resolveRef(run, localRef)

	remotes, err := remoteTrackingTips(run, mergeTarget)
	if err != nil {
		return MergedResult{}, err
	}

	var candidates []refTip
	if localExists {
		candidates = append(candidates, refTip{ref: localRef, sha: localSHA})
	}
	for _, r := range remotes {
		if !localExists {
			candidates = append(candidates, r)
			continue
		}
		behind, err := isBehind(run, localSHA, r.sha)
		if err != nil {
			return MergedResult{}, err
		}
		if behind {
			candidates = append(candidates, r)
		}
	}

	if len(candidates) == 0 {
		return MergedResult{TargetUnknown: true}, nil
	}

	for _, c := range candidates {
		merged, arm, err := checkMerged(run, tipSHA, c.sha, baseSHA)
		if err != nil {
			return MergedResult{}, err
		}
		if merged {
			return MergedResult{Merged: true, Arm: arm, TargetRef: c.ref}, nil
		}
	}
	return MergedResult{Merged: false, TargetRef: candidates[0].ref}, nil
}

// Adopted implements the two-SHA rule of spec §5 sweep 2: a surviving
// branch counts as adopted (the user built on it after serf's `remove`)
// when its tip is neither the recorded base SHA nor the tip serf recorded
// at removal time. A branch reset back to baseSHA is NOT adopted — it is
// collectible via Unchanged, exactly as if nothing had ever been committed.
// Pure: no git access, so callers pass the SHAs already resolved.
func Adopted(tipSHA, baseSHA, tipAtRemoval string) bool {
	return tipSHA != baseSHA && tipSHA != tipAtRemoval
}
