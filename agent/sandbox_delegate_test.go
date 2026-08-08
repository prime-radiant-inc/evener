package agent

import (
	"encoding/json"
	"os"
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

	// Exercise every input field, including the security-sensitive DenylistRemove
	// (un-masks a credential dir) and the extra roots, so a silently dropped field
	// is caught.
	net := true
	inputs := sandbox.SandboxPolicy{
		Mode:               sandbox.ModeRestricted,
		Network:            &net,
		DenylistAdd:        []string{"/opt/tight-secret"},
		DenylistRemove:     []string{"/home/x/.kube"},
		ExtraWritableRoots: []string{"/srv/build"},
		ExtraReadRoots:     []string{"/srv/ro"},
	}
	snap := sandboxSnapshotFromInputs(inputs)
	if snap == nil {
		t.Fatal("a sandboxed policy must yield a snapshot")
	}
	if snap.Mode != "restricted" || snap.Network == nil || !*snap.Network {
		t.Errorf("snapshot lost mode/net: %+v", snap)
	}

	pol, ok := sandboxPolicyFromSnapshot(snap)
	if !ok {
		t.Fatal("snapshot must round-trip to a policy request")
	}
	if pol.Mode != sandbox.ModeRestricted ||
		!slices.Contains(pol.DenylistAdd, "/opt/tight-secret") ||
		!slices.Contains(pol.DenylistRemove, "/home/x/.kube") ||
		!slices.Contains(pol.ExtraWritableRoots, "/srv/build") ||
		!slices.Contains(pol.ExtraReadRoots, "/srv/ro") {
		t.Errorf("round-tripped policy lost inputs: %+v", pol)
	}

	// The env-capture path also reflects the enforced policy's inputs.
	env := execenv.NewLocalExecutionEnvironment(lane)
	env.Sandbox = sbxResolve(t, facts, lane, sandbox.ModeRestricted, "/opt/tight-secret")
	if envSnap := sandboxSnapshotFromEnv(env); envSnap == nil || !slices.Contains(envSnap.DenylistAdd, "/opt/tight-secret") {
		t.Errorf("env capture lost the denylist delta: %+v", envSnap)
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

// TestDelegateRestoreRejectsNonOriginatableAxes: a persisted snapshot carrying a
// denylist delta or extra roots could not have come from any serf create path (all
// originate mode+net only), so it is a tampered/foreign descriptor and restore fails
// closed rather than resuming a box serf never granted. Covers the dangerous
// un-masking case (DenylistRemove of a credential dir) and the loosening extra-root
// case.
func TestDelegateRestoreRejectsNonOriginatableAxes(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)

	cases := map[string]sandbox.SandboxPolicy{
		"denylist_remove_unmasks_ssh": {Mode: sandbox.ModeWorkspaceWrite, DenylistRemove: []string{".ssh"}},
		"denylist_add":                {Mode: sandbox.ModeWorkspaceWrite, DenylistAdd: []string{filepath.Join(home, "extra-secret")}},
		"extra_read_root_reopens_fs":  {Mode: sandbox.ModeRestricted, ExtraReadRoots: []string{"/"}},
		"extra_writable_root":         {Mode: sandbox.ModeRestricted, ExtraWritableRoots: []string{"/opt"}},
	}
	for name, pol := range cases {
		t.Run(name, func(t *testing.T) {
			desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: sandboxSnapshotFromInputs(pol)}
			_, err := s.restoreDelegateChildEnvironment(desc, "dlg_tamper")
			if err == nil {
				t.Fatal("a snapshot with non-originatable axes must not be resumable")
			}
			if !strings.Contains(err.Error(), notResumableSandboxUnsatisfiable) {
				t.Errorf("rejection must name %q, got %v", notResumableSandboxUnsatisfiable, err)
			}
		})
	}
}

// TestDelegateRestoreReappliesModeFloor: the no-escalation floor is re-checked on
// resume against the CURRENT parent. A snapshot tampered to a LOOSER mode than the
// resume-time parent (workspace-write child under a restricted parent — a box the
// create floor would never have granted) is refused.
func TestDelegateRestoreReappliesModeFloor(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	sbxSetParentMode(t, s, facts, lane, sandbox.ModeRestricted) // tighter parent

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite})
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: snap}

	_, err := s.restoreDelegateChildEnvironment(desc, "dlg_looser")
	if err == nil {
		t.Fatal("a delegate box looser than the resume-time parent must be refused")
	}
	if !strings.Contains(err.Error(), notResumableSandboxUnsatisfiable) {
		t.Errorf("rejection must name %q, got %v", notResumableSandboxUnsatisfiable, err)
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

// TestDelegateRestoreReleasesLockOnLateFailure: a worktree-isolated restore
// re-acquires the lane's serf:dlg: lock (the §7 revival re-lock) before it
// re-resolves the delegate's sandbox. When that later resolution fails closed
// (host can no longer enforce the persisted mode), the restore produces no
// tracked delegate — so nothing will ever release the lock it just took unless
// the failure path releases it itself. A leaked lock here permanently strands
// the lane.
func TestDelegateRestoreReleasesLockOnLateFailure(t *testing.T) {
	cfg := worktreeTestSessionConfig()
	cfg.testOnly.sandboxProber = sandbox.FakeProber{Facts: sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapCapable: false}}
	r := newWorktreeRepoWithConfig(t, cfg)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	r.unlockLane(t, lanePath) // a kept (unlocked) lane, the shape restore revives

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted})
	desc := &jobstore.DelegateRestoreDescriptor{
		WorkingDir:     lanePath,
		LocalEnvPolicy: "default",
		Isolation:      "worktree",
		Sandbox:        snap,
	}

	_, err := r.s.restoreDelegateChildEnvironment(desc, delegateID)
	if err == nil {
		t.Fatal("restore on a host that cannot enforce the mode must fail closed")
	}
	if !strings.Contains(err.Error(), notResumableSandboxUnsatisfiable) {
		t.Errorf("restore error must name %q, got %v", notResumableSandboxUnsatisfiable, err)
	}

	_, locked, reason := r.laneLocked(t, lanePath)
	if locked {
		t.Errorf("failed restore left the lane locked (reason %q); the re-acquired lock was never released", reason)
	}
}

// TestDiscardRestoredCandidateDisposesSandboxScratch: discardRestoredCandidate
// tears down a restore candidate abandoned on one of restoreTerminalDelegateChildClaimed's
// abort paths (tool validation, tracking collision, side-effect failure). Unlike
// close() — whose subagent teardown loop RETAINS an owned env's sandbox scratch
// for a human handoff — a discarded candidate was never adopted by anything, so
// its fresh per-lane scratch has no owner left and must be disposed outright,
// mirroring disposeUnadoptedSubagentSession's unadopted-env discipline on the
// create-path twin of this abort.
func TestDiscardRestoredCandidateDisposesSandboxScratch(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	child := newSession(t, withClient(c), withDir(lane), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	le, ok := child.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("expected a local execution environment")
	}
	rp := sbxResolve(t, facts, lane, sandbox.ModeWorkspaceWrite)
	if err := le.EnableSandbox(rp); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	tmp := le.Wrapper.SessionTmp()
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("session tmp must exist after EnableSandbox: %v", err)
	}

	child.discardRestoredCandidate()

	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("discardRestoredCandidate must dispose the aborted restore's scratch, stat err = %v", err)
	}
}

// TestSandboxHostFactsProbedOncePerSession: re-resolving a resumed delegate's
// sandbox is what a jobs listing / watch does per delegate record; the host probe
// (RealProber forks ~3 subprocesses) must be gathered ONCE per session and shared,
// not re-forked per record — a fork storm on the resume path.
func TestSandboxHostFactsProbedOncePerSession(t *testing.T) {
	lane, home := sbxLane(t)
	prober := &countingProber{facts: sbxBwrapFacts(home)}
	s := sbxDelegateSessionWithProber(t, prober)

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted})
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: snap}

	const n = 5
	for i := range n {
		rp, reason := s.resolveRestoredDelegateSandbox(desc, execenv.NewLocalExecutionEnvironment(lane))
		if reason != "" {
			t.Fatalf("assessment %d: unexpected not-resumable reason %q", i, reason)
		}
		if rp == nil || !rp.Enforced() {
			t.Fatalf("assessment %d: expected an enforced re-resolved policy", i)
		}
	}
	if prober.calls != 1 {
		t.Errorf("%d resume assessments must probe the host ONCE, got %d probes", n, prober.calls)
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

// sbxSandboxedParent sets the session's currentEnv() to a SANDBOXED local env
// (workspace-write with an out-of-lane extra writable root + a kernel wrapper), so
// a restore test can prove the delegate's box is its OWN policy, never the parent's
// re-rooted one. Returns the extra root that must NOT leak onto the delegate.
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
	w, err := sandbox.NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("parent wrapper: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	env.Wrapper = w
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
	return extra
}

// TestDelegateRestoreOverridesSandboxedParentPolicy: a sandboxed delegate whose own
// persisted snapshot is TIGHTER (restricted) than a LOOSER sandboxed parent
// (workspace-write + extra root) resumes with its OWN tighter box — the parent's
// re-rooted policy must not merge in.
func TestDelegateRestoreOverridesSandboxedParentPolicy(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	parentExtra := sbxSandboxedParent(t, s, facts, lane)

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted})
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: snap}

	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_override")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	le := childEnv.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || le.Sandbox.Mode != sandbox.ModeRestricted {
		t.Fatalf("restored delegate must carry its OWN restricted policy, got %+v", le.Sandbox)
	}
	if slices.Contains(le.Sandbox.FileTool.WriteRoots, parentExtra) || slices.Contains(le.Sandbox.Spawned.WriteRoots, parentExtra) {
		t.Errorf("delegate box leaked the parent's extra writable root %q: file=%v spawned=%v", parentExtra, le.Sandbox.FileTool.WriteRoots, le.Sandbox.Spawned.WriteRoots)
	}
}

// TestDelegateRestoreOffUnderSandboxedParentStaysOff: an OFF delegate (no snapshot)
// resuming under a now-SANDBOXED parent must stay off — it must NOT inherit the
// parent's re-rooted policy or wrapper (the immutability hole the verifier caught).
func TestDelegateRestoreOffUnderSandboxedParentStaysOff(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxBwrapFacts(home)
	s := sbxDelegateSession(t, facts)
	sbxSandboxedParent(t, s, facts, lane)

	// desc.Sandbox nil == an off delegate spawned before the parent was sandboxed.
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default"}

	childEnv, err := s.restoreDelegateChildEnvironment(desc, "dlg_off")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	le := childEnv.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox != nil {
		t.Errorf("off delegate must resume off, not under the parent's policy: %+v", le.Sandbox)
	}
	if le.Wrapper != nil {
		t.Errorf("off delegate must not inherit the parent's kernel wrapper")
	}
}

// TestDelegateRestoreGuardsInfraRootsAgainstItsOwnLane: the infrastructure grant's
// RootGuard must be anchored on the DELEGATE's lane, not on the parent session's
// working directory — a root at or above the delegate's own lane hands the spawned
// child every sibling lane. The spawn path (subagents.go) already passes the
// re-rooted child env; the restore path must agree, because two paths disagreeing
// about a security rule is how a hole gets introduced later.
func TestDelegateRestoreGuardsInfraRootsAgainstItsOwnLane(t *testing.T) {
	hermeticInfraEnv(t)
	lane, home := sbxLane(t)
	laneParent := filepath.Dir(lane) // holds every sibling lane, including this one
	s := sbxDelegateSessionWithConfig(t, sandbox.FakeProber{Facts: sbxBwrapFacts(home)},
		func(cfg *SessionConfig) { cfg.PluginDirs = []string{laneParent} })

	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted})
	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: lane, LocalEnvPolicy: "default", Sandbox: snap}

	rp, reason := s.resolveRestoredDelegateSandbox(desc, execenv.NewLocalExecutionEnvironment(lane))
	if reason != "" {
		t.Fatalf("unexpected not-resumable reason %q", reason)
	}
	if rp == nil {
		t.Fatal("expected a re-resolved policy")
	}
	if slices.Contains(rp.Spawned.ReadRoots, laneParent) {
		t.Errorf("a plugin dir at the delegate lane's parent must be refused by the lane-anchored guard; roots: %v", rp.Spawned.ReadRoots)
	}
}

// sbxDelegateSession builds a delegate-capable session whose resumed-delegate
// sandbox re-resolution uses an injected FakeProber (never the live host).
func sbxDelegateSession(t *testing.T, facts sandbox.HostFacts) *Session {
	t.Helper()
	return sbxDelegateSessionWithProber(t, sandbox.FakeProber{Facts: facts})
}

// sbxDelegateSessionWithProber is sbxDelegateSession with a caller-supplied prober,
// so a test can observe how often the host is probed across resume assessments.
func sbxDelegateSessionWithProber(t *testing.T, prober sandbox.Prober) *Session {
	t.Helper()
	return sbxDelegateSessionWithConfig(t, prober, nil)
}

// sbxDelegateSessionWithConfig is sbxDelegateSessionWithProber with a hook to
// adjust the session config (e.g. configured plugin dirs, which feed the
// hook/MCP infrastructure grant).
func sbxDelegateSessionWithConfig(t *testing.T, prober sandbox.Prober, tweak func(*SessionConfig)) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	cfg := SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-delegate-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       prober,
		},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	return newSession(t, withClient(c), withConfig(cfg))
}

// countingProber records how many times the host is probed, returning fixed facts.
type countingProber struct {
	facts sandbox.HostFacts
	calls int
}

func (c *countingProber) Probe() sandbox.HostFacts {
	c.calls++
	return c.facts
}
