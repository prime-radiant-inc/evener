package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// sbxUnconfirmedBwrapFacts is a Linux host whose bwrap user-namespace check has
// not answered and does not answer on recheck.
func sbxUnconfirmedBwrapFacts(home string) sandbox.HostFacts {
	return sandbox.HostFacts{
		OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap",
		BwrapRecheck: sandbox.NewBackendRecheck(func() sandbox.CheckOutcome { return sandbox.CheckInconclusive }),
	}
}

// TestARecheckThatAnswersNoRestoresTheNoBackendDelegateBehavior: once the
// host's recheck answers no, delegates see the host exactly as one that
// answered no at probe time: no sandbox modes on offer, and a derived
// read-only scope degrades to confined file tools.
func TestARecheckThatAnswersNoRestoresTheNoBackendDelegateBehavior(t *testing.T) {
	t.Parallel()
	_, home := sbxLane(t)
	facts := sandbox.HostFacts{
		OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap",
		BwrapRecheck: sandbox.NewBackendRecheck(func() sandbox.CheckOutcome { return sandbox.CheckFailed }),
	}
	parent := sbxDelegateSession(t, facts)
	facts.BwrapRecheck.Outcome(context.Background())

	if schema := parent.delegateSandboxSchemaForEnv(parent.env); schema.Available {
		t.Fatalf("a host whose recheck answered no must offer no sandbox modes: %+v", schema)
	}
	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	if policy == nil || policy.Mode != sandbox.ModeOff || !policy.WriteBlocked {
		t.Fatalf("read-only delegate after a definitive recheck = %+v, want the degraded off/write-blocked scope", policy)
	}
}

// TestRestoreOnAnUnconfirmedHostResumesWithTheSandboxPending: a session
// persisted sandboxed resumes on a host whose bwrap check has not answered,
// with its box enforced but unconfirmed, instead of failing the resume.
func TestRestoreOnAnUnconfirmedHostResumesWithTheSandboxPending(t *testing.T) {
	t.Parallel()
	env, err := restoreWithSandbox(t, "workspace-write", sandbox.FakeProber{Facts: sbxUnconfirmedBwrapFacts(t.TempDir())})
	if err != nil {
		t.Fatalf("an unanswered host check must not fail the resume: %v", err)
	}
	if env.Sandbox == nil || !env.Sandbox.Enforced() || !env.Sandbox.Unconfirmed() {
		t.Fatalf("the resumed sandbox must be enforced and unconfirmed, got %+v", env.Sandbox)
	}
}

// TestUnconfirmedHostKeepsDelegateSandboxesAndNeverDegrades: a host whose bwrap
// check has not answered is not a host without a backend. The delegate tool
// keeps offering its modes, and a derived read-only scope stays a real
// read-only box that waits for confirmation rather than falling back to
// confined file tools with an unconfined shell.
func TestUnconfirmedHostKeepsDelegateSandboxesAndNeverDegrades(t *testing.T) {
	t.Parallel()
	lane, home := sbxLane(t)
	facts := sbxUnconfirmedBwrapFacts(home)
	parent := sbxDelegateSession(t, facts)

	if schema := parent.delegateSandboxSchemaForEnv(parent.env); !schema.Available || len(schema.Modes) == 0 {
		t.Fatalf("the delegate tool must keep offering sandbox modes while the check is inconclusive: %+v", schema)
	}

	policy, err := parent.readOnlyDelegateSandbox()
	if err != nil {
		t.Fatalf("readOnlyDelegateSandbox: %v", err)
	}
	if policy == nil || policy.Mode != sandbox.ModeReadOnly {
		t.Fatalf("read-only delegate on an unconfirmed host = %+v, want a read-only box, never the degraded fallback", policy)
	}
	rp, err := sandbox.Resolve(*policy, facts, lane)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !rp.Unconfirmed() {
		t.Fatal("the delegate's box must wait for the host check before any command runs")
	}
}
