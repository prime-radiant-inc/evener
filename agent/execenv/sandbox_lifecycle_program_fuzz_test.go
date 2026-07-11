//go:build serffuzz

package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// FuzzSandboxLifecycleProgram drives environment sandbox provisioning, re-root,
// control-policy replacement, disposal, cleanup, and wrapper configuration using
// only synthetic policies and fixture directories. It never starts the constructed
// exec.Cmd: wrapper assertions inspect its argv/environment before execution. All
// session scratch uses an instance-local fixture base, and the oracle rejects any
// path escaping that base or either synthetic worktree.
func FuzzSandboxLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x4a},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 32 {
			program = program[:32]
		}
		first := runSandboxLifecycleProgram(t, program)
		second := runSandboxLifecycleProgram(t, program)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("sandbox lifecycle program is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type sandboxLifecycleTrace struct {
	Modes       []string
	Worktrees   []string
	ScratchBase string
	Escaped     bool
	WrapperArgv []string
	EscapedArgs string
}

func runSandboxLifecycleProgram(t *testing.T, program []byte) sandboxLifecycleTrace {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	worktree := filepath.Join(home, "worktree")
	lane := filepath.Join(home, "lane")
	tmpBase := filepath.Join(base, "sandbox-tmp")
	for _, dir := range []string{filepath.Join(worktree, ".git"), filepath.Join(lane, ".git"), tmpBase} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make sandbox fixture %q: %v", dir, err)
		}
	}

	trace := sandboxLifecycleTrace{ScratchBase: "sandbox-tmp"}
	host := sandbox.HostFacts{
		OS: "linux", Home: home, BwrapCapable: true, BwrapPath: "/fixture/bwrap", OverlaySupported: true,
	}
	for _, mode := range []sandbox.Mode{sandbox.ModeRestricted, sandbox.ModeWorkspaceWrite, sandbox.ModeReadOnly} {
		policy := sandboxLifecyclePolicy(t, mode, host, worktree)
		env := NewLocalExecutionEnvironment(worktree)
		env.sandboxTmpBase = tmpBase
		if err := env.EnableSandbox(policy); err != nil {
			t.Fatalf("EnableSandbox(%s): %v", mode, err)
		}
		if env.Sandbox != policy || env.Wrapper == nil || env.ownedSessionTmp == nil {
			t.Fatalf("EnableSandbox(%s) state Sandbox=%p policy=%p Wrapper=%v tmp=%v", mode, env.Sandbox, policy, env.Wrapper != nil, env.ownedSessionTmp != nil)
		}
		sandboxLifecycleAssertWithin(t, tmpBase, env.ownedSessionTmp.Dir)
		if fs := env.sandbox(); fs == nil {
			t.Fatalf("EnableSandbox(%s) did not build an enforced sandbox filesystem", mode)
		}

		grant := env.WithSandboxInvocationGrant(filepath.Join(worktree, "granted.txt"))
		grantEnv, ok := grant.(*LocalExecutionEnvironment)
		if !ok || grantEnv == env || grantEnv.sandboxTmpBase != tmpBase || grantEnv.sandbox() == nil {
			t.Fatalf("sandbox invocation grant did not create an isolated fixture clone for %s", mode)
		}
		grantEnv.Cleanup()

		child := env.WithWorkingDirectory(lane)
		if child.SandboxReRootError() != nil || child.Sandbox == nil || child.Wrapper == nil || child.ownedSessionTmp != nil {
			t.Fatalf("sandbox child re-root failed for %s: err=%v sandbox=%v wrapper=%v owned=%v", mode, child.SandboxReRootError(), child.Sandbox != nil, child.Wrapper != nil, child.ownedSessionTmp != nil)
		}
		if child.Sandbox.Git.WorktreeRoot != lane || child.Wrapper.Policy().Git.WorktreeRoot != lane || child.sandboxTmpBase != tmpBase {
			t.Fatalf("sandbox child did not re-root to lane %q: policy=%q wrapper=%q tmpBase=%q", lane, child.Sandbox.Git.WorktreeRoot, child.Wrapper.Policy().Git.WorktreeRoot, child.sandboxTmpBase)
		}
		child.Cleanup()

		oldFS := env.sandbox()
		if err := env.UseControlPolicy(worktree); err != nil {
			t.Fatalf("UseControlPolicy(%s): %v", mode, err)
		}
		if env.Sandbox == nil || env.Wrapper == nil || env.sandbox() == nil || env.sandbox() == oldFS {
			t.Fatalf("UseControlPolicy(%s) did not replace sandbox state", mode)
		}
		trace.Modes = append(trace.Modes, mode.String())
		trace.Worktrees = append(trace.Worktrees, sandboxLifecycleRelative(base, env.Sandbox.Git.WorktreeRoot))
		ownedScratch := env.ownedSessionTmp.Dir
		env.Cleanup()
		if _, err := os.Stat(ownedScratch); !os.IsNotExist(err) {
			t.Fatalf("Cleanup(%s) did not remove the owned scratch: %v", mode, err)
		}
	}

	// A nil/off policy must stay inert and never create a scratch directory.
	off := NewLocalExecutionEnvironment(worktree)
	off.sandboxTmpBase = tmpBase
	if err := off.EnableSandbox(nil); err != nil || off.Sandbox != nil || off.Wrapper != nil || off.ownedSessionTmp != nil {
		t.Fatalf("EnableSandbox(nil) state err=%v sandbox=%v wrapper=%v tmp=%v", err, off.Sandbox, off.Wrapper, off.ownedSessionTmp)
	}
	offPolicy, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeOff}, host, worktree)
	if err != nil {
		t.Fatalf("resolve off: %v", err)
	}
	if err := off.EnableSandbox(&offPolicy); err != nil || off.Sandbox != &offPolicy || off.Wrapper != nil || off.ownedSessionTmp != nil {
		t.Fatalf("EnableSandbox(off) state err=%v sandbox=%v wrapper=%v tmp=%v", err, off.Sandbox, off.Wrapper, off.ownedSessionTmp)
	}
	off.Cleanup()

	// Both failure branches leave an environment fully unsandboxed and clean the
	// per-call scratch before returning.
	missingBinaryHost := host
	missingBinaryHost.BwrapPath = ""
	missingBinary := sandboxLifecyclePolicy(t, sandbox.ModeRestricted, missingBinaryHost, worktree)
	missingEnv := NewLocalExecutionEnvironment(worktree)
	missingEnv.sandboxTmpBase = tmpBase
	if err := missingEnv.EnableSandbox(missingBinary); err == nil || missingEnv.Sandbox != nil || missingEnv.Wrapper != nil || missingEnv.ownedSessionTmp != nil {
		t.Fatalf("missing backend binary state err=%v sandbox=%v wrapper=%v tmp=%v", err, missingEnv.Sandbox, missingEnv.Wrapper, missingEnv.ownedSessionTmp)
	}
	relativeBinaryHost := host
	relativeBinaryHost.BwrapPath = "relative-bwrap"
	relativeBinary := sandboxLifecyclePolicy(t, sandbox.ModeRestricted, relativeBinaryHost, worktree)
	relativeEnv := NewLocalExecutionEnvironment(worktree)
	relativeEnv.sandboxTmpBase = tmpBase
	if err := relativeEnv.EnableSandbox(relativeBinary); err == nil || relativeEnv.Sandbox != nil || relativeEnv.Wrapper != nil || relativeEnv.ownedSessionTmp != nil {
		t.Fatalf("relative backend binary state err=%v sandbox=%v wrapper=%v tmp=%v", err, relativeEnv.Sandbox, relativeEnv.Wrapper, relativeEnv.ownedSessionTmp)
	}

	// Re-provisioning tears down an owned scratch before creating its replacement;
	// failure to create the replacement must likewise leave no half-sandbox state.
	reprovision := NewLocalExecutionEnvironment(worktree)
	reprovision.sandboxTmpBase = tmpBase
	if err := reprovision.EnableSandbox(sandboxLifecyclePolicy(t, sandbox.ModeRestricted, host, worktree)); err != nil {
		t.Fatalf("first reprovision EnableSandbox: %v", err)
	}
	firstScratch := reprovision.ownedSessionTmp.Dir
	if err := reprovision.EnableSandbox(sandboxLifecyclePolicy(t, sandbox.ModeRestricted, host, worktree)); err != nil {
		t.Fatalf("second reprovision EnableSandbox: %v", err)
	}
	if reprovision.ownedSessionTmp == nil || reprovision.ownedSessionTmp.Dir == firstScratch {
		t.Fatalf("reprovision did not replace scratch: first=%q current=%v", firstScratch, reprovision.ownedSessionTmp)
	}
	if _, err := os.Stat(firstScratch); !os.IsNotExist(err) {
		t.Fatalf("reprovision retained old scratch %q: %v", firstScratch, err)
	}
	reprovision.Cleanup()

	tmpBaseFile := filepath.Join(base, "sandbox-tmp-file")
	if err := os.WriteFile(tmpBaseFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write invalid tmp base: %v", err)
	}
	tmpFailure := NewLocalExecutionEnvironment(worktree)
	tmpFailure.sandboxTmpBase = tmpBaseFile
	if err := tmpFailure.EnableSandbox(sandboxLifecyclePolicy(t, sandbox.ModeRestricted, host, worktree)); err == nil || tmpFailure.Sandbox != nil || tmpFailure.Wrapper != nil || tmpFailure.ownedSessionTmp != nil {
		t.Fatalf("invalid tmp base state err=%v sandbox=%v wrapper=%v tmp=%v", err, tmpFailure.Sandbox, tmpFailure.Wrapper, tmpFailure.ownedSessionTmp)
	}

	// DisposeSandboxScratch owns both its cached fd layer and its fixture-local
	// scratch without waiting for or signalling any process.
	disposable := NewLocalExecutionEnvironment(worktree)
	disposable.sandboxTmpBase = tmpBase
	policy := sandboxLifecyclePolicy(t, sandbox.ModeRestricted, host, worktree)
	if err := disposable.EnableSandbox(policy); err != nil {
		t.Fatalf("EnableSandbox disposable: %v", err)
	}
	disposePath := disposable.ownedSessionTmp.Dir
	_ = disposable.sandbox()
	disposable.DisposeSandboxScratch()
	if disposable.ownedSessionTmp != nil || disposable.sbfs != nil {
		t.Fatal("DisposeSandboxScratch retained owned state")
	}
	if _, err := os.Stat(disposePath); !os.IsNotExist(err) {
		t.Fatalf("DisposeSandboxScratch did not remove %q: %v", disposePath, err)
	}

	// A manually built wrapper without resolver inputs cannot re-root. The child
	// must fail closed as a unit rather than carrying one half of the sandbox.
	divergent := NewLocalExecutionEnvironment(worktree)
	divergent.Sandbox = sandboxLifecyclePolicy(t, sandbox.ModeWorkspaceWrite, host, worktree)
	literal, err := sandbox.NewWrapper(sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite, Backend: sandbox.BackendBwrap}, "/fixture/bwrap", filepath.Join(tmpBase, "literal"))
	if err != nil {
		t.Fatalf("NewWrapper literal: %v", err)
	}
	divergent.Wrapper = literal
	failedChild := divergent.WithWorkingDirectory(lane)
	if failedChild.SandboxReRootError() == nil || failedChild.Sandbox != nil || failedChild.Wrapper != nil {
		t.Fatalf("divergent re-root did not fail closed: err=%v sandbox=%v wrapper=%v", failedChild.SandboxReRootError(), failedChild.Sandbox != nil, failedChild.Wrapper != nil)
	}

	// A literal enforced policy cannot be re-rooted or converted to a control
	// policy because it carries no resolver inputs. Both callers must surface that
	// refusal instead of preserving an old worktree's grants.
	unrootable := NewLocalExecutionEnvironment(worktree)
	unrootable.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite}
	if child := unrootable.WithWorkingDirectory(lane); child.SandboxReRootError() == nil || child.Sandbox != nil || child.Wrapper != nil {
		t.Fatalf("literal policy re-root did not fail closed: err=%v sandbox=%v wrapper=%v", child.SandboxReRootError(), child.Sandbox != nil, child.Wrapper != nil)
	}
	if err := unrootable.UseControlPolicy(worktree); err == nil {
		t.Fatal("literal policy UseControlPolicy unexpectedly succeeded")
	}

	// Wrapper configuration is inspected, never executed. It must scrub secrets,
	// clear inherited descriptors, and put the bwrap binary first in argv.
	wrapped := NewLocalExecutionEnvironment(worktree)
	wrapped.sandboxTmpBase = tmpBase
	if err := wrapped.EnableSandbox(sandboxLifecyclePolicy(t, sandbox.ModeWorkspaceWrite, host, worktree)); err != nil {
		t.Fatalf("EnableSandbox wrapped: %v", err)
	}
	scratch := wrapped.ownedSessionTmp.Dir
	sandboxLifecycleAssertWithin(t, tmpBase, scratch)
	fd, err := os.CreateTemp(base, "descriptor")
	if err != nil {
		t.Fatalf("create extra descriptor: %v", err)
	}
	defer fd.Close()                                 //nolint:errcheck
	cmd := exec.Command("/fixture/tool", "argument") //nolint:noctx // configured only, never started
	// ApplyEnvFloor is deliberately layered on the already-secret-scrubbed
	// ExecCommand environment. Exercise the floor's own ssh-agent/cloud drops
	// here; FuzzProcessRuntimeProgram proves the preceding secret scrub.
	cmd.Env = []string{"PATH=/fixture/bin", "SSH_AUTH_SOCK=/fixture/agent.sock", "AWS_ACCESS_KEY_ID=secret"}
	cmd.ExtraFiles = []*os.File{fd}
	wrapped.wrapForSandbox(cmd, worktree)
	if cmd.Path != "/fixture/bwrap" || len(cmd.Args) == 0 || cmd.Args[0] != "/fixture/bwrap" || cmd.ExtraFiles != nil {
		t.Fatalf("wrapped command = path=%q args=%v extra=%v", cmd.Path, cmd.Args, cmd.ExtraFiles)
	}
	joinedEnv := strings.Join(cmd.Env, "\n")
	if strings.Contains(joinedEnv, "SSH_AUTH_SOCK=") || strings.Contains(joinedEnv, "AWS_ACCESS_KEY_ID=") || !strings.Contains(joinedEnv, "TMPDIR="+scratch) {
		t.Fatalf("wrapped command environment did not enforce sandbox floor: %v", cmd.Env)
	}
	trace.WrapperArgv = sandboxLifecycleNormalizedArgv(base, scratch, cmd.Args)
	escaped := ShellEscapeArgs("plain", "", "semi;"+string(program), "quote'x")
	if !strings.Contains(escaped, "''") || !strings.Contains(escaped, "'semi;") || !strings.Contains(escaped, "'\"'\"'") {
		t.Fatalf("ShellEscapeArgs did not preserve shell boundaries: %q", escaped)
	}
	trace.EscapedArgs = escaped
	wrapped.Cleanup()
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("wrapped Cleanup did not remove scratch %q: %v", scratch, err)
	}

	trace.Escaped = sandboxLifecycleAnyEscaped(base, trace.Worktrees)
	if trace.Escaped {
		t.Fatalf("sandbox lifecycle trace escaped fixture: %#v", trace)
	}
	return trace
}

func sandboxLifecyclePolicy(t *testing.T, mode sandbox.Mode, host sandbox.HostFacts, worktree string) *sandbox.ResolvedPolicy {
	t.Helper()
	policy, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, worktree)
	if err != nil {
		t.Fatalf("resolve %s policy: %v", mode, err)
	}
	return &policy
}

func sandboxLifecycleAssertWithin(t *testing.T, base, value string) {
	t.Helper()
	rel, err := filepath.Rel(base, value)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("path %q escaped fixture base %q (rel=%q err=%v)", value, base, rel, err)
	}
}

func sandboxLifecycleRelative(base, value string) string {
	rel, err := filepath.Rel(base, value)
	if err != nil {
		return value
	}
	return rel
}

func sandboxLifecycleRelativeSlice(base string, values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ReplaceAll(value, base, "$ROOT")
	}
	return result
}

func sandboxLifecycleNormalizedArgv(base, scratch string, values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		value = strings.ReplaceAll(value, scratch, "$SESSION_TMP")
		result[i] = strings.ReplaceAll(value, base, "$ROOT")
	}
	return result
}

func sandboxLifecycleAnyEscaped(base string, values []string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, "$ROOT") || !filepath.IsAbs(value) {
			continue
		}
		rel, err := filepath.Rel(base, value)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
