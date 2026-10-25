package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// newSandboxedShellEnv resolves policy against this host and enables the real
// backend. The cwd cases below are about the backend's visible namespace as
// much as the policy, so a host with no usable backend skips instead of
// proving a policy-only approximation.
func newSandboxedShellEnv(t *testing.T, policy sandbox.SandboxPolicy, root string) *execenv.LocalExecutionEnvironment {
	t.Helper()
	facts := sandbox.RealProber{}.Probe()
	if !facts.BwrapCapable && !facts.SeatbeltAvailable() {
		t.Skip("no usable sandbox backend on this host")
	}
	net := true
	policy.Network = &net
	rp, err := sandbox.Resolve(policy, facts, root)
	if err != nil {
		t.Fatalf("resolve %s sandbox: %v", policy.Mode, err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)
	t.Cleanup(func() {
		env.Cleanup()
		env.DisposeSandboxScratch()
	})
	if err := env.EnableSandbox(&rp); err != nil {
		t.Skipf("real %s sandbox unavailable: %v", policy.Mode, err)
	}
	return env
}

func newSandboxedShellSession(t *testing.T, env *execenv.LocalExecutionEnvironment) *Session {
	t.Helper()
	return newSession(t, withEnv(env), withoutGitSnapshot())
}

func executeSandboxedShell(t *testing.T, s *Session, args string) tool.ExecResult {
	t.Helper()
	return s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "issue-4332",
		Name:      "shell",
		Arguments: json.RawMessage(args),
	})
}

// decodeSandboxedShellOutput fails on a tool error and decodes a completed
// shell result. A pre-spawn cwd refusal is a tool error, which is what the
// refusal cases assert; the acceptance cases use this helper so a refusal
// cannot masquerade as empty output.
func decodeSandboxedShellOutput(t *testing.T, res tool.ExecResult) shellToolResult {
	t.Helper()
	if res.IsError {
		t.Fatalf("shell returned a tool error: %s", res.Output)
	}
	var out shellToolResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("decode shell result: %v (output: %s)", err, res.Output)
	}
	if out.Output == nil {
		t.Fatalf("shell result has no output: %s", res.Output)
	}
	return out
}

// issue4332Fixture creates a session root and a sibling readable directory
// under base. /var/tmp is the default because it is outside bubblewrap's
// private /tmp and /dev mounts, so a cwd there exercises the policy read set
// rather than the private-mount refusal.
func issue4332Fixture(t *testing.T, base string) (root, outside string) {
	t.Helper()
	base, err := os.MkdirTemp(base, "evener-4332-")
	if err != nil {
		t.Skipf("no writable fixture base %q: %v", base, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return root, outside
}

func requireBwrapPrivateMounts(t *testing.T, env *execenv.LocalExecutionEnvironment) {
	t.Helper()
	if runtime.GOOS != "linux" || env.Wrapper == nil {
		t.Skip("bubblewrap private /tmp mounts are Linux-only")
	}
	if env.Wrapper.Policy().Backend != sandbox.BackendBwrap {
		t.Skip("bubblewrap private /tmp mounts are Linux-only")
	}
}

// TestShellToolReadOnlyCwdOutsideRootAcceptedWhenReadable is the red-first
// case for #4332: a read-only shell must accept a cwd it can read outside the
// session root, matching `cd` inside the command. Before the fix the cwd call
// fails at resolveShellWorkingDir; the `cd` control already works.
func TestShellToolReadOnlyCwdOutsideRootAcceptedWhenReadable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("pwd is POSIX-only")
	}
	root, outside := issue4332Fixture(t, "/var/tmp")
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	s := newSandboxedShellSession(t, env)
	want, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, outside))
	out := decodeSandboxedShellOutput(t, res)
	if got := strings.TrimSpace(*out.Output); got != want {
		t.Fatalf("shell pwd with cwd %q = %q, want %q", outside, got, want)
	}

	res = executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"cd %s && pwd"}`, outside))
	out = decodeSandboxedShellOutput(t, res)
	if got := strings.TrimSpace(*out.Output); got != want {
		t.Fatalf("shell cd %q = %q, want %q", outside, got, want)
	}
}

// TestShellToolReadOnlyCwdOutsideRootStillReadOnly pins that accepting the
// outside cwd did not grant a write: the real read-only wrapper still mounts
// the tree read-only.
func TestShellToolReadOnlyCwdOutsideRootStillReadOnly(t *testing.T) {
	t.Parallel()
	root, outside := issue4332Fixture(t, "/var/tmp")
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	s := newSandboxedShellSession(t, env)

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"touch attempted-write && printf wrote","cwd":%q}`, outside))
	if res.IsError {
		t.Fatalf("write probe was refused before it ran: %s", res.Output)
	}
	if _, err := os.Stat(filepath.Join(outside, "attempted-write")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only write under accepted cwd: stat err = %v, want not-exist", err)
	}
}

// TestShellToolReadOnlyCwdMaskedRefused covers both a masked directory and a
// workspace symlink that resolves to it. The symlink case keeps today's
// symlink-aware refusal through the new canonicalisation check.
func TestShellToolReadOnlyCwdMaskedRefused(t *testing.T) {
	t.Parallel()
	base, err := os.MkdirTemp("/var/tmp", "evener-4332-")
	if err != nil {
		t.Skipf("no writable fixture base /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "root")
	masked := filepath.Join(base, "masked")
	for _, dir := range []string{root, masked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly, DenylistAdd: []string{masked}}, root)
	s := newSandboxedShellSession(t, env)
	marker := filepath.Join(base, "marker")

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"touch %s","cwd":%q}`, marker, masked))
	if !res.IsError {
		t.Fatalf("masked cwd was accepted: %s", res.Output)
	}
	if !strings.Contains(res.Output, masked) {
		t.Fatalf("masked-cwd refusal = %q, want it to name %q", res.Output, masked)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("masked cwd ran a command: %v", err)
	}

	link := filepath.Join(root, "link-to-masked")
	if err := os.Symlink(masked, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	res = executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"touch %s","cwd":%q}`, marker, link))
	if !res.IsError {
		t.Fatalf("symlinked masked cwd was accepted: %s", res.Output)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlinked masked cwd ran a command: %v", err)
	}
}

// TestShellToolReadOnlyCwdUnderHostTmpRefused is the no-widening control: a
// host /tmp directory is hidden from the shell by bubblewrap's private tmpfs,
// so naming it as cwd must not re-bind it into the sandbox.
func TestShellToolReadOnlyCwdUnderHostTmpRefused(t *testing.T) {
	t.Parallel()
	root, _ := issue4332Fixture(t, "/var/tmp")
	hostTmp, err := os.MkdirTemp("/tmp", "evener-4332-host-tmp-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(hostTmp) })
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)
	marker := filepath.Join(hostTmp, "marker")

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"touch %s","cwd":%q}`, marker, hostTmp))
	if !res.IsError {
		t.Fatalf("host /tmp cwd was accepted: %s", res.Output)
	}
	if !strings.Contains(res.Output, hostTmp) {
		t.Fatalf("private-tmp refusal = %q, want it to name %q", res.Output, hostTmp)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host /tmp cwd ran a command: %v", err)
	}
}

// TestShellToolRestrictedCwdOutsideReadRootsRefused keeps the narrower
// restricted spawned read set: a directory outside it is still refused.
func TestShellToolRestrictedCwdOutsideReadRootsRefused(t *testing.T) {
	t.Parallel()
	root, outside := issue4332Fixture(t, "/var/tmp")
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, root)
	s := newSandboxedShellSession(t, env)
	marker := filepath.Join(outside, "marker")

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"touch %s","cwd":%q}`, marker, outside))
	if !res.IsError {
		t.Fatalf("restricted cwd outside the read roots was accepted: %s", res.Output)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restricted outside cwd ran a command: %v", err)
	}
}

// TestShellToolReadOnlyCwdSessionScratchAccepted covers the one environment
// grant that is not policy state: the scratch the wrapper already provisioned.
func TestShellToolReadOnlyCwdSessionScratchAccepted(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("pwd is POSIX-only")
	}
	root, _ := issue4332Fixture(t, "/var/tmp")
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	scratch := env.SessionScratchDir()
	if scratch == "" {
		t.Fatal("enabled sandbox has no session scratch")
	}
	s := newSandboxedShellSession(t, env)
	want, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, scratch))
	out := decodeSandboxedShellOutput(t, res)
	if got := strings.TrimSpace(*out.Output); got != want {
		t.Fatalf("shell pwd with session scratch = %q, want %q", got, want)
	}
}

// TestShellToolReadOnlyCwdInsideTmpRoot pins the inside-root path when the
// workspace itself lives under host /tmp: the wrapper re-binds the root, so a
// subdirectory is accepted, while a sibling outside the root stays private.
func TestShellToolReadOnlyCwdInsideTmpRoot(t *testing.T) {
	t.Parallel()
	base, err := os.MkdirTemp("/tmp", "evener-4332-tmp-root-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "root")
	sub := filepath.Join(root, "sub")
	for _, dir := range []string{root, sub} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)
	want, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, sub))
	out := decodeSandboxedShellOutput(t, res)
	if got := strings.TrimSpace(*out.Output); got != want {
		t.Fatalf("shell pwd inside a /tmp root = %q, want %q", got, want)
	}

	sibling := filepath.Join(base, "sibling")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	res = executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, sibling))
	if !res.IsError {
		t.Fatalf("sibling of a /tmp root was accepted: %s", res.Output)
	}
}
