package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

func sbxGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func sbxLane(t *testing.T) (lane, home string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	sbxGit(t, main, "init", "-q")
	sbxGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	lane = filepath.Join(base, "lane")
	sbxGit(t, main, "worktree", "add", "-q", lane, "-b", "feat")
	resolved, err := filepath.EvalSymlinks(lane)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, t.TempDir()
}

func sbxBwrapFacts(home string) sandbox.HostFacts {
	return sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}
}

func sbxResolve(t *testing.T, facts sandbox.HostFacts, cwd string, mode sandbox.Mode, add ...string) *sandbox.ResolvedPolicy {
	t.Helper()
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &net, DenylistAdd: add}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return &rp
}

func TestSandboxSnapshotRoundTrip(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	net := true
	inputs := sandbox.SandboxPolicy{
		Mode:               sandbox.ModeRestricted,
		Network:            &net,
		DenylistAdd:        []string{"/opt/tight-secret"},
		DenylistRemove:     []string{"/home/x/.kube"},
		ExtraWritableRoots: []string{"/srv/build"},
		ExtraReadRoots:     []string{"/srv/ro"},
	}
	snapshot := sandboxSnapshotFromInputs(inputs)
	if snapshot == nil {
		t.Fatal("a sandboxed policy must yield a snapshot")
	}
	policy := sandboxPolicyFromStableSnapshot(snapshot)
	if policy == nil || policy.Mode != sandbox.ModeRestricted ||
		!slices.Contains(policy.DenylistAdd, "/opt/tight-secret") ||
		!slices.Contains(policy.DenylistRemove, "/home/x/.kube") ||
		!slices.Contains(policy.ExtraWritableRoots, "/srv/build") ||
		!slices.Contains(policy.ExtraReadRoots, "/srv/ro") {
		t.Fatalf("stable snapshot round trip lost policy inputs: %#v", policy)
	}
	policy.DenylistAdd[0] = "mutated"
	if snapshot.DenylistAdd[0] != "/opt/tight-secret" {
		t.Fatal("stable sandbox policy aliases its durable descriptor")
	}

	env := execenv.NewLocalExecutionEnvironment(lane)
	env.Sandbox = sbxResolve(t, facts, lane, sandbox.ModeRestricted, "/opt/tight-secret")
	if envSnapshot := sandboxSnapshotFromEnv(env); envSnapshot == nil || !slices.Contains(envSnapshot.DenylistAdd, "/opt/tight-secret") {
		t.Fatalf("environment snapshot lost the denylist delta: %#v", envSnapshot)
	}
}

func TestSandboxSnapshotOffIsNil(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if snapshot := sandboxSnapshotFromEnv(env); snapshot != nil {
		t.Fatalf("an off environment must yield a nil snapshot, got %#v", snapshot)
	}
	if policy := sandboxPolicyFromStableSnapshot(&delegatestore.SandboxSnapshot{Mode: "not-a-mode"}); policy != nil {
		t.Fatalf("malformed stable sandbox mode yielded a policy: %#v", policy)
	}
}

func TestDiscardRestoredCandidateDisposesSandboxScratch(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	child := newSession(t, withClient(client), withDir(lane), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	local := child.currentEnv().(*execenv.LocalExecutionEnvironment)
	if err := local.EnableSandbox(sbxResolve(t, facts, lane, sandbox.ModeWorkspaceWrite)); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	tmp := local.Wrapper.SessionTmp()
	child.discardRestoredCandidate()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("discarded restore candidate retained sandbox scratch: %v", err)
	}
}

func TestDelegateDescriptorJSONRoundTripSnapshot(t *testing.T) {
	net := false
	descriptor := delegatestore.Descriptor{
		ChildSessionID: "child",
		Sandbox: sandboxSnapshotFromInputs(sandbox.SandboxPolicy{
			Mode:        sandbox.ModeWorkspaceWrite,
			Network:     &net,
			DenylistAdd: []string{"/opt/a"},
		}),
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var restored delegatestore.Descriptor
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Sandbox == nil || restored.Sandbox.Mode != "workspace-write" || restored.Sandbox.Network == nil || *restored.Sandbox.Network || !slices.Contains(restored.Sandbox.DenylistAdd, "/opt/a") {
		t.Fatalf("stable descriptor lost sandbox snapshot: %#v", restored.Sandbox)
	}
}

func sbxSandboxedParent(t *testing.T, s *Session, facts sandbox.HostFacts, cwd string) string {
	t.Helper()
	extra := filepath.Join(facts.Home, "parent-extra")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode:               sandbox.ModeWorkspaceWrite,
		Network:            &net,
		ExtraWritableRoots: []string{extra},
	}, facts, cwd)
	if err != nil {
		t.Fatalf("resolve parent: %v", err)
	}
	wrapper, err := sandbox.NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("parent wrapper: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	env.Wrapper = wrapper
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
	return extra
}

func sbxDelegateSession(t *testing.T, facts sandbox.HostFacts) *Session {
	t.Helper()
	return sbxDelegateSessionWithProber(t, sandbox.FakeProber{Facts: facts})
}

func sbxDelegateSessionWithProber(t *testing.T, prober sandbox.Prober) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	return newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-delegate-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       prober,
		},
	}))
}
