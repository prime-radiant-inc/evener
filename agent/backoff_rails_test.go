package agent

import (
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm"
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

// Seed a real producer journal without reserving its transport claim.
// Only the current retry may attempt delivery or change receiver custody.
func seedRetryRailDelivery(t *testing.T, s *Session) string {
	t.Helper()
	c := s.delegateController
	const id = "dlg_retry_rail"
	created := delegateControllerCreatedEvent(id, "")
	created.Created.Descriptor.OwnerSessionID = s.ID()
	c.mu.Lock()
	_, err := c.appendLocked(created)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	seedDelegateControllerDelivery(t, c, id)
	ids := pendingDeliveryIDs(c, id)
	if len(ids) != 1 {
		t.Fatalf("journal pending IDs=%v, want one", ids)
	}
	return ids[0]
}

func TestDelegateDeliveryRetryResetWhileArmedStrandsTheFiring(t *testing.T) {
	t.Parallel()
	h := newStandDownHarness(t)
	s := h.sess
	clk := h.clock
	id := seedRetryRailDelivery(t, s)
	var attempts atomic.Int32
	s.cfg.testOnly.delegateDeliveryClassified = func(*Session, bool) { attempts.Add(1) }

	s.delegateDeliveryMu.Lock()
	s.scheduleDelegateDeliveryRetryLocked()
	s.resetDelegateDeliveryRetryLocked()
	s.delegateDeliveryMu.Unlock()
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	if got := h.wakeCount(); got != 0 {
		t.Fatalf("wakes after the armed firing was reset = %d, want 0", got)
	}
	if got := attempts.Load(); got != 0 || !s.hasPendingDelegateDeliveries() {
		t.Fatalf("reset firing touched source: attempts=%d pending=%t", got, s.hasPendingDelegateDeliveries())
	}
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if err != nil || len(fold.content) != 0 {
		t.Fatalf("reset firing created receiver custody: content=%v err=%v", fold.content, err)
	}

	var readFailures atomic.Int32
	s.cfg.testOnly.delegateAttentionReadFold = func(string, string) (delegateAttentionFold, error) {
		readFailures.Add(1)
		return delegateAttentionFold{}, errInjectedTranscriptWrite
	}
	s.delegateDeliveryMu.Lock()
	s.scheduleDelegateDeliveryRetryLocked()
	s.delegateDeliveryMu.Unlock()
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	s.delegateDeliveryMu.Lock()
	active, delay := s.delegateDeliveryRetry.active, s.delegateDeliveryRetry.delay
	s.delegateDeliveryMu.Unlock()
	// The callback settles unarmed, then actual failed source replay schedules
	// its successor. That successor, not the retired wake projection, owns debt.
	if !active || delay != 2*jobNotificationRetryInitialDelay || attempts.Load() != 1 || readFailures.Load() != 1 || !s.hasPendingDelegateDeliveries() || h.wakeCount() != 0 {
		t.Fatalf("current failed replay: active=%t delay=%v attempts=%d read failures=%d pending=%t wakes=%d", active, delay, attempts.Load(), readFailures.Load(), s.hasPendingDelegateDeliveries(), h.wakeCount())
	}
	if got := h.warningsMatching("delegate delivery retry failed"); got != 1 {
		h.mu.Lock()
		messages := append([]string(nil), h.warnings...)
		h.mu.Unlock()
		t.Fatalf("expected one captured receiver-failure diagnostic, got %d, captured %v, read failures %d", got, messages, readFailures.Load())
	}
	t.Logf("captured expected receiver failure: %s", <-h.warned)

	// Removing the receiver read fault lets the doubled successor transfer custody
	// and acknowledge the source, exactly once, without a model request.
	s.cfg.testOnly.delegateAttentionReadFold = nil
	clk.Advance(delay)
	clk.Drain()
	s.delegateDeliveryMu.Lock()
	active, delay = s.delegateDeliveryRetry.active, s.delegateDeliveryRetry.delay
	s.delegateDeliveryMu.Unlock()
	s.attentionMu.Lock()
	fold, err = s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if active || delay != jobNotificationRetryInitialDelay || attempts.Load() != 2 || s.hasPendingDelegateDeliveries() || h.wakeCount() != 1 || err != nil || fold.content[delegateAttentionID(id)].Role != llm.RoleUser {
		t.Fatalf("settled retry: active=%t delay=%v attempts=%d pending=%t wakes=%d custody=%v err=%v", active, delay, attempts.Load(), s.hasPendingDelegateDeliveries(), h.wakeCount(), fold.content, err)
	}
}

func TestDelegateDeliveryRetryWithNothingPendingStartsOver(t *testing.T) {
	t.Parallel()
	h := newStandDownHarness(t)
	s := h.sess
	id := seedRetryRailDelivery(t, s)
	if err := s.replayDelegateDeliveries(); err != nil {
		t.Fatal(err)
	}
	if s.hasPendingDelegateDeliveries() {
		t.Fatal("actual replay did not drain the producer journal")
	}
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if err != nil || fold.content[delegateAttentionID(id)].Role != llm.RoleUser {
		t.Fatalf("drained source lacks receiver custody: %v %v", fold.content, err)
	}
	wakesBefore := h.wakeCount()
	s.delegateDeliveryMu.Lock()
	s.delegateDeliveryRetry.delay = 4 * jobNotificationRetryInitialDelay
	s.scheduleDelegateDeliveryRetryLocked()
	s.delegateDeliveryMu.Unlock()
	h.clock.Advance(4 * jobNotificationRetryInitialDelay)
	h.clock.Drain()

	s.delegateDeliveryMu.Lock()
	delay := s.delegateDeliveryRetry.delay
	s.delegateDeliveryMu.Unlock()
	if delay != jobNotificationRetryInitialDelay {
		t.Fatalf("delay after the work drained = %v, want the initial delay", delay)
	}
	if got := h.wakeCount() - wakesBefore; got != 0 {
		t.Fatalf("wakes with nothing pending = %d, want 0", got)
	}
}
