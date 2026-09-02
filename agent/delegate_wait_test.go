package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// A waiter registered on a delegate's running generation resolves with that
// generation's terminal packet and a delivery commit, exactly like the inline
// waiter delegate_send registers at start.
func TestDelegateWait_RegisterForRunningResolvesOnFinish(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_a", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_a", false)
	waiter, err := c.RegisterInlineWaiterForRunning(rootDelegateActor("root-session"), "dlg_a")
	if err != nil {
		t.Fatalf("RegisterInlineWaiterForRunning: %v", err)
	}
	plans := finishDelegateDeliveryGeneration(t, c, lease, "done")
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != waiter {
		t.Fatalf("finish plans = %#v, want one delivery carrying the registered waiter", plans.deliveries)
	}
	if _, err := deliverDelegatePacket(plans.deliveries[0], nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	// The delivery above already resolved the waiter; no deadline is needed.
	resolution := c.waitForDelegateInline(context.Background(), waiter)
	if resolution.fallback || resolution.packet == nil || resolution.commit == nil {
		t.Fatalf("resolution = %#v, want packet and commit", resolution)
	}
	var message string
	if err := json.Unmarshal(resolution.packet.Message, &message); err != nil || message != "done" {
		t.Fatalf("packet message = %s (%v), want done", resolution.packet.Message, err)
	}
}

func TestDelegateWait_RegisterForRunningRejectsIdleAndDoubleRegistration(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_a", "")
	if _, err := c.RegisterInlineWaiterForRunning(rootDelegateActor("root-session"), "dlg_a"); err == nil {
		t.Fatal("registered a waiter on an idle delegate")
	}
	startDelegateDeliveryGeneration(t, c, "dlg_a", false)
	if _, err := c.RegisterInlineWaiterForRunning(rootDelegateActor("root-session"), "dlg_a"); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := c.RegisterInlineWaiterForRunning(rootDelegateActor("root-session"), "dlg_a"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("second registration err = %v, want busy", err)
	}
}

// Timing out withdraws the waiter so the eventual delivery goes the normal
// notification route instead of resolving a waiter nobody reads.
func TestDelegateWait_TimeoutWithdrawsWaiter(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_a", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_a", false)
	waiter, err := c.RegisterInlineWaiterForRunning(rootDelegateActor("root-session"), "dlg_a")
	if err != nil {
		t.Fatal(err)
	}
	// An already-cancelled context stands in for the budget running out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if resolution := c.waitForDelegateInline(ctx, waiter); !resolution.fallback {
		t.Fatalf("resolution before finish = %#v, want fallback", resolution)
	}
	plans := finishDelegateDeliveryGeneration(t, c, lease, "late")
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != nil {
		t.Fatalf("post-timeout finish plans = %#v, want a waiter-less delivery", plans.deliveries)
	}
}

// delegate_wait on a live session: a spawned child that completes is returned
// as completed with its report and its delivery is committed through the tool
// round; an unknown target is refused in place; bad arguments are rejected.
func TestDelegateWaitTool_ReturnsCompletedChildReport(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	result := root.createDelegate(context.Background(), delegateArgs{Task: "report READY"})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	ctx := context.WithValue(context.Background(), ctxToolCallID, "call_wait_1")
	raw, err := stableDelegateWaitTool(ctx, root, map[string]any{"targets": []any{result.DelegateID, "dlg_missing"}, "max_wait_ms": 10000}, 8192)
	if err != nil {
		t.Fatalf("delegate_wait: %v", err)
	}
	var out delegateWaitResult
	if err := json.Unmarshal([]byte(raw.(string)), &out); err != nil {
		t.Fatalf("decode: %v (%v)", err, raw)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %+v, want two entries", out.Results)
	}
	got := out.Results[0]
	if got.DelegateID != result.DelegateID || got.Action != "completed" || got.RunningInBackground || got.TimedOut {
		t.Fatalf("completed entry = %+v", got)
	}
	if got.Output == nil || *got.Output == "" {
		t.Errorf("completed entry carries no report output: %+v", got)
	}
	if out.Results[1].Action != "refused" || out.Results[1].DelegateID != "dlg_missing" {
		t.Errorf("unknown target entry = %+v, want refused", out.Results[1])
	}
	root.delegateDeliveryMu.Lock()
	commits := len(root.delegateDeliveryCommits["call_wait_1"])
	root.delegateDeliveryMu.Unlock()
	if commits != 1 {
		t.Errorf("delivery commits queued for the tool call = %d, want 1", commits)
	}
}

func TestDelegateWaitTool_RejectsBadArguments(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	ctx := context.WithValue(context.Background(), ctxToolCallID, "call_wait_2")
	for _, args := range []map[string]any{{}, {"max_wait_ms": 0}, {"max_wait_ms": -5}, {"max_wait_ms": 100, "targets": "dlg_x"}, {"max_wait_ms": 100, "targets": []any{"job_123"}}} {
		if _, err := stableDelegateWaitTool(ctx, root, args, 8192); err == nil || !strings.Contains(err.Error(), "invalid_request") {
			t.Errorf("args %v: err = %v, want invalid_request", args, err)
		}
	}
}

// delegate_wait budgets are bounded for delegates, which run for minutes, not
// by the shell-job inline window of one minute.
func TestDelegateWait_ClampIsDelegateScale(t *testing.T) {
	if got := clampDelegateWaitTimeout(600000); got != 10*time.Minute {
		t.Errorf("clamp(600000) = %v, want 10m", got)
	}
	if got := clampDelegateWaitTimeout(1); got != time.Second {
		t.Errorf("clamp(1) = %v, want the 1s floor", got)
	}
	if got := clampDelegateWaitTimeout(1<<31 - 1); got != time.Duration(maxDelegateWaitMS)*time.Millisecond {
		t.Errorf("clamp(huge) = %v, want the delegate ceiling", got)
	}
	if maxDelegateWaitMS <= maxJobBlockTimeoutMS {
		t.Errorf("delegate ceiling %d must exceed the shell-job inline window %d", maxDelegateWaitMS, maxJobBlockTimeoutMS)
	}
}
