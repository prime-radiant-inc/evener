//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	// The shared base repo is built once per package run under the temp dir
	// current at that moment; build it before this test redirects TMPDIR to a
	// directory it will delete.
	worktreeBaseRepo(t)
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
	r.s.cfg.testOnly.swapEnvAfterAdopt = func(context.Context) {
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

// The swap installs the new environment and runs the caller's record — which
// publishes the environment the enter parks (worktreeRestoreEnv) — in ONE
// s.mu hold, so no observer can see one without the other. By the time the
// swap returns both are recorded, and a close from there on finds the parked
// environment and retains the scratch a child sharing that object minted on
// it after the move; without that, the lease is one nothing releases.
//
// The close below runs concurrently, from inside the enter's tail: the
// earliest point outside the swap at which a close can land. That is NOT the
// interval between the install and the record — since the record joined the
// install's lock hold there is no such interval, and a seam between them
// would have to release the very lock that closes it.
//
// The hook releases as soon as the close has BEGUN, never waiting for it to
// return. Waiting would deadlock the two against each other: this hook runs
// inside the manage_worktree handler, which holds the dispatch admission on the
// close fence, so a close blocked on that join cannot finish and the hook
// blocked on the close cannot release it. Only the close budget breaks the tie,
// thirty seconds later and with a fence warning that means the opposite of what
// it says here. The assertions below pin both.
func TestWorktreeSwap_CloseAfterTheEnterRetainsTheParkedEnvironmentScratch(t *testing.T) {
	started := time.Now()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	warnings := collectWarningsUntilClosed(r.s)
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
		// Both halves of the swap's locked section are already visible: the
		// lane clone is installed AND the launch environment is parked.
		r.s.mu.Lock()
		installed, _ := r.s.env.(*execenv.LocalExecutionEnvironment)
		parked := r.s.worktreeRestoreEnv
		r.s.mu.Unlock()
		if installed == launch {
			t.Errorf("the swap returned with the launch environment %p still installed", launch)
		}
		if parked != launch {
			t.Errorf("parked environment = %p, want the launch environment %p recorded with the install", parked, launch)
		}
		// What a child sharing the parked object does after the move: its
		// command mints a scratch on the object the enter just emptied.
		if _, err := launch.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
			t.Errorf("child command on the parked environment: %v", err)
		}
		second = launch.SessionScratchDir()
		// The close begins here, concurrently with the enter's tail, and has to
		// find the parked environment. Released at closeBegun: see the deadlock
		// this hook must not create, above.
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
	}

	_, err := r.create(t, map[string]any{"name": "lane"})
	<-closeDone
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if second == "" || second == first {
		t.Fatalf("parked environment scratch after the move = %q, want a fresh one beside %q", second, first)
	}
	t.Cleanup(func() { _ = os.RemoveAll(second) })

	for name, dir := range map[string]string{"session": first, "parked environment": second} {
		if _, statErr := os.Stat(dir); statErr != nil {
			t.Errorf("the %s scratch %s was removed, want it retained for the handoff: %v", name, dir, statErr)
		}
		if scratchLeaseHeld(t, dir) {
			t.Errorf("the %s scratch %s lease is still held after the close", name, dir)
		}
	}
	// The close drained its fence join instead of giving up on it. Both of
	// these are tripwires for the same regression — a hook that holds the
	// dispatch admission until the close returns — which costs the whole
	// LaneClosePassBudget and reports work it walked past that was never
	// stuck. A healthy run here is well under a second.
	if got := fenceWarnings(<-warnings); len(got) != 0 {
		t.Errorf("the close gave up on its environment-work fence: %q", got)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the test took %s; the close waited out its budget instead of joining promptly", elapsed)
	}
}

// heldScratchDirsIn lists the session scratch dirs under base whose lease is
// still held by a live owner.
func heldScratchDirsIn(t *testing.T, base string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(base, "evener-sandbox-*"))
	if err != nil {
		t.Fatalf("glob session scratch dirs: %v", err)
	}
	var held []string
	for _, dir := range found {
		if scratchLeaseHeld(t, dir) {
			held = append(held, dir)
		}
	}
	return held
}

// sessionOwnedScratchDirs is the set of scratch dirs the session's own
// environments (current and parked) report: the only leases a live session
// may hold.
func sessionOwnedScratchDirs(t *testing.T, s *Session) map[string]bool {
	t.Helper()
	own := map[string]bool{}
	if dir := currentLocalEnv(t, s).SessionScratchDir(); dir != "" {
		own[dir] = true
	}
	s.mu.Lock()
	parked := s.worktreeRestoreEnv
	s.mu.Unlock()
	if parked != nil {
		if dir := parked.SessionScratchDir(); dir != "" {
			own[dir] = true
		}
	}
	return own
}

// A worktree lifecycle op runs its git through a control environment cloned
// for the op, and that clone's first command mints a scratch and takes its
// lease. Nothing references the clone after the op, so the op has to dispose
// it — every op, including one refused mid-swap and its rollback — or each op
// leaves a directory with a held lease for the daemon's uptime.
func TestWorktreeOps_ControlEnvironmentsLeaveNoHeldLease(t *testing.T) {
	// See TestWorktreeSwap_SnapshotOnTheEnteredCloneUsesTheSessionScratch: pin
	// the shared base repo before any subtest redirects TMPDIR.
	worktreeBaseRepo(t)
	t.Run("create, exit, remove", func(t *testing.T) {
		isolated := t.TempDir()
		t.Setenv("TMPDIR", isolated)
		r := newWorktreeRepo(t)
		assertOnlyOwnHeld := func(step string) {
			t.Helper()
			own := sessionOwnedScratchDirs(t, r.s)
			for _, dir := range heldScratchDirsIn(t, isolated) {
				if !own[dir] {
					t.Errorf("%s left a held lease on %s that no session environment owns", step, dir)
				}
			}
		}
		if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
			t.Fatalf("create: %v", err)
		}
		assertOnlyOwnHeld("create")
		if _, err := r.exitOp(t); err != nil {
			t.Fatalf("exit: %v", err)
		}
		assertOnlyOwnHeld("exit")
		if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err != nil {
			t.Fatalf("remove: %v", err)
		}
		assertOnlyOwnHeld("remove")
	})
	t.Run("refused create", func(t *testing.T) {
		isolated := t.TempDir()
		t.Setenv("TMPDIR", isolated)
		r := newWorktreeRepo(t)
		closeDone := armCloseDuringSwap(r, nil)
		_, err := r.create(t, map[string]any{"name": "lane"})
		<-closeDone
		if err == nil {
			t.Fatal("create succeeded while the session closed under its swap, want a refusal")
		}
		if held := heldScratchDirsIn(t, isolated); len(held) != 0 {
			t.Errorf("the refused create and the closed session left held leases on %v, want none", held)
		}
	})
	t.Run("refused switch", func(t *testing.T) {
		isolated := t.TempDir()
		t.Setenv("TMPDIR", isolated)
		r := newWorktreeRepo(t)
		if _, err := r.create(t, map[string]any{"name": "a"}); err != nil {
			t.Fatalf("create a: %v", err)
		}
		if _, err := r.create(t, map[string]any{"name": "b"}); err != nil {
			t.Fatalf("create b: %v", err)
		}
		closeDone := armCloseDuringSwap(r, nil)
		_, err := r.switchOp(t, map[string]any{"name": "a"})
		<-closeDone
		if err == nil {
			t.Fatal("switch succeeded while the session closed under its swap, want a refusal")
		}
		if held := heldScratchDirsIn(t, isolated); len(held) != 0 {
			t.Errorf("the refused switch and the closed session left held leases on %v, want none", held)
		}
	})
}

// The worktree report a delegate's finish and create result carry runs its
// git through clones built for the report — a lane probe and a control
// environment — and a command on either mints a scratch and takes its lease.
// The report has to dispose both on the way out, or every delegate finish on
// an unsandboxed session leaks a held lease.
func TestDelegateWorktreeReport_LeavesNoUnownedHeldLease(t *testing.T) {
	worktreeBaseRepo(t)
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	r := newWorktreeRepo(t)
	_, lanePath, _ := r.seedStableIsolationLane(t)

	report := r.s.delegateWorktreeReport("worktree", lanePath)

	if report == nil || report.Path != lanePath {
		t.Fatalf("delegateWorktreeReport = %+v, want a report for %s", report, lanePath)
	}
	own := sessionOwnedScratchDirs(t, r.s)
	for _, dir := range heldScratchDirsIn(t, isolated) {
		if !own[dir] {
			t.Errorf("the report left a held lease on %s that no session environment owns", dir)
		}
	}
}

// enterLaneWithSharedChild puts the session in a fresh lane and gives it a child
// that shares the lane environment OBJECT (a child with no working directory of
// its own gets the parent's environment untouched). It returns that environment,
// which the next enter will abandon.
func enterLaneWithSharedChild(t *testing.T, r *wtRepo, name string) *execenv.LocalExecutionEnvironment {
	t.Helper()
	if _, err := r.create(t, map[string]any{"name": name}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	lane := currentLocalEnv(t, r.s)
	child, err := NewSession(r.s.client, r.s.currentProfile(), lane, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the lane environment: %v", err)
	}
	r.s.subagents.track(&subagent{id: child.id, sess: child, done: make(chan struct{})})
	return lane
}

// A second enter leaves the first lane's environment reachable from nothing:
// worktreeRestoreEnv holds the LAUNCH environment, so the clone between two
// enters is neither current nor parked. A child spawned while the session was in
// it still shares the object and can mint a scratch there afterwards — one its
// own teardown skips, because the environment is not the child's, and one the
// current clone's Cleanup never reaches. Nothing but the parent's close can
// release it, so the parent's close has to.
func TestParentCloseRetainsScratchOnAnEnvironmentASecondEnterAbandoned(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	launch := currentLocalEnv(t, r.s)

	laneA := enterLaneWithSharedChild(t, r, "a")
	if laneA == launch {
		t.Fatal("the enter did not swap the session onto a lane clone")
	}
	// The second enter drops laneA: current becomes laneB, parked stays launch.
	if _, err := r.create(t, map[string]any{"name": "b"}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if laneB := currentLocalEnv(t, r.s); laneB == laneA {
		t.Fatal("the second enter did not swap the session off laneA")
	}

	// What the shared child does afterwards: its command mints a scratch on the
	// environment the session has already dropped.
	if _, err := laneA.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("child command on the abandoned environment: %v", err)
	}
	abandoned := laneA.SessionScratchDir()
	if abandoned == "" {
		t.Fatal("the child's command minted no scratch on the abandoned environment")
	}
	t.Cleanup(func() { _ = os.RemoveAll(abandoned) })
	if !scratchLeaseHeld(t, abandoned) {
		t.Fatal("the abandoned environment's scratch lease must be held before the close")
	}

	r.s.Close()

	if _, err := os.Stat(abandoned); err != nil {
		t.Errorf("close removed the abandoned environment's scratch %s, want it retained for the handoff: %v", abandoned, err)
	}
	if scratchLeaseHeld(t, abandoned) {
		t.Errorf("the abandoned environment's scratch %s lease is still held after the close; nothing else will ever release it", abandoned)
	}
}

// The same session, exiting back to the launch environment before it closes.
// Every environment it swapped away from is abandoned — both lanes — but the
// launch environment it swapped back ONTO is not: it is current, close cleans
// it, and recording it as abandoned would have close retain the very
// environment it is about to tear down.
func TestParentCloseAfterExitRetainsEachAbandonedEnvironmentAndNotTheLaunchOne(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	launch := currentLocalEnv(t, r.s)

	laneA := enterLaneWithSharedChild(t, r, "a")
	laneB := enterLaneWithSharedChild(t, r, "b")
	if laneA == laneB {
		t.Fatal("the second enter did not swap the session off laneA")
	}
	if _, err := r.exitOp(t); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if got := currentLocalEnv(t, r.s); got != launch {
		t.Fatalf("exit landed on %p, want the launch environment %p", got, launch)
	}

	scratches := map[string]string{}
	for name, env := range map[string]*execenv.LocalExecutionEnvironment{"laneA": laneA, "laneB": laneB} {
		if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
			t.Fatalf("child command on %s: %v", name, err)
		}
		dir := env.SessionScratchDir()
		if dir == "" {
			t.Fatalf("the child's command minted no scratch on %s", name)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		scratches[name] = dir
	}

	r.s.mu.Lock()
	abandoned := append([]*execenv.LocalExecutionEnvironment(nil), r.s.abandonedEnvs...)
	r.s.mu.Unlock()
	if len(abandoned) != 2 {
		t.Errorf("abandoned environments = %d, want exactly the two lanes (each recorded once)", len(abandoned))
	}
	for _, env := range abandoned {
		if env == launch {
			t.Error("the launch environment was recorded as abandoned; the close would retain the environment it is about to clean")
		}
	}

	r.s.Close()

	for name, dir := range scratches {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("close removed %s's scratch %s, want it retained: %v", name, dir, err)
		}
		if scratchLeaseHeld(t, dir) {
			t.Errorf("%s's scratch %s lease is still held after the close", name, dir)
		}
	}
}
