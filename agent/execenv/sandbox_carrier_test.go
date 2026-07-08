package execenv

import (
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// TestSandboxCarrierInertAndInherited covers the M1 inert-carrier contract on
// LocalExecutionEnvironment:
//
//   - a fresh/default env carries a nil Sandbox — off, exactly today's behavior
//     (the whole "off is a byte-identical no-op" guarantee rests on this);
//   - the field rides WithWorkingDirectory to a re-rooted child, like EnvPolicy,
//     which is the seam M4 uses to scope a subagent worktree's policy.
//
// Nothing here (or anywhere in M1) READS the field — it is carried, not consulted.
func TestSandboxCarrierInertAndInherited(t *testing.T) {
	t.Parallel()

	env := NewLocalExecutionEnvironment("/work")
	if env.Sandbox != nil {
		t.Fatalf("a fresh env must carry a nil Sandbox (off = today's behavior), got %+v", env.Sandbox)
	}
	// A child of a nil-Sandbox env stays nil — no policy is fabricated.
	if child := env.WithWorkingDirectory("/work/sub"); child.Sandbox != nil {
		t.Errorf("child of a nil-Sandbox env must stay nil, got %+v", child.Sandbox)
	}

	// A test-constructed policy (the only way a non-off policy exists in M1 — the
	// user flag is gated off) is carried by pointer to the re-rooted child.
	pol := &sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, Backend: sandbox.BackendBwrap}
	env.Sandbox = pol
	child := env.WithWorkingDirectory("/work/sub")
	if child.Sandbox != pol {
		t.Errorf("WithWorkingDirectory must carry the Sandbox pointer to the child; got %v want %v", child.Sandbox, pol)
	}
	// The child's RootDir is re-rooted while the policy rides along (M4 re-roots
	// the policy's own paths; M1 only proves the field is inherited).
	if child.RootDir != "/work/sub" {
		t.Errorf("child RootDir = %q, want /work/sub", child.RootDir)
	}
}
