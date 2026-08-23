package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestValidGrantRange covers the validGrantRange function (lines 150-154).
func TestValidGrantRange(t *testing.T) {
	if got := validGrantRange(0); got != "0" {
		t.Fatalf("validGrantRange(0) = %q, want '0'", got)
	}
	if got := validGrantRange(1); got != "0" {
		t.Fatalf("validGrantRange(1) = %q, want '0'", got)
	}
	if got := validGrantRange(5); got != "0..4" {
		t.Fatalf("validGrantRange(5) = %q, want '0..4'", got)
	}
}

// TestValidateDelegateGrant covers the validateDelegateGrant function
// (lines 157-158).
func TestValidateDelegateGrant(t *testing.T) {
	// requested < own: valid.
	ok, rangeStr := validateDelegateGrant(2, 5)
	if !ok || rangeStr != "0..4" {
		t.Fatalf("validateDelegateGrant(2, 5) = (%v, %q), want (true, '0..4')", ok, rangeStr)
	}
	// requested >= own: invalid.
	ok, rangeStr = validateDelegateGrant(5, 5)
	if ok || rangeStr != "0..4" {
		t.Fatalf("validateDelegateGrant(5, 5) = (%v, %q), want (false, '0..4')", ok, rangeStr)
	}
}

// TestDelegateStartFailed covers the delegateStartFailed constructor
// (lines 161-167).
func TestDelegateStartFailed(t *testing.T) {
	err := errors.New("boom")
	result := delegateStartFailed(err)
	if result.Type != delegateResourceType || result.Status != jobstore.StatusFailed || result.Reason != "start_failed" || result.Err != err {
		t.Fatalf("delegateStartFailed = %+v", result)
	}
}

// TestSendMessageFailed covers the sendMessageFailed constructor
// (lines 170-175).
func TestSendMessageFailed(t *testing.T) {
	err := errors.New("send failed")
	result := sendMessageFailed("caller", err)
	if result.Target != "caller" || result.Err != err {
		t.Fatalf("sendMessageFailed = %+v", result)
	}
}

// TestSandboxHostFacts_NilSession covers the nil-session path in
// sandboxHostFacts (lines 137-138).
func TestSandboxHostFacts_NilSession(t *testing.T) {
	var s *Session
	facts := s.sandboxHostFacts()
	// Should return real prober facts, not panic.
	_ = facts // just verify no panic
}

// TestDelegateWorktreeReport_NilSession covers the nil-session path in
// delegateWorktreeReport (line 186).
func TestDelegateWorktreeReport_NilSession(t *testing.T) {
	var s *Session
	if got := s.delegateWorktreeReport("worktree", "/some/path"); got != nil {
		t.Fatalf("expected nil for nil session, got %+v", got)
	}
}

// TestDelegateWorktreeReport_NonWorktreeIsolation covers the non-worktree
// isolation path (line 186-187).
func TestDelegateWorktreeReport_NonWorktreeIsolation(t *testing.T) {
	s := &Session{}
	if got := s.delegateWorktreeReport("none", "/some/path"); got != nil {
		t.Fatalf("expected nil for non-worktree isolation, got %+v", got)
	}
}

// TestDelegateWorktreeReport_EmptyWorkDir covers the empty working-directory
// path (line 189-191).
func TestDelegateWorktreeReport_EmptyWorkDir(t *testing.T) {
	s := &Session{}
	if got := s.delegateWorktreeReport("worktree", ""); got != nil {
		t.Fatalf("expected nil for empty workdir, got %+v", got)
	}
	if got := s.delegateWorktreeReport("worktree", "  "); got != nil {
		t.Fatalf("expected nil for whitespace workdir, got %+v", got)
	}
}

// TestStableDelegateDisposalHint_NilSession covers the nil-session path
// (line 241).
func TestStableDelegateDisposalHint_NilSession(t *testing.T) {
	var s *Session
	desc := delegatestore.Descriptor{}
	if got := s.stableDelegateDisposalHint(desc, "dlg_123"); got != "" {
		t.Fatalf("expected empty for nil session, got %q", got)
	}
}

// TestStableDelegateDisposalHint_OwnerMismatch covers the owner mismatch
// path (line 241).
func TestStableDelegateDisposalHint_OwnerMismatch(t *testing.T) {
	s := &Session{id: "sess123"}
	desc := delegatestore.Descriptor{OwnerSessionID: "other"}
	if got := s.stableDelegateDisposalHint(desc, "dlg_123"); got != "" {
		t.Fatalf("expected empty for owner mismatch, got %q", got)
	}
}

// TestStableDelegateDisposalHint_NonDelegateID covers the non-delegate-ID
// path (line 241).
func TestStableDelegateDisposalHint_NonDelegateID(t *testing.T) {
	s := &Session{id: "sess123"}
	desc := delegatestore.Descriptor{OwnerSessionID: "sess123"}
	if got := s.stableDelegateDisposalHint(desc, "not-a-delegate-id"); got != "" {
		t.Fatalf("expected empty for non-delegate ID, got %q", got)
	}
}

// TestUseDelegateWorktreeControlPolicy_CustomPolicy covers the custom policy
// path (lines 248-249).
func TestUseDelegateWorktreeControlPolicy_CustomPolicy(t *testing.T) {
	orig := delegateWorktreeControlPolicy
	called := false
	delegateWorktreeControlPolicy = func(env *execenv.LocalExecutionEnvironment, mainRoot string) error {
		called = true
		return errors.New("custom policy error")
	}
	defer func() { delegateWorktreeControlPolicy = orig }()

	s := &Session{}
	err := s.useDelegateWorktreeControlPolicy(nil, "/some/root")
	if !called {
		t.Fatal("expected custom policy to be called")
	}
	if err == nil || !strings.Contains(err.Error(), "custom policy error") {
		t.Fatalf("error = %v, want custom policy error", err)
	}
}
