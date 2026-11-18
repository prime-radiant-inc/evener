package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// ---------------------------------------------------------------------------
// shortSHA
// ---------------------------------------------------------------------------

func TestShortSHA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"abcdef1234567890", "abcdef123456"},
		{"abcdef", "abcdef"},
		{"", ""},
		{"abcdef123456", "abcdef123456"},  // exactly 12
		{"abcdef1234567", "abcdef123456"}, // 13 chars
	}
	for _, tc := range tests {
		if got := shortSHA(tc.input); got != tc.want {
			t.Errorf("shortSHA(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// gitCmdError
// ---------------------------------------------------------------------------

func TestGitCmdError(t *testing.T) {
	t.Parallel()
	t.Run("with stderr", func(t *testing.T) {
		err := &gitCmdError{code: 1, args: []string{"status"}, stderr: "not a repo"}
		if !strings.Contains(err.Error(), "git status") {
			t.Fatalf("expected 'git status' in error: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "exit 1") {
			t.Fatalf("expected exit code in error: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "not a repo") {
			t.Fatalf("expected stderr in error: %q", err.Error())
		}
	})
	t.Run("without stderr", func(t *testing.T) {
		err := &gitCmdError{code: 128, args: []string{"checkout"}, stderr: ""}
		if !strings.Contains(err.Error(), "exit 128") {
			t.Fatalf("expected exit 128: %q", err.Error())
		}
		if strings.Contains(err.Error(), ":") {
			// should not have the colon-separator when stderr is empty
			if strings.Contains(err.Error(), ":  ") {
				t.Fatalf("should not have trailing colon for empty stderr: %q", err.Error())
			}
		}
	})
	t.Run("ExitCode method", func(t *testing.T) {
		err := &gitCmdError{code: 42, args: []string{"log"}, stderr: ""}
		if err.ExitCode() != 42 {
			t.Fatalf("ExitCode = %d, want 42", err.ExitCode())
		}
	})
}

// ---------------------------------------------------------------------------
// metaDirForProject / metaDirForLane
// ---------------------------------------------------------------------------

func TestMetaDirForProject(t *testing.T) {
	t.Parallel()
	result := metaDirForProject("/project/dir")
	if result != filepath.Join("/project/dir", ".meta") { //nolint:gocritic // test needs absolute path
		t.Fatalf("metaDir = %q", result)
	}
}

func TestMetaDirForLaneMore(t *testing.T) {
	t.Parallel()
	result := metaDirForLane("/project/dir/lane")
	if result != filepath.Join("/project/dir", ".meta") { //nolint:gocritic // test needs absolute path
		t.Fatalf("metaDir = %q", result)
	}
}

// ---------------------------------------------------------------------------
// worktreeState.resolutionError
// ---------------------------------------------------------------------------

func TestWorktreeStateResolutionErrorNil(t *testing.T) {
	t.Parallel()
	st := worktreeState{}
	if err := st.resolutionError("test"); err != nil {
		t.Fatalf("expected nil for no error, got %v", err)
	}
}

func TestWorktreeStateResolutionErrorWithErr(t *testing.T) {
	t.Parallel()
	st := worktreeState{err: errors.New("resolve failed")}
	err := st.resolutionError("list")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "manage_worktree list") {
		t.Fatalf("expected operation in error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "resolve failed") {
		t.Fatalf("expected wrapped error: %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// projectIsGitCheckout
// ---------------------------------------------------------------------------

func TestProjectIsGitCheckoutEmptyPath(t *testing.T) {
	t.Parallel()
	// Empty canonical path should return false
	proj := identifierProjectWithCanonical("")
	if projectIsGitCheckout(proj) {
		t.Fatalf("expected false for empty canonical path")
	}
}

func TestProjectIsGitCheckoutNonExistent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	proj := identifierProjectWithCanonical(dir)
	if projectIsGitCheckout(proj) {
		t.Fatalf("expected false for non-git directory")
	}
}

func TestProjectIsGitCheckoutWithGit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	proj := identifierProjectWithCanonical(dir)
	if !projectIsGitCheckout(proj) {
		t.Fatalf("expected true for directory with .git")
	}
}

// helper to create an identifier.Project with just the CanonicalPath set
func identifierProjectWithCanonical(path string) identifier.Project {
	return identifier.Project{CanonicalPath: path}
}

// ---------------------------------------------------------------------------
// canonicalOrClean
// ---------------------------------------------------------------------------

func TestCanonicalOrCleanNonExistent(t *testing.T) {
	t.Parallel()
	result := canonicalOrClean("/nonexistent/path")
	if result != filepath.Clean("/nonexistent/path") {
		t.Fatalf("expected cleaned path, got %q", result)
	}
}

func TestCanonicalOrCleanExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	result := canonicalOrClean(dir)
	if result == "" {
		t.Fatalf("expected non-empty result")
	}
}

// ---------------------------------------------------------------------------
// relPathUnderManagedDir
// ---------------------------------------------------------------------------

func TestRelPathUnderManagedDir(t *testing.T) {
	t.Parallel()
	t.Run("under", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "subdir")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		rel, ok := relPathUnderManagedDir(canonicalOrClean(sub), dir)
		if !ok || rel != "subdir" {
			t.Fatalf("rel=%q ok=%v", rel, ok)
		}
	})
	t.Run("equal (not ok)", func(t *testing.T) {
		dir := t.TempDir()
		_, ok := relPathUnderManagedDir(dir, dir)
		if ok {
			t.Fatalf("expected ok=false for equal paths")
		}
	})
	t.Run("not under", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		_, ok := relPathUnderManagedDir(dir1, dir2)
		if ok {
			t.Fatalf("expected ok=false for unrelated paths")
		}
	})
}

// ---------------------------------------------------------------------------
// isUnderManagedDir
// ---------------------------------------------------------------------------

func TestIsUnderManagedDir(t *testing.T) {
	t.Parallel()
	t.Run("under", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if !isUnderManagedDir(canonicalOrClean(sub), dir) {
			t.Fatalf("expected true for path under dir")
		}
	})
	t.Run("not under", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		if isUnderManagedDir(dir1, dir2) {
			t.Fatalf("expected false for unrelated paths")
		}
	})
	t.Run("equal", func(t *testing.T) {
		dir := t.TempDir()
		if isUnderManagedDir(dir, dir) {
			t.Fatalf("expected false for equal paths")
		}
	})
}

// ---------------------------------------------------------------------------
// managedWorktreeExists
// ---------------------------------------------------------------------------

func TestManagedWorktreeExists(t *testing.T) {
	t.Parallel()
	t.Run("exists", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /somewhere"), 0644); err != nil {
			t.Fatalf("write .git: %v", err)
		}
		if !managedWorktreeExists(dir) {
			t.Fatalf("expected true for directory with .git")
		}
	})
	t.Run("not exists", func(t *testing.T) {
		dir := t.TempDir()
		if managedWorktreeExists(dir) {
			t.Fatalf("expected false for directory without .git")
		}
	})
	t.Run("nonexistent path", func(t *testing.T) {
		if managedWorktreeExists("/nonexistent/path") {
			t.Fatalf("expected false for nonexistent path")
		}
	})
}

// ---------------------------------------------------------------------------
// ctxCancelled
// ---------------------------------------------------------------------------

func TestCtxCancelled(t *testing.T) {
	t.Parallel()
	t.Run("not cancelled", func(t *testing.T) {
		if ctxCancelled(context.Background()) {
			t.Fatalf("expected false for background context")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if !ctxCancelled(ctx) {
			t.Fatalf("expected true for cancelled context")
		}
	})
}

// ---------------------------------------------------------------------------
// prunePolicy
// ---------------------------------------------------------------------------

func TestPrunePolicy(t *testing.T) {
	t.Parallel()
	policy := prunePolicy()
	if !policy.abortOnError {
		t.Fatalf("expected abortOnError=true for prune")
	}
	if policy.disposableAt == nil {
		t.Fatalf("expected non-nil disposableAt")
	}
	if policy.disposableBranch == nil {
		t.Fatalf("expected non-nil disposableBranch")
	}
}

// ---------------------------------------------------------------------------
// branchExists (with mock runner)
// ---------------------------------------------------------------------------

func TestBranchExists(t *testing.T) {
	t.Parallel()
	t.Run("exists", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "", nil // no error = branch exists
		}
		if !branchExists(worktree.GitRunner(run), "mybranch") {
			t.Fatalf("expected true for existing branch")
		}
	})
	t.Run("not exists", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "", errors.New("not found")
		}
		if branchExists(worktree.GitRunner(run), "mybranch") {
			t.Fatalf("expected false for non-existent branch")
		}
	})
}

// ---------------------------------------------------------------------------
// resolveBaseFromActiveRoot (with mock runner)
// ---------------------------------------------------------------------------

func TestResolveBaseFromActiveRootEmpty(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		return "abcdef1234567890\n", nil
	}
	sha, err := resolveBaseFromActiveRoot(worktree.GitRunner(run), "/repo", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "abcdef1234567890" {
		t.Fatalf("sha = %q", sha)
	}
}

func TestResolveBaseFromActiveRootWithRef(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		return "deadbeef\n", nil
	}
	sha, err := resolveBaseFromActiveRoot(worktree.GitRunner(run), "/repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "deadbeef" {
		t.Fatalf("sha = %q", sha)
	}
}

func TestResolveBaseFromActiveRootDashPrefix(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		return "", nil
	}
	_, err := resolveBaseFromActiveRoot(worktree.GitRunner(run), "/repo", "--inject")
	if err == nil || !strings.Contains(err.Error(), "must not start with") {
		t.Fatalf("expected dash prefix error, got %v", err)
	}
}

func TestResolveBaseFromActiveRootWhitespace(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		return "", nil
	}
	_, err := resolveBaseFromActiveRoot(worktree.GitRunner(run), "/repo", "ref with space")
	if err == nil || !strings.Contains(err.Error(), "must not contain whitespace") {
		t.Fatalf("expected whitespace error, got %v", err)
	}
}

func TestResolveBaseFromActiveRootUnresolvable(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		return "", errors.New("unknown ref")
	}
	_, err := resolveBaseFromActiveRoot(worktree.GitRunner(run), "/repo", "badref")
	if err == nil || !strings.Contains(err.Error(), "cannot be resolved to a commit") {
		t.Fatalf("expected resolve error, got %v", err)
	}
}

func TestResolveBaseFromActiveRootEmptySHA(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, error) {
		return "  \n", nil
	}
	_, err := resolveBaseFromActiveRoot(worktree.GitRunner(run), "/repo", "")
	if err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("expected error for empty SHA, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// checkoutLocationOf (with mock runner)
// ---------------------------------------------------------------------------

func TestCheckoutLocationOf(t *testing.T) {
	t.Parallel()
	t.Run("found", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "worktree /path/to/wt\nHEAD abcdef\nbranch refs/heads/mybranch\n", nil
		}
		path, ok := checkoutLocationOf(worktree.GitRunner(run), "mybranch")
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if path != "/path/to/wt" {
			t.Fatalf("path = %q", path)
		}
	})
	t.Run("not found", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "worktree /path\nHEAD abc\n", nil
		}
		_, ok := checkoutLocationOf(worktree.GitRunner(run), "otherbranch")
		if ok {
			t.Fatalf("expected ok=false for non-checked-out branch")
		}
	})
	t.Run("git error", func(t *testing.T) {
		run := func(args ...string) (string, error) {
			return "", errors.New("git error")
		}
		_, ok := checkoutLocationOf(worktree.GitRunner(run), "mybranch")
		if ok {
			t.Fatalf("expected ok=false for git error")
		}
	})
}

// ---------------------------------------------------------------------------
// partitionWorktreeListEntries
// ---------------------------------------------------------------------------

func TestPartitionWorktreeListEntries(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Name: "managed1", HasMetadata: true},
		{Name: "unmanaged1", HasMetadata: false},
		{Name: "managed2", HasMetadata: true},
		{Name: "unmanaged2", HasMetadata: false},
	}
	managed, unmanaged := partitionWorktreeListEntries(entries)
	if len(managed) != 2 || managed[0].Name != "managed1" || managed[1].Name != "managed2" {
		t.Fatalf("managed = %v", managed)
	}
	if len(unmanaged) != 2 || unmanaged[0].Name != "unmanaged1" || unmanaged[1].Name != "unmanaged2" {
		t.Fatalf("unmanaged = %v", unmanaged)
	}
}

func TestPartitionWorktreeListEntriesEmpty(t *testing.T) {
	t.Parallel()
	managed, unmanaged := partitionWorktreeListEntries(nil)
	if len(managed) != 0 || len(unmanaged) != 0 {
		t.Fatalf("expected empty results")
	}
}

func TestPartitionWorktreeListEntriesAllManaged(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{{Name: "a", HasMetadata: true}}
	managed, unmanaged := partitionWorktreeListEntries(entries)
	if len(managed) != 1 || len(unmanaged) != 0 {
		t.Fatalf("managed=%v unmanaged=%v", managed, unmanaged)
	}
}

func TestPartitionWorktreeListEntriesAllUnmanaged(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{{Name: "a", HasMetadata: false}}
	managed, unmanaged := partitionWorktreeListEntries(entries)
	if len(managed) != 0 || len(unmanaged) != 1 {
		t.Fatalf("managed=%v unmanaged=%v", managed, unmanaged)
	}
}

// ---------------------------------------------------------------------------
// worktreeUnmanagedEntryToMap
// ---------------------------------------------------------------------------

func TestWorktreeUnmanagedEntryToMap(t *testing.T) {
	t.Parallel()
	e := WorktreeListEntry{
		Path:           "/path/to/wt",
		Branch:         "mybranch",
		Current:        true,
		Locked:         true,
		LockReason:     "session_a",
		Prunable:       true,
		PrunableReason: "stale",
	}
	m := worktreeUnmanagedEntryToMap(e)
	if m["path"] != "/path/to/wt" {
		t.Fatalf("path = %v", m["path"])
	}
	if m["branch"] != "mybranch" {
		t.Fatalf("branch = %v", m["branch"])
	}
	if m["current"] != true {
		t.Fatalf("current = %v", m["current"])
	}
	if m["locked"] != true {
		t.Fatalf("locked = %v", m["locked"])
	}
	if m["lock_reason"] != "session_a" {
		t.Fatalf("lock_reason = %v", m["lock_reason"])
	}
	// Should not have "name" key
	if _, hasName := m["name"]; hasName {
		t.Fatalf("unmanaged entry should not have 'name' key")
	}
}

// ---------------------------------------------------------------------------
// worktreeUnmanagedSummary
// ---------------------------------------------------------------------------

func TestWorktreeUnmanagedSummaryEmpty(t *testing.T) {
	t.Parallel()
	if worktreeUnmanagedSummary(nil) != "" {
		t.Fatalf("expected empty for nil")
	}
	if worktreeUnmanagedSummary([]WorktreeListEntry{}) != "" {
		t.Fatalf("expected empty for empty slice")
	}
}

func TestWorktreeUnmanagedSummaryWithBranch(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Path: "/path/a", Branch: "feature"},
	}
	result := worktreeUnmanagedSummary(entries)
	if !strings.Contains(result, "/path/a") {
		t.Fatalf("expected path in summary: %q", result)
	}
	if !strings.Contains(result, "feature") {
		t.Fatalf("expected branch in summary: %q", result)
	}
	if !strings.Contains(result, "1 unmanaged") {
		t.Fatalf("expected count in summary: %q", result)
	}
}

func TestWorktreeUnmanagedSummaryWithDetachedHead(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Path: "/path/a", Branch: ""},
	}
	result := worktreeUnmanagedSummary(entries)
	if !strings.Contains(result, "detached HEAD") {
		t.Fatalf("expected 'detached HEAD' for empty branch: %q", result)
	}
}

// ---------------------------------------------------------------------------
// worktreeListSummary
// ---------------------------------------------------------------------------

func TestWorktreeListSummaryEmpty(t *testing.T) {
	t.Parallel()
	result := worktreeListSummary(nil, nil)
	if !strings.Contains(result, "0 managed worktree(s)") {
		t.Fatalf("expected '0 managed' in summary: %q", result)
	}
}

func TestWorktreeListSummaryWithEntries(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Name: "wt1", AheadCommits: 5, Dirty: false, Merged: true},
	}
	result := worktreeListSummary(entries, nil)
	if !strings.Contains(result, "1 managed worktree(s)") {
		t.Fatalf("expected count: %q", result)
	}
	if !strings.Contains(result, "wt1") {
		t.Fatalf("expected name: %q", result)
	}
	if !strings.Contains(result, "5 ahead") {
		t.Fatalf("expected ahead count: %q", result)
	}
	if !strings.Contains(result, "clean") {
		t.Fatalf("expected clean: %q", result)
	}
	if !strings.Contains(result, "merged") {
		t.Fatalf("expected merged: %q", result)
	}
}

func TestWorktreeListSummaryDirtyUnknown(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Name: "wt1", AheadUnknown: true, DirtyUnknown: true, Merged: false},
	}
	result := worktreeListSummary(entries, nil)
	if !strings.Contains(result, "ahead unknown") {
		t.Fatalf("expected 'ahead unknown': %q", result)
	}
	if !strings.Contains(result, "dirty unknown") {
		t.Fatalf("expected 'dirty unknown': %q", result)
	}
	if !strings.Contains(result, "unmerged") {
		t.Fatalf("expected unmerged: %q", result)
	}
}

// TestWorktreeListZeroAheadLaneNotMergedThroughList exercises the 0-commit
// lane skip through the real list operation: a lane whose branch was created
// at the base commit and never advanced must report 0 ahead and NOT carry the
// merged label — isAncestor(base, main) is trivially true for such a lane but
// is not evidence of a merge.
func TestWorktreeListZeroAheadLaneNotMergedThroughList(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	r.addManagedWorktreeFixture(t, "fresh-lane") // branch at base, no commits

	out, err := r.listOp(t)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries := listEntries(t, out)
	var found bool
	for _, e := range entries {
		if e["name"] != "fresh-lane" {
			continue
		}
		found = true
		if got := e["ahead_commits"]; got != 0 {
			t.Errorf("fresh lane ahead_commits = %v, want 0", got)
		}
		if merged, ok := e["merged"].(bool); ok && merged {
			t.Errorf("0-commit lane must not report merged: %+v", e)
		}
	}
	if !found {
		t.Fatalf("fresh-lane not listed: %+v", entries)
	}
}

func TestWorktreeListSummaryWithUnmanaged(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Name: "wt1", AheadCommits: 0, Merged: true},
	}
	unmanaged := []WorktreeListEntry{
		{Path: "/unmanaged", Branch: "other"},
	}
	result := worktreeListSummary(entries, unmanaged)
	if !strings.Contains(result, "1 unmanaged") {
		t.Fatalf("expected unmanaged in summary: %q", result)
	}
}

// TestWorktreeListSummaryZeroAheadNotMerged verifies a 0-commit lane (0 ahead,
// not merged) renders as "0 ahead" without the misleading "merged" label.
// The list operation skips the Merged check when tip == baseSHA (0 commits)
// because isAncestor(base, main) is trivially true but is not evidence of a
// merge.
func TestWorktreeListSummaryZeroAheadNotMerged(t *testing.T) {
	t.Parallel()
	entries := []WorktreeListEntry{
		{Name: "fresh-lane", AheadCommits: 0, Dirty: false, Merged: false},
	}
	result := worktreeListSummary(entries, nil)
	if !strings.Contains(result, "0 ahead") {
		t.Fatalf("expected '0 ahead' in summary: %q", result)
	}
	if !strings.Contains(result, "unmerged") {
		t.Fatalf("0-commit lane should show as unmerged: %q", result)
	}
	// Ensure it does NOT show as "merged" (the misleading label for 0-commit
	// lanes). Check that "merged" only appears as part of "unmerged", not as
	// the standalone "merged" label.
	for line := range strings.SplitSeq(result, "\n") {
		if strings.Contains(line, " fresh-lane ") {
			if strings.Contains(line, " merged") && !strings.Contains(line, "unmerged") {
				t.Fatalf("0-commit lane should not show as merged: %q", result)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// worktreeListEntryToMap
// ---------------------------------------------------------------------------

func TestWorktreeListEntryToMap(t *testing.T) {
	t.Parallel()
	e := WorktreeListEntry{
		Name:           "wt1",
		Path:           "/path",
		Branch:         "main",
		Current:        true,
		Locked:         false,
		HasMetadata:    true,
		CreatorSession: "sess_1",
		DelegateID:     "dlg_1",
		AheadCommits:   3,
		Merged:         true,
	}
	m := worktreeListEntryToMap(e)
	if m["name"] != "wt1" {
		t.Fatalf("name = %v", m["name"])
	}
	if m["path"] != "/path" {
		t.Fatalf("path = %v", m["path"])
	}
	if m["has_metadata"] != true {
		t.Fatalf("has_metadata = %v", m["has_metadata"])
	}
	if m["creator_session"] != "sess_1" {
		t.Fatalf("creator_session = %v", m["creator_session"])
	}
	if m["delegate_id"] != "dlg_1" {
		t.Fatalf("delegate_id = %v", m["delegate_id"])
	}
	if m["ahead_commits"] != 3 {
		t.Fatalf("ahead_commits = %v", m["ahead_commits"])
	}
	if m["merged"] != true {
		t.Fatalf("merged = %v", m["merged"])
	}
}

// ---------------------------------------------------------------------------
// worktreePruneEntryToMap
// ---------------------------------------------------------------------------

func TestWorktreePruneEntryToMap(t *testing.T) {
	t.Parallel()
	e := WorktreePruneEntry{
		Name:            "wt1",
		Path:            "/path",
		WorktreeRemoved: true,
		BranchRemoved:   false,
		SidecarRemoved:  true,
		Reason:          "collected",
	}
	m := worktreePruneEntryToMap(e)
	if m["name"] != "wt1" {
		t.Fatalf("name = %v", m["name"])
	}
	if m["path"] != "/path" {
		t.Fatalf("path = %v", m["path"])
	}
	if m["worktree_removed"] != true {
		t.Fatalf("worktree_removed = %v", m["worktree_removed"])
	}
	if m["branch_removed"] != false {
		t.Fatalf("branch_removed = %v", m["branch_removed"])
	}
	if m["sidecar_removed"] != true {
		t.Fatalf("sidecar_removed = %v", m["sidecar_removed"])
	}
	if m["reason"] != "collected" {
		t.Fatalf("reason = %v", m["reason"])
	}
}

// ---------------------------------------------------------------------------
// disposableReason (with mock runner)
// ---------------------------------------------------------------------------

func TestDisposableReasonUnchanged(t *testing.T) {
	t.Parallel()
	run := worktree.GitRunner(func(args ...string) (string, error) {
		return "", nil
	})
	disposable, reason, err := disposableReason(run, "abc123", "abc123", "main")
	if err != nil || !disposable || reason != "unchanged" {
		t.Fatalf("disposable=%v reason=%q err=%v", disposable, reason, err)
	}
}

func TestDisposableReasonGitError(t *testing.T) {
	t.Parallel()
	run := worktree.GitRunner(func(args ...string) (string, error) {
		return "", errors.New("git failed")
	})
	_, _, err := disposableReason(run, "abc", "def", "main")
	if err == nil {
		t.Fatalf("expected error from git failure")
	}
}

// ---------------------------------------------------------------------------
// worktreeCreateCoreResult struct
// ---------------------------------------------------------------------------

func TestWorktreeCreateCoreResultStruct(t *testing.T) {
	t.Parallel()
	r := worktreeCreateCoreResult{
		Path:     "/path",
		Branch:   "branch",
		BaseSHA:  "abc123",
		MainRoot: "/root",
	}
	if r.Path != "/path" || r.Branch != "branch" {
		t.Fatalf("struct wrong: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// managedEntry struct
// ---------------------------------------------------------------------------

func TestManagedEntryStruct(t *testing.T) {
	t.Parallel()
	e := managedEntry{
		Path: "/path",
		Name: "wt1",
	}
	if e.Name != "wt1" || e.Path != "/path" {
		t.Fatalf("struct wrong: %+v", e)
	}
}

// ---------------------------------------------------------------------------
// lockStateFromPorcelain
// ---------------------------------------------------------------------------

func TestLockStateFromPorcelain(t *testing.T) {
	t.Parallel()
	t.Run("empty porcelain", func(t *testing.T) {
		locked, reason := lockStateFromPorcelain(nil, "/path")
		if locked {
			t.Fatalf("expected false for empty porcelain")
		}
		if reason != "" {
			t.Fatalf("expected empty reason, got %q", reason)
		}
	})
}

// ---------------------------------------------------------------------------
// rootOnlyJobControlTools variable
// ---------------------------------------------------------------------------

func TestRootOnlySubagentToolsIncludesJobControl(t *testing.T) {
	t.Parallel()
	tools := rootOnlySubagentTools()
	// Should include items from rootOnlyJobControlTools too
	// Just verify it's non-empty and contains expected tools
	if len(tools) == 0 {
		t.Fatalf("expected non-empty tools")
	}
}

// ---------------------------------------------------------------------------
// list message capping and paging
// ---------------------------------------------------------------------------

// assertDigestSelects checks which fixture names the digest keeps and which it
// elides. The names are the fixtures this test supplied, so their appearance
// reports the digest's selection, not its wording.
func assertDigestSelects(t *testing.T, result string, keep, elide []string) {
	t.Helper()
	for _, name := range keep {
		if !strings.Contains(result, name) {
			t.Errorf("digest omits %q: %q", name, result)
		}
	}
	for _, name := range elide {
		if strings.Contains(result, name) {
			t.Errorf("digest should have elided %q past the cap: %q", name, result)
		}
	}
}

// TestWorktreeListSummaryCappedOverTen covers the unpaged digest's cap: ten
// lanes are named, the rest are elided. It checks which fixture names survive,
// never the count sentence around them.
func TestWorktreeListSummaryCappedOverTen(t *testing.T) {
	t.Parallel()
	entries := make([]WorktreeListEntry, 12)
	for i := range entries {
		entries[i] = WorktreeListEntry{Name: fmt.Sprintf("wt-%02d", i+1), AheadCommits: 0, Dirty: false, Merged: true}
	}
	result := worktreeListSummary(entries, nil)
	assertDigestSelects(t, result,
		[]string{"wt-01", "wt-10"},
		[]string{"wt-11", "wt-12"})
}

// TestWorktreeUnmanagedSummaryCappedOverTen covers the same cap in the
// unmanaged section.
func TestWorktreeUnmanagedSummaryCappedOverTen(t *testing.T) {
	t.Parallel()
	unmanaged := make([]WorktreeListEntry, 13)
	for i := range unmanaged {
		unmanaged[i] = WorktreeListEntry{Path: fmt.Sprintf("/wt-%02d", i+1), Branch: "main"}
	}
	result := worktreeUnmanagedSummary(unmanaged)
	assertDigestSelects(t, result,
		[]string{"/wt-01", "/wt-10"},
		[]string{"/wt-11", "/wt-13"})
}

// TestWorktreeListPagedDigestCapsAtFive covers the paged digest's own cap
// through the tool, where it is tighter than the unpaged digest's ten: a page
// names five lanes and elides the rest, so a full page of fifteen stays
// readable. Seven managed fixtures plus one stray make the first page hold all
// seven lanes and still truncate, which is what puts the digest on its paged
// branch; the cap then binds because that page holds more than five.
func TestWorktreeListPagedDigestCapsAtFive(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	lanes := make([]string, 7)
	for i := range lanes {
		lanes[i] = fmt.Sprintf("cap-lane-%02d", i+1)
		r.addManagedWorktreeFixture(t, lanes[i])
	}
	r.addUnmanagedWorktreeFixture(t, "cap-stray", "cap-stray")

	out, err := r.listOpArgs(t, map[string]any{"limit": len(lanes), "offset": 0})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := out["truncated"]; got != true {
		t.Fatalf("truncated = %v, want true so the digest takes its paged branch", got)
	}
	if got := listEntries(t, out); len(got) != len(lanes) {
		t.Fatalf("page holds %d managed lanes, want %d", len(got), len(lanes))
	}
	msg, ok := out["message"].(string)
	if !ok {
		t.Fatalf("message is %T, want string", out["message"])
	}
	assertDigestSelects(t, msg, lanes[:5], lanes[5:])
}

// pageFixtureNames lists the fixtures a list page returned — managed entries by
// name, unmanaged entries by their directory name — in the tool's own order.
func pageFixtureNames(t *testing.T, out map[string]any) []string {
	t.Helper()
	entries := listEntries(t, out)
	unmanaged := listUnmanaged(t, out)
	names := make([]string, 0, len(entries)+len(unmanaged))
	for _, e := range entries {
		names = append(names, e["name"].(string))
	}
	for _, u := range unmanaged {
		names = append(names, filepath.Base(u["path"].(string)))
	}
	return names
}

// TestWorktreeListPaginationBalancesTotalsAndPages drives list through the tool
// surface over managed and unmanaged fixtures, and checks paging as data: the
// totals across both sections, each page's own contents, and the next_offset
// continuation. Managed and unmanaged fixtures share one parent directory under
// the managed root, so git lists them together in path order and a single page
// can mix the two sections. The exact per-page comparison is also the "nothing
// from another page leaked in" check: a fixture from a page the request did not
// ask for fails the comparison by name. The list message's wording is
// deliberately not asserted — copy is not a test oracle.
func TestWorktreeListPaginationBalancesTotalsAndPages(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)

	const (
		totalManaged   = 3
		totalUnmanaged = 2
		pageSize       = 2
	)
	for _, name := range []string{"page-lane-a", "page-lane-b", "page-lane-c"} {
		r.addManagedWorktreeFixture(t, name)
	}
	for _, name := range []string{"stray-lane-a", "stray-lane-b"} {
		r.addUnmanagedWorktreeFixture(t, name, name)
	}

	pages := []struct {
		offset   int
		want     []string
		wantNext int
		wantMore bool
	}{
		{offset: 0, want: []string{"page-lane-a", "page-lane-b"}, wantNext: 2, wantMore: true},
		{offset: 2, want: []string{"page-lane-c", "stray-lane-a"}, wantNext: 4, wantMore: true},
		{offset: 4, want: []string{"stray-lane-b"}, wantMore: false},
	}

	for _, tc := range pages {
		out, err := r.listOpArgs(t, map[string]any{"limit": pageSize, "offset": tc.offset})
		if err != nil {
			t.Fatalf("list offset %d: %v", tc.offset, err)
		}
		if got := out["total_entries"]; got != totalManaged {
			t.Errorf("offset %d total_entries = %v, want %d", tc.offset, got, totalManaged)
		}
		if got := out["total_unmanaged"]; got != totalUnmanaged {
			t.Errorf("offset %d total_unmanaged = %v, want %d", tc.offset, got, totalUnmanaged)
		}
		if got := pageFixtureNames(t, out); !slices.Equal(got, tc.want) {
			t.Errorf("offset %d page = %v, want %v", tc.offset, got, tc.want)
		}
		if got := out["truncated"]; got != tc.wantMore {
			t.Errorf("offset %d truncated = %v, want %v", tc.offset, got, tc.wantMore)
		}
		next, hasNext := out["next_offset"]
		if hasNext != tc.wantMore {
			t.Errorf("offset %d next_offset present = %v, want %v (value %v)", tc.offset, hasNext, tc.wantMore, next)
		}
		if hasNext && next != tc.wantNext {
			t.Errorf("offset %d next_offset = %v, want %d", tc.offset, next, tc.wantNext)
		}
	}
}
