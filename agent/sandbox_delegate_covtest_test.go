package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/sandbox"
)

// TestResolveDelegateSandboxRequest_Inherit covers the inherit path (line
// 96-97): both args unset returns nil, nil.
func TestResolveDelegateSandboxRequest_Inherit(t *testing.T) {
	pol, err := resolveDelegateSandboxRequest("", nil, sandbox.ModeOff, true)
	if err != nil || pol != nil {
		t.Fatalf("inherit: pol=%v err=%v, want nil, nil", pol, err)
	}
}

// TestResolveDelegateSandboxRequest_NetOnlyUnderOffParent covers the error
// when sandbox_net is set without a mode under an off parent (line 100-101).
func TestResolveDelegateSandboxRequest_NetOnlyUnderOffParent(t *testing.T) {
	netFalse := false
	_, err := resolveDelegateSandboxRequest("", &netFalse, sandbox.ModeOff, true)
	if err == nil || !strings.Contains(err.Error(), "sandbox_net requires") {
		t.Fatalf("error = %v, want sandbox_net requires", err)
	}
}

// TestResolveDelegateSandboxRequest_NetOnlyUnderSandboxedParent covers
// the inherit-mode path when sandbox_net is set under a sandboxed parent
// (line 103-104).
func TestResolveDelegateSandboxRequest_NetOnlyUnderSandboxedParent(t *testing.T) {
	netFalse := false
	pol, err := resolveDelegateSandboxRequest("", &netFalse, sandbox.ModeReadOnly, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pol == nil {
		t.Fatal("expected non-nil policy")
	}
}

// TestBuildDelegateSandboxPolicy_InvalidMode covers the parse error path
// (line 133-135).
func TestBuildDelegateSandboxPolicy_InvalidMode(t *testing.T) {
	_, err := buildDelegateSandboxPolicy("bogus", nil, sandbox.ModeOff, true)
	if err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("error = %v, want invalid_request", err)
	}
}

// TestBuildDelegateSandboxPolicy_NotConfining covers the no-escalation floor
// violation (line 142-143).
func TestBuildDelegateSandboxPolicy_NotConfining(t *testing.T) {
	_, err := buildDelegateSandboxPolicy("off", nil, sandbox.ModeReadOnly, true)
	if err == nil || !strings.Contains(err.Error(), "not at least as confining") {
		t.Fatalf("error = %v, want not at least as confining", err)
	}
}

// TestBuildDelegateSandboxPolicy_OffWithNet covers the contradiction error
// for sandbox="off" with sandbox_net set (line 150-151).
func TestBuildDelegateSandboxPolicy_OffWithNet(t *testing.T) {
	netTrue := true
	_, err := buildDelegateSandboxPolicy("off", &netTrue, sandbox.ModeOff, true)
	if err == nil || !strings.Contains(err.Error(), "no effect") {
		t.Fatalf("error = %v, want no effect", err)
	}
}

// TestBuildDelegateSandboxPolicy_OffWithoutNet covers the off-inherit path
// (line 156-157).
func TestBuildDelegateSandboxPolicy_OffWithoutNet(t *testing.T) {
	pol, err := buildDelegateSandboxPolicy("off", nil, sandbox.ModeOff, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pol != nil {
		t.Fatal("expected nil policy for off under off parent")
	}
}

// TestBuildDelegateSandboxPolicy_NetEscalation covers the error when
// sandbox_net=true would grant more network than the parent has (line
// 163-164).
func TestBuildDelegateSandboxPolicy_NetEscalation(t *testing.T) {
	netTrue := true
	_, err := buildDelegateSandboxPolicy("read-only", &netTrue, sandbox.ModeReadOnly, false)
	if err == nil || !strings.Contains(err.Error(), "more network") {
		t.Fatalf("error = %v, want more network", err)
	}
}

// TestBuildDelegateSandboxPolicy_Success covers the happy path with a
// valid policy (line 183).
func TestBuildDelegateSandboxPolicy_Success(t *testing.T) {
	netFalse := false
	pol, err := buildDelegateSandboxPolicy("read-only", &netFalse, sandbox.ModeReadOnly, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pol == nil {
		t.Fatal("expected non-nil policy")
	}
	if pol.Mode != sandbox.ModeReadOnly {
		t.Fatalf("mode = %v, want read_only", pol.Mode)
	}
}

// TestAllowedDelegateModes covers the function (lines 112-119).
func TestAllowedDelegateModes(t *testing.T) {
	got := allowedDelegateModes(sandbox.ModeOff)
	if got == "" {
		t.Fatal("expected non-empty modes for off parent")
	}
	// Under ModeOff, ALL modes should be allowed (off is the least confining).
	if !strings.Contains(got, "off") {
		t.Fatalf("expected 'off' in allowed modes, got %q", got)
	}

	// Under a more confining mode, fewer modes should be allowed.
	got = allowedDelegateModes(sandbox.ModeReadOnly)
	if got == "" {
		t.Fatal("expected non-empty modes for read_only parent")
	}
}

// TestSandboxSnapshotFromInputs_Off covers the off-mode nil return (line
// 237-238).
func TestSandboxSnapshotFromInputs_Off(t *testing.T) {
	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{Mode: sandbox.ModeOff})
	if snap != nil {
		t.Fatal("expected nil for off mode")
	}
}

// TestSandboxSnapshotFromInputs_WithNetwork covers the snapshot with
// network (lines 240-251).
func TestSandboxSnapshotFromInputs_WithNetwork(t *testing.T) {
	net := false
	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{
		Mode:    sandbox.ModeReadOnly,
		Network: &net,
	})
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Mode != "read-only" {
		t.Fatalf("mode = %q, want read_only", snap.Mode)
	}
	if snap.Network == nil || *snap.Network != false {
		t.Fatalf("network = %v, want false", snap.Network)
	}
}

// TestSandboxSnapshotFromInputs_WithoutNetwork covers the snapshot without
// network (lines 240-246, skipping 247-250).
func TestSandboxSnapshotFromInputs_WithoutNetwork(t *testing.T) {
	snap := sandboxSnapshotFromInputs(sandbox.SandboxPolicy{
		Mode: sandbox.ModeReadOnly,
	})
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Network != nil {
		t.Fatal("expected nil network")
	}
}

// TestCloneSandboxSnapshot_Nil covers the nil input (line 260-261).
func TestCloneSandboxSnapshot_Nil(t *testing.T) {
	if got := cloneSandboxSnapshot(nil); got != nil {
		t.Fatal("expected nil for nil input")
	}
}

// TestCloneSandboxSnapshot_WithNetwork covers the clone with network
// (lines 263-274).
func TestCloneSandboxSnapshot_WithNetwork(t *testing.T) {
	net := true
	orig := &delegatestore.SandboxSnapshot{
		Mode:    "read-only",
		Network: &net,
	}
	clone := cloneSandboxSnapshot(orig)
	if clone == nil {
		t.Fatal("expected non-nil clone")
	}
	if clone.Mode != "read-only" {
		t.Fatalf("mode = %q", clone.Mode)
	}
	if clone.Network == nil || !*clone.Network {
		t.Fatal("expected network=true")
	}
	// Verify deep copy — modifying clone should not affect original.
	*clone.Network = false
	if *orig.Network != true {
		t.Fatal("modifying clone affected original")
	}
}

// TestSandboxPolicyFromSnapshot_Nil covers the nil input (line 284-285).
func TestSandboxPolicyFromSnapshot_Nil(t *testing.T) {
	_, ok := sandboxPolicyFromSnapshot(nil)
	if ok {
		t.Fatal("expected false for nil snapshot")
	}
}

// TestSandboxPolicyFromSnapshot_InvalidMode covers the parse error path
// (line 287-289).
func TestSandboxPolicyFromSnapshot_InvalidMode(t *testing.T) {
	_, ok := sandboxPolicyFromSnapshot(&delegatestore.SandboxSnapshot{Mode: "bogus"})
	if ok {
		t.Fatal("expected false for invalid mode")
	}
}

// TestSandboxPolicyFromSnapshot_Success covers the happy path (lines
// 291-302).
func TestSandboxPolicyFromSnapshot_Success(t *testing.T) {
	net := false
	snap := &delegatestore.SandboxSnapshot{
		Mode:    "read-only",
		Network: &net,
	}
	pol, ok := sandboxPolicyFromSnapshot(snap)
	if !ok {
		t.Fatal("expected true")
	}
	if pol.Mode != sandbox.ModeReadOnly {
		t.Fatalf("mode = %v, want read_only", pol.Mode)
	}
	if pol.Network == nil || *pol.Network != false {
		t.Fatalf("network = %v, want false", pol.Network)
	}
}

// TestSandboxPolicyFromSnapshot_SuccessNoNetwork covers the happy path
// without network (line 298-300 skip).
func TestSandboxPolicyFromSnapshot_SuccessNoNetwork(t *testing.T) {
	snap := &delegatestore.SandboxSnapshot{Mode: "read-only"}
	pol, ok := sandboxPolicyFromSnapshot(snap)
	if !ok {
		t.Fatal("expected true")
	}
	if pol.Network != nil {
		t.Fatal("expected nil network")
	}
}

// TestSandboxSnapshotFromEnv_NonLocal covers the non-local env path (line
// 226-227).
func TestSandboxSnapshotFromEnv_NonLocal(t *testing.T) {
	// Use a nil env — should return nil.
	if got := sandboxSnapshotFromEnv(nil); got != nil {
		t.Fatal("expected nil for non-local env")
	}
}

// TestParentSandboxModeNet_NonLocal covers the non-local env path (line
// 78-82).
func TestParentSandboxModeNet_NonLocal(t *testing.T) {
	s := &Session{}
	// currentEnv returns nil for a bare Session — should return ModeOff, true.
	mode, net := s.parentSandboxModeNet()
	if mode != sandbox.ModeOff {
		t.Fatalf("mode = %v, want ModeOff", mode)
	}
	if !net {
		t.Fatal("expected network=true for unsandboxed session")
	}
}

// TestProvisionRestoredSandbox_Off covers the off-mode early return (line
// 197-198).
func TestProvisionRestoredSandbox_Off(t *testing.T) {
	cfg := SessionConfig{Sandbox: "off"}
	if err := provisionRestoredSandbox(cfg, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestProvisionRestoredSandbox_NonLocalWithMode covers the non-local env
// error path (line 200-202).
func TestProvisionRestoredSandbox_NonLocalWithMode(t *testing.T) {
	cfg := SessionConfig{Sandbox: "read-only"}
	err := provisionRestoredSandbox(cfg, nil)
	if err == nil {
		t.Fatal("expected error for non-local env with non-off mode")
	}
	if !strings.Contains(err.Error(), "does not support sandboxing") {
		t.Fatalf("error = %v, want does not support sandboxing", err)
	}
}

// Ensure errors import is used.
var _ = errors.New
