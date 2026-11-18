package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// sbxWorktreeSession builds a minimal session suitable for exercising the worktree
// env-swap paths (no git snapshot / project prompts, minimal prompt).
func sbxWorktreeSession(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return newSession(t, withClient(c), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
}

// sbxMainAndLanes materializes a main repo with two linked worktrees.
func sbxMainAndLanes(t *testing.T) (main, laneA, laneB, home string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	sbxGit(t, main, "init", "-q")
	sbxGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	laneA = filepath.Join(base, "a")
	laneB = filepath.Join(base, "b")
	sbxGit(t, main, "worktree", "add", "-q", laneA, "-b", "fa")
	sbxGit(t, main, "worktree", "add", "-q", laneB, "-b", "fb")
	return evalSyms(t, main), evalSyms(t, laneA), evalSyms(t, laneB), t.TempDir()
}

func evalSyms(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(r)
}

// TestWorktreeControlEnvUsesControlPolicy: the manage_worktree control env carries
// the CONTROL policy (main repo + registry writable, config/hooks denied), NOT the
// current worktree's tool policy.
func TestWorktreeControlEnvUsesControlPolicy(t *testing.T) {
	t.Parallel()
	main, laneA, _, home := sbxMainAndLanes(t)
	facts := sbxBwrapFacts(home)

	s := sbxWorktreeSession(t)
	laneEnv := execenv.NewLocalExecutionEnvironment(laneA)
	laneEnv.Sandbox = sbxResolve(t, facts, laneA, sandbox.ModeWorkspaceWrite)
	s.mu.Lock()
	s.env = laneEnv
	s.mu.Unlock()

	ctrl, err := s.worktreeControlEnv(main)
	if err != nil {
		t.Fatalf("worktreeControlEnv: %v", err)
	}
	le := ctrl.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || !le.Sandbox.Enforced() {
		t.Fatal("control env must be sandboxed")
	}
	// Anchored at the MAIN repo, not the current lane's tool root.
	if le.Sandbox.Git.WorktreeRoot != main {
		t.Errorf("control env worktree = %q, want main %q (not lane %q)", le.Sandbox.Git.WorktreeRoot, main, laneA)
	}
	registry := filepath.Join(main, ".git", "worktrees")
	if !rootGrantsAny(le.Sandbox.Spawned.WriteRoots, registry) {
		t.Errorf("control env must grant the registry %q writable: %v", registry, le.Sandbox.Spawned.WriteRoots)
	}
	for _, denied := range []string{filepath.Join(main, ".git", "config"), filepath.Join(main, ".git", "hooks")} {
		if !slices.Contains(le.Sandbox.Git.ProtectedPaths, denied) {
			t.Errorf("control env must keep %q write-protected: %v", denied, le.Sandbox.Git.ProtectedPaths)
		}
	}
}

// TestEnterExitWorktreeReRootsAndRestores: entering a managed worktree re-roots the
// session sandbox to that worktree; exiting restores the pre-worktree env with its
// original roots.
func TestEnterExitWorktreeReRootsAndRestores(t *testing.T) {
	t.Parallel()
	_, laneA, laneB, home := sbxMainAndLanes(t)
	facts := sbxBwrapFacts(home)
	probes := testConfig{skipGitSnapshot: true, envProbes: &envctx.Probes{Now: func() time.Time { return envctxFixedTime }}, sandboxProber: sandbox.FakeProber{Facts: facts}}
	policy := sbxResolve(t, facts, laneA, sandbox.ModeWorkspaceWrite)
	launchRoots := writableRootsSummary(*policy)
	launch := func(t *testing.T, mode string, installed *sandbox.ResolvedPolicy) (*Session, *agenttest.ScriptedAdapter) {
		t.Helper()
		env := execenv.NewLocalExecutionEnvironment(laneA)
		cfg := SessionConfig{Sandbox: mode, StateDir: t.TempDir(), testOnly: probes}
		if err := PickFreshSessionID(env, &cfg); err != nil {
			t.Fatal(err)
		}
		if err := env.EnableSandbox(installed); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
		adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") }}
		s := newSession(t, withEnv(env), withConfig(cfg), withAdapter(adapter))
		sendOneUserInput(t, s, "first request")
		return s, adapter
	}
	restore := func(t *testing.T, s *Session, meta schema.SessionMeta) *Session {
		t.Helper()
		env := execenv.NewLocalExecutionEnvironment(laneA)
		t.Cleanup(env.Cleanup)
		resumed, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, env, meta, RestoreSessionConfig{StateDir: s.stateDir, testOnly: probes})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(resumed.Close)
		return resumed
	}
	updates := func(req llm.Request, roots string) int {
		count := 0
		for _, msg := range req.Messages {
			observed := envctx.NewTracker(envctx.State{WritableRootsUnknown: true})
			if msg.Role == llm.RoleUser && observed.ReplayBlock(msg.Text()) {
				if st := observed.State(); !st.WritableRootsUnknown && st.Last.WritableRoots == roots {
					count++
				}
			}
		}
		return count
	}
	fold := func(t *testing.T, s *Session) {
		t.Helper()
		seedNumberedSessionHistory(t, s, 8)
		s.contextMgr.PreserveRecentTurns = 1
		if err := s.Compact(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("old launch grants without an active sandbox", func(t *testing.T) {
		// An embedding may supply its own sandbox directly. An empty config
		// does not request sandbox installation on a plain environment at resume.
		netOff, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: new(false)}, facts, laneA)
		if err != nil {
			t.Fatal(err)
		}
		s, adapter := launch(t, "", &netOff)
		first := adapter.Requests()[0]
		launchState, _ := requestEnvironment(first)
		if launchState.Sandbox != "off" || !strings.Contains(first.Messages[0].Text(), launchRoots) {
			t.Fatal("expected launch grants from the installed environment and an off configuration")
		}
		if launchState.ShellNetwork != "" || launchState.DetachedShell != "" || first.WebSearch {
			t.Fatal("launch repeated its shell capabilities or enabled provider web search under network-off")
		}
		requireShellModeEnum(t, s, "embedded sandbox", "foreground", "background")
		requireDetachUnsupported(t, s)
		if egressDeniedByNet(s.currentEnv(), "web_fetch") == nil {
			t.Fatal("the installed launch sandbox permitted network egress")
		}
		s.Close()
		meta := loadMetaForTest(t, s)
		meta.PromptLaunch.WritableRoots = nil
		s = restore(t, s, meta)
		requireShellModeEnum(t, s, "restored plain shell", "foreground", "background", "detached")
		if d := fileToolCall(t, s, "shell", map[string]string{"command": "exec true", "mode": "detached"}); d.isError {
			t.Fatalf("the restored shell refused its advertised detached mode: %s", d.output)
		}
		if err := egressDeniedByNet(s.currentEnv(), "web_fetch"); err != nil {
			t.Fatalf("the restored plain environment denied egress: %v", err)
		}
		outside := fileToolCall(t, s, "write_file", map[string]string{"file_path": filepath.Join(laneB, "unconfined"), "content": s.ID()})
		if outside.isError {
			t.Fatalf("resumed file tool denied a path outside the launch roots: %s", outside.output)
		}
		check := func(prefixIntact bool) {
			t.Helper()
			sendOneUserInput(t, s, "continue")
			reqs := adapter.Requests()
			last := reqs[len(reqs)-1]
			if got := updates(last, ""); got != 1 || last.Messages[0].Text() != first.Messages[0].Text() {
				t.Errorf("request carried %d explicit withdrawals of the frozen launch roots, want one", got)
			}
			if prefixIntact && (len(last.Messages) < len(first.Messages) || !reflect.DeepEqual(last.Messages[:len(first.Messages)], first.Messages)) {
				t.Error("restoring the plain shell rewrote the provider's launch prefix")
			}
			if !last.WebSearch {
				t.Error("the restored provider request disabled its available web search")
			}
			var shellUpdates int
			for _, m := range last.Messages {
				var tracker envctx.Tracker
				if m.Role == llm.RoleUser && tracker.ReplayBlock(m.Text()) {
					if state := tracker.State().Last; state.ShellNetwork == envctx.NetworkAllowed && state.DetachedShell == envctx.DetachAvailable {
						shellUpdates++
					}
				}
			}
			if shellUpdates != 1 {
				t.Errorf("restored request carried %d observations of the installed network policy and detachable shell, want one", shellUpdates)
			}
		}
		check(true)
		check(true)
		fold(t, s)
		check(false)
	})

	s, adapter := launch(t, sandbox.ModeWorkspaceWrite.String(), policy)
	first := adapter.Requests()[0]
	if !strings.Contains(first.Messages[0].Text(), launchRoots) {
		t.Fatal("the startup prompt did not report the installed sandbox roots")
	}
	if got := updates(first, launchRoots); got != 0 {
		t.Errorf("first request repeated its launch roots %d times", got)
	}

	s.Close()
	s = restore(t, s, loadMetaForTest(t, s))
	sendOneUserInput(t, s, "resumed worktree")
	requests := adapter.Requests()
	if got := updates(requests[len(requests)-1], launchRoots); got != 0 {
		t.Errorf("cold resume repeated its launch roots %d times", got)
	}
	beforeEnter := len(requests)
	if err := s.enterWorktree(laneB, true); err != nil {
		t.Fatal(err)
	}
	entered := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if entered.Sandbox == nil || entered.Sandbox.Git.WorktreeRoot != laneB {
		t.Errorf("enterWorktree must re-root the sandbox to lane B %q, got %+v", laneB, entered.Sandbox)
	}
	if !rootGrantsAny(entered.Sandbox.FileTool.WriteRoots, laneB) || rootGrantsAny(entered.Sandbox.FileTool.WriteRoots, laneA) {
		t.Errorf("entered sandbox write roots must be lane B only: %v", entered.Sandbox.FileTool.WriteRoots)
	}
	for _, dir := range []string{laneB, laneA} {
		write := fileToolCall(t, s, "write_file", map[string]string{"file_path": filepath.Join(dir, "probe"), "content": s.ID()})
		if write.isError != (dir == laneA) {
			t.Fatalf("write in %q after entering %q: error=%v, output=%q", dir, laneB, write.isError, write.output)
		}
	}
	activeRoots := writableRootsSummary(*entered.Sandbox)
	sendOneUserInput(t, s, "entered worktree")
	sendOneUserInput(t, s, "same worktree")
	for _, req := range adapter.Requests()[beforeEnter:] {
		if got := updates(req, activeRoots); got != 1 {
			t.Errorf("request after entering worktree has %d updates for installed roots %q, want one", got, activeRoots)
		}
		if len(req.Messages) < len(first.Messages) || !reflect.DeepEqual(req.Messages[:len(first.Messages)], first.Messages) {
			t.Fatal("changing worktrees rewrote the provider's launch prefix")
		}
	}
	fold(t, s)
	sendOneUserInput(t, s, "after folding")
	requests = adapter.Requests()
	afterFold := requests[len(requests)-1]
	if updates(afterFold, activeRoots) != 1 || first.Messages[0].Text() != afterFold.Messages[0].Text() {
		t.Error("folded request did not retain the startup prompt and restore the installed roots")
	}

	root, ok, err := s.exitWorktree()
	if err != nil || !ok || root != laneA {
		t.Fatalf("exitWorktree must restore lane A, got (%q, %v)", root, ok)
	}
	restored := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if restored.Sandbox == nil || restored.Sandbox.Git.WorktreeRoot != laneA {
		t.Errorf("exitWorktree must restore the lane A sandbox, got %+v", restored.Sandbox)
	}
	if restored.SandboxReRootError() != nil {
		t.Errorf("restoring a saved env must not re-root or error, got %v", restored.SandboxReRootError())
	}
	sendOneUserInput(t, s, "restored worktree")
	requests = adapter.Requests()
	if got := updates(requests[len(requests)-1], launchRoots); got != 1 {
		t.Errorf("request after leaving worktree has %d updates for the restored roots, want one", got)
	}
}

// TestEnterWorktreeRefusesUnsatisfiableReRoot: a switch/create into a target whose
// sandbox policy the host cannot re-anchor is REFUSED — enterWorktree returns the
// re-root error and leaves the session in its prior confined env, never running
// unconfined in the new worktree (the cardinal fail-open).
func TestEnterWorktreeRefusesUnsatisfiableReRoot(t *testing.T) {
	t.Parallel()
	_, laneA, laneB, _ := sbxMainAndLanes(t)

	s := sbxWorktreeSession(t)
	laneAEnv := execenv.NewLocalExecutionEnvironment(laneA)
	// An ENFORCED policy that retained no resolve inputs (a hand-built literal)
	// cannot be re-rooted, so WithWorkingDirectory to any target fails closed — a
	// deterministic stand-in for a target the host cannot classify/enforce.
	laneAEnv.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, Backend: sandbox.BackendBwrap}
	s.swapEnvAndRefresh(laneAEnv, nil)

	if err := s.enterWorktree(laneB, true); err == nil {
		t.Fatal("enterWorktree into an unsatisfiable re-root target must be refused")
	}

	cur, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("session env must still be a LocalExecutionEnvironment")
	}
	if cur != laneAEnv {
		t.Error("a refused switch must leave the session in its prior env pointer")
	}
	if cur.Sandbox == nil {
		t.Error("the prior env must stay confined (Sandbox non-nil) after a refused switch")
	}
	if cur.RootDir != laneA {
		t.Errorf("session must stay rooted at the prior worktree %q, got %q", laneA, cur.RootDir)
	}

	s.mu.Lock()
	gotPath := s.worktreeCurrentPath
	gotRestore := s.worktreeRestoreEnv
	s.mu.Unlock()
	if gotPath == laneB {
		t.Error("worktreeCurrentPath must not advance to the refused target")
	}
	if gotRestore != nil {
		t.Error("a refused switch must not save a restore env")
	}
}

// rootGrantsAny reports whether any root equals or is an ancestor of target.
func rootGrantsAny(roots []string, target string) bool {
	for _, r := range roots {
		if r == target {
			return true
		}
		if rel, err := filepath.Rel(r, target); err == nil && rel != ".." && !hasDotDotPrefix(rel) {
			return true
		}
	}
	return false
}

func hasDotDotPrefix(rel string) bool {
	return rel == ".." || (len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator)
}
