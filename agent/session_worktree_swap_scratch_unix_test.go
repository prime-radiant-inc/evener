//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// currentLocalEnv returns the session's current environment as the local one
// every worktree swap produces.
func currentLocalEnv(t *testing.T, s *Session) *execenv.LocalExecutionEnvironment {
	t.Helper()
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("current env = %T, want a local environment", s.currentEnv())
	}
	return local
}

// A resumed session re-enters its worktree on a clone that adopted the launch
// environment's scratch, and a later exit swaps it onto the restore-root clone
// saved at re-entry. Whichever environment the session holds when it closes is
// the only one its close releases, so the scratch has to follow the session
// across the exit too — otherwise the lease sits on a clone the exit discarded
// and is held for the rest of the daemon's uptime.
func TestWorktreeSwap_ScratchFollowsTheSessionThroughExitAfterReentry(t *testing.T) {
	sr, meta, env, scratch := scratchFollowsReentryFixture(t, "01RESUMESCRATCHEXIT0000001")
	sess, err := sr.restoreSessionOn(env, meta, sr.restoreConfig())
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(sess.Close)
	sess.mu.Lock()
	sess.worktreeGitVersionOK = true
	sess.mu.Unlock()
	if got := currentLocalEnv(t, sess).SessionScratchDir(); got != scratch {
		t.Fatalf("re-entered environment scratch = %q, want the launch environment's %q", got, scratch)
	}
	r := &wtRepo{s: sess, mainRoot: sr.mainRoot, stateDir: sr.stateDir}

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := currentLocalEnv(t, sess).WorkingDirectory(); got != sr.mainRoot {
		t.Fatalf("after exit the session is rooted at %q, want the restore root %q", got, sr.mainRoot)
	}
	if got := currentLocalEnv(t, sess).SessionScratchDir(); got != scratch {
		t.Errorf("exited environment scratch = %q, want the launch environment's %q", got, scratch)
	}

	sess.Close()

	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("session close removed the scratch %s, want it retained for the handoff: %v", scratch, err)
	}
	if scratchLeaseHeld(t, scratch) {
		t.Errorf("the scratch %s lease is still held after the session closed", scratch)
	}
}

// A live session that enters a worktree, exits it, and enters again holds a
// second clone when it closes, and that clone must own the scratch by then:
// every swap moves ownership onto the environment the session now holds.
func TestWorktreeSwap_ScratchFollowsTheSessionThroughEnterExitEnter(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	launch := currentLocalEnv(t, r.s)
	// The session's first command is what mints the launch environment's
	// scratch and takes its lease.
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand on the launch environment: %v", err)
	}
	scratch := launch.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the launch environment minted no session scratch, so there is nothing to follow the session")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the launch environment's scratch lease is not held")
	}

	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := currentLocalEnv(t, r.s).SessionScratchDir(); got != scratch {
		t.Errorf("entered environment scratch = %q, want the launch environment's %q", got, scratch)
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := currentLocalEnv(t, r.s).SessionScratchDir(); got != scratch {
		t.Errorf("exited environment scratch = %q, want the launch environment's %q", got, scratch)
	}
	if _, err := r.switchOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("switch back into the lane: %v", err)
	}
	if got := currentLocalEnv(t, r.s).SessionScratchDir(); got != scratch {
		t.Errorf("re-entered environment scratch = %q, want the launch environment's %q", got, scratch)
	}

	r.s.Close()

	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("session close removed the scratch %s, want it retained for the handoff: %v", scratch, err)
	}
	if scratchLeaseHeld(t, scratch) {
		t.Errorf("the scratch %s lease is still held after the session closed", scratch)
	}
}

// A child spawned with no working_dir shares the parent's environment object.
// The parent's enter moves the scratch off that object, so the child's next
// command mints a new one there, and the exit's move back must not drop it:
// that scratch is the one the child is working in, and dropping its pointer
// holds its lease for the daemon's uptime while the child's $TMPDIR silently
// changes a second time mid-session. The environment keeps what it owns; the
// incoming scratch is retained instead.
func TestWorktreeSwap_ExitKeepsTheScratchAChildMintedOnTheSharedEnvironment(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	launch := currentLocalEnv(t, r.s)
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	first := launch.SessionScratchDir()
	if first == "" {
		t.Fatal("the root's command minted no session scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(first) })

	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// What a child sharing the launch environment does next: its command mints
	// a scratch on the object the parent's enter just emptied.
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("child command on the launch environment: %v", err)
	}
	second := launch.SessionScratchDir()
	if second == "" || second == first {
		t.Fatalf("shared environment scratch after the enter = %q, want a fresh one beside %q", second, first)
	}
	t.Cleanup(func() { _ = os.RemoveAll(second) })
	if !scratchLeaseHeld(t, second) {
		t.Fatalf("the child's scratch %s lease is not held", second)
	}

	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := currentLocalEnv(t, r.s); got != launch {
		t.Fatalf("after exit the session holds %p, want the launch environment %p", got, launch)
	}
	if got := launch.SessionScratchDir(); got != second {
		t.Errorf("the exit changed the shared environment's scratch from %q to %q", second, got)
	}

	r.s.Close()

	for name, dir := range map[string]string{"launch": first, "child": second} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("session close removed the %s scratch %s, want it retained for the handoff: %v", name, dir, err)
		}
		if scratchLeaseHeld(t, dir) {
			t.Errorf("the %s scratch %s lease is still held after the session closed", name, dir)
		}
	}
}

// A child spawned before its parent enters a worktree shares the environment
// the enter parks (worktreeRestoreEnv) and can mint a scratch there. If the
// parent closes while still entered, the child's teardown skips that
// environment (it is not the child's) and the parent's Cleanup runs on the
// current clone only, so the parked environment's scratch has to be retained
// at the parent's close — without a second process-table cleanup, since the
// parked environment shares the table the current clone's Cleanup just reaped.
func TestParentCloseWhileEnteredRetainsTheParkedEnvironmentScratch(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	parent := r.s
	launch := currentLocalEnv(t, parent)
	// A child with no working_dir shares the parent's environment object.
	child, err := NewSession(parent.client, parent.currentProfile(), launch, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the parent's environment: %v", err)
	}
	parent.subagents.track(&subagent{id: child.id, sess: child, done: make(chan struct{})})
	var cleaned []execenv.ExecutionEnvironment
	parent.cfg.testOnly.envCleanupObserved = func(env execenv.ExecutionEnvironment) { cleaned = append(cleaned, env) }

	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	entered := currentLocalEnv(t, parent)
	if entered == launch {
		t.Fatal("the enter did not swap the parent onto a lane clone")
	}
	// The child's command mints a scratch on the parked environment; the
	// parent's own command mints the current clone's.
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("child command on the parked environment: %v", err)
	}
	parked := launch.SessionScratchDir()
	if parked == "" {
		t.Fatal("the child's command minted no scratch on the parked environment")
	}
	t.Cleanup(func() { _ = os.RemoveAll(parked) })
	if _, err := entered.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("parent command on the lane clone: %v", err)
	}
	current := entered.SessionScratchDir()
	if current == "" || current == parked {
		t.Fatalf("lane clone scratch = %q, want one of its own beside the parked %q", current, parked)
	}
	t.Cleanup(func() { _ = os.RemoveAll(current) })
	if !scratchLeaseHeld(t, parked) || !scratchLeaseHeld(t, current) {
		t.Fatal("both scratch leases must be held before the parent closes")
	}

	parent.Close()

	if got := child.State(); got != SessionClosed {
		t.Errorf("shared-env child state after parent close = %q, want %q", got, SessionClosed)
	}
	for name, dir := range map[string]string{"parked": parked, "current": current} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("parent close removed the %s environment's scratch %s, want it retained for the handoff: %v", name, dir, err)
		}
		if scratchLeaseHeld(t, dir) {
			t.Errorf("the %s environment's scratch %s lease is still held after the parent closed", name, dir)
		}
	}
	if len(cleaned) != 1 || cleaned[0] != execenv.ExecutionEnvironment(entered) {
		t.Errorf("Cleanup ran on %d environment(s) %v, want exactly once on the current clone %p", len(cleaned), cleaned, entered)
	}
}

// retainedScratchDirsIn lists the session scratch dirs under base whose lease
// is no longer held: the ones some environment retained for a handoff.
func retainedScratchDirsIn(t *testing.T, base string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(base, "evener-sandbox-*"))
	if err != nil {
		t.Fatalf("glob session scratch dirs: %v", err)
	}
	var retained []string
	for _, dir := range found {
		if !scratchLeaseHeld(t, dir) {
			retained = append(retained, dir)
		}
	}
	return retained
}

// The swap's git snapshot and prompt pre-warm run commands on the entered
// clone before it is installed, and a command is what mints a scratch on an
// environment that owns none. The clone has to adopt the session's scratch
// before any of that runs, or the snapshot mints a fresh one, the adoption
// keeps it (the clone now owns one) and retains the session's original, and
// the enter silently changes $EVENER_SCRATCH_DIR while leaving an extra
// retained directory behind.
func TestWorktreeSwap_SnapshotOnTheEnteredCloneUsesTheSessionScratch(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	cfg := worktreeTestSessionConfig()
	cfg.testOnly.skipGitSnapshot = false
	r := newWorktreeRepoWithConfig(t, cfg)
	launch := currentLocalEnv(t, r.s)
	// The session's own snapshot at construction minted the launch scratch.
	scratch := launch.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the session's construction snapshot minted no scratch on the launch environment")
	}
	if !scratchLeaseHeld(t, scratch) {
		t.Fatal("the launch environment's scratch lease is not held")
	}
	if got := retainedScratchDirsIn(t, isolated); len(got) != 0 {
		t.Fatalf("retained scratch dirs before the enter = %v, want none", got)
	}

	if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if got := currentLocalEnv(t, r.s).SessionScratchDir(); got != scratch {
		t.Errorf("the enter changed the session's scratch from %q to %q", scratch, got)
	}
	if !scratchLeaseHeld(t, scratch) {
		t.Errorf("the session's scratch %s lease was released by the enter", scratch)
	}
	if got := retainedScratchDirsIn(t, isolated); len(got) != 0 {
		t.Errorf("the enter left retained scratch dirs %v behind, want none", got)
	}
}

// A session's close can begin while an enter is between moving the scratch
// onto the next environment and installing it. Close cleans the environment
// the session holds — the old one, which owns nothing any more — and the swap
// must not then install next with a lease nothing will ever release: it
// refuses, surfaces the refusal, and retains what next adopted.
func TestWorktreeSwap_CloseDuringTheSwapLeavesNoOwnerlessLease(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	launch := currentLocalEnv(t, r.s)
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	scratch := launch.SessionScratchDir()
	if scratch == "" {
		t.Fatal("the root's command minted no session scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.swapEnvAfterAdopt = func() {
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
	}

	_, err := r.create(t, map[string]any{"name": "lane"})
	<-closeDone

	if err == nil {
		t.Error("the enter succeeded while the session closed under it, want a refusal")
	}
	if got := currentLocalEnv(t, r.s); got != launch {
		t.Errorf("the session installed %p during its close, want the launch environment %p kept", got, launch)
	}
	if _, statErr := os.Stat(scratch); statErr != nil {
		t.Errorf("the scratch %s was removed, want it retained for the handoff: %v", scratch, statErr)
	}
	if scratchLeaseHeld(t, scratch) {
		t.Errorf("the scratch %s lease is still held after the close: no environment the session closes owns it", scratch)
	}
}

// The enter's swap installs the new environment, and the environment it
// leaves is recorded as parked (worktreeRestoreEnv) only afterwards. A close
// landing in between cleans the new environment and sees nothing parked, so a
// child sharing the old environment that minted a scratch there after the
// move leaves a lease nothing releases. The parked environment has to be
// published under the same lock hold that installs the new one.
func TestWorktreeSwap_CloseBetweenInstallAndRecordRetainsTheOldEnvironmentScratch(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	launch := currentLocalEnv(t, r.s)
	if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("root command on the launch environment: %v", err)
	}
	first := launch.SessionScratchDir()
	if first == "" {
		t.Fatal("the root's command minted no session scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(first) })
	var second string
	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.enterWorktreeAfterSwap = func() {
		// What a child sharing the old environment does after the move: its
		// command mints a scratch on the object the enter just emptied.
		if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
			t.Errorf("child command on the old environment: %v", err)
		}
		second = launch.SessionScratchDir()
		// The close runs to completion here: this is the interval between the
		// install and the record, and close must find the parked environment.
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
		<-closeDone
	}

	_, err := r.create(t, map[string]any{"name": "lane"})
	<-closeDone
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if second == "" || second == first {
		t.Fatalf("old environment scratch after the move = %q, want a fresh one beside %q", second, first)
	}
	t.Cleanup(func() { _ = os.RemoveAll(second) })

	for name, dir := range map[string]string{"session": first, "old environment": second} {
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Errorf("the %s scratch %s was removed, want it retained for the handoff: %v", name, dir, statErr)
		}
		if scratchLeaseHeld(t, dir) {
			t.Errorf("the %s scratch %s lease is still held after the close", name, dir)
		}
	}
}
