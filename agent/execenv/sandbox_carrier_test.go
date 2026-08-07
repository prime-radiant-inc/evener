package execenv

import (
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// TestSandboxCarrierOffIsInert covers the "off is a byte-identical no-op" half of
// the carrier contract: a fresh/default env carries a nil Sandbox, and re-rooting
// it fabricates no policy and records no error. (The re-root behavior for a real
// Resolve-produced policy lives in sandbox_reroot_test.go.)
func TestSandboxCarrierOffIsInert(t *testing.T) {
	t.Parallel()

	env := NewLocalExecutionEnvironment("/work")
	if env.Sandbox != nil {
		t.Fatalf("a fresh env must carry a nil Sandbox (off = today's behavior), got %+v", env.Sandbox)
	}
	child := env.WithWorkingDirectory("/work/sub")
	if child.Sandbox != nil {
		t.Errorf("child of a nil-Sandbox env must stay nil, got %+v", child.Sandbox)
	}
	if child.SandboxReRootError() != nil {
		t.Errorf("an off re-root must record no error, got %v", child.SandboxReRootError())
	}
	if child.RootDir != "/work/sub" {
		t.Errorf("child RootDir = %q, want /work/sub", child.RootDir)
	}
}

// TestSandboxCarrierLiteralFailsClosed proves the fail-closed posture: an ENFORCED
// policy that was NOT produced by Resolve (a hand-built literal — the only enforced
// policy that retains no re-root inputs) cannot be re-rooted, so WithWorkingDirectory
// refuses it (Sandbox nil, sticky error) rather than leak the source's roots to the
// child. A Resolve-produced policy never hits this — see sandbox_reroot_test.go.
func TestSandboxCarrierLiteralFailsClosed(t *testing.T) {
	t.Parallel()

	env := NewLocalExecutionEnvironment("/work")
	env.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, Backend: sandbox.BackendBwrap}
	child := env.WithWorkingDirectory("/work/sub")
	if child.Sandbox != nil {
		t.Errorf("re-rooting an inputs-less enforced literal must fail closed (nil Sandbox), got %+v", child.Sandbox)
	}
	if child.SandboxReRootError() == nil {
		t.Error("re-rooting an inputs-less enforced literal must record a sticky SandboxReRootError()")
	}
}
