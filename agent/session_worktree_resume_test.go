package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
)

// These cover worktree SessionMeta persistence, resume re-entry, and the
// init-inside occupancy lock (native worktree tools spec §7 "Persistence and
// resume" + §5 table row "session init, launch cwd inside a managed worktree").
//
// This file is MIXED across the two lane harnesses; see docs/testing.md for the
// rule. Most tests' subject is serf's own re-entry DECISION — lock, adopt, refuse
// to the restore root, warn and co-occupy, no-op — plus the notice text and what
// serf recorded in its own SessionMeta, so they run on the scripted git boundary
// (scriptedLaneRepo), with the verification and lock failures the decision reacts
// to injected at the boundary. These six stay on real git (wtRepo) because their
// subject IS git's own behavior:
//
//   - TestResumeWorktreeReentry_ManagedSymlinkCanonicalizesBeforeContainment —
//     canonicalization of a symlinked spelling against git's own canonical
//     registry path
//   - TestResumeWorktreeReentry_UnresolvableMainRootNoticesAndRestoresRoot — a
//     corrupted .git pointer driven through ResolveMainRepoRoot's structural walk
//     and its git-binary fallback
//   - TestResumeWorktreeReentry_NotRegisteredAtPathNoticesAndRestoresRoot —
//     rewrites the real .git/worktrees/<id>/gitdir reverse pointer, so git's own
//     registry reports the worktree at a different path
//   - TestResumeWorktreeReentry_ManagedForeignBareLockUnknownOwnerNotice — the
//     real --porcelain shape of a reasonless `git worktree lock`
//   - TestInitInside_ForeignBareLockUnknownOwnerWarns — same reasonless lock shape
//   - TestInitInside_SymlinkSpelledCwdCanonicalizesStoredPathAndLockKey — the lock
//     key must match git's canonical porcelain entry for the lane

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

// pendingTranscriptWarningMessages returns the message text of every
// currently-buffered pendingTranscriptWarnings entry, WITHOUT draining it.
// resumeWorktreeReentry and applyInitInsideWorktreeLock now buffer their
// warnings there (kata 57j8) rather than emitting directly, so they only
// reach the events channel via the session's OWN emitSessionStartEnvelope
// call during construction. A test that invokes either function a second
// time, directly, on a session that already finished construction (the
// pattern several tests below use to isolate the function under test) will
// never see another emitSessionStartEnvelope flush — inspect the buffer
// itself instead of warningMessages(sess) in that shape of test.
func pendingTranscriptWarningMessages(sess *Session) []string {
	msgs := make([]string, 0, len(sess.pendingTranscriptWarnings))
	for _, w := range sess.pendingTranscriptWarnings {
		msgs = append(msgs, w.Message)
	}
	return msgs
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
	meta.Config.NoProjectPrompts = true
	sess, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(launchDir), meta,
		RestoreSessionConfig{StateDir: r.stateDir, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}},
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
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
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
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	sibling := sr.addSiblingLane(t, "sibling", "sibling")

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
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Simulate the state a clean close leaves behind (spec §7: "unlocked ->
	// ... the clean-close case — close unlocked it").
	sr.unlockLane(t, path)

	meta := schema.SessionMeta{
		ID:                  "01RESUMEMANAGEDUNLOCKED01",
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	sess := sr.restoreSession(t, meta, sr.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, path)
	}
	_, locked, reason := sr.laneLocked(t, path)
	wantMarker := worktree.FormatSessionMarker(meta.ID)
	if !locked || reason != wantMarker {
		t.Errorf("lock = (%v,%q), want locked with %q", locked, reason, wantMarker)
	}
	if got := sess.Meta().WorktreePath; got != path {
		t.Errorf("resumed Meta().WorktreePath = %q, want %q", got, path)
	}
	if !sess.Meta().WorktreeManaged {
		t.Error("resumed Meta().WorktreeManaged = false, want true")
	}
}

func TestResumeWorktreeReentry_ManagedOwnMarkerStale_Adopts(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	creatorID := sr.s.id // create already locked path with serf:<creatorID>

	meta := schema.SessionMeta{
		ID:                  creatorID, // same session id resuming: the crash case
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	sess := sr.restoreSession(t, meta, sr.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, path)
	}
	_, locked, reason := sr.laneLocked(t, path)
	wantMarker := worktree.FormatSessionMarker(creatorID)
	if !locked || reason != wantMarker {
		t.Errorf("lock = (%v,%q), want adopted (unchanged) lock %q", locked, reason, wantMarker)
	}
}

func TestResumeWorktreeReentry_ManagedForeign_RestoresRootAndNotices(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Simulate another session having moved in after our clean close.
	sr.setLaneLock(t, path, "serf:someone-else-session")

	meta := schema.SessionMeta{
		ID:                  "01RESUMEMANAGEDFOREIGN001",
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	sess := sr.restoreSession(t, meta, sr.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != sr.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q (refused re-entry)", got, sr.mainRoot)
	}
	if _, _, reason := sr.laneLocked(t, path); reason != "serf:someone-else-session" {
		t.Errorf("foreign lock must be left untouched, got %q", reason)
	}
	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, path, "someone-else-session") {
		t.Errorf("no notice naming the occupant among warnings: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("resumed (refused) Meta().WorktreePath = %q, want empty", got)
	}
}

func TestResumeWorktreeReentry_ManagedRegisteredOutsideProject_RestoresRootAndNotices(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	sibling := sr.addSiblingLane(t, "outside-managed", "outside-managed")

	meta := schema.SessionMeta{
		ID:                  "01RESUMEMANAGEDOUTSIDE001",
		WorktreePath:        sibling,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	sess := sr.restoreSession(t, meta, sr.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != sr.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, sr.mainRoot)
	}
	msgs := warningMessages(sess)
	if !anyContainsAll(msgs, sibling, "managed") {
		t.Fatalf("no managed-containment notice among warnings: %v", msgs)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Fatalf("resumed outside managed tree with WorktreePath %q, want empty", got)
	}
}

// REAL git: the persisted alias must canonicalize to the SAME path git's own
// registry records for the lane, so both sides of the comparison have to be real.
func TestResumeWorktreeReentry_ManagedSymlinkCanonicalizesBeforeContainment(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "canonical-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
	alias := filepath.Join(t.TempDir(), "canonical-lane-alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatalf("symlink managed lane: %v", err)
	}

	meta := schema.SessionMeta{
		ID:                  "01RESUMEMANAGEDSYMLINK001",
		WorktreePath:        alias,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: r.mainRoot,
	}
	sess := r.restoreWorktreeSession(t, meta, r.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("currentEnv WorkingDirectory = %q, want canonical lane %q", got, path)
	}
	if got := sess.Meta().WorktreePath; got != path {
		t.Fatalf("resumed WorktreePath = %q, want canonical lane %q", got, path)
	}
}

func TestResumeWorktreeReentry_NonManagedPathEntered_ReentersNoLock(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	sibling := sr.addSiblingLane(t, "sibling", "sibling")
	sr.unlockLane(t, sibling)

	meta := schema.SessionMeta{
		ID:                  "01RESUMENONMANAGEDPATH001",
		WorktreePath:        sibling,
		WorktreeManaged:     false,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	sess := sr.restoreSession(t, meta, sr.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != sibling {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, sibling)
	}
	if _, locked, reason := sr.laneLocked(t, sibling); locked {
		t.Errorf("non-managed re-entry must not take a lock, got locked (%q)", reason)
	}
	if got := sess.Meta().WorktreePath; got != sibling {
		t.Errorf("Meta().WorktreePath = %q, want %q", got, sibling)
	}
	if sess.Meta().WorktreeManaged {
		t.Error("Meta().WorktreeManaged = true, want false")
	}
}

func TestResumeWorktreeReentry_WorktreeGone_RestoresRootAndNotices(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	ghost := filepath.Join(sr.stateDir, "worktrees", "ghost-project", "ghost-lane")

	meta := schema.SessionMeta{
		ID:                  "01RESUMEWORKTREEGONE00001",
		WorktreePath:        ghost,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	sess := sr.restoreSession(t, meta, sr.mainRoot)

	if got := sess.currentEnv().WorkingDirectory(); got != sr.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, sr.mainRoot)
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
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// Simulate a kept lane that lost its lock (spec §5 rev-7 finding: "a
	// session merely launched inside a kept lane held no lock").
	r.unlockLane(t, path)

	sess := r.launchInside(t, path).s

	_, locked, reason := r.laneLocked(t, path)
	wantMarker := worktree.FormatSessionMarker(sess.id)
	if !locked || reason != wantMarker {
		t.Errorf("lock = (%v,%q), want locked with %q", locked, reason, wantMarker)
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
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.setLaneLock(t, path, "serf:someone-else-session")

	sess := r.launchInside(t, path).s

	if _, _, reason := r.laneLocked(t, path); reason != "serf:someone-else-session" {
		t.Errorf("foreign lock must be left untouched, got %q", reason)
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

// TestInitInside_CoOccupyWarningRidesAfterSessionStart (kata 57j8) asserts
// that applyInitInsideWorktreeLock's co-occupying warning does not jump the
// SESSION_START envelope on a FRESH session (NewSession, not resume) launched
// inside an existing managed worktree lane that's foreign-locked. et0x's
// ruling applies here exactly as it did to the two call sites it fixed: a
// client only creates its per-thread state off SESSION_START's projection,
// and a warning is never transcript-persisted, so one emitted before
// SESSION_START is lost, not merely reordered.
//
// The kata's own filing initially misattributed this to the RESUME-only
// resumeWorktreeReentry flow; applyInitInsideWorktreeLock runs from
// initSessionState, which NewSession also calls, so a FRESH session hitting
// this exact scenario (launched with its cwd already inside a kept,
// foreign-locked managed lane) reaches the same bug — fixing only the resume
// path would have fixed half of it. This test's r.launchInside goes through
// NewSession, not RestoreSessionFromMetaWithConfig, proving the fresh-session
// path is reachable, not just theorized.
func TestInitInside_CoOccupyWarningRidesAfterSessionStart(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.setLaneLock(t, path, "serf:someone-else-session")

	sess := r.launchInside(t, path).s
	sess.Close()
	var evs []events.SessionEvent
	for ev := range sess.Events() {
		evs = append(evs, ev)
	}

	sessionStartIdx := -1
	warningIdx := -1
	for i, ev := range evs {
		switch ev.Kind {
		case events.EventSessionStart:
			if sessionStartIdx == -1 {
				sessionStartIdx = i
			}
		case events.EventWarning:
			if w, ok := ev.Data.(events.WarningData); ok && strings.Contains(w.Message, "co-occupying") && warningIdx == -1 {
				warningIdx = i
			}
		}
	}
	if sessionStartIdx == -1 {
		t.Fatalf("no SESSION_START event found; got %d events", len(evs))
	}
	if warningIdx == -1 {
		t.Fatalf("no co-occupying WARNING event found; got %+v", evs)
	}
	if warningIdx <= sessionStartIdx {
		t.Fatalf("co-occupying WARNING event (index %d) did not arrive after SESSION_START (index %d) — jumped the envelope", warningIdx, sessionStartIdx)
	}
}

func TestInitInside_NotInWorktree_NoOp(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	sess := r.launchInside(t, r.mainRoot).s
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty (not inside any worktree)", got)
	}
}

// --- resumeWorktreeReentry: gaps left by the scenarios above ---

// TestResumeWorktreeReentry_NonLocalEnvNoOp: worktree re-entry is a
// local-execution-environment-only feature; a non-local env is left
// completely untouched.
func TestResumeWorktreeReentry_NonLocalEnvNoOp(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
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
//
// REAL git: the refusal comes out of ResolveMainRepoRoot, whose structural walk
// and git-binary fallback sit below the worktree git-runner seam entirely.
func TestResumeWorktreeReentry_UnresolvableMainRootNoticesAndRestoresRoot(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("not a gitdir pointer\n"), 0o644); err != nil {
		t.Fatalf("corrupt .git pointer: %v", err)
	}
	restore := hideGitInRepo(t, r.mainRoot)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	restore()
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := pendingTranscriptWarningMessages(r.s)
	if !anyContainsAll(msgs, path, "no longer part of a git repository") {
		t.Errorf("no notice about the unresolvable main root: %v", msgs)
	}
}

// TestResumeWorktreeReentry_WorktreeListFailsNoticesAndRestoresRoot: the
// path's own ".git" pointer resolves fine (structural, no git needed), but
// the `worktree list --porcelain` verification call itself fails (git
// unavailable) — re-entry notices and lands at the restore root.
func TestResumeWorktreeReentry_WorktreeListFailsNoticesAndRestoresRoot(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
			return "", errors.New("scripted git: worktree list unavailable")
		}
		return next(args...)
	})

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := pendingTranscriptWarningMessages(r.s)
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
//
// REAL git: the fixture rewrites the real .git/worktrees/<id>/gitdir reverse
// pointer, and it is git itself that must then report the worktree elsewhere.
func TestResumeWorktreeReentry_NotRegisteredAtPathNoticesAndRestoresRoot(t *testing.T) {
	t.Parallel()
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
	msgs := pendingTranscriptWarningMessages(r.s)
	if !anyContainsAll(msgs, path, "no longer a registered worktree") {
		t.Errorf("no notice about the unregistered path: %v", msgs)
	}
}

// TestResumeWorktreeReentry_ManagedRelockFailsNoticesAndRestoresRoot: the lane is
// genuinely unlocked, but the re-lock command itself fails — re-entry notices and
// lands at the restore root rather than occupying an unprotected lane.
func TestResumeWorktreeReentry_ManagedRelockFailsNoticesAndRestoresRoot(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.unlockLane(t, path)
	fail, _ := r.failLockRunner()
	fail.Store(true)

	meta := schema.SessionMeta{WorktreePath: path, WorktreeManaged: true, WorktreeRestoreRoot: r.mainRoot}
	r.s.resumeWorktreeReentry(meta)

	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("currentEnv WorkingDirectory = %q, want restore root %q", got, r.mainRoot)
	}
	msgs := pendingTranscriptWarningMessages(r.s)
	if !anyContainsAll(msgs, path, "failed to re-lock previous worktree") {
		t.Errorf("no notice about the failed re-lock: %v", msgs)
	}
}

// TestResumeWorktreeReentry_ManagedForeignBareLockUnknownOwnerNotice: a bare
// `git worktree lock` (no --reason) is a reasonless, unparseable lock —
// Foreign with an empty reason — so the notice must fall back to naming "an
// unknown owner" rather than printing an empty occupant.
//
// REAL git: only real git emits the reasonless `locked` porcelain line the empty
// occupant is parsed from.
func TestResumeWorktreeReentry_ManagedForeignBareLockUnknownOwnerNotice(t *testing.T) {
	t.Parallel()
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
	msgs := pendingTranscriptWarningMessages(r.s)
	if !anyContainsAll(msgs, path, "an unknown owner") {
		t.Errorf("no notice naming an unknown owner for the bare-locked worktree: %v", msgs)
	}
}

// --- applyInitInsideWorktreeLock: gaps left by the scenarios above ---

// TestInitInside_NonLocalEnvNoOp: the occupancy lock is a
// local-execution-environment-only feature; a non-local env leaves occupancy
// untracked.
func TestInitInside_NonLocalEnvNoOp(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
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
	t.Parallel()
	r := newScriptedLaneRepo(t)
	notARepo := scriptedCanonicalDir(t, t.TempDir())
	sess := r.launchInside(t, notARepo).s

	sess.applyInitInsideWorktreeLock(true)

	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("Meta().WorktreePath = %q, want empty (unresolvable main root must no-op)", got)
	}
}

// TestInitInside_LockStateUnverifiableWarns: the lockStateOf call inside the
// occupancy check itself fails (git unavailable) — the session warns and
// does NOT track occupancy, rather than guessing the lock state.
func TestInitInside_LockStateUnverifiableWarns(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.unlockLane(t, path)

	second := r.sessionAt(t, r.mainRoot)
	sess := second.s
	sess.mu.Lock()
	local := sess.env.(*execenv.LocalExecutionEnvironment)
	sess.env = local.WithWorkingDirectory(path)
	sess.mu.Unlock()
	second.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
			return "", errors.New("scripted git: worktree list unavailable")
		}
		return next(args...)
	})

	sess.applyInitInsideWorktreeLock(true)

	msgs := pendingTranscriptWarningMessages(sess)
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
	t.Parallel()
	r := newScriptedLaneRepo(t)
	res, err := r.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	r.unlockLane(t, path)

	second := r.sessionAt(t, r.mainRoot)
	sess := second.s
	sess.mu.Lock()
	local := sess.env.(*execenv.LocalExecutionEnvironment)
	sess.env = local.WithWorkingDirectory(path)
	sess.mu.Unlock()
	fail, _ := second.failLockRunner()
	fail.Store(true)

	sess.applyInitInsideWorktreeLock(true)

	msgs := pendingTranscriptWarningMessages(sess)
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
//
// REAL git: only real git emits the reasonless `locked` porcelain line the empty
// occupant is parsed from.
func TestInitInside_ForeignBareLockUnknownOwnerWarns(t *testing.T) {
	t.Parallel()
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

// TestInitInside_SymlinkSpelledCwdCanonicalizesStoredPathAndLockKey: a session
// launched with its cwd inside a managed worktree via a symlinked spelling of
// that same directory (e.g. macOS's /var -> /private/var) must store and lock
// against the SAME canonical path a canonically-spelled launch would. Two
// spellings of one worktree must not hash to two different keys and escape
// the occupancy lock's mutual exclusion.
func TestInitInside_SymlinkSpelledCwdCanonicalizesStoredPathAndLockKey(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	// r.create's own session already holds the lock; free it so the new
	// session below takes a fresh one (isolates this test from the separate
	// foreign/adopt lock-state cases covered above).
	wtGit(t, r.mainRoot, "worktree", "unlock", path)

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatalf("symlink %s -> %s: %v", alias, path, err)
	}
	if resolved, err := filepath.EvalSymlinks(alias); err != nil {
		t.Fatalf("EvalSymlinks(alias): %v", err)
	} else if resolved != path {
		t.Fatalf("test setup: alias resolves to %q, want %q", resolved, path)
	}

	sess := newSession(t, withDir(alias), withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: r.stateDir}))

	if got := sess.Meta().WorktreePath; got != path {
		t.Errorf("Meta().WorktreePath = %q, want canonical %q (symlink-spelled launch cwd escaped canonicalization)", got, path)
	}
	entry := r.porcelainEntry(t, path)
	wantReason := worktree.FormatSessionMarker(sess.id)
	if !entry.Locked || entry.LockReason != wantReason {
		t.Errorf("porcelain entry for %s = locked=%v reason=%q, want locked=true reason=%q (lock key must match git's canonical porcelain entry)", path, entry.Locked, entry.LockReason, wantReason)
	}
}
