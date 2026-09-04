package agent

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"

	"primeradiant.com/evener/agent/execenv"
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
	if runtime.GOOS == "windows" {
		t.Skip("flock-based lease verification is unix-only")
	}
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

	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("session close removed the scratch %s, want it retained for the handoff: %v", scratch, err)
	}
	if scratchLeaseHeld(t, scratch) {
		t.Errorf("the launch environment's scratch %s lease is still held after the session closed", scratch)
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
