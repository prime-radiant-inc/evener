package agent

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// ---- session_attention.go nil guards ----

// TestCovArmDelegateAttention_NilSession covers armDelegateAttention nil guard
// (session_attention.go lines 420-423).
func TestCovArmDelegateAttention_NilSession(t *testing.T) {
	var s *Session
	err := s.armDelegateAttention("")
	if err == nil || err.Error() != "delegate attention wake identity is incomplete" {
		t.Fatalf("nil session empty id: %v", err)
	}
}

// TestCovArmDelegateAttention_EmptyID covers armDelegateAttention empty ID
// (session_attention.go lines 421-423).
func TestCovArmDelegateAttention_EmptyID(t *testing.T) {
	s := &Session{}
	err := s.armDelegateAttention("")
	if err == nil || err.Error() != "delegate attention wake identity is incomplete" {
		t.Fatalf("empty id: %v", err)
	}
}

// TestCovIsRootDelegateAttentionReceiver_NilSession covers
// isRootDelegateAttentionReceiver nil guard (session_attention.go lines 552-559).
func TestCovIsRootDelegateAttentionReceiver_NilSession(t *testing.T) {
	var s *Session
	if s.isRootDelegateAttentionReceiver() {
		t.Fatal("nil session should return false")
	}
}

// TestCovIsRootDelegateAttentionReceiver_NoController covers
// isRootDelegateAttentionReceiver with no controller (session_attention.go line 553).
func TestCovIsRootDelegateAttentionReceiver_NoController(t *testing.T) {
	s := &Session{}
	if s.isRootDelegateAttentionReceiver() {
		t.Fatal("no controller should return false")
	}
}

// TestCovAcceptDelegateAttention_NilSession covers acceptDelegateAttention nil guard
// (session_attention.go lines 527-529).
func TestCovAcceptDelegateAttention_NilSession(t *testing.T) {
	var s *Session
	err := s.acceptDelegateAttention(nil)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil session: %v", err)
	}
}

// TestCovAcceptDelegateAttention_NoController covers acceptDelegateAttention
// with no controller (session_attention.go lines 528-529).
func TestCovAcceptDelegateAttention_NoController(t *testing.T) {
	s := &Session{}
	err := s.acceptDelegateAttention(nil)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("no controller: %v", err)
	}
}

// ---- session_queue.go ----

// TestCovDrainSteering_Empty covers drainSteering on empty queue
// (session_queue.go lines 791-802).
func TestCovDrainSteering_Empty(t *testing.T) {
	s := &Session{}
	got := s.drainSteering()
	if got != nil {
		t.Fatalf("empty drain = %v", got)
	}
}

// TestCovPopSteeringHead_Empty covers popSteeringHead on empty queue
// (session_queue.go lines 810-816).
func TestCovPopSteeringHead_Empty(t *testing.T) {
	s := &Session{}
	message, ok := s.popSteeringHead()
	if ok || !reflect.DeepEqual(message, steeringMessage{}) {
		t.Fatalf("empty pop = (%+v, %v), want zero message and false", message, ok)
	}
}

// TestCovPopQueueHead_Empty covers popQueueHead on empty queue.
func TestCovPopQueueHead_Empty(t *testing.T) {
	s := &Session{}
	entry := s.popQueueHead()
	if !reflect.DeepEqual(entry, queuedInput{}) {
		t.Fatalf("empty queue should return zero entry, got %+v", entry)
	}
}

// TestCovQueuePreview_Empty covers QueuePreview on empty queue.
func TestCovQueuePreview_Empty(t *testing.T) {
	s := &Session{}
	preview := s.QueuePreview()
	if len(preview) != 0 {
		t.Fatalf("empty preview = %v", preview)
	}
}

// TestCovQueueIDs_Empty covers QueueIDs on empty queue.
func TestCovQueueIDs_Empty(t *testing.T) {
	s := &Session{}
	ids := s.QueueIDs()
	if len(ids) != 0 {
		t.Fatalf("empty ids = %v", ids)
	}
}

// TestCovQueueTexts_Empty covers QueueTexts on empty queue.
func TestCovQueueTexts_Empty(t *testing.T) {
	s := &Session{}
	texts := s.QueueTexts()
	if len(texts) != 0 {
		t.Fatalf("empty texts = %v", texts)
	}
}

// ---- delegate_runtime.go nil guards ----

// TestCovRunDelegateQuietWatchdogTick_NilSession covers
// runDelegateQuietWatchdogTick nil guard (delegate_runtime.go lines 118-121).
func TestCovRunDelegateQuietWatchdogTick_NilSession(t *testing.T) {
	var s *Session
	err := s.runDelegateQuietWatchdogTick(delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("nil session error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovRunDelegateQuietWatchdogTick_NoController covers
// runDelegateQuietWatchdogTick with no controller.
func TestCovRunDelegateQuietWatchdogTick_NoController(t *testing.T) {
	s := &Session{}
	err := s.runDelegateQuietWatchdogTick(delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("no controller error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovStartDelegateQuietWatchdog_NilCtx covers startDelegateQuietWatchdog
// with nil context (delegate_runtime.go lines 137-140).
func TestCovStartDelegateQuietWatchdog_NilCtx(t *testing.T) {
	s := &Session{}
	cancel := s.startDelegateQuietWatchdog(nil, delegateLease{})
	if cancel == nil {
		t.Fatal("cancel should not be nil")
	}
	cancel()
}

// TestCovReportActivity_NilController covers ReportActivity nil guard
// (delegate_runtime.go lines 84-87).
func TestCovReportActivity_NilController(t *testing.T) {
	var c *delegateTreeController
	err := c.ReportActivity(delegateLease{}, time.Now())
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil controller: %v", err)
	}
}

// TestCovBeginQuietAttention_NilController covers BeginQuietAttention nil guard
// (delegate_runtime.go lines 176-178).
func TestCovBeginQuietAttention_NilController(t *testing.T) {
	var c *delegateTreeController
	_, err := c.BeginQuietAttention(nil, delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("nil controller error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovBeginQuietAttention_NilReceiver covers BeginQuietAttention nil receiver.
func TestCovBeginQuietAttention_NilReceiver(t *testing.T) {
	c := &delegateTreeController{}
	_, err := c.BeginQuietAttention(nil, delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("nil receiver error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovCompleteQuietAttention_NilController covers CompleteQuietAttention nil guard
// (delegate_runtime.go lines 230-232).
func TestCovCompleteQuietAttention_NilController(t *testing.T) {
	var c *delegateTreeController
	err := c.CompleteQuietAttention(nil, false)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil controller: %v", err)
	}
}

// TestCovCompleteQuietAttention_NilClaim covers CompleteQuietAttention nil claim.
func TestCovCompleteQuietAttention_NilClaim(t *testing.T) {
	c := &delegateTreeController{}
	err := c.CompleteQuietAttention(nil, false)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil claim: %v", err)
	}
}

// ---- session_tools_worktree.go ----

// TestCovProjectIsGitCheckout_NoPath covers projectIsGitCheckout with empty path
// (session_tools_worktree.go lines 701-707).
func TestCovProjectIsGitCheckout_NoPath(t *testing.T) {
	if projectIsGitCheckout(identifier.Project{}) {
		t.Fatal("empty project should return false")
	}
}

// TestCovWorktreeRootForProject_EmptyProject covers worktreeRootForProject
// with empty project (session_tools_worktree.go lines 683-691).
func TestCovWorktreeRootForProject_EmptyProject(t *testing.T) {
	s := &Session{}
	_, err := s.worktreeRootForProject("state", identifier.Project{})
	if err == nil || !strings.Contains(err.Error(), "project identity is empty") {
		t.Fatalf("empty project: %v", err)
	}
}

// ---- jobs_nested.go ----

// TestCovStopNestedOrLocal_NilSession covers stopNestedOrLocal nil guard
// (jobs_nested.go lines 346-349).
func TestCovStopNestedOrLocal_NilSession(t *testing.T) {
	var s *Session
	_, err := s.stopNestedOrLocal("job1")
	if err == nil {
		t.Fatal("nil session should return error")
	}
}

// ---- session_tools_jobs.go ----

// TestCovJobStatusTool_EmptyTarget covers jobStatusTool with empty target
// (session_tools_jobs.go lines 385-387).
func TestCovJobStatusTool_EmptyTarget(t *testing.T) {
	s := &Session{}
	_, err := jobStatusTool(s, map[string]any{}, 0)
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("empty target: %v", err)
	}
}
