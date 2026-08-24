package agent

import (
	"testing"
)

// TestResultToolName_Default covers the default name (line 233-234).
func TestResultToolName_Default(t *testing.T) {
	s := &Session{}
	if got := s.resultToolName(); got != "communicate" {
		t.Fatalf("resultToolName = %q, want 'communicate'", got)
	}
}

// TestResultToolName_Custom covers the custom name (line 231-232).
func TestResultToolName_Custom(t *testing.T) {
	s := &Session{cfg: SessionConfig{ResultToolName: "custom_result"}}
	if got := s.resultToolName(); got != "custom_result" {
		t.Fatalf("resultToolName = %q, want 'custom_result'", got)
	}
}

// TestQueueDelegateDeliveryCommit_NilSession covers the nil-session guard
// (line 852-853).
func TestQueueDelegateDeliveryCommit_NilSession(t *testing.T) {
	var s *Session
	s.queueDelegateDeliveryCommit("call1", nil) // should not panic
}

// TestQueueDelegateDeliveryCommit_EmptyCallID covers the empty-callID guard
// (line 852).
func TestQueueDelegateDeliveryCommit_EmptyCallID(t *testing.T) {
	s := &Session{}
	s.queueDelegateDeliveryCommit("", nil) // should not panic
}

// TestQueueDelegateDeliveryCommit_NilCommit covers the nil-commit guard
// (line 852).
func TestQueueDelegateDeliveryCommit_NilCommit(t *testing.T) {
	s := &Session{}
	s.queueDelegateDeliveryCommit("call1", nil) // should not panic
}

// TestAbortDelegateDeliveryCommits_NilSession covers the nil-session guard
// (line 872-873).
func TestAbortDelegateDeliveryCommits_NilSession(t *testing.T) {
	var s *Session
	s.abortDelegateDeliveryCommits() // should not panic
}

// TestAbortDelegateDeliveryCommits_Empty covers the empty case (no commits).
func TestAbortDelegateDeliveryCommits_Empty(t *testing.T) {
	s := &Session{}
	s.abortDelegateDeliveryCommits() // should not panic
}
