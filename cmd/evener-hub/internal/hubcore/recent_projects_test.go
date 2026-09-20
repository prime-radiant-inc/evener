package hubcore

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// mkExistingDir creates and returns a real directory under t's temp root so
// RecentProjectDirs' existence filter (issue #50) keeps it.
func mkExistingDir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	return dir
}

// TestPastIndex_RecentProjectDirs_DedupesGlobalRecencyLastN covers the session
// creation flows' recent-project source (issue #35): distinct working dirs in
// the index's most-recently-updated-first order, deduped on first (most
// recent) occurrence, blank dirs skipped, capped at the limit.
func TestPastIndex_RecentProjectDirs_DedupesGlobalRecencyLastN(t *testing.T) {
	root := t.TempDir()
	alpha := mkExistingDir(t, root, "alpha")
	beta := mkExistingDir(t, root, "beta")
	gamma := mkExistingDir(t, root, "gamma")

	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: alpha}},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: beta}},
		{ID: "02wMz5Txv47YP64RR3B9YJ", UpdatedAt: now.Add(-3 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: "  "}},  // blank — skipped
		{ID: "02wMz5Txv5aIxgf9yVdd0N", UpdatedAt: now.Add(-4 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: alpha}}, // dup of alpha, older — dropped
		{ID: "02wMz5Txv733WHFsVy66SR", UpdatedAt: now.Add(-5 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: gamma}},
	})
	got := idx.RecentProjectDirs(15)
	want := []string{alpha, beta, gamma}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentProjectDirs(15) = %v, want %v", got, want)
	}
}

func TestPastIndex_RecentProjectDirs_CapsAtLimit(t *testing.T) {
	root := t.TempDir()
	idx := NewPastIndex("")
	now := time.Now().UTC()
	metas := make([]schema.SessionMeta, 0, 20)
	dirs := make([]string, 0, 20)
	for n := range 20 {
		dir := mkExistingDir(t, root, fmt.Sprintf("proj-%02d", n))
		dirs = append(dirs, dir)
		metas = append(metas, schema.SessionMeta{
			ID:        fmt.Sprintf("02wMz5Txv1C3Hut0M8GC%02d", n),
			UpdatedAt: now.Add(-time.Duration(n) * time.Minute),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: dir},
		})
	}
	idx.SeedForTest(metas)
	got := idx.RecentProjectDirs(15)
	if len(got) != 15 {
		t.Fatalf("RecentProjectDirs(15) returned %d dirs, want 15", len(got))
	}
	if got[0] != dirs[0] || got[14] != dirs[14] {
		t.Fatalf("RecentProjectDirs(15) = %v…%v, want %v…%v (most recent first)", got[0], got[14], dirs[0], dirs[14])
	}
}

func TestPastIndex_RecentProjectDirs_EmptyIndexOrNonPositiveLimitReturnsNil(t *testing.T) {
	idx := NewPastIndex("")
	if got := idx.RecentProjectDirs(15); got != nil {
		t.Fatalf("RecentProjectDirs on empty index = %v, want nil", got)
	}
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: time.Now().UTC(), EnvInfo: schema.EnvironmentInfo{WorkingDir: t.TempDir()}},
	})
	if got := idx.RecentProjectDirs(0); got != nil {
		t.Fatalf("RecentProjectDirs(0) = %v, want nil", got)
	}
}

// TestPastIndex_RecentProjectDirs_DeletedDirsDoNotConsumeLimitSlots pins the
// limit/filter ordering when the distinct dirs outnumber the limit: the cap
// applies after the existence filter, so a deleted dir in the most-recent
// slots must not crowd out an existing dir further down the recency order.
func TestPastIndex_RecentProjectDirs_DeletedDirsDoNotConsumeLimitSlots(t *testing.T) {
	root := t.TempDir()
	deleted := filepath.Join(root, "deleted-project") // never created
	older := mkExistingDir(t, root, "older")
	oldest := mkExistingDir(t, root, "oldest")

	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: deleted}},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: older}},
		{ID: "02wMz5Txv47YP64RR3B9YJ", UpdatedAt: now.Add(-3 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: oldest}},
	})
	got := idx.RecentProjectDirs(2)
	want := []string{older, oldest}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentProjectDirs(2) = %v, want %v (deleted dir %q must not consume a limit slot)", got, want, deleted)
	}
}

// TestPastIndex_RecentProjectDirs_FiltersDeletedDirs covers issue #50: a
// WorkingDir that no longer exists on disk (its project directory was
// deleted) must be excluded from the recent-projects list, while a
// WorkingDir that still exists is kept.
func TestPastIndex_RecentProjectDirs_FiltersDeletedDirs(t *testing.T) {
	root := t.TempDir()
	existing := mkExistingDir(t, root, "still-here")
	deleted := filepath.Join(root, "deleted-project")
	// Never created — simulates a project directory that has since been
	// removed from disk.

	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: deleted}},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: existing}},
	})
	got := idx.RecentProjectDirs(15)
	want := []string{existing}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentProjectDirs(15) = %v, want %v (deleted dir %q must be dropped)", got, want, deleted)
	}
}

// TestPastIndex_RecentProjectDirs_ExcludesSessionMachinery pins the recents
// contract: recents are spawn destinations, so session machinery never
// surfaces as a project. A managed worktree lane's WorkingDir is machinery
// under the state root (WorktreeManaged, set by the native worktree tools'
// create/switch and by delegate isolation lanes): the project it was entered
// from (WorktreeRestoreRoot) carries its recency instead, when known. A
// delegate session (IsSubagent) is machinery too — an isolation lane's meta
// can predate the worktree fields or share its parent's dir, and either way
// the parent session's own dir already carries the signal. A worktree entered
// by path but not managed (WorktreePath set, WorktreeManaged false) is the
// user's own checkout and stays.
func TestPastIndex_RecentProjectDirs_ExcludesSessionMachinery(t *testing.T) {
	root := t.TempDir()
	project := mkExistingDir(t, root, "project")
	restoree := mkExistingDir(t, root, "restoree")          // the rooted lane's restore root — surfaces
	laneRooted := mkExistingDir(t, root, "lane-rooted")     // managed lane, restore root known
	lane := mkExistingDir(t, root, "lane")                  // managed lane, no restore root — skipped
	delegateLane := mkExistingDir(t, root, "delegate-lane") // isolated delegate lane — skipped
	laneSubagent := mkExistingDir(t, root, "lane-subagent") // delegate lane carrying worktree fields — skipped
	restoreeSub := mkExistingDir(t, root, "restoree-sub")   // the subagent lane's restore root — must NOT surface
	unmanaged := mkExistingDir(t, root, "unmanaged")        // path-entered worktree — kept

	idx := NewPastIndex("")
	now := time.Now().UTC()
	idx.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv0RootedLaneWt", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: laneRooted}, WorktreePath: laneRooted, WorktreeManaged: true, WorktreeRestoreRoot: restoree},
		{ID: "02wMz5TxvLaneSubagentW1", UpdatedAt: now.Add(-30 * time.Second), EnvInfo: schema.EnvironmentInfo{WorkingDir: laneSubagent}, WorktreePath: laneSubagent, WorktreeManaged: true, WorktreeRestoreRoot: restoreeSub, IsSubagent: true},
		{ID: "02wMz5TxvDelegateLaneW1", UpdatedAt: now.Add(-1 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: delegateLane}, IsSubagent: true},
		{ID: "02wMz5Txv1C3Hut0M8GCeB", UpdatedAt: now.Add(-2 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: lane}, WorktreePath: lane, WorktreeManaged: true},
		{ID: "02wMz5Txv2enqVTitaig6F", UpdatedAt: now.Add(-3 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: unmanaged}, WorktreePath: unmanaged},
		{ID: "02wMz5Txv47YP64RR3B9YJ", UpdatedAt: now.Add(-4 * time.Minute), EnvInfo: schema.EnvironmentInfo{WorkingDir: project}},
	})
	got := idx.RecentProjectDirs(15)
	want := []string{restoree, unmanaged, project}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentProjectDirs(15) = %v, want %v (machinery dirs %q, %q must be dropped; the rooted lane must surface its restore root %q)", got, want, lane, delegateLane, restoree)
	}
}
