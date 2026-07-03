package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
)

// These are REAL-git integration tests for worktree SessionMeta persistence,
// resume re-entry, and the init-inside occupancy lock (native worktree tools
// spec §7 "Persistence and resume" + §5 table row "session init, launch cwd
// inside a managed worktree"). They reuse wtRepo/wtGit/newWorktreeRepo from
// session_tools_worktree_create_test.go and porcelainEntry/canonicalMain from
// session_tools_worktree_switch_test.go.

// warningMessages drains every buffered EventWarning message off sess. Safe
// to call once sess's synchronous construction-time emits are done (they are
// already sitting in the buffered events channel by the time the caller has
// sess back).
func warningMessages(sess *Session) []string {
	var msgs []string
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind != events.EventWarning {
				continue
			}
			if data, ok := ev.Data.(events.WarningData); ok {
				msgs = append(msgs, data.Message)
			}
		default:
			return msgs
		}
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func anyContainsAll(msgs []string, subs ...string) bool {
	for _, m := range msgs {
		if containsAll(m, subs...) {
			return true
		}
	}
	return false
}

// restoreWorktreeSession is the shared driver for the resumeWorktreeReentry
// tests: it calls RestoreSessionFromMetaWithConfig with the given meta,
// rooted at launchDir, against r's isolated state dir.
func (r *wtRepo) restoreWorktreeSession(t *testing.T, meta schema.SessionMeta, launchDir string) *Session {
	t.Helper()
	if meta.ProfileID == "" {
		meta.ProfileID = "openai"
	}
	if meta.Model == "" {
		meta.Model = "gpt-5.2"
	}
	sess, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(launchDir), meta,
		RestoreSessionConfig{StateDir: r.stateDir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// --- SessionMeta round-trip ---

// TestWorktreeMeta_ReflectsManagedOccupancyAfterCreate covers Meta() wiring:
// after create, the live session's meta carries the managed worktree path,
// the managed flag, and the restore root recorded by the first enterWorktree
// (spec §7 "Persistence and resume").
func TestWorktreeMeta_ReflectsManagedOccupancyAfterCreate(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	meta := r.s.Meta()
	if meta.WorktreePath != path {
		t.Errorf("Meta().WorktreePath = %q, want %q", meta.WorktreePath, path)
	}
	if !meta.WorktreeManaged {
		t.Error("Meta().WorktreeManaged = false, want true")
	}
	if meta.WorktreeRestoreRoot != r.mainRoot {
		t.Errorf("Meta().WorktreeRestoreRoot = %q, want %q", meta.WorktreeRestoreRoot, r.mainRoot)
	}
}

// TestWorktreeMeta_PathEnteredNonManagedTracksPathButNotManaged covers a
// by-path switch into a non-managed (unregistered-with-serf) sibling
// worktree: Meta() still records WorktreePath/WorktreeRestoreRoot (spec §7:
// "both switch modes swap the env, so both must survive resume") but
// WorktreeManaged is false.
func TestWorktreeMeta_PathEnteredNonManagedTracksPathButNotManaged(t *testing.T) {
	r := newWorktreeRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "sibling", sibling, r.head)

	out, err := r.switchOp(t, map[string]any{"path": sibling})
	if err != nil {
		t.Fatalf("switch by path: %v", err)
	}
	if out["path"] != sibling {
		t.Fatalf("switch result path = %v, want %s", out["path"], sibling)
	}

	meta := r.s.Meta()
	if meta.WorktreePath != sibling {
		t.Errorf("Meta().WorktreePath = %q, want %q", meta.WorktreePath, sibling)
	}
	if meta.WorktreeManaged {
		t.Error("Meta().WorktreeManaged = true, want false (non-managed by-path entry)")
	}
	if meta.WorktreeRestoreRoot != r.mainRoot {
		t.Errorf("Meta().WorktreeRestoreRoot = %q, want %q", meta.WorktreeRestoreRoot, r.mainRoot)
	}
}

// --- resumeWorktreeReentry (spec §7) ---

func TestResumeWorktreeReentry_ManagedUnlocked_LocksAndRootsEnv(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Simulate the state a clean close leaves behind (spec §7: "unlocked ->
	// ... the clean-close case — close unlocked it").
	wtGit(t, r.mainRoot, "worktree", "unlock", path)

	meta := schema.SessionMeta{
		ID:                  "01RESUMEMANAGEDUNLOCKED01",
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: r.mainRoot,
	}
	sess := r.restoreWorktreeSession(t, meta, r.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, path)
	}
	entry := r.porcelainEntry(t, path)
	wantMarker := worktree.FormatSessionMarker(meta.ID)
	if !entry.Locked || entry.LockReason != wantMarker {
		t.Errorf("lock = (%v,%q), want locked with %q", entry.Locked, entry.LockReason, wantMarker)
	}
	if got := sess.Meta().WorktreePath; got != path {
		t.Errorf("resumed Meta().WorktreePath = %q, want %q", got, path)
	}
	if !sess.Meta().WorktreeManaged {
		t.Error("resumed Meta().WorktreeManaged = false, want true")
	}
}

func TestResumeWorktreeReentry_ManagedOwnMarkerStale_Adopts(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	creatorID := r.s.id // create already locked path with serf:<creatorID>

	meta := schema.SessionMeta{
		ID:                  creatorID, // same session id resuming: the crash case
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: r.mainRoot,
	}
	sess := r.restoreWorktreeSession(t, meta, r.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, path)
	}
	entry := r.porcelainEntry(t, path)
	wantMarker := worktree.FormatSessionMarker(creatorID)
	if !entry.Locked || entry.LockReason != wantMarker {
		t.Errorf("lock = (%v,%q), want adopted (unchanged) lock %q", entry.Locked, entry.LockReason, wantMarker)
	}
}

func TestResumeWorktreeReentry_ManagedForeign_RestoresRootAndNotices(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Simulate another session having moved in after our clean close.
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "serf:someone-else-session", path)

	meta := schema.SessionMeta{
		ID:                  "01RESUMEMANAGEDFOREIGN001",
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: r.mainRoot,
	}
	sess := r.restoreWorktreeSession(t, meta, r.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q (refused re-entry)", got, r.mainRoot)
	}
	entry := r.porcelainEntry(t, path)
	if entry.LockReason != "serf:someone-else-session" {
		t.Errorf("foreign lock must be left untouched, got %q", entry.LockReason)
	}
	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, path, "someone-else-session") {
		t.Errorf("no notice naming the occupant among warnings: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("resumed (refused) Meta().WorktreePath = %q, want empty", got)
	}
}

func TestResumeWorktreeReentry_NonManagedPathEntered_ReentersNoLock(t *testing.T) {
	r := newWorktreeRepo(t)
	sibling := filepath.Join(t.TempDir(), "sibling")
	wtGit(t, r.mainRoot, "worktree", "add", "-b", "sibling", sibling, r.head)

	meta := schema.SessionMeta{
		ID:                  "01RESUMENONMANAGEDPATH001",
		WorktreePath:        sibling,
		WorktreeManaged:     false,
		WorktreeRestoreRoot: r.mainRoot,
	}
	sess := r.restoreWorktreeSession(t, meta, r.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != sibling {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, sibling)
	}
	entry := r.porcelainEntry(t, sibling)
	if entry.Locked {
		t.Errorf("non-managed re-entry must not take a lock, got locked (%q)", entry.LockReason)
	}
	if got := sess.Meta().WorktreePath; got != sibling {
		t.Errorf("Meta().WorktreePath = %q, want %q", got, sibling)
	}
	if sess.Meta().WorktreeManaged {
		t.Error("Meta().WorktreeManaged = true, want false")
	}
}

func TestResumeWorktreeReentry_WorktreeGone_RestoresRootAndNotices(t *testing.T) {
	r := newWorktreeRepo(t)
	ghost := filepath.Join(r.stateDir, "worktrees", "ghost-project", "ghost-lane")

	meta := schema.SessionMeta{
		ID:                  "01RESUMEWORKTREEGONE00001",
		WorktreePath:        ghost,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: r.mainRoot,
	}
	sess := r.restoreWorktreeSession(t, meta, r.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, ghost, "no longer exists") {
		t.Errorf("no notice about the missing worktree among warnings: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("resumed (gone) Meta().WorktreePath = %q, want empty", got)
	}
}

// --- init-inside occupancy lock (spec §5) ---

func TestInitInside_ManagedUnlocked_LocksAtSessionStart(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Simulate a kept lane that lost its lock (spec §5 rev-7 finding: "a
	// session merely launched inside a kept lane held no lock").
	wtGit(t, r.mainRoot, "worktree", "unlock", path)

	sess := newSession(t, withDir(path), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))

	entry := r.porcelainEntry(t, path)
	wantMarker := worktree.FormatSessionMarker(sess.id)
	if !entry.Locked || entry.LockReason != wantMarker {
		t.Errorf("lock = (%v,%q), want locked with %q", entry.Locked, entry.LockReason, wantMarker)
	}
	meta := sess.Meta()
	if meta.WorktreePath != path {
		t.Errorf("Meta().WorktreePath = %q, want %q", meta.WorktreePath, path)
	}
	if !meta.WorktreeManaged {
		t.Error("Meta().WorktreeManaged = false, want true")
	}
}

func TestInitInside_ManagedForeign_WarnsAndContinuesCoOccupying(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "serf:someone-else-session", path)

	sess := newSession(t, withDir(path), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))

	entry := r.porcelainEntry(t, path)
	if entry.LockReason != "serf:someone-else-session" {
		t.Errorf("foreign lock must be left untouched, got %q", entry.LockReason)
	}
	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, path, "someone-else-session", "co-occupying") {
		t.Errorf("no co-occupying notice among warnings: %v", msgs)
	}
	// The session continues (a session cannot be un-launched) and still
	// tracks its own occupancy despite the foreign lock (spec §5: "the
	// session continues but warns loudly that it is co-occupying").
	if got := sess.Meta().WorktreePath; got != path {
		t.Errorf("Meta().WorktreePath = %q, want %q (co-occupying still tracked)", got, path)
	}
}

func TestInitInside_NotInWorktree_NoOp(t *testing.T) {
	r := newWorktreeRepo(t)
	sess := newSession(t, withDir(r.mainRoot), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty (not inside any worktree)", got)
	}
}
