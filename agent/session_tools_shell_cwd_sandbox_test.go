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
	return newSandboxedShellEnvWithFacts(t, policy, facts, root)
}

func newSandboxedShellEnvWithFacts(t *testing.T, policy sandbox.SandboxPolicy, facts sandbox.HostFacts, root string) *execenv.LocalExecutionEnvironment {
	t.Helper()
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

// TestShellToolRestrictedCwdSymlinkedReadRoot pins the resolved-root case: a
// symlinked extra read root is bound at its literal spelling, so a cwd under
// that spelling is accepted, while the target's own spelling is not exposed by
// the restricted namespace and stays refused.
func TestShellToolRestrictedCwdSymlinkedReadRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("pwd is POSIX-only")
	}
	base, err := os.MkdirTemp("/var/tmp", "evener-4332-link-")
	if err != nil {
		t.Skipf("no writable fixture base /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "root")
	realDir := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	for _, dir := range []string{root, filepath.Join(realDir, "sub")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, ExtraReadRoots: []string{link}}, root)
	s := newSandboxedShellSession(t, env)

	literal := filepath.Join(link, "sub")
	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, literal))
	out := decodeSandboxedShellOutput(t, res)
	want := literal
	if env.Wrapper.Policy().Backend == sandbox.BackendSeatbelt {
		// Seatbelt leaves the host symlink intact, so the shell reports the
		// physical target rather than the literal bind path.
		if physical, err := filepath.EvalSymlinks(literal); err == nil {
			want = physical
		}
	}
	if got := strings.TrimSpace(*out.Output); got != want {
		t.Fatalf("shell pwd under a symlinked read root = %q, want %q", got, want)
	}

	resolved := filepath.Join(realDir, "sub")
	res = executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, resolved))
	if !res.IsError {
		t.Fatalf("resolved spelling of a symlinked read root was accepted: %s", res.Output)
	}
}

// TestShellToolReadOnlyOutsideCwdKeepsTmpWorkspaceVisible pins that the
// workspace under host /tmp stays readable from an accepted outside cwd. The
// wrapper rebinds the workspace anchor independently of the chosen cwd.
func TestShellToolReadOnlyOutsideCwdKeepsTmpWorkspaceVisible(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap private /tmp is Linux-only")
	}
	base, err := os.MkdirTemp("/tmp", "evener-4332-tmp-workspace-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	workspaceFile := filepath.Join(root, "workspace.txt")
	if err := os.WriteFile(workspaceFile, []byte("workspace-readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside, err := os.MkdirTemp("/var/tmp", "evener-4332-outside-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"cat %s","cwd":%q}`, workspaceFile, outside))
	out := decodeSandboxedShellOutput(t, res)
	if !strings.Contains(*out.Output, "workspace-readable") {
		t.Fatalf("workspace file unreadable from an outside cwd: %q", *out.Output)
	}
}

// TestShellToolWorkspaceWriteCwdSymlinkedWritableRoot pins that a literal cwd
// under a symlinked writable root is accepted and writable, while the target's
// own spelling is not bound by bubblewrap.
func TestShellToolWorkspaceWriteCwdSymlinkedWritableRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap private /tmp is Linux-only")
	}
	root, _ := issue4332Fixture(t, "/var/tmp")
	base, err := os.MkdirTemp("/tmp", "evener-4332-write-link-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	realDir := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, ExtraWritableRoots: []string{link}}, root)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)

	literal := filepath.Join(link, "sub")
	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"touch made && pwd","cwd":%q}`, literal))
	out := decodeSandboxedShellOutput(t, res)
	if got := strings.TrimSpace(*out.Output); got != literal {
		t.Fatalf("shell pwd under a symlinked writable root = %q, want %q", got, literal)
	}
	if _, err := os.Stat(filepath.Join(realDir, "sub", "made")); err != nil {
		t.Fatalf("write under the symlinked writable root: %v", err)
	}

	res = executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, filepath.Join(realDir, "sub")))
	if !res.IsError {
		t.Fatalf("target spelling of a symlinked writable root was accepted: %s", res.Output)
	}
}

// TestShellToolReadOnlyCwdContentCarveOutGitMasked pins the content carve-out
// path: a cwd inside a carve-out under the private /tmp is bound by the
// validated carve-out handler, not early, so .git stays masked at the resolved
// root even when the config directory is a symlink.
func TestShellToolReadOnlyCwdContentCarveOutGitMasked(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap private /tmp is Linux-only")
	}
	base, err := os.MkdirTemp("/tmp", "evener-4332-carve-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	realConfig, err := os.MkdirTemp("/var/tmp", "evener-4332-config-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realConfig) })
	skills := filepath.Join(realConfig, "skills")
	if err := os.MkdirAll(filepath.Join(skills, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, ".git", "config"), []byte("SECRET-CARVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "ok.txt"), []byte("CARVE-OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(home, ".config", "evener")); err != nil {
		t.Skipf("cannot create config symlink: %v", err)
	}
	root, err := os.MkdirTemp("/var/tmp", "evener-4332-carve-root-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	facts := sandbox.RealProber{}.Probe()
	if !facts.BwrapCapable {
		t.Skip("bubblewrap is unavailable")
	}
	facts.Home = home
	facts.EvenerConfigRoot = filepath.Join(home, ".config", "evener")
	facts.EvenerContentRoots = []string{filepath.Join(home, ".config", "evener", "skills")}
	facts.EvenerManagedContentRoots = nil
	facts.EvenerManagedContentClean = false
	env := newSandboxedShellEnvWithFacts(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, facts, root)
	requireBwrapPrivateMounts(t, env)
	if len(env.Sandbox.UnmaskedRoots) == 0 {
		t.Fatalf("fixture did not carve out the skills root: %+v", env.Sandbox.MaskedPaths)
	}
	s := newSandboxedShellSession(t, env)

	literal := filepath.Join(home, ".config", "evener", "skills")
	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"cat ok.txt && (cat .git/config || true)","cwd":%q}`, literal))
	out := decodeSandboxedShellOutput(t, res)
	if strings.Contains(*out.Output, "SECRET-CARVE") {
		t.Fatalf("carve-out .git was readable through cwd: %q", *out.Output)
	}
	if !strings.Contains(*out.Output, "CARVE-OK") {
		t.Fatalf("carve-out cwd did not run the ordinary read: %q", *out.Output)
	}
}

// TestShellToolReadOnlyCwdWorkspaceSymlinkUnderTmp pins the workspace-anchor
// visibility case: a symlink inside a workspace under /tmp points to a readable
// /var/tmp directory, so the symlink spelling is accepted as cwd.
func TestShellToolReadOnlyCwdWorkspaceSymlinkUnderTmp(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap private /tmp is Linux-only")
	}
	base, err := os.MkdirTemp("/tmp", "evener-4332-ws-link-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside, err := os.MkdirTemp("/var/tmp", "evener-4332-ws-target-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, root)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"pwd","cwd":%q}`, link))
	out := decodeSandboxedShellOutput(t, res)
	if got := strings.TrimSpace(*out.Output); got != link {
		t.Fatalf("shell pwd with a workspace symlink = %q, want the literal cwd %q", got, link)
	}
}

// TestShellToolReadOnlySymlinkedRootVisibleFromOutside pins that a session
// rooted at a symlinked /tmp spelling keeps that advertised spelling bound when
// the chosen cwd is elsewhere.
func TestShellToolReadOnlySymlinkedRootVisibleFromOutside(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap private /tmp is Linux-only")
	}
	base, err := os.MkdirTemp("/tmp", "evener-4332-root-link-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	realRoot, err := os.MkdirTemp("/var/tmp", "evener-4332-root-real-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realRoot) })
	workspaceFile := filepath.Join(realRoot, "workspace.txt")
	if err := os.WriteFile(workspaceFile, []byte("root-readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "workspace-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("cannot create root symlink: %v", err)
	}
	outside, err := os.MkdirTemp("/var/tmp", "evener-4332-root-outside-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, linkRoot)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"cat %s","cwd":%q}`, filepath.Join(linkRoot, "workspace.txt"), outside))
	out := decodeSandboxedShellOutput(t, res)
	if !strings.Contains(*out.Output, "root-readable") {
		t.Fatalf("advertised root spelling unreadable from an outside cwd: %q", *out.Output)
	}
}

// TestShellToolReadOnlySymlinkedRootKeepsMasks pins that recreating the
// advertised workspace spelling as a link does not alias a masked child: the
// mask lands at the resolved root and applies through the link.
func TestShellToolReadOnlySymlinkedRootKeepsMasks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap private /tmp is Linux-only")
	}
	base, err := os.MkdirTemp("/tmp", "evener-4332-mask-link-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	realRoot, err := os.MkdirTemp("/var/tmp", "evener-4332-mask-real-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(realRoot) })
	if err := os.WriteFile(filepath.Join(realRoot, "ok.txt"), []byte("ROOT-OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	secretDir := filepath.Join(realRoot, "secret")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "data"), []byte("SECRET-MASK"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "workspace-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("cannot create root symlink: %v", err)
	}
	outside, err := os.MkdirTemp("/var/tmp", "evener-4332-mask-outside-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	env := newSandboxedShellEnv(t, sandbox.SandboxPolicy{
		Mode:        sandbox.ModeReadOnly,
		DenylistAdd: []string{secretDir},
	}, linkRoot)
	requireBwrapPrivateMounts(t, env)
	s := newSandboxedShellSession(t, env)

	res := executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"cat %s","cwd":%q}`, filepath.Join(linkRoot, "ok.txt"), outside))
	out := decodeSandboxedShellOutput(t, res)
	if !strings.Contains(*out.Output, "ROOT-OK") {
		t.Fatalf("advertised root spelling did not run: %q", *out.Output)
	}

	res = executeSandboxedShell(t, s, fmt.Sprintf(`{"command":"cat %s","cwd":%q}`, filepath.Join(linkRoot, "secret", "data"), outside))
	out = decodeSandboxedShellOutput(t, res)
	if strings.Contains(*out.Output, "SECRET-MASK") {
		t.Fatalf("masked child was readable through the workspace alias: %q", *out.Output)
	}
}
