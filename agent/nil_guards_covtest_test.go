package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// ---- session_attention.go nil guards ----

// TestCovArmDelegateAttention_NilSession covers armDelegateAttention nil guard.
func TestCovArmDelegateAttention_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	err := s.armDelegateAttention("")
	if err == nil || err.Error() != "delegate attention wake identity is incomplete" {
		t.Fatalf("nil session empty id: %v", err)
	}
}

// TestCovArmDelegateAttention_EmptyID covers armDelegateAttention empty ID.
func TestCovArmDelegateAttention_EmptyID(t *testing.T) {
	t.Parallel()
	s := &Session{}
	err := s.armDelegateAttention("")
	if err == nil || err.Error() != "delegate attention wake identity is incomplete" {
		t.Fatalf("empty id: %v", err)
	}
}

// TestCovIsRootDelegateAttentionReceiver_NilSession covers
// isRootDelegateAttentionReceiver nil guard.
func TestCovIsRootDelegateAttentionReceiver_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	if s.isRootDelegateAttentionReceiver() {
		t.Fatal("nil session should return false")
	}
}

// TestCovAcceptDelegateAttention_NilSession covers acceptDelegateAttention nil guard.
func TestCovAcceptDelegateAttention_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	err := s.acceptDelegateAttention(nil)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil session: %v", err)
	}
}

// ---- session_queue.go ----

// TestCovDrainSteering_Empty covers drainSteering on empty queue.
func TestCovDrainSteering_Empty(t *testing.T) {
	t.Parallel()
	s := &Session{}
	got := s.drainSteering()
	if got != nil {
		t.Fatalf("empty drain = %v", got)
	}
}

// TestCovPopSteeringHead_Empty covers popSteeringHead on empty queue.
func TestCovPopSteeringHead_Empty(t *testing.T) {
	t.Parallel()
	s := &Session{}
	message, ok := s.popSteeringHead()
	if ok || !reflect.DeepEqual(message, steeringMessage{}) {
		t.Fatalf("empty pop = (%+v, %v), want zero message and false", message, ok)
	}
}

// TestCovPopQueueHead_Empty covers popQueueHead on empty queue.
func TestCovPopQueueHead_Empty(t *testing.T) {
	t.Parallel()
	s := &Session{}
	entry := s.popQueueHead()
	if !reflect.DeepEqual(entry, queuedInput{}) {
		t.Fatalf("empty queue should return zero entry, got %+v", entry)
	}
}

// TestCovQueuePreview_Empty covers QueuePreview on empty queue.
func TestCovQueuePreview_Empty(t *testing.T) {
	t.Parallel()
	s := &Session{}
	preview := s.QueuePreview()
	if len(preview) != 0 {
		t.Fatalf("empty preview = %v", preview)
	}
}

// TestCovQueueIDs_Empty covers QueueIDs on empty queue.
func TestCovQueueIDs_Empty(t *testing.T) {
	t.Parallel()
	s := &Session{}
	ids := s.QueueIDs()
	if len(ids) != 0 {
		t.Fatalf("empty ids = %v", ids)
	}
}

// TestCovQueueTexts_Empty covers QueueTexts on empty queue.
func TestCovQueueTexts_Empty(t *testing.T) {
	t.Parallel()
	s := &Session{}
	texts := s.QueueTexts()
	if len(texts) != 0 {
		t.Fatalf("empty texts = %v", texts)
	}
}

// ---- delegate_runtime.go nil guards ----

// TestCovRunDelegateQuietWatchdogTick_NilSession covers
// runDelegateQuietWatchdogTick nil guard.
func TestCovRunDelegateQuietWatchdogTick_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	err := s.runDelegateQuietWatchdogTick(delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("nil session error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovRunDelegateQuietWatchdogTick_NoController covers
// runDelegateQuietWatchdogTick with no controller.
func TestCovRunDelegateQuietWatchdogTick_NoController(t *testing.T) {
	t.Parallel()
	s := &Session{}
	err := s.runDelegateQuietWatchdogTick(delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("no controller error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovStartDelegateQuietWatchdog_NilCtx covers startDelegateQuietWatchdog
// with nil context.
func TestCovStartDelegateQuietWatchdog_NilCtx(t *testing.T) {
	t.Parallel()
	s := &Session{}
	var ctx context.Context
	cancel := s.startDelegateQuietWatchdog(ctx, delegateLease{})
	if cancel == nil {
		t.Fatal("cancel should not be nil")
	}
	cancel()
}

// TestCovReportActivity_NilController covers ReportActivity nil guard.
func TestCovReportActivity_NilController(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	err := c.ReportActivity(delegateLease{}, time.Now())
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil controller: %v", err)
	}
}

// TestCovBeginQuietAttention_NilController covers BeginQuietAttention nil guard.
func TestCovBeginQuietAttention_NilController(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	_, err := c.BeginQuietAttention(nil, delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("nil controller error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovBeginQuietAttention_NilReceiver covers BeginQuietAttention nil receiver.
func TestCovBeginQuietAttention_NilReceiver(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	_, err := c.BeginQuietAttention(nil, delegateLease{}, time.Now())
	if !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("nil receiver error = %v, want %v", err, errDelegateDeliveryReceiverUnavailable)
	}
}

// TestCovCompleteQuietAttention_NilController covers CompleteQuietAttention nil guard.
func TestCovCompleteQuietAttention_NilController(t *testing.T) {
	t.Parallel()
	var c *delegateTreeController
	err := c.CompleteQuietAttention(nil, false)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil controller: %v", err)
	}
}

// TestCovCompleteQuietAttention_NilClaim covers CompleteQuietAttention nil claim.
func TestCovCompleteQuietAttention_NilClaim(t *testing.T) {
	t.Parallel()
	c := &delegateTreeController{}
	err := c.CompleteQuietAttention(nil, false)
	if err == nil || !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("nil claim: %v", err)
	}
}

// ---- session_tools_worktree.go ----

// TestCovProjectIsGitCheckout_NoPath covers projectIsGitCheckout with empty path.
func TestCovProjectIsGitCheckout_NoPath(t *testing.T) {
	t.Parallel()
	if projectIsGitCheckout(identifier.Project{}) {
		t.Fatal("empty project should return false")
	}
}

// TestCovWorktreeRootForProject_EmptyProject covers worktreeRootForProject
// with empty project.
func TestCovWorktreeRootForProject_EmptyProject(t *testing.T) {
	t.Parallel()
	s := &Session{}
	_, err := s.worktreeRootForProject("state", identifier.Project{})
	if err == nil || !strings.Contains(err.Error(), "project identity is empty") {
		t.Fatalf("empty project: %v", err)
	}
}

// ---- jobs_nested.go ----

// TestCovStopNestedOrLocal_NilSession covers stopNestedOrLocal nil guard.
func TestCovStopNestedOrLocal_NilSession(t *testing.T) {
	t.Parallel()
	var s *Session
	_, err := s.stopNestedOrLocal("job1")
	if err == nil {
		t.Fatal("nil session should return error")
	}
}

// ---- session_tools_jobs.go ----

// TestCovJobStatusTool_EmptyTarget covers jobStatusTool with empty target.
func TestCovJobStatusTool_EmptyTarget(t *testing.T) {
	t.Parallel()
	s := &Session{}
	_, err := jobStatusTool(s, map[string]any{}, 0)
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("empty target: %v", err)
	}
}
