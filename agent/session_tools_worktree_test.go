package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
)

// TestManageWorktreeToolRegisteredRegistryOnlyNonReadOnly asserts spec §2's
// registration shape (mirroring session_init_registry_test.go's registry-tool
// checks): manage_worktree is registered directly on the registry (not part
// of the provider profile's own tool definitions, like update_goal/task_list),
// it is non-read-only (so execToolBatch serializes it, per spec), and it is
// advertised to the model via ToolDefinitions().
func TestManageWorktreeToolRegisteredRegistryOnlyNonReadOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t)

	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	if rt.ReadOnly {
		t.Error("manage_worktree.ReadOnly = true, want false (list is part of a stateful lifecycle tool)")
	}

	for _, td := range s.profile.ToolDefinitions() {
		if td.Name == "manage_worktree" {
			t.Error("manage_worktree must not be part of the provider profile's tool definitions (registry-only per spec §2)")
		}
	}

	found := false
	for _, td := range s.ToolDefinitions() {
		if td.Name == "manage_worktree" {
			found = true
			break
		}
	}
	if !found {
		t.Error("manage_worktree not advertised in ToolDefinitions()")
	}
}

// TestManageWorktreeToolUnknownOperationErrors asserts the dispatch switch's
// default arm returns a clear error rather than panicking or silently
// no-opping for an operation string outside the enum. All six real
// operations (create/list/switch/exit/remove/prune) landed across Tasks
// 13-16; this now exercises the fallback arm, not a stub.
func TestManageWorktreeToolUnknownOperationErrors(t *testing.T) {
	t.Parallel()
	s := newSession(t)

	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	_, err := rt.Exec(t.Context(), s.currentEnv(), map[string]any{"operation": "bogus"})
	if err == nil {
		t.Fatal("expected an error for an unknown operation, got nil")
	}
}

// TestShortSHA_UntruncatedWhenShort covers shortSHA's pass-through branch: a
// string already at or under the 12-char display width is returned
// unchanged rather than sliced (which would panic on a short input if the
// bounds check were missing). Every other worktree test only ever exercises
// shortSHA with a full 40-char SHA (base_sha in create results), so the
// short-input branch is otherwise never reached.
func TestShortSHA_UntruncatedWhenShort(t *testing.T) {
	cases := []string{"", "abc", "0123456789ab"} // len 0, 3, 12 (== the cutoff)
	for _, c := range cases {
		if got := shortSHA(c); got != c {
			t.Errorf("shortSHA(%q) = %q, want unchanged", c, got)
		}
	}
	if got := shortSHA("0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("shortSHA(16 chars) = %q, want the first 12", got)
	}
}

// TestWorktreeControlEnv_NonLocalEnvErrors is a direct white-box test of
// worktreeControlEnv's own local-execution-environment guard. Every current
// call site (worktreeCreateCore, worktreeControlRun's callers) only reaches
// this function after its OWN worktreeStateSnapshot/currentEnv check already
// confirmed a local env, so the guard here is otherwise unreachable through
// the tool surface — it exists as this function's own contract
// (worktreeControlEnv is usable standalone), verified directly.
func TestWorktreeControlEnv_NonLocalEnvErrors(t *testing.T) {
	s := newSession(t)
	s.mu.Lock()
	s.env = &timeoutEnv{wd: "/tmp"}
	s.mu.Unlock()

	_, err := s.worktreeControlEnv("/tmp")
	if err == nil || err.Error() != "manage_worktree requires a local execution environment" {
		t.Fatalf("worktreeControlEnv with a non-local env: err = %v, want the local-execution-environment error", err)
	}
}

// fakeZeroExitErrEnv is a minimal ExecutionEnvironment whose ExecCommand
// returns ExitCode 0 alongside a non-nil error — a combination the real
// execenv.LocalExecutionEnvironment.ExecCommand never actually produces
// (cmd.Wait() only returns a non-nil error for a non-zero exit, a signal, or
// an I/O failure, all of which set ExitCode != 0), but which the
// ExecutionEnvironment interface does not itself forbid. It exists solely to
// exercise gitRunner's own defensive "exit 0 but err != nil" branch, which
// no real git invocation can reach.
type fakeZeroExitErrEnv struct{}

func (fakeZeroExitErrEnv) Initialize() error                                     { return nil }
func (fakeZeroExitErrEnv) Cleanup()                                              {}
func (fakeZeroExitErrEnv) WorkingDirectory() string                              { return "/tmp" }
func (fakeZeroExitErrEnv) Platform() string                                      { return "test" }
func (fakeZeroExitErrEnv) OSVersion() string                                     { return "test" }
func (fakeZeroExitErrEnv) ReadFile(string, *int, *int) (string, error)           { return "", nil }
func (fakeZeroExitErrEnv) WriteFile(string, string) (string, error)              { return "", nil }
func (fakeZeroExitErrEnv) EditFile(string, string, string, bool) (string, error) { return "", nil }
func (fakeZeroExitErrEnv) FileExists(string) bool                                { return false }
func (fakeZeroExitErrEnv) Glob(string, string) ([]string, error)                 { return nil, nil }
func (fakeZeroExitErrEnv) Grep(string, string, string, bool, int, string) (string, error) {
	return "", nil
}
func (fakeZeroExitErrEnv) ListDirectory(string, int) ([]execenv.DirEntry, error) { return nil, nil }
func (fakeZeroExitErrEnv) ExecCommand(context.Context, string, int, string, map[string]string) (execenv.ExecResult, error) {
	return execenv.ExecResult{ExitCode: 0, Stdout: "partial\n"}, errors.New("wait: unexpected I/O error")
}

// TestEnterWorktree_NonLocalEnvNoOps is a direct white-box test of
// enterWorktree's own local-execution-environment guard. Every current call
// site (worktreeCreate, worktreeEnterManaged, worktreeSwitchByPath) already
// confirmed a local env before reaching it, so this guard is otherwise
// unreachable through the tool surface — verified directly as this
// function's own standalone contract, mirroring
// TestWorktreeControlEnv_NonLocalEnvErrors above.
func TestEnterWorktree_NonLocalEnvNoOps(t *testing.T) {
	s := newSession(t)
	s.mu.Lock()
	s.env = &timeoutEnv{wd: "/tmp"}
	before := s.worktreeCurrentPath
	s.mu.Unlock()

	s.enterWorktree("/tmp/somewhere", true)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worktreeCurrentPath != before {
		t.Errorf("enterWorktree with a non-local env mutated worktreeCurrentPath: got %q, want unchanged %q", s.worktreeCurrentPath, before)
	}
}

// TestExitWorktree_NoSavedRestoreEnvReturnsFalse is a direct white-box test
// of exitWorktree's own "nothing to restore" guard. worktreeExit's own
// worktreeStateSnapshot check already filters this case out before ever
// calling exitWorktree, so this guard is otherwise unreachable through the
// tool surface — verified directly as this function's own standalone
// contract: a fresh session that never entered a worktree has no saved
// restore env.
func TestExitWorktree_NoSavedRestoreEnvReturnsFalse(t *testing.T) {
	s := newSession(t)
	root, ok := s.exitWorktree()
	if ok || root != "" {
		t.Fatalf("exitWorktree with no saved restore env = (%q, %v), want (\"\", false)", root, ok)
	}
}

// TestRelPathUnderManagedDir_RelErrorReturnsFalse covers
// relPathUnderManagedDir's own filepath.Rel error branch: mismatched
// absolute/relative arguments (Rel refuses to relate a relative path to an
// absolute one) must report ok=false rather than propagate the error or
// panic. Every real production caller always passes two absolute,
// already-canonicalized paths, so this is only reachable via a direct call
// with deliberately mismatched inputs, exactly as exercised here.
func TestRelPathUnderManagedDir_RelErrorReturnsFalse(t *testing.T) {
	_, ok := relPathUnderManagedDir("relative/path", "/absolute/dir")
	if ok {
		t.Fatalf("relPathUnderManagedDir with mismatched absolute/relative args: ok=true, want false")
	}
}

func TestGitRunner_ExitZeroButErrorPropagates(t *testing.T) {
	run := gitRunner(context.Background(), fakeZeroExitErrEnv{})
	out, err := run("status")
	if err == nil || err.Error() != "wait: unexpected I/O error" {
		t.Fatalf("gitRunner with exit=0/err!=nil: err = %v, want the underlying error propagated", err)
	}
	if out != "partial\n" {
		t.Errorf("gitRunner with exit=0/err!=nil: stdout = %q, want it still returned", out)
	}
}

// TestWorktreeListSummaryLine covers the F4 ergonomics fix: the list result's
// human-readable message must carry a one-line-per-lane summary (name · ahead ·
// dirty · merged), not just a bare count — a live scenario showed a strong
// model ignore the rich entries array and shell out to git log because the
// message alone read as uninformative.
func TestWorktreeListSummaryLine(t *testing.T) {
	entries := []WorktreeListEntry{
		{Name: "untouched-lane", AheadCommits: 0, Dirty: false, Merged: true},
		{Name: "work-lane", AheadCommits: 1, Dirty: false, Merged: false},
	}
	got := worktreeListSummary(entries)
	for _, want := range []string{"2 managed worktree", "untouched-lane", "work-lane", "0 ahead", "1 ahead"} {
		if !strings.Contains(got, want) {
			t.Fatalf("list summary missing %q\ngot: %s", want, got)
		}
	}
	if worktreeListSummary(nil) != "0 managed worktree(s)." {
		t.Fatalf("empty summary = %q", worktreeListSummary(nil))
	}
}

// TestPruneDescriptionConveysBulkCleanup covers the F2 ergonomics fix: prune's
// description must convey that it removes lanes with no unmerged work
// (including from finished sessions), not read as a narrow "stale
// registrations" chore — no live run reached for prune under the old wording.
func TestPruneDescriptionConveysBulkCleanup(t *testing.T) {
	desc := tool.DefManageWorktree().Description
	if strings.Contains(desc, "stale worktree registrations") {
		t.Fatal("prune still described as 'stale worktree registrations' — undersells it")
	}
	if !strings.Contains(desc, "unmerged") {
		t.Fatalf("prune description should mention it removes lanes with no unmerged work; got: %s", desc)
	}
}
