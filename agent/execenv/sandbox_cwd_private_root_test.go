package execenv

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestConfinedCwdPrivateRootAliasCannotBypassMask covers the High finding at
// agent/sandbox/backend.go:250: a read-only session anchored at /tmp must not
// accept a /tmp descendant that resolves outside the workspace. Accepting it
// would bind the alias as an independent cwd, and the canonical masks under its
// target would no longer cover the alias spelling.
func TestConfinedCwdPrivateRootAliasCannotBypassMask(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bwrap is linux-only")
	}
	facts := sandbox.RealProber{}.Probe()
	if !facts.BwrapCapable || facts.BwrapPath == "" {
		t.Skip("bwrap not capable on this host")
	}

	outside, err := os.MkdirTemp("/var/tmp", "evener-4332-mask-")
	if err != nil {
		t.Skipf("no writable /var/tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	secret := filepath.Join(outside, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "data"), []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	aliasBase, err := os.MkdirTemp("/tmp", "evener-4332-alias-")
	if err != nil {
		t.Skipf("no writable /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(aliasBase) })
	alias := filepath.Join(aliasBase, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	net := true
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{
		Mode:        sandbox.ModeReadOnly,
		Network:     &net,
		DenylistAdd: []string{secret},
	}, facts, "/tmp")
	if err != nil {
		t.Fatalf("Resolve(read-only at /tmp): %v", err)
	}
	if rp.WorkspaceRoot != "/tmp" {
		t.Fatalf("WorkspaceRoot = %q, want /tmp for this regression", rp.WorkspaceRoot)
	}
	sessionTmp := filepath.Join(aliasBase, "session")
	if err := os.MkdirAll(sessionTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := sandbox.NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	env := NewLocalExecutionEnvironment("/tmp")
	env.Sandbox = &rp
	env.Wrapper = w
	t.Cleanup(env.Cleanup)

	if err := env.EnsureCommandWorkingDirectory(alias); err == nil {
		t.Fatalf("EnsureCommandWorkingDirectory(%q) accepted an alias outside the /tmp workspace", alias)
	}
	res, err := env.ExecCommand(context.Background(), "cat "+alias+"/secret/data", 5000, alias, nil)
	if err == nil {
		t.Fatalf("ExecCommand accepted the masked alias cwd: %+v", res)
	}
	if strings.Contains(res.Stdout+res.Stderr, "TOPSECRET") {
		t.Fatalf("masked child leaked through the alias:\n%s%s", res.Stdout, res.Stderr)
	}
}
