package agent

import (
	"testing"
)

// Each retry rail is a generation-guarded one-shot timer: a reset that lands
// while the timer is armed strands that firing, and a firing that stays current
// doubles the delay for the next arm. These pin both for the rails whose
// owning file has no other fake-clock coverage of them.

func TestRunningTurnNameRetryResetWhileArmedStrandsTheFiring(t *testing.T) {
	t.Parallel()
	h := newStandDownHarness(t)
	s := h.sess

	s.scheduleRunningTurnNameRetry(jobNotificationRetryMaxDelay)
	s.resetRunningTurnNameRetry()
	h.clock.Advance(jobNotificationRetryInitialDelay)
	h.clock.Drain()
	if got := h.wakeCount(); got != 0 {
		t.Fatalf("wakes after the armed firing was reset = %d, want 0", got)
	}

	s.scheduleRunningTurnNameRetry(jobNotificationRetryMaxDelay)
	h.clock.Advance(jobNotificationRetryInitialDelay)
	h.clock.Drain()
	<-h.woken
	s.turnNameRetryMu.Lock()
	active, delay := s.turnNameRetry.active, s.turnNameRetry.delay
	s.turnNameRetryMu.Unlock()
	if active || delay != 2*jobNotificationRetryInitialDelay {
		t.Fatalf("after a current firing: active %v delay %v, want unarmed and doubled", active, delay)
	}
}

func TestRunningTurnNameRetryDoublesOnlyToItsCeiling(t *testing.T) {
	t.Parallel()
	h := newStandDownHarness(t)
	s := h.sess
	ceiling := 3 * jobNotificationRetryInitialDelay

	for range 4 {
		s.scheduleRunningTurnNameRetry(ceiling)
		s.turnNameRetryMu.Lock()
		delay := s.turnNameRetry.delay
		s.turnNameRetryMu.Unlock()
		if delay <= 0 {
			delay = jobNotificationRetryInitialDelay
		}
		h.clock.Advance(delay)
		h.clock.Drain()
		<-h.woken
	}
	s.turnNameRetryMu.Lock()
	delay := s.turnNameRetry.delay
	s.turnNameRetryMu.Unlock()
	if delay != ceiling {
		t.Fatalf("delay after repeated firings = %v, want the ceiling %v", delay, ceiling)
	}
}

func TestDelegateDeliveryRetryResetWhileArmedStrandsTheFiring(t *testing.T) {
	t.Parallel()
	h := newStandDownHarness(t)
	s := h.sess
	clk := h.clock

	s.delegateDeliveryMu.Lock()
	s.pendingDelegateDeliveries = append(s.pendingDelegateDeliveries, delegateDeliveryPlan{})
	s.scheduleDelegateDeliveryRetryLocked()
	s.resetDelegateDeliveryRetryLocked()
	s.delegateDeliveryMu.Unlock()
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	if got := h.wakeCount(); got != 0 {
		t.Fatalf("wakes after the armed firing was reset = %d, want 0", got)
	}

	s.delegateDeliveryMu.Lock()
	s.scheduleDelegateDeliveryRetryLocked()
	s.delegateDeliveryMu.Unlock()
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	<-h.woken
	s.delegateDeliveryMu.Lock()
	active, delay, wake := s.delegateDeliveryRetry.active, s.delegateDeliveryRetry.delay, s.delegateDeliveryWake
	s.delegateDeliveryMu.Unlock()
	if active || !wake || delay != 2*jobNotificationRetryInitialDelay {
		t.Fatalf("after a current firing: active %v wake %v delay %v, want unarmed, woken and doubled", active, wake, delay)
	}
}

func TestDelegateDeliveryRetryWithNothingPendingStartsOver(t *testing.T) {
	t.Parallel()
	h := newStandDownHarness(t)
	s := h.sess

	s.delegateDeliveryMu.Lock()
	s.pendingDelegateDeliveries = append(s.pendingDelegateDeliveries, delegateDeliveryPlan{})
	s.delegateDeliveryRetry.delay = 4 * jobNotificationRetryInitialDelay
	s.scheduleDelegateDeliveryRetryLocked()
	s.pendingDelegateDeliveries = nil
	s.delegateDeliveryMu.Unlock()
	h.clock.Advance(4 * jobNotificationRetryInitialDelay)
	h.clock.Drain()

	s.delegateDeliveryMu.Lock()
	delay := s.delegateDeliveryRetry.delay
	s.delegateDeliveryMu.Unlock()
	if delay != jobNotificationRetryInitialDelay {
		t.Fatalf("delay after the work drained = %v, want the initial delay", delay)
	}
	if got := h.wakeCount(); got != 0 {
		t.Fatalf("wakes with nothing pending = %d, want 0", got)
	}
}
