package agent

import (
	"os"
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

// --- resumeWorktreeReentry: gaps left by the scenarios above ---

// TestResumeWorktreeReentry_NonLocalEnvNoOp: worktree re-entry is a
// local-execution-environment-only feature; a non-local env is left
// completely untouched.
func TestResumeWorktreeReentry_NonLocalEnvNoOp(t *testing.T) {
	r := newWorktreeRepo(t)
	r.s.mu.Lock()
	r.s.env = &timeoutEnv{wd: r.mainRoot}
	r.s.mu.Unlock()

	meta := schema.SessionMeta{WorktreePath: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if _, ok := r.s.currentEnv().(*timeoutEnv); !ok {
		t.Error("env replaced despite a non-local execution environment")
	}
	if got := r.s.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty (re-entry must no-op)", got)
	}
}

// TestResumeWorktreeReentry_UnresolvableMainRootNoticesAndRestoresRoot: the
// persisted path's own ".git" pointer no longer resolves to a main repo root
// (corrupted content, git unavailable for the binary fallback) — re-entry
// notices and lands at the restore root instead of guessing.
func TestResumeWorktreeReentry_UnresolvableMainRootNoticesAndRestoresRoot(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git pointer: %v", err)
	}
	restore := hideGitEntirely(t)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	restore()
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "no longer part of a git repository") {
		t.Errorf("no notice about the unresolvable main root: %v", msgs)
	}
}

// TestResumeWorktreeReentry_WorktreeListFailsNoticesAndRestoresRoot: the
// path's own ".git" pointer resolves fine (structural, no git needed), but
// the `worktree list --porcelain` verification call itself fails (git
// unavailable) — re-entry notices and lands at the restore root.
func TestResumeWorktreeReentry_WorktreeListFailsNoticesAndRestoresRoot(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	restore := hideGitEntirely(t)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	restore()
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "could not be verified as a worktree") {
		t.Errorf("no notice about the worktree-list failure: %v", msgs)
	}
}

// TestResumeWorktreeReentry_NotRegisteredAtPathNoticesAndRestoresRoot: the
// path's own ".git" pointer is untouched and resolves fine, but git's
// registry reports the SAME internal worktree id at a different path (its
// reverse "gitdir" pointer was rewritten elsewhere) — the path is no longer
// "registered" at the persisted location, so re-entry notices and lands at
// the restore root.
func TestResumeWorktreeReentry_NotRegisteredAtPathNoticesAndRestoresRoot(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), path)
	bogus := filepath.Join(t.TempDir(), "elsewhere", ".git")
	if err := os.WriteFile(filepath.Join(internalDir, "gitdir"), []byte(bogus+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite reverse gitdir pointer: %v", err)
	}

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "no longer a registered worktree") {
		t.Errorf("no notice about the unregistered path: %v", msgs)
	}
}

// TestResumeWorktreeReentry_ManagedLockStateUnverifiableNoticesAndRestoresRoot:
// the FIRST `worktree list --porcelain` call (the registered-at-path check)
// succeeds, but the SECOND identical call (lockStateOf, inside the managed
// lock check) fails — re-entry notices and lands at the restore root rather
// than guessing the lock state.
func TestResumeWorktreeReentry_ManagedLockStateUnverifiableNoticesAndRestoresRoot(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	gitFailOnNthMatchingCallShim(t, "worktree list --porcelain", 2)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "lock state could not be verified") {
		t.Errorf("no notice about the unverifiable lock state: %v", msgs)
	}
}

// TestResumeWorktreeReentry_ManagedRelockFailsNoticesAndRestoresRoot: the
// lane is genuinely unlocked (clean-close residue), but the re-lock command
// itself fails (permission denied writing the internal marker file) —
// re-entry notices and lands at the restore root rather than re-entering an
// unprotected tree.
func TestResumeWorktreeReentry_ManagedRelockFailsNoticesAndRestoresRoot(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), path)
	chmodReadOnly(t, internalDir)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "failed to re-lock previous worktree") {
		t.Errorf("no notice about the failed re-lock: %v", msgs)
	}
}

// TestResumeWorktreeReentry_ManagedForeignBareLockUnknownOwnerNotice: a bare
// `git worktree lock` (no --reason) is a reasonless, unparseable lock —
// Foreign with an empty reason — so the notice must fall back to naming "an
// unknown owner" rather than printing an empty occupant.
func TestResumeWorktreeReentry_ManagedForeignBareLockUnknownOwnerNotice(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	wtGit(t, r.mainRoot, "worktree", "lock", path) // bare lock: no --reason

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := warningMessages(r.s)
	if !anyContainsAll(msgs, path, "an unknown owner") {
		t.Errorf("no notice naming an unknown owner for the bare-locked worktree: %v", msgs)
	}
}

// --- applyInitInsideWorktreeLock: gaps left by the scenarios above ---

// TestInitInside_NonLocalEnvNoOp: the occupancy lock is a
// local-execution-environment-only feature; a non-local env leaves occupancy
// untracked.
func TestInitInside_NonLocalEnvNoOp(t *testing.T) {
	r := newWorktreeRepo(t)
	r.s.mu.Lock()
	r.s.env = &timeoutEnv{wd: r.mainRoot}
	r.s.mu.Unlock()

	r.s.applyInitInsideWorktreeLock(true)

	if got := r.s.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty (non-local env must no-op)", got)
	}
}

// TestInitInside_UnresolvableMainRootNoOp: isGitRepo is the caller's
// already-computed snapshotGit result; when it says true but the launch cwd
// genuinely is not part of any repository, ResolveMainRepoRoot legitimately
// returns "" and the function must no-op rather than panic or guess.
func TestInitInside_UnresolvableMainRootNoOp(t *testing.T) {
	r := newWorktreeRepo(t)
	notARepo := t.TempDir()
	sess := newSession(t, withDir(notARepo), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))

	sess.applyInitInsideWorktreeLock(true)

	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty (unresolvable main root must no-op)", got)
	}
}

// TestInitInside_LockStateUnverifiableWarns: the lockStateOf call inside the
// occupancy check itself fails (git unavailable) — the session warns and
// does NOT track occupancy, rather than guessing the lock state.
func TestInitInside_LockStateUnverifiableWarns(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)

	sess := newSession(t, withDir(r.mainRoot), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))
	sess.mu.Lock()
	local := sess.env.(*execenv.LocalExecutionEnvironment)
	sess.env = local.WithWorkingDirectory(path)
	sess.mu.Unlock()
	restore := hideGitEntirely(t)

	sess.applyInitInsideWorktreeLock(true)

	restore()
	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, path, "could not inspect the lock") {
		t.Errorf("no warning about the unverifiable lock state: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty", got)
	}
}

// TestInitInside_RelockFailsWarns: the lane is genuinely unlocked, but the
// lock command itself fails (permission denied writing the internal marker
// file) — the session warns and does NOT track occupancy over an
// unprotected lane.
func TestInitInside_RelockFailsWarns(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	internalDir := worktreeInternalDir(t, r.canonicalMain(t), path)
	chmodReadOnly(t, internalDir)

	sess := newSession(t, withDir(r.mainRoot), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))
	sess.mu.Lock()
	local := sess.env.(*execenv.LocalExecutionEnvironment)
	sess.env = local.WithWorkingDirectory(path)
	sess.mu.Unlock()

	sess.applyInitInsideWorktreeLock(true)

	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, path, "failed to lock worktree") {
		t.Errorf("no warning about the failed relock: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty", got)
	}
}

// TestInitInside_ForeignBareLockUnknownOwnerWarns: a bare `git worktree lock`
// (no --reason) is a reasonless, unparseable lock — Foreign with an empty
// reason — so the co-occupying warning must fall back to "an unknown owner"
// rather than naming an empty occupant.
func TestInitInside_ForeignBareLockUnknownOwnerWarns(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	wtGit(t, r.mainRoot, "worktree", "lock", path) // bare lock: no --reason

	sess := newSession(t, withDir(path), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))

	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, path, "an unknown owner", "co-occupying") {
		t.Errorf("no co-occupying notice naming an unknown owner: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != path {
		t.Errorf("Meta().WorktreePath = %q, want %q (co-occupying still tracked)", got, path)
	}
}
