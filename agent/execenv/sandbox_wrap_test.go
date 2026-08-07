//go:build darwin || linux

package execenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// kernelWrappedEnv resolves mode against bwrap facts anchored at home and attaches a
// live kernel wrapper (rooted at cwd) to a fresh execution environment.
func kernelWrappedEnv(t *testing.T, bwrapPath, home, cwd, sessionTmp string, mode sandbox.Mode, netOn bool) *LocalExecutionEnvironment {
	t.Helper()
	net := netOn
	facts := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: bwrapPath, BwrapCapable: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, bwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env := NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	env.Wrapper = w
	return env
}

// realBwrapPath skips under -short or when the host cannot run bwrap, returning
// the resolved bwrap path for tests that actually spawn confined commands.
func realBwrapPath(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("real-bwrap integration test skipped under -short")
	}
	facts := sandbox.RealProber{}.Probe()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}
	return facts.BwrapPath
}

func TestNoSandboxByteIdentical(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment("/work") // Wrapper nil
	cmd := exec.Command("/bin/echo", "hi")       //nolint:noctx // test seam
	cmd.Path = "/bin/echo"
	before := slices.Clone(cmd.Args)

	env.wrapForSandbox(cmd, "/work")

	if !slices.Equal(cmd.Args, before) {
		t.Errorf("unsandboxed env must not rewrite argv: got %v want %v", cmd.Args, before)
	}
	if cmd.Path != "/bin/echo" {
		t.Errorf("unsandboxed env must not rewrite cmd.Path: got %q", cmd.Path)
	}
}

func TestWrapForSandboxRewritesArgvAndRaisesEnvFloor(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	sessionTmp := t.TempDir()
	env := kernelWrappedEnv(t, "/usr/bin/bwrap", home, cwd, sessionTmp, sandbox.ModeWorkspaceWrite, true)

	cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test seam
	cmd.Env = []string{"SSH_AUTH_SOCK=/run/agent.sock", "PATH=/usr/bin", "AWS_SECRET_ACCESS_KEY=x"}

	env.wrapForSandbox(cmd, cwd)

	if cmd.Args[0] != "/usr/bin/bwrap" || cmd.Path != "/usr/bin/bwrap" {
		t.Errorf("sandboxed spawn must exec bwrap: args[0]=%q path=%q", cmd.Args[0], cmd.Path)
	}
	if !slices.Contains(cmd.Args, "--unshare-pid") {
		t.Errorf("wrapped argv missing confinement flags: %v", cmd.Args)
	}
	// Env floor: ssh-agent + cloud creds dropped, TMPDIR points at the session tmp.
	joined := strings.Join(cmd.Env, "\n")
	if strings.Contains(joined, "SSH_AUTH_SOCK=") {
		t.Errorf("env floor must drop SSH_AUTH_SOCK: %v", cmd.Env)
	}
	if strings.Contains(joined, "AWS_SECRET_ACCESS_KEY=") {
		t.Errorf("env floor must drop AWS_* creds: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "TMPDIR="+sessionTmp) {
		t.Errorf("env floor must point TMPDIR at the session tmp: %v", cmd.Env)
	}
}

func TestNoInheritedFDs(t *testing.T) {
	t.Parallel()
	env := kernelWrappedEnv(t, "/usr/bin/bwrap", t.TempDir(), sandbox.MaterializeWorkspace(t, sandbox.MainCheckout), t.TempDir(), sandbox.ModeWorkspaceWrite, true)

	f, err := os.CreateTemp(t.TempDir(), "leak")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()                  //nolint:errcheck
	cmd := exec.Command("/bin/true") //nolint:noctx // test seam
	cmd.ExtraFiles = []*os.File{f}   // deliberately try to leak a high fd

	env.wrapForSandbox(cmd, env.RootDir)

	if cmd.ExtraFiles != nil {
		t.Errorf("fd hygiene: a sandboxed spawn must inherit no extra fds, got %d", len(cmd.ExtraFiles))
	}
}

func TestExecWrappedConfinesSecret(t *testing.T) {
	bwrapPath := realBwrapPath(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "LEAKED-EXEC-SECRET"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	env := kernelWrappedEnv(t, bwrapPath, home, cwd, t.TempDir(), sandbox.ModeWorkspaceWrite, true)

	res, err := env.ExecCommand(context.Background(), "cat "+filepath.Join(home, ".ssh", "id")+" 2>&1 || true", 15000, cwd, nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v (%s)", err, res.Stderr)
	}
	if strings.Contains(res.Stdout+res.Stderr, secret) {
		t.Errorf("credential leaked through a sandboxed ExecCommand:\n%s\n%s", res.Stdout, res.Stderr)
	}
}

func TestStreamWrappedConfinesSecret(t *testing.T) {
	bwrapPath := realBwrapPath(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "LEAKED-STREAM-SECRET"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := sandbox.MaterializeWorkspace(t, sandbox.MainCheckout)
	env := kernelWrappedEnv(t, bwrapPath, home, cwd, t.TempDir(), sandbox.ModeWorkspaceWrite, true)

	var buf strings.Builder
	h, err := env.StreamCommand(context.Background(), "cat "+filepath.Join(home, ".ssh", "id")+" 2>&1 || true", cwd, nil, &buf)
	if err != nil {
		t.Fatalf("StreamCommand: %v", err)
	}
	_, _ = h.Wait()
	if strings.Contains(buf.String(), secret) {
		t.Errorf("credential leaked through a sandboxed StreamCommand:\n%s", buf.String())
	}
}
