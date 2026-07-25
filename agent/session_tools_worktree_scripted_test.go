package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/agent/schema"
)

func scriptedEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "scripted",
		OSVersion:  "scripted",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func TestWorktreeControlRunUsesConfiguredGitRunner(t *testing.T) {
	t.Parallel()

	called := false
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot: true,
			environmentInfo: scriptedEnvironmentInfo,
			worktreeGitRunner: func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
				called = true
				return func(args ...string) (string, error) {
					if len(args) != 1 || args[0] != "sentinel" {
						t.Fatalf("args = %q, want sentinel", args)
					}
					return "scripted", nil
				}
			},
		},
	}))

	run, err := s.worktreeControlRun(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("worktreeControlRun: %v", err)
	}
	got, err := run("sentinel")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !called {
		t.Fatal("configured Git runner was not used")
	}
	if got != "scripted" {
		t.Fatalf("output = %q, want scripted", got)
	}
}

func TestSessionUsesConfiguredEnvironmentSnapshot(t *testing.T) {
	t.Parallel()

	called := false
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot: true,
			environmentInfo: func(env execenv.ExecutionEnvironment, _ clock.Clock) schema.EnvironmentInfo {
				called = true
				return schema.EnvironmentInfo{WorkingDir: env.WorkingDirectory(), Platform: "scripted", OSVersion: "scripted"}
			},
		},
	}))
	if !called {
		t.Fatal("configured environment snapshot was not used")
	}
	if got := s.envInfo.Platform; got != "scripted" {
		t.Fatalf("environment platform = %q, want scripted", got)
	}
}

func TestScriptedWorktreeSessionPreservesLockAndRestoreInvariants(t *testing.T) {
	h := newScriptedWorktreeSession(t)

	first, err := h.exec(map[string]any{"operation": "create", "name": "alpha"})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	alpha, _ := first["path"].(string)
	h.requireCurrent(t, alpha, true)
	h.requireOwnLock(t, alpha)

	second, err := h.exec(map[string]any{"operation": "create", "name": "beta"})
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	beta, _ := second["path"].(string)
	h.requireCurrent(t, beta, true)
	h.requireUnlocked(t, alpha)
	h.requireOwnLock(t, beta)

	if _, err := h.exec(map[string]any{"operation": "switch", "name": "alpha"}); err != nil {
		t.Fatalf("switch alpha: %v", err)
	}
	h.requireCurrent(t, alpha, true)
	h.requireOwnLock(t, alpha)
	h.requireUnlocked(t, beta)

	if _, err := h.exec(map[string]any{"operation": "exit"}); err != nil {
		t.Fatalf("exit: %v", err)
	}
	h.requireAtRoot(t)
	h.requireUnlocked(t, alpha)

	h.setLock(beta, "serf:foreign-session")
	before := h.s.currentEnv().WorkingDirectory()
	if _, err := h.exec(map[string]any{"operation": "switch", "name": "beta"}); err == nil {
		t.Fatal("switch to a foreign-locked worktree succeeded")
	}
	if got := h.s.currentEnv().WorkingDirectory(); got != before {
		t.Fatalf("foreign-lock refusal moved the session to %q, want %q", got, before)
	}
	h.requireForeignLock(t, beta, "serf:foreign-session")
}

// scriptedWorktreeSession drives a real Session through its registered
// manage_worktree tool while scriptedWorktreeGit replaces only Git's external
// command boundary. Files under t.TempDir are deliberate: the production
// worktree orchestration owns sidecars and .git pointer files through os.*, so
// keeping that layer real exercises the same persistence and structural-root
// paths without a host Git process.
type scriptedWorktreeSession struct {
	t    *testing.T
	s    *Session
	git  *scriptedWorktreeGit
	root string
}

func newScriptedWorktreeSession(t *testing.T) *scriptedWorktreeSession {
	t.Helper()
	root := scriptedCanonicalDir(t, t.TempDir())
	stateDir := scriptedCanonicalDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".git", "worktrees"), 0o755); err != nil {
		t.Fatalf("create scripted main git dir: %v", err)
	}

	git := newScriptedWorktreeGit(root)
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         stateDir,
		clock:            agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:             true,
			minimalSystemPrompt:         true,
			minimalWorktreeToolRegistry: true,
			noSyncJobStore:              true,
			worktreeGitRunner: func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
				return git.run
			},
			environmentInfo: scriptedEnvironmentInfo,
		},
	}
	s := newSession(t, withDir(root), withConfig(cfg))
	return &scriptedWorktreeSession{t: t, s: s, git: git, root: root}
}

func scriptedCanonicalDir(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return filepath.Clean(canonical)
}

func (h *scriptedWorktreeSession) exec(args map[string]any) (map[string]any, error) {
	h.t.Helper()
	rt := h.s.reg.Get("manage_worktree")
	if rt == nil || rt.Exec == nil {
		h.t.Fatal("manage_worktree is not registered with an executor")
	}
	out, err := rt.Exec(context.Background(), h.s.currentEnv(), args)
	if err != nil {
		return nil, err
	}
	result, ok := out.(map[string]any)
	if !ok {
		h.t.Fatalf("manage_worktree result is %T, want map[string]any", out)
	}
	return result, nil
}

func (h *scriptedWorktreeSession) requireCurrent(t *testing.T, path string, managed bool) {
	t.Helper()
	if got := h.s.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("current working directory = %q, want %q", got, path)
	}
	h.s.mu.Lock()
	current, gotManaged := h.s.worktreeCurrentPath, h.s.worktreeCurrentManaged
	h.s.mu.Unlock()
	if current != path || gotManaged != managed {
		t.Fatalf("session worktree state = (%q, %v), want (%q, %v)", current, gotManaged, path, managed)
	}
}

func (h *scriptedWorktreeSession) requireAtRoot(t *testing.T) {
	t.Helper()
	if got := h.s.currentEnv().WorkingDirectory(); got != h.root {
		t.Fatalf("current working directory = %q, want root %q", got, h.root)
	}
	h.s.mu.Lock()
	current, restore := h.s.worktreeCurrentPath, h.s.worktreeRestoreEnv
	h.s.mu.Unlock()
	if current != "" || restore != nil {
		t.Fatalf("session did not clear worktree restore state: current=%q restore=%v", current, restore)
	}
}

func (h *scriptedWorktreeSession) requireOwnLock(t *testing.T, path string) {
	t.Helper()
	entry := h.git.entry(path)
	if entry == nil {
		t.Fatalf("no scripted Git entry for %q", path)
	}
	want := worktree.FormatSessionMarker(h.s.id)
	if entry.lockReason != want {
		t.Fatalf("lock for %q = %q, want own marker %q", path, entry.lockReason, want)
	}
}

func (h *scriptedWorktreeSession) requireUnlocked(t *testing.T, path string) {
	t.Helper()
	entry := h.git.entry(path)
	if entry == nil {
		t.Fatalf("no scripted Git entry for %q", path)
	}
	if entry.lockReason != "" {
		t.Fatalf("lock for %q = %q, want unlocked", path, entry.lockReason)
	}
}

func (h *scriptedWorktreeSession) requireForeignLock(t *testing.T, path, reason string) {
	t.Helper()
	entry := h.git.entry(path)
	if entry == nil || entry.lockReason != reason {
		got := ""
		if entry != nil {
			got = entry.lockReason
		}
		t.Fatalf("lock for %q = %q, want foreign reason %q", path, got, reason)
	}
}

func (h *scriptedWorktreeSession) setLock(path, reason string) {
	h.t.Helper()
	entry := h.git.entry(path)
	if entry == nil {
		h.t.Fatalf("no scripted Git entry for %q", path)
	}
	entry.lockReason = reason
}

type scriptedWorktreeEntry struct {
	path       string
	branch     string
	head       string
	lockReason string
	managed    bool
}

// scriptedWorktreeGit is a deliberately small semantic model of the Git
// commands reached by the create/switch/exit/list lifecycle. Unsupported
// argv fail loudly so adding a new production command cannot silently turn the
// harness into a permissive mock.
type scriptedWorktreeGit struct {
	root        string
	branches    map[string]string
	entries     map[string]*scriptedWorktreeEntry
	calls       [][]string
	failNextAdd bool
}

func newScriptedWorktreeGit(root string) *scriptedWorktreeGit {
	return &scriptedWorktreeGit{
		root:     root,
		branches: map[string]string{"main": "base-sha"},
		entries:  make(map[string]*scriptedWorktreeEntry),
	}
}

func (g *scriptedWorktreeGit) entry(path string) *scriptedWorktreeEntry {
	return g.entries[filepath.Clean(path)]
}

func (g *scriptedWorktreeGit) run(args ...string) (string, error) {
	g.calls = append(g.calls, append([]string(nil), args...))

	switch {
	case scriptedArgs(args, "version"):
		return "git version 2.45.0\n", nil
	case len(args) == 3 && args[0] == "check-ref-format" && args[1] == "--branch":
		// Only serf's own ValidateName is modeled here. Git's additional
		// reserved-name rules (HEAD, "@", ".lock" suffixes, …) are NOT modeled:
		// a test that exists to prove real git rejects such a name must use the
		// real-git harness, or it would be asserting against this fake's
		// hardcoding rather than against git.
		if worktree.ValidateName(args[2]) != nil {
			return "", fmt.Errorf("scripted git: invalid branch %q", args[2])
		}
		return "", nil
	case len(args) == 4 && args[0] == "show-ref" && args[1] == "--verify" && args[2] == "--quiet":
		name := strings.TrimPrefix(args[3], "refs/heads/")
		if _, ok := g.branches[name]; !ok {
			return "", fmt.Errorf("scripted git: branch %q does not exist", name)
		}
		return "", nil
	case len(args) >= 2 && args[0] == "worktree":
		return g.runWorktree(args)
	case len(args) >= 3 && args[0] == "-C":
		return g.runAtPath(args)
	case len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify":
		return g.resolveRef(args[2])
	case len(args) == 3 && args[0] == "for-each-ref":
		return "", nil
	case len(args) == 4 && args[0] == "merge-base" && args[1] == "--is-ancestor":
		return "", nil
	case len(args) == 4 && args[0] == "cherry":
		return "", nil
	case len(args) == 3 && args[0] == "branch" && args[1] == "-D":
		if args[2] == "main" {
			return "", errors.New("scripted git: refusing to delete main")
		}
		if _, ok := g.branches[args[2]]; !ok {
			return "", fmt.Errorf("scripted git: branch %q does not exist", args[2])
		}
		delete(g.branches, args[2])
		return "", nil
	default:
		return "", fmt.Errorf("scripted git: unsupported argv %q", args)
	}
}

func scriptedArgs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (g *scriptedWorktreeGit) runWorktree(args []string) (string, error) {
	if scriptedArgs(args, "worktree", "list", "--porcelain") {
		return g.porcelain(), nil
	}
	if len(args) == 10 && args[1] == "add" && args[2] == "--lock" && args[3] == "--reason" && args[5] == "-b" && args[7] == "--" {
		return g.add(args[4], args[6], args[8], args[9])
	}
	if len(args) == 5 && args[1] == "lock" && args[2] == "--reason" {
		entry := g.entry(args[4])
		if entry == nil {
			return "", fmt.Errorf("scripted git: lock target %q does not exist", args[4])
		}
		entry.lockReason = args[3]
		return "", nil
	}
	if len(args) == 3 && args[1] == "unlock" {
		entry := g.entry(args[2])
		if entry == nil {
			return "", fmt.Errorf("scripted git: unlock target %q does not exist", args[2])
		}
		entry.lockReason = ""
		return "", nil
	}
	if len(args) >= 4 && args[1] == "remove" {
		path := filepath.Clean(args[len(args)-1])
		if _, ok := g.entries[path]; !ok {
			return "", fmt.Errorf("scripted git: remove target %q does not exist", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return "", err
		}
		delete(g.entries, path)
		return "", nil
	}
	if scriptedArgs(args, "worktree", "prune") {
		return "", nil
	}
	return "", fmt.Errorf("scripted git: unsupported worktree argv %q", args)
}

func (g *scriptedWorktreeGit) add(lockReason, name, path, baseSHA string) (string, error) {
	if g.failNextAdd {
		g.failNextAdd = false
		return "", errors.New("scripted git: injected worktree add failure")
	}
	path = filepath.Clean(path)
	if _, exists := g.branches[name]; exists {
		return "", fmt.Errorf("scripted git: branch %q already exists", name)
	}
	if _, exists := g.entries[path]; exists {
		return "", fmt.Errorf("scripted git: worktree %q already exists", path)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	id := strings.NewReplacer("/", "-", "\\", "-").Replace(name)
	gitDir := filepath.Join(g.root, ".git", "worktrees", id)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		return "", err
	}
	g.branches[name] = baseSHA
	g.entries[path] = &scriptedWorktreeEntry{path: path, branch: name, head: baseSHA, lockReason: lockReason, managed: true}
	return "", nil
}

func (g *scriptedWorktreeGit) runAtPath(args []string) (string, error) {
	path := filepath.Clean(args[1])
	branch, head, ok := g.pathState(path)
	if !ok {
		return "", fmt.Errorf("scripted git: path %q is not a worktree", path)
	}

	switch {
	case len(args) == 6 && args[2] == "rev-parse" && args[3] == "--verify" && args[4] == "--quiet":
		return g.resolveAtPath(path, strings.TrimSuffix(args[5], "^{commit}"))
	case len(args) == 4 && args[2] == "rev-parse" && args[3] == "HEAD":
		return head + "\n", nil
	case len(args) == 6 && args[2] == "symbolic-ref" && args[3] == "--quiet" && args[4] == "--short" && args[5] == "HEAD":
		return branch + "\n", nil
	case len(args) == 5 && args[2] == "status" && args[3] == "--porcelain=v1" && args[4] == "--untracked-files=all":
		return "", nil
	case len(args) == 5 && args[2] == "rev-list" && args[3] == "--count":
		return "0\n", nil
	default:
		return "", fmt.Errorf("scripted git: unsupported -C argv %q", args)
	}
}

func (g *scriptedWorktreeGit) pathState(path string) (branch, head string, ok bool) {
	if path == g.root {
		return "main", g.branches["main"], true
	}
	entry := g.entry(path)
	if entry == nil {
		return "", "", false
	}
	return entry.branch, entry.head, true
}

func (g *scriptedWorktreeGit) resolveAtPath(path, ref string) (string, error) {
	_, head, ok := g.pathState(path)
	if !ok {
		return "", fmt.Errorf("scripted git: unknown path %q", path)
	}
	if ref == "HEAD" {
		return head + "\n", nil
	}
	return g.resolveRef(ref)
}

func (g *scriptedWorktreeGit) resolveRef(ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	if sha, ok := g.branches[ref]; ok {
		return sha + "\n", nil
	}
	if ref == "base-sha" {
		return ref + "\n", nil
	}
	return "", fmt.Errorf("scripted git: ref %q does not exist", ref)
}

func (g *scriptedWorktreeGit) porcelain() string {
	paths := make([]string, 0, len(g.entries))
	for path := range g.entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var b strings.Builder
	b.WriteString("worktree ")
	b.WriteString(g.root)
	b.WriteString("\nHEAD ")
	b.WriteString(g.branches["main"])
	b.WriteString("\nbranch refs/heads/main\n\n")
	for _, path := range paths {
		entry := g.entries[path]
		b.WriteString("worktree ")
		b.WriteString(entry.path)
		b.WriteString("\nHEAD ")
		b.WriteString(entry.head)
		b.WriteString("\nbranch refs/heads/")
		b.WriteString(entry.branch)
		b.WriteByte('\n')
		if entry.lockReason != "" {
			b.WriteString("locked ")
			b.WriteString(entry.lockReason)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}
