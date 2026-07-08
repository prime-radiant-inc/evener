package agent

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// sbxGit runs a git command in dir with a fixed identity, skipping when git is
// absent. Shared by the delegate-sandbox plumbing tests.
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

// sbxLane materializes a main repo with one linked worktree; returns the lane path
// and a fake home for the credential denylist anchor.
func sbxLane(t *testing.T) (lane, home string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := exec.Command("mkdir", "-p", main).Run(); err != nil {
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

// TestSandboxSnapshotRoundTrip: capturing a re-rooted env's policy INPUTS and
// rebuilding the request from them preserves mode, net, and denylist deltas — the
// lane-independent request a resumed delegate re-resolves.
func TestSandboxSnapshotRoundTrip(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	env := execenv.NewLocalExecutionEnvironment(lane)
	env.Sandbox = sbxResolve(t, facts, lane, sandbox.ModeRestricted, "/opt/tight-secret")

	snap := sandboxSnapshotFromEnv(env)
	if snap == nil {
		t.Fatal("a sandboxed env must yield a snapshot")
	}
	if snap.Mode != "restricted" {
		t.Errorf("snapshot mode = %q, want restricted", snap.Mode)
	}
	if snap.Network == nil || !*snap.Network {
		t.Errorf("snapshot must carry net=on")
	}
	if !slices.Contains(snap.DenylistAdd, "/opt/tight-secret") {
		t.Errorf("snapshot must carry the denylist delta: %v", snap.DenylistAdd)
	}

	pol, ok := sandboxPolicyFromSnapshot(snap)
	if !ok {
		t.Fatal("snapshot must round-trip to a policy request")
	}
	if pol.Mode != sandbox.ModeRestricted || !slices.Contains(pol.DenylistAdd, "/opt/tight-secret") {
		t.Errorf("round-tripped policy lost inputs: %+v", pol)
	}
}

// An off env yields no snapshot (byte-identical descriptor to today).
func TestSandboxSnapshotOffIsNil(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if snap := sandboxSnapshotFromEnv(env); snap != nil {
		t.Errorf("an off env must yield a nil snapshot, got %+v", snap)
	}
}

// TestDelegateRestoreReResolvesSandboxAtLane: restore re-resolves the persisted
// snapshot against the delegate's OWN lane with freshly-probed host facts, anchors
// the box there, and provisions a fresh per-lane session tmp.
func TestDelegateRestoreReResolvesSandboxAtLane(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted})
	desc := &jobstore.DelegateRestoreDescriptor{
		WorkingDir:     lane,
		LocalEnvPolicy: "default",
		Sandbox:        snap,
	}
	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_test")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	le, ok := childEnv.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("restored env must be a LocalExecutionEnvironment")
	}
	if le.Sandbox == nil || !le.Sandbox.Enforced() {
		t.Fatal("restored delegate env must be sandboxed")
	}
	if le.Sandbox.Git.WorktreeRoot != lane {
		t.Errorf("restored sandbox worktree = %q, want lane %q", le.Sandbox.Git.WorktreeRoot, lane)
	}
	if !slices.Contains(le.Sandbox.FileTool.WriteRoots, lane) {
		t.Errorf("restored write roots must anchor at the lane: %v", le.Sandbox.FileTool.WriteRoots)
	}
	if le.Wrapper == nil || le.Wrapper.SessionTmp() == "" {
		t.Error("restore must provision a fresh per-lane session tmp + kernel wrapper")
	}
}

// TestDelegateRestoreImmutableAcrossConfigDrift: restore uses the persisted
// snapshot's (tighter) denylist, NOT the parent session's current (looser) config
// — a config that loosened between serf runs cannot widen a live delegate's box.
func TestDelegateRestoreImmutableAcrossConfigDrift(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts) // parent env is off — the "loosened" config

	tight := filepath.Join(home, "extra-secret")
	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{
		Mode:        sandbox.ModeWorkspaceWrite,
		DenylistAdd: []string{tight},
	})
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: snap}

	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_drift")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	le := childEnv.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || !slices.Contains(le.Sandbox.MaskedPaths, filepath.Clean(tight)) {
		t.Errorf("restored box must keep the persisted tight denylist %q: %v", tight, maskedOf(le.Sandbox))
	}
}

// TestDelegateRestoreFailsClosedOnUnsatisfiableHost: a host that can no longer
// enforce the mode refuses the restore (notResumableSandboxUnsatisfiable) rather
// than resuming unscoped.
func TestDelegateRestoreFailsClosedOnUnsatisfiableHost(t *testing.T) {
	lane, _ := sbxLane(t)
	// bwrap NOT capable → the floor cannot enforce any sandboxed mode.
	facts := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapCapable: false}
	s := sbxDelegateSession(t, facts)

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted})
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: snap}

	_, err := s.restoreDelegateChildEnvironment(desc, "dlg_nofit")
	if err == nil {
		t.Fatal("restore on a host that cannot enforce the mode must fail closed")
	}
	if !strings.Contains(err.Error(), notResumableSandboxUnsatisfiable) {
		t.Errorf("restore error must name %q, got %v", notResumableSandboxUnsatisfiable, err)
	}
}

// TestDelegateDescriptorJSONRoundTripSnapshot: the snapshot survives a descriptor
// marshal/unmarshal with snake_case keys.
func TestDelegateDescriptorJSONRoundTripSnapshot(t *testing.T) {
	net := false
	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{
		Mode:        sandbox.ModeWorkspaceWrite,
		Network:     &net,
		DenylistAdd: []string{"/opt/a"},
	})
	desc := &jobstore.DelegateRestoreDescriptor{Version: 1, ChildSessionID: "c", WorkingDir: "/w", Sandbox: snap}

	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"sandbox"`, `"mode"`, `"denylist_add"`, `"network"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("descriptor JSON missing snake_case key %s: %s", key, b)
		}
	}
	var back jobstore.DelegateRestoreDescriptor
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Sandbox == nil || back.Sandbox.Mode != "workspace-write" ||
		back.Sandbox.Network == nil || *back.Sandbox.Network != false ||
		!slices.Contains(back.Sandbox.DenylistAdd, "/opt/a") {
		t.Errorf("snapshot did not survive JSON round-trip: %+v", back.Sandbox)
	}
}

// TestBothDescriptorBuildersCarrySandbox: the initial builder copies the prepared
// snapshot, and the resume builder deep-copies the previous descriptor's snapshot
// (so it is not silently dropped on a resumed turn).
func TestBothDescriptorBuildersCarrySandbox(t *testing.T) {
	facts := sbxBwrapFacts(t.TempDir())
	s := sbxDelegateSession(t, facts)
	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, DenylistAdd: []string{"/opt/x"}})

	// Initial builder.
	prepared := &preparedSubagentRun{workingDir: "/w", localEnvPolicy: "default", sandboxSnapshot: snap}
	initial := s.delegateRestoreDescriptor("job_1", "child_1", "task", encodeRef("", "child_1"), nil, prepared)
	if initial.Sandbox != snap {
		t.Errorf("initial builder must carry the prepared snapshot")
	}

	// Resume builder.
	resumed := s.resumedDelegateRestoreDescriptor("job_2", "child_1", encodeRef("", "child_1"), nil, initial)
	if resumed.Sandbox == nil {
		t.Fatal("resume builder DROPPED the sandbox snapshot")
	}
	if resumed.Sandbox == initial.Sandbox {
		t.Error("resume builder must DEEP-COPY the snapshot, not alias it")
	}
	if resumed.Sandbox.Mode != "restricted" || !slices.Contains(resumed.Sandbox.DenylistAdd, "/opt/x") {
		t.Errorf("resume builder lost snapshot content: %+v", resumed.Sandbox)
	}
}

func maskedOf(rp *sandbox.ResolvedPolicy) []string {
	if rp == nil {
		return nil
	}
	return rp.MaskedPaths
}

// sbxDelegateSession builds a delegate-capable session whose resumed-delegate
// sandbox re-resolution uses an injected FakeProber (never the live host).
func sbxDelegateSession(t *testing.T, facts sandbox.HostFacts) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return newSession(t, withClient(c), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-delegate-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: facts},
		},
	}))
}
