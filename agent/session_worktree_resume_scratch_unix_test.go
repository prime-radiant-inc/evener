//go:build unix

package agent

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
)

// scratchFollowsReentryFixture builds the shape both re-entry scratch tests
// share: a scripted lane a clean close left unlocked, a resume meta pointing
// at it, and a launch environment that has already run a command — as the
// environment a resume is launched on often has by the time the session
// re-enters its worktree (the launcher's project resolution, for one) — so it
// owns a minted scratch and holds its lease.
func scratchFollowsReentryFixture(t *testing.T, id string) (*scriptedLaneRepo, schema.SessionMeta, *execenv.LocalExecutionEnvironment, string) {
	t.Helper()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	sr.unlockLane(t, path)
	meta := schema.SessionMeta{
		ID:                  id,
		WorktreePath:        path,
		WorktreeManaged:     true,
		WorktreeRestoreRoot: sr.mainRoot,
	}
	env := execenv.NewLocalExecutionEnvironment(sr.mainRoot)
	if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand on the launch environment: %v", err)
	}
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the launch environment minted no session scratch, so there is nothing to follow the session")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the launch environment's scratch lease is not held before the restore")
	}
	return sr, meta, env, scratch
}

// Re-entry swaps the session onto a clone rooted in the persisted worktree,
// and a clone owns nothing of its original, so the launch environment's
// scratch has to follow the session onto the clone: the session's own close
// reaches only the clone, and otherwise the original's lease is held for the
// rest of the daemon's uptime.
func TestResumeWorktreeReentry_LaunchEnvironmentScratchFollowsTheSession(t *testing.T) {
	sr, meta, env, scratch := scratchFollowsReentryFixture(t, "01RESUMESCRATCHFOLLOWS0001")
	sess, err := sr.restoreSessionOn(env, meta, sr.restoreConfig())
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	if got := sess.currentEnv().WorkingDirectory(); got != meta.WorktreePath {
		t.Fatalf("currentEnv WorkingDirectory = %q, want %q (re-entered)", got, meta.WorktreePath)
	}
	reentered, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("re-entered env = %T, want a local environment", sess.currentEnv())
	}
	if got := reentered.SessionScratchDir(); got != scratch {
		t.Errorf("re-entered environment scratch = %q, want the launch environment's %q", got, scratch)
	}

	sess.Close()

	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Errorf("session close left the scratch %s: %v", scratch, err)
	}
}

// A restore that fails after re-entry has no session to hand anything to, so
// the re-entered clone's scratch is dropped — and with the launch
// environment's scratch now the clone's, that drops it too, rather than leaving
// a directory and a held lease that neither the caller's disposal of the
// environment it handed in nor anything else will ever reach.
func TestResumeWorktreeReentry_RejectedRestoreDropsTheLaunchEnvironmentScratch(t *testing.T) {
	sr, meta, env, scratch := scratchFollowsReentryFixture(t, "01RESUMESCRATCHREJECTED001")
	boom := errors.New("restore failed after re-entry")
	cfg := sr.restoreConfig()
	cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return boom
		}
		return nil
	}

	sess, err := sr.restoreSessionOn(env, meta, cfg)
	if !errors.Is(err, boom) {
		t.Fatalf("RestoreSessionFromMetaWithConfig error = %v, want the injected %v", err, boom)
	}
	if sess != nil {
		t.Fatalf("a rejected restore returned a session %p", sess)
	}

	// The lease lives inside the scratch dir, so the directory's removal is the
	// lease's removal too.
	if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the launch environment's scratch %s survived the rejected restore: stat err = %v", scratch, err)
	}
}

// A child session may create its own native worktree lane. When the restore
// hands it an environment it owns (the delegate path now builds one), re-entry
// enters that lane and its scratch follows the session, like a root session.
func TestResumeWorktreeReentry_ChildSessionReentersOwnLane(t *testing.T) {
	sr, meta, env, scratch := scratchFollowsReentryFixture(t, "01RESUMESCRATCHCHILD000001")
	cfg := sr.restoreConfig()
	cfg.spawn.parentSessionID = "01PARENTSESSION00000000001"

	sess, err := sr.restoreSessionOn(env, meta, cfg)
	if err != nil {
		t.Fatalf("child re-entry: %v", err)
	}
	t.Cleanup(sess.Close)
	assertReenteredChildLane(t, sr, sess, meta, scratch)
}

// A bare `serve --resume <child-id>` restores a persisted child with an empty
// spawn carrier; only the persisted meta says it is a subagent. That restore
// already owns its environment, so it re-enters the child's own lane.
func TestResumeWorktreeReentry_BareResumeOfAPersistedChildReentersOwnLane(t *testing.T) {
	sr, meta, env, scratch := scratchFollowsReentryFixture(t, "01RESUMESCRATCHBARECHILD01")
	meta.IsSubagent = true
	meta.ParentSessionID = "01PARENTSESSION00000000002"

	sess, err := sr.restoreSessionOn(env, meta, sr.restoreConfig())
	if err != nil {
		t.Fatalf("bare child resume: %v", err)
	}
	t.Cleanup(sess.Close)
	assertReenteredChildLane(t, sr, sess, meta, scratch)
}

func assertReenteredChildLane(t *testing.T, sr *scriptedLaneRepo, sess *Session, meta schema.SessionMeta, scratch string) {
	t.Helper()
	if got := sess.currentEnv().WorkingDirectory(); got != meta.WorktreePath {
		t.Fatalf("currentEnv WorkingDirectory = %q, want the child lane %q", got, meta.WorktreePath)
	}
	if _, locked, reason := sr.laneLocked(t, meta.WorktreePath); !locked || reason != worktree.FormatSessionMarker(meta.ID) {
		t.Fatalf("child lane lock = locked=%t reason=%q, want its session marker", locked, reason)
	}
	reentered, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("re-entered env = %T, want a local environment", sess.currentEnv())
	}
	if got := reentered.SessionScratchDir(); got != scratch {
		t.Errorf("re-entered environment scratch = %q, want the handed-in environment's %q", got, scratch)
	}
}

// The init-inside occupancy lock is a root session's: a child launched inside
// a managed worktree never takes one (the lane is its parent's lifecycle). A
// bare resume of a persisted child has an empty spawn carrier, so that rule
// has to read the persisted subagent flag as well.
func TestInitInside_BareResumeOfAPersistedChildTakesNoLock(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	res, err := sr.wt().create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)
	sr.unlockLane(t, path)
	meta := schema.SessionMeta{
		ID:              "01RESUMEINITINSIDECHILD001",
		IsSubagent:      true,
		ParentSessionID: "01PARENTSESSION00000000003",
	}

	// Launched inside the lane, in a checkout: the shape the init-inside rule
	// reacts to (launchInside's seam).
	cfg := sr.restoreConfig()
	cfg.testOnly.environmentInfo = scriptedGitRepoEnvironmentInfo
	sess, err := sr.restoreSessionOn(execenv.NewLocalExecutionEnvironment(path), meta, cfg)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(sess.Close)

	if _, locked, reason := sr.laneLocked(t, path); locked {
		t.Errorf("a resumed child took the init-inside lock on %s (%q), want the lane left unlocked", path, reason)
	}
	if got := sess.Meta().WorktreePath; got != "" {
		t.Errorf("a resumed child recorded worktree occupancy %q, want none", got)
	}
}
