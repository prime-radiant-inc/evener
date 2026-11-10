package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func busyErrorOf(t *testing.T, err error) delegateBusyError {
	t.Helper()
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("error = %v, want target busy", err)
	}
	busy, ok := errors.AsType[delegateBusyError](err)
	if !ok {
		t.Fatalf("target busy error %v carries no reason", err)
	}
	return busy
}

// Every reachable refusal says why, so the model can choose between waiting
// and changing course.
func TestDelegateSendTargetBusyCarriesReason(t *testing.T) {
	t.Parallel()

	t.Run("finalizing carries a retry-after", func(t *testing.T) {
		t.Parallel()
		held := holdDelegateFinalizing(t)
		defer held.release()
		ceiling := 50 * time.Millisecond
		updateSessionTestConfig(held.s, func(cfg *testConfig) { cfg.delegateFinalizationWaitCeiling = &ceiling })
		outcome := (delegateRuntime{owner: held.s}).send(context.Background(), held.delegateID, "ping", 60_000)
		busy := busyErrorOf(t, outcome.result.Err)
		if busy.reason != delegateBusyFinalizing {
			t.Fatalf("reason = %q, want %q", busy.reason, delegateBusyFinalizing)
		}
		if busy.retryAfter <= 0 {
			t.Fatalf("retryAfter = %v, want a positive wait", busy.retryAfter)
		}
	})

	t.Run("concurrent send", func(t *testing.T) {
		t.Parallel()
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerIdle(t, c, "dlg_target", "")
		actor := rootDelegateActor(c.rootSessionID)
		first, err := c.ReserveStart(actor, "dlg_target")
		if err != nil {
			t.Fatalf("first ReserveStart: %v", err)
		}
		defer func() { _ = c.AbortStart(first) }()
		_, err = c.ReserveStart(actor, "dlg_target")
		if busy := busyErrorOf(t, err); busy.reason != delegateBusyConcurrentSend {
			t.Fatalf("reason = %q, want %q", busy.reason, delegateBusyConcurrentSend)
		}
	})

	t.Run("busy child the controller cannot see", func(t *testing.T) {
		t.Parallel()
		held := holdDelegateReleasedByTheController(t)
		defer held.release()
		held.child.mu.Lock()
		held.child.driving = true
		held.child.mu.Unlock()
		defer func() {
			held.child.mu.Lock()
			held.child.driving = false
			held.child.mu.Unlock()
		}()
		outcome := (delegateRuntime{owner: held.s}).send(context.Background(), held.delegateID, "ping", 0)
		if busy := busyErrorOf(t, outcome.result.Err); busy.reason != delegateBusyConcurrentSend {
			t.Fatalf("reason = %q, want %q", busy.reason, delegateBusyConcurrentSend)
		}
	})

	t.Run("stopping", func(t *testing.T) {
		t.Parallel()
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerIdle(t, c, "dlg_target", "")
		c.mu.Lock()
		c.durable["dlg_target"].PendingStopSeq = 1
		c.mu.Unlock()
		_, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_target")
		if busy := busyErrorOf(t, err); busy.reason != delegateBusyStopping {
			t.Fatalf("reason = %q, want %q", busy.reason, delegateBusyStopping)
		}
	})

	t.Run("reclaiming is a delayed retry, not a wait for notification", func(t *testing.T) {
		t.Parallel()
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerIdle(t, c, "dlg_target", "")
		c.mu.Lock()
		c.reclaiming["dlg_target"] = 1
		c.mu.Unlock()
		_, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_target")
		busy := busyErrorOf(t, err)
		if busy.reason != delegateBusyReclaiming {
			t.Fatalf("reason = %q, want %q", busy.reason, delegateBusyReclaiming)
		}
		if busy.guidance() != delegateBusyRetryShortly || busy.retryAfter <= 0 {
			t.Fatalf("guidance = %v retryAfter = %v, want a delayed retry", busy.guidance(), busy.retryAfter)
		}
	})

	t.Run("closing", func(t *testing.T) {
		t.Parallel()
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerIdle(t, c, "dlg_target", "")
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		_, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_target")
		if busy := busyErrorOf(t, err); busy.reason != delegateBusyClosing {
			t.Fatalf("reason = %q, want %q", busy.reason, delegateBusyClosing)
		}
	})
}

func TestWithBusyReasonTypesOnlyUntypedBusy(t *testing.T) {
	t.Parallel()
	if busy := busyErrorOf(t, withBusyReason(errDelegateTargetBusy)); busy.reason != delegateBusyConcurrentSend {
		t.Fatalf("untyped busy reason = %q, want %q", busy.reason, delegateBusyConcurrentSend)
	}
	stopping := busyBecause(delegateBusyStopping)
	if busy := busyErrorOf(t, withBusyReason(stopping)); busy.reason != delegateBusyStopping {
		t.Fatalf("typed busy reason = %q, want it kept", busy.reason)
	}
	other := errors.New("boom")
	if got := withBusyReason(other); got != other {
		t.Fatalf("non-busy error = %v, want it unchanged", got)
	}
}

// Only a finalizing delegate is guaranteed to notify the parent when the
// refusal clears; every other reason is a delayed retry.
func TestDelegateBusyGuidanceMatchesWhatHappensNext(t *testing.T) {
	t.Parallel()
	for _, reason := range []delegateBusyReason{delegateBusyStopping, delegateBusyConcurrentSend, delegateBusyReclaiming, delegateBusyClosing} {
		busy := busyErrorOf(t, busyBecause(reason))
		if busy.guidance() != delegateBusyRetryShortly || busy.retryAfter <= 0 {
			t.Fatalf("%s: guidance = %v retryAfter = %v, want a delayed retry", reason, busy.guidance(), busy.retryAfter)
		}
	}
	finalizing := busyErrorOf(t, finalizationOutwaited())
	if finalizing.guidance() != delegateBusyWaitForNotification {
		t.Fatalf("finalizing guidance = %v, want wait for notification", finalizing.guidance())
	}
}
