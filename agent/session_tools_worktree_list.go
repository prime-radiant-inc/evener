package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/worktree"
)

// partitionWorktreeListEntries splits list's entries into the ones evener
// manages and the ones it merely found sitting under the managed root. The
// sidecar is the whole test (spec §6 "Metadata sidecar": a worktree without
// one is provenance-unknown). They are reported in separate sections because
// only a managed entry's name is an id the other operations accept — an
// unmanaged entry can be switched into and adopted by path, and nothing else.
func partitionWorktreeListEntries(entries []WorktreeListEntry) (managed, unmanaged []WorktreeListEntry) {
	for _, e := range entries {
		if e.HasMetadata {
			managed = append(managed, e)
			continue
		}
		unmanaged = append(unmanaged, e)
	}
	return managed, unmanaged
}

// worktreeUnmanagedEntryToMap renders an unmanaged worktree found under the
// managed root: deliberately id-less (no "name"), because evener has no claim
// on it and no operation takes it by name until it is adopted. Path is what
// adopt and switch accept; the lock/prunable state is what tells a reader
// whether someone else is living in it.
func worktreeUnmanagedEntryToMap(e WorktreeListEntry) map[string]any {
	return map[string]any{
		"path":            e.Path,
		"branch":          e.Branch,
		"current":         e.Current,
		"locked":          e.Locked,
		"lock_reason":     e.LockReason,
		"prunable":        e.Prunable,
		"prunable_reason": e.PrunableReason,
	}
}

// worktreeUnmanagedSummary renders the unmanaged section's digest, naming the
// operation that converts one and stating exactly how far evener's hands-off
// treatment goes meanwhile — the two facts a reader needs to act. The digest
// promises only what remove step 5 delivers: an unmanaged worktree is never
// pruned, and never removed unless the caller passes force.
func worktreeUnmanagedSummary(unmanaged []WorktreeListEntry) string {
	if len(unmanaged) == 0 {
		return ""
	}
	parts := make([]string, len(unmanaged))
	for i, e := range unmanaged {
		branch := e.Branch
		if branch == "" {
			branch = "detached HEAD"
		}
		parts[i] = fmt.Sprintf("%s (%s)", e.Path, branch)
	}
	return fmt.Sprintf(
		" %d unmanaged worktree(s) under the managed root, which evener never prunes and never removes without force — use manage_worktree adopt with the path to take one over: %s.",
		len(unmanaged), strings.Join(parts, "; "))
}

// worktreeListSummary builds the human-readable list message: a count plus a
// one-line-per-lane digest (name, commits ahead, dirty/clean, merged) so a
// model that reads only the result message — not the structured entries array —
// still learns which lanes have work. A live ergonomics run showed a strong
// model shell out to `git log` per lane when the message was a bare count.
//
// A lane whose state could not be read says so. Rendering its zero values would
// describe it as empty and clean, which is the description a model acts on by
// discarding it.
//
// Unmanaged worktrees found under the managed root are appended as their own
// labeled clause rather than folded into the count, so a reader never mistakes
// one for a lane evener will clean up.
func worktreeListSummary(entries, unmanaged []WorktreeListEntry) string {
	if len(entries) == 0 {
		return "0 managed worktree(s)." + worktreeUnmanagedSummary(unmanaged)
	}
	parts := make([]string, len(entries))
	for i, e := range entries {
		ahead := fmt.Sprintf("%d ahead", e.AheadCommits)
		if e.AheadUnknown {
			ahead = "ahead unknown"
		}
		state := "clean"
		switch {
		case e.DirtyUnknown:
			state = "dirty unknown"
		case e.Dirty:
			state = "dirty"
		}
		merged := "unmerged"
		if e.Merged {
			merged = "merged"
		}
		parts[i] = fmt.Sprintf("%s (%s, %s, %s)", e.Name, ahead, state, merged)
	}
	return fmt.Sprintf("%d managed worktree(s): %s.", len(entries), strings.Join(parts, "; ")) + worktreeUnmanagedSummary(unmanaged)
}

// worktreeListEntryToMap renders a WorktreeListEntry into the tool result
// shape (spec §5 list step 3).
func worktreeListEntryToMap(e WorktreeListEntry) map[string]any {
	return map[string]any{
		"name":                 e.Name,
		"path":                 e.Path,
		"branch":               e.Branch,
		"current":              e.Current,
		"locked":               e.Locked,
		"lock_reason":          e.LockReason,
		"prunable":             e.Prunable,
		"prunable_reason":      e.PrunableReason,
		"has_metadata":         e.HasMetadata,
		"creator_session":      e.CreatorSession,
		"delegate_id":          e.DelegateID,
		"created_at":           e.CreatedAt,
		"age_seconds":          e.AgeSeconds,
		"dirty":                e.Dirty,
		"dirty_unknown":        e.DirtyUnknown,
		"base_sha":             e.BaseSHA,
		"ahead_commits":        e.AheadCommits,
		"ahead_unknown":        e.AheadUnknown,
		"merge_target":         e.MergeTarget,
		"merged":               e.Merged,
		"merged_arm":           e.MergedArm,
		"merge_target_unknown": e.MergeTargetUnknown,
	}
}

// worktreePruneEntryToMap renders a WorktreePruneEntry into the tool result
// shape (spec §5 prune's report shape).
func worktreePruneEntryToMap(e WorktreePruneEntry) map[string]any {
	return map[string]any{
		"name":             e.Name,
		"path":             e.Path,
		"worktree_removed": e.WorktreeRemoved,
		"branch_removed":   e.BranchRemoved,
		"sidecar_removed":  e.SidecarRemoved,
		"reason":           e.Reason,
	}
}

// worktreeList performs the list operation (spec §5 list, all three steps):
// enumerate managed worktrees from `git worktree list --porcelain` (never
// running `git worktree prune`), filter with the same prefix-collision-safe,
// symlink-canonicalized comparison remove/switch use, and report each with
// its lock/prunable state plus disposal-relevant staleness pulled from the
// metadata sidecar and cheap git queries.
func (s *Session) worktreeList(ctx context.Context) ([]WorktreeListEntry, error) {
	// list is read-only, so it is the one operation allowed to resolve the
	// project from a live restore root when the session's own anchor is gone
	// (issue #4671): the lanes that still exist are still enumerated, and the
	// dead lane is simply absent from git's registry (or reported prunable when
	// only its directory was removed).
	st := s.worktreeStateSnapshotWith(true)
	if err := st.resolutionError("list"); err != nil {
		return nil, err
	}
	if st.env == nil {
		return nil, errors.New("manage_worktree requires a local execution environment")
	}
	if st.mainRepoRoot == "" {
		return nil, errors.New("manage_worktree list: not in a git repository")
	}
	run, done, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return nil, err
	}
	defer done()

	// Step 1: `git worktree list --porcelain` — never `git worktree prune`.
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("manage_worktree list: listing worktrees: %w", err)
	}

	// Step 2: filter to evener-managed worktrees, canonicalized comparison.
	projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
	metaDir := metaDirForProject(projectDir)
	managed := managedPorcelainEntries(worktree.ParsePorcelain(out), projectDir)

	activeRoot := filepath.Clean(st.env.WorkingDirectory())
	now := s.sclock().Now()

	// Step 3: build one structured entry per managed worktree.
	entries := make([]WorktreeListEntry, 0, len(managed))
	for _, e := range managed {
		entry := WorktreeListEntry{
			Name:           e.Name,
			Path:           e.Path,
			Branch:         strings.TrimPrefix(e.Branch, "refs/heads/"),
			Current:        filepath.Clean(e.Path) == activeRoot,
			Locked:         e.Locked,
			LockReason:     e.LockReason,
			Prunable:       e.Prunable,
			PrunableReason: e.PrunableReason,
		}

		if sc, scErr := worktree.ReadSidecar(metaDir, e.Name); scErr == nil {
			entry.HasMetadata = true
			entry.CreatorSession = sc.CreatorSession
			entry.DelegateID = sc.DelegateID
			entry.CreatedAt = sc.CreatedAt
			entry.BaseSHA = sc.BaseSHA
			entry.MergeTarget = sc.MergeTarget
			if created, perr := time.Parse(time.RFC3339, sc.CreatedAt); perr == nil {
				entry.AgeSeconds = now.Sub(created).Seconds()
			}
		}

		// Cheap git queries: best-effort, and only meaningful when the
		// worktree directory actually exists (a `prunable` entry's directory
		// is momentarily or permanently absent — spec §5 list step 1).
		if _, statErr := os.Stat(e.Path); statErr == nil {
			if clean, _, cErr := worktree.CleanTree(run, e.Path); cErr == nil {
				entry.Dirty = !clean
			} else {
				entry.DirtyUnknown = true
			}
			// A sidecar with no base SHA — an adopt that could not compute a
			// merge base — cannot answer "how far ahead"; saying so beats
			// rendering the zero value as a confident 0.
			if entry.HasMetadata && entry.BaseSHA == "" {
				entry.AheadUnknown = true
			}
			if entry.HasMetadata && entry.BaseSHA != "" {
				aheadOut, aErr := run("-C", e.Path, "rev-list", "--count", entry.BaseSHA+"..HEAD")
				n, convErr := strconv.Atoi(strings.TrimSpace(aheadOut))
				switch {
				case aErr != nil, convErr != nil:
					entry.AheadUnknown = true
				default:
					entry.AheadCommits = n
				}
				// A failed merge read leaves Merged false, which reads as
				// "unmerged" — the direction that keeps the lane rather than
				// inviting its removal, so it needs no unknown marker.
				if tipOut, tErr := run("-C", e.Path, "rev-parse", "HEAD"); tErr == nil {
					tip := strings.TrimSpace(tipOut)
					// A 0-commit lane (tip == baseSHA) is "unchanged", not
					// "merged": isAncestor(base, main) is trivially true since
					// the branch was cut from main, but that is not evidence of
					// a merge — it is the absence of any work to merge. Skip
					// the Merged check so the lane reads as "0 ahead" rather
					// than the misleading "merged".
					if tip != entry.BaseSHA {
						if mr, mErr := worktree.Merged(run, tip, entry.MergeTarget, entry.BaseSHA); mErr == nil {
							entry.Merged = mr.Merged
							entry.MergedArm = mr.Arm
							entry.MergeTargetUnknown = mr.TargetUnknown
						}
					}
				}
			}
		}

		entries = append(entries, entry)
	}

	return entries, nil
}
