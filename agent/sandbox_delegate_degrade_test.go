package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// sbxNoBackendFacts describes the host the read-only delegate regression was
// measured on: an ordinary unprivileged Linux container where bubblewrap cannot
// namespace, so no backend can enforce any sandboxed mode.
func sbxNoBackendFacts(home string) sandbox.HostFacts {
	return sandbox.HostFacts{OS: "linux", Home: home}
}

// TestReadOnlyDelegateSandbox_DegradesWhenHostCannotEnforce: choosing a read-only
// delegate scope is not, by itself, a request the host must be able to satisfy
// with an OS sandbox. Where no backend can enforce ModeReadOnly the derived scope
// becomes write-blocked OFF — the delegate keeps its capability and loses every
// file-tool write root — instead of refusing the spawn.
func TestReadOnlyDelegateSandbox_DegradesWhenHostCannotEnforce(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxNoBackendFacts(home)
	parent := sbxDelegateSession(t, facts) // parent env is off

	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("a derived read-only scope must not refuse on a backendless host: %v", err)
	}
	if policy == nil || policy.Mode != sandbox.ModeOff || !policy.WriteBlocked {
		t.Fatalf("degraded read-only delegate policy = %+v, want off/write-blocked", policy)
	}
	rp, err := sandbox.Resolve(*policy, facts, lane)
	if err != nil {
		t.Fatalf("Resolve degraded policy: %v", err)
	}
	if rp.Enforced() {
		t.Error("the degraded policy must not claim an OS sandbox it does not have")
	}
	if !rp.FileToolConfined() {
		t.Error("the degraded policy must confine the file tools")
	}
	if len(rp.FileTool.WriteRoots) != 0 || len(rp.Spawned.WriteRoots) != 0 {
		t.Errorf("degraded policy retained write roots: file=%v spawned=%v", rp.FileTool.WriteRoots, rp.Spawned.WriteRoots)
	}

	// The same derivation on a host that CAN enforce is untouched: degrading is a
	// last resort, never a shortcut past a working backend.
	enforcing := sbxDelegateSession(t, sbxBwrapFacts(home))
	enforced, err := enforcing.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox on a bwrap host: %v", err)
	}
	if enforced == nil || enforced.Mode != sandbox.ModeReadOnly {
		t.Fatalf("read-only delegate on a bwrap host = %+v, want an enforced read-only box", enforced)
	}
}

// TestReadOnlyDelegateSandbox_NoDegradeWhereFileToolsCannotEnforce: the degrade
// is only sound where the file tools can actually hold the boundary. The
// fd-anchored, symlink-refusing primitives they need exist on linux and darwin
// only; everywhere else the stand-ins fail closed on EVERY operation, reads
// included. Degrading there would hand back a delegate whose file tools are all
// broken, so the spawn keeps its loud refusal instead.
func TestReadOnlyDelegateSandbox_NoDegradeWhereFileToolsCannotEnforce(t *testing.T) {
	lane, home := sbxLane(t)
	windows := sandbox.HostFacts{OS: "windows", Home: home}
	parent := sbxDelegateSession(t, windows)
	sbxSetParentEnv(t, parent, lane)

	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	if policy == nil || policy.Mode != sandbox.ModeReadOnly {
		t.Fatalf("derived scope on a host with no file-tool enforcement = %+v, want an unmodified read-only box", policy)
	}
	if _, _, err := parent.prepareSubagentEnvironment("", policy); err == nil {
		t.Fatal("a box neither the kernel nor the file tools can enforce must refuse the spawn")
	}
}

// TestReadOnlyDelegateSandbox_NoDegradeWhenSecureOpenIsUnavailable catches a
// Linux host being classified from GOOS alone even though its kernel or seccomp
// policy refuses openat2. Such a delegate would launch and then fail every file
// operation, so it must keep the enforced request and refuse before launch.
func TestReadOnlyDelegateSandbox_NoDegradeWhenSecureOpenIsUnavailable(t *testing.T) {
	lane, home := sbxLane(t)
	parent := sbxDelegateSession(t, sbxNoBackendFacts(home))
	parent.cfg.testOnly.fileToolEnforceable = func() bool { return false }
	sbxSetParentEnv(t, parent, lane)

	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	if policy == nil || policy.Mode != sandbox.ModeReadOnly {
		t.Fatalf("derived scope without secure-open support = %+v, want an unmodified read-only box", policy)
	}
	if _, _, err := parent.prepareSubagentEnvironment("", policy); err == nil {
		t.Fatal("a host without a kernel sandbox or working secure-open primitive must refuse the spawn")
	}
}

// TestExplicitReadOnlyDelegateSandboxStillFailsClosed: only the DERIVED scope
// degrades. A caller that states the contract — sandbox="read-only" — must not
// silently receive a weaker one, so its request keeps ModeReadOnly and the spawn
// fails closed with the resolver's typed refusal.
func TestExplicitReadOnlyDelegateSandboxStillFailsClosed(t *testing.T) {
	lane, home := sbxLane(t)
	facts := sbxNoBackendFacts(home)
	parent := sbxDelegateSession(t, facts)
	sbxSetParentEnv(t, parent, lane)

	requested, err := parent.resolveReadOnlyDelegateSandboxRequest("read-only", nil)
	if err != nil {
		t.Fatalf("resolveReadOnlyDelegateSandboxRequest: %v", err)
	}
	if requested == nil || requested.Mode != sandbox.ModeReadOnly || requested.WriteBlocked {
		t.Fatalf("an explicit read-only request = %+v, want an unmodified read-only box", requested)
	}

	_, _, err = parent.prepareSubagentEnvironment("", requested)
	if err == nil {
		t.Fatal("an explicit read-only request must fail closed where no backend can enforce it")
	}
	if _, ok := errors.AsType[*sandbox.RefusalError](err); !ok {
		t.Fatalf("explicit refusal = %T (%v), want a *sandbox.RefusalError", err, err)
	}
	if !strings.Contains(err.Error(), "no sandbox backend is available") {
		t.Errorf("refusal must still say why the host cannot enforce it, got %v", err)
	}
}

// TestDegradedReadOnlyDelegateFileToolsRefuseWrites is the enforcement the
// degrade rests on: a real delegate, spawned from the derived scope on a host
// with no sandbox backend, cannot write the shared workspace through its file
// tools. Its reads work and its own scratch dir is writable, so the delegate is
// useful — it just cannot touch the parent's deliverable.
func TestDegradedReadOnlyDelegateFileToolsRefuseWrites(t *testing.T) {
	lane, home := sbxLane(t)
	parent := sbxDelegateSession(t, sbxNoBackendFacts(home))
	sbxSetParentEnv(t, parent, lane)

	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	prepared := prepareWithDelegateSandbox(t, parent, lane, policy)
	child, ok := prepared.sub.sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("child env must be a LocalExecutionEnvironment")
	}
	if child.Sandbox == nil || child.Sandbox.Enforced() || !child.Sandbox.FileToolConfined() {
		t.Fatalf("child box = %+v, want an unenforced but file-tool-confined policy", child.Sandbox)
	}
	if child.KernelWrapper() != nil {
		t.Error("a host with no backend must give the child no kernel wrapper")
	}

	deliverable := filepath.Join(lane, "deliverable.md")
	if err := os.WriteFile(deliverable, []byte("the parent's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := child.WriteFile(filepath.Join(lane, "new.txt"), "clobber"); err == nil {
		t.Fatal("a read-only delegate must not write the shared workspace")
	} else if _, ok := errors.AsType[*sandbox.DeniedError](err); !ok {
		t.Fatalf("workspace write refusal = %T (%v), want a typed *sandbox.DeniedError", err, err)
	}
	if _, err := child.EditFile(deliverable, "the parent's work", "destroyed", false); err == nil {
		t.Fatal("a read-only delegate must not edit the parent's deliverable")
	}
	if got, _ := os.ReadFile(deliverable); string(got) != "the parent's work\n" {
		t.Fatalf("a denied edit mutated the deliverable: %q", got)
	}

	if got, err := child.ReadFile(deliverable, nil, nil); err != nil || !strings.Contains(got, "the parent's work") {
		t.Fatalf("the delegate must keep reading: got %q err %v", got, err)
	}
	scratch := child.SessionScratchDir()
	if scratch == "" {
		t.Fatal("a degraded read-only delegate must still get a scratch dir to write into")
	}
	if _, err := child.WriteFile(filepath.Join(scratch, "findings.md"), "what I found\n"); err != nil {
		t.Fatalf("the delegate's own scratch dir must stay writable: %v", err)
	}
}

// TestDegradedDelegateSpawnFailureDisposesItsScratch: the degraded box provisions
// a scratch dir like any other per-delegate sandbox, so a spawn that fails after
// provisioning must dispose it. The leak is not just a stray directory: the
// scratch holds a flock lease until it is retained or cleaned, and the crashed-
// scratch sweeper only reclaims candidates whose lease it can acquire — so a
// leaked one survives the sweeper for the daemon's whole uptime. Driven through
// the real abort path (a nil child client fails NewSession after
// prepareSubagentEnvironment), not by calling DisposeSandboxScratch directly. Not
// parallel: it isolates TMPDIR to observe the scratch base.
func TestDegradedDelegateSpawnFailureDisposesItsScratch(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)

	lane, home := sbxLane(t)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-degrade-leak-*"),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       sandbox.FakeProber{Facts: sbxNoBackendFacts(home)},
			childClientFactory:  func() *llm.Client { return nil },
		},
	}))
	if before := sandboxScratchDirs(t, isolated); len(before) != 0 {
		t.Fatalf("isolated tmp base must start free of sandbox scratch, got %v", before)
	}

	policy, err := s.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	ctx = context.WithValue(ctx, ctxDelegateSandboxPolicy, policy)
	prepared, err := s.prepareSubagentRun(ctx, "child task", "", lane, 0, "", "", nil, nil)
	if err == nil {
		releasePreparedTreeSlot(prepared)
		prepared.sub.sess.Close()
		t.Fatal("expected NewSession to fail with a nil child client")
	}
	if left := sandboxScratchDirs(t, isolated); len(left) != 0 {
		t.Errorf("degraded delegate scratch leaked on spawn failure (with its lease): %v", left)
	}
}

// TestDegradedReadOnlyBoundaryDisclosesUnsandboxedShellCapabilities protects the
// structured facts both parent- and delegate-facing renderers consume. It catches
// a disclosure that narrows the residual gap to writes while omitting host reads
// or network access, without pinning model-facing prose.
func TestDegradedReadOnlyBoundaryDisclosesUnsandboxedShellCapabilities(t *testing.T) {
	boundary, ok := degradedReadOnlyBoundaryFor(sandbox.ModeOff, true)
	if !ok {
		t.Fatal("write-blocked off must report the degraded read-only boundary")
	}
	if boundary.ShellSandboxed {
		t.Error("the degraded delegate must not report its shell as sandboxed")
	}
	if !boundary.ShellMayReadHostFiles || !boundary.ShellMayWriteHostFiles {
		t.Errorf("degraded shell host file access = read:%v write:%v, want both allowed",
			boundary.ShellMayReadHostFiles, boundary.ShellMayWriteHostFiles)
	}
	if !boundary.ShellNetworkUnrestricted {
		t.Error("the degraded delegate must report unrestricted shell network access")
	}
	for _, tc := range []struct {
		mode         sandbox.Mode
		writeBlocked bool
	}{
		{mode: sandbox.ModeOff},
		{mode: sandbox.ModeReadOnly, writeBlocked: true},
	} {
		if _, ok := degradedReadOnlyBoundaryFor(tc.mode, tc.writeBlocked); ok {
			t.Errorf("mode=%v writeBlocked=%v reported a degraded boundary", tc.mode, tc.writeBlocked)
		}
	}
}

// TestCreateExplorerDelegateDegradesInsteadOfRefusing drives the reported
// regression end to end: delegate(agent_type="explorer") with NO sandbox argument,
// on a host where no backend can enforce read-only. It must launch — the caller
// asked for an agent type, not for an OS sandbox.
func TestCreateExplorerDelegateDegradesInsteadOfRefusing(t *testing.T) {
	root := newNoBackendDelegateSession(t)

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:      "investigate the failing test",
		AgentType: "explorer",
	})
	if result.Err != nil {
		t.Fatalf("an explorer delegate must launch on a host with no sandbox backend, got %v", result.Err)
	}
	if result.DelegateID == "" {
		t.Fatalf("degraded explorer delegate returned no delegate_id: %+v", result)
	}
}

// TestDegradedWorktreeIsolationDoesNotAddKernelConfinement proves a worktree lane
// changes where the delegate works but cannot add the missing host sandbox.
func TestDegradedWorktreeIsolationDoesNotAddKernelConfinement(t *testing.T) {
	lane, home := sbxLane(t)
	parent := sbxDelegateSession(t, sbxNoBackendFacts(home))
	sbxSetParentEnv(t, parent, lane)

	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	// The isolation path (an explicit working dir) is what the advisory would be
	// pointing at. It confines nothing more: no backend, so no kernel wrapper.
	isolated, _, err := parent.prepareSubagentEnvironment(lane, policy)
	if err != nil {
		t.Fatalf("prepareSubagentEnvironment(isolated): %v", err)
	}
	le, ok := isolated.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("isolated env must be a LocalExecutionEnvironment")
	}
	t.Cleanup(le.DisposeSandboxScratch)
	if le.KernelWrapper() != nil {
		t.Fatal("this host has no backend; an isolated delegate must be no more confined than a shared one")
	}
}

// TestInheritedDegradedWorktreeProvisionsScratchBeforeFileToolUse catches an
// isolated child inheriting a wrapperless write block without re-running
// EnableSandbox. The child must own its scratch before its first file tool builds
// the cached enforcement layer, or the later shell scratch never becomes writable
// through that cache.
func TestInheritedDegradedWorktreeProvisionsScratchBeforeFileToolUse(t *testing.T) {
	lane, home := sbxLane(t)
	parent := sbxDelegateSession(t, sbxNoBackendFacts(home))
	sbxSetParentEnv(t, parent, lane)
	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	degraded, _, err := parent.prepareSubagentEnvironment("", policy)
	if err != nil {
		t.Fatalf("prepare degraded parent environment: %v", err)
	}
	parent.mu.Lock()
	parent.env = degraded
	parent.mu.Unlock()

	childLane := filepath.Join(filepath.Dir(lane), "isolated-child")
	if err := os.MkdirAll(childLane, 0o755); err != nil {
		t.Fatal(err)
	}
	childEnv, ownsFresh, err := parent.prepareSubagentEnvironment(childLane, nil)
	if err != nil {
		t.Fatalf("prepare inherited child environment: %v", err)
	}
	child, ok := childEnv.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("inherited child env must be a LocalExecutionEnvironment")
	}
	if !ownsFresh {
		t.Fatal("an isolated inherited child must own its fresh environment")
	}
	t.Cleanup(child.DisposeSandboxScratch)

	scratch := child.SessionScratchDir()
	if scratch == "" {
		t.Fatal("an inherited wrapperless write block must provision scratch before file-tool use")
	}
	if _, err := child.WriteFile(filepath.Join(scratch, "findings.md"), "ready\n"); err != nil {
		t.Fatalf("the first file-tool use must write the inherited child's scratch: %v", err)
	}
	if _, ok := degradedReadOnlyBoundaryFromEnv(child); !ok {
		t.Fatal("the inherited child's effective environment must report its degraded boundary")
	}
}

// TestDegradedParentPropagatesItsWriteBlock: WriteBlocked is a FLOOR, built so a
// child can never drop it. A degraded parent's block is real — its file tools deny
// every workspace write — so it must reach its own children too, both as the
// floor an explicit child request is checked against and as the box a restoring
// child inherits. Reading that axis through the OS-sandbox predicate reported
// false and silently dropped the floor.
func TestDegradedParentPropagatesItsWriteBlock(t *testing.T) {
	_, home := sbxLane(t)
	parent := sbxDelegateSession(t, sbxNoBackendFacts(home))
	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	env, _, err := parent.prepareSubagentEnvironment("", policy)
	if err != nil {
		t.Fatalf("prepareSubagentEnvironment: %v", err)
	}
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("degraded env must be a LocalExecutionEnvironment")
	}
	t.Cleanup(le.DisposeSandboxScratch)

	mode, network, writeBlocked := parentSandboxFloorForEnv(le)
	if !writeBlocked {
		t.Errorf("a degraded parent must report its write block to its children, got mode=%v net=%v blocked=%v", mode, network, writeBlocked)
	}

	// The floor is what an explicitly write-capable child request is refused
	// against, and what a child with no request of its own inherits.
	if _, err := applyWriteBlockedFloor(writeBlocked, sandbox.ModeWorkspaceWrite.String(),
		&sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}); err == nil {
		t.Error("a write-capable child under a write-blocked parent must be refused")
	}
	sub := sbxDelegateSession(t, sbxNoBackendFacts(home))
	sub.mu.Lock()
	sub.env = le
	sub.mu.Unlock()
	descriptor := delegatestore.Descriptor{ToolNameCeiling: []string{"read_file", "shell"}}
	inherited, err := sub.restoreDelegateSandboxFloor(&descriptor)
	if err != nil {
		t.Fatalf("a child restoring under a degraded parent must inherit its box, not fail: %v", err)
	}
	if inherited == nil || !inherited.WriteBlocked {
		t.Fatalf("restored child floor = %+v, want the parent's write block", inherited)
	}
}

// TestRestoreDelegateSandboxFloorDegradesForReadOnlyCeiling: a degraded delegate
// persists no sandbox snapshot (there is no OS box to record), so resuming it
// re-derives the floor from its structured tool ceiling. That derivation must
// produce the same write-blocked box and must not trip the write-capable guard
// that exists to stop a read-only delegate resuming with workspace writes.
func TestRestoreDelegateSandboxFloorDegradesForReadOnlyCeiling(t *testing.T) {
	lane, home := sbxLane(t)
	s := sbxDelegateSession(t, sbxNoBackendFacts(home))
	sbxSetParentEnv(t, s, lane)

	descriptor := delegatestore.Descriptor{ToolNameCeiling: []string{"read_file", "grep", "glob"}}
	policy, err := s.restoreDelegateSandboxFloor(&descriptor)
	if err != nil {
		t.Fatalf("restoring a read-only delegate on a backendless host must not fail closed: %v", err)
	}
	if policy == nil || policy.Mode != sandbox.ModeOff || !policy.WriteBlocked {
		t.Fatalf("restored floor = %+v, want the degraded off/write-blocked box", policy)
	}
}

// newNoBackendDelegateSession builds a delegate-capable root session on a host
// where no sandbox backend can enforce any mode — the ordinary unprivileged
// container the regression was measured on.
func newNoBackendDelegateSession(t *testing.T) *Session {
	t.Helper()
	workspace := realTempDirForTest(t)
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       sandbox.FakeProber{Facts: sbxNoBackendFacts(t.TempDir())},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

// realTempDirForTest returns a fresh temp dir through its canonical path, so a
// host symlink (macOS /var → /private/var) does not sit in a path the file-tool
// layer must resolve without following symlinks.
func realTempDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return dir
}

// sbxSetParentEnv points the session's current env at dir with no sandbox policy,
// the ordinary unsandboxed parent a delegate is spawned from.
func sbxSetParentEnv(t *testing.T, s *Session, dir string) {
	t.Helper()
	env := execenv.NewLocalExecutionEnvironment(dir)
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
}
