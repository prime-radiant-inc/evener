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

// TestSandboxPromptLineDisclosesDegradedReadOnlyDelegate: silent degradation is
// its own bug, so the delegate's own prompt must say exactly where its read-only
// boundary holds — enforced for the file tools, advisory for the shell — and name
// the one directory it may write.
func TestSandboxPromptLineDisclosesDegradedReadOnlyDelegate(t *testing.T) {
	root := realTempDirForTest(t)
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeOff, WriteBlocked: true},
		sbxNoBackendFacts(realTempDirForTest(t)), root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	local := execenv.NewLocalExecutionEnvironment(root)
	t.Cleanup(func() { local.Cleanup(); local.DisposeSandboxScratch() })
	if err := local.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}

	got := sandboxPromptLine(local)
	if got == "" {
		t.Fatal("a degraded read-only delegate must still be told what its box does")
	}
	if strings.Contains(got, "off (network") {
		t.Errorf("the line must not describe the delegate's scope as the mode name %q: %q", sandbox.ModeOff, got)
	}
	for _, want := range []string{"read-only", "file tools", "shell"} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded sandbox line must mention %q: %q", want, got)
		}
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("a degraded read-only delegate must be given a scratch dir")
	}
	if !strings.Contains(got, scratch) {
		t.Errorf("degraded sandbox line must name the one writable directory: got %q, want it to contain %q", got, scratch)
	}
}

// TestCreateExplorerDelegateDegradesInsteadOfRefusing drives the reported
// regression end to end: delegate(agent_type="explorer") with NO sandbox argument,
// on a host where no backend can enforce read-only. It must launch — the caller
// asked for an agent type, not for an OS sandbox — and the parent must be told in
// band that the delegate's read-only boundary holds for file tools and is
// advisory for its shell, so a degradation is never silent.
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
	var disclosure string
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "read-only") {
			disclosure = warning
		}
	}
	if disclosure == "" {
		t.Fatalf("the parent must be told the boundary degraded, got warnings %q", result.Warnings)
	}
	for _, want := range []string{"file tools", "shell", "no sandbox backend"} {
		if !strings.Contains(disclosure, want) {
			t.Errorf("degradation disclosure must mention %q: %q", want, disclosure)
		}
	}
}

// TestCreateExplorerDelegateEnforcedHostSaysNothing: the disclosure is a report
// of a real gap, not decoration. Where the host CAN enforce the read-only box,
// the spawn carries no such warning.
func TestCreateExplorerDelegateEnforcedHostSaysNothing(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t) // bwrap-capable prober

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:      "investigate the failing test",
		AgentType: "explorer",
	})
	if result.Err != nil {
		t.Fatalf("createDelegate on a bwrap host: %v", result.Err)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "no sandbox backend") {
			t.Errorf("an enforced read-only delegate must not claim a degraded boundary: %q", warning)
		}
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
