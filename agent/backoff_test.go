package agent

import (
	"testing"
	"time"
)

func TestBackoffDelayDoublesAndSaturatesAtMax(t *testing.T) {
	const initial, limit = 20 * time.Millisecond, 100 * time.Millisecond
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 20 * time.Millisecond},
		{1, 40 * time.Millisecond},
		{2, 80 * time.Millisecond},
		{3, limit},
		{4, limit},
		{1000, limit},
	}
	for _, tc := range cases {
		if got := backoffDelay(tc.attempt, initial, limit); got != tc.want {
			t.Fatalf("backoffDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoffDelayCallersKeepTheirSchedules(t *testing.T) {
	ms := time.Millisecond
	for attempt, want := range []time.Duration{20 * ms, 40 * ms, 50 * ms, 50 * ms} {
		if got := shellFinalizeBackoff(attempt); got != want {
			t.Fatalf("shellFinalizeBackoff(%d) = %v, want %v", attempt, got, want)
		}
	}
	for attempt, want := range []time.Duration{20 * ms, 40 * ms, 80 * ms, 160 * ms, 200 * ms, 200 * ms} {
		if got := durabilityRetryBackoff(attempt); got != want {
			t.Fatalf("durabilityRetryBackoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestBackoffTimerFiringOwnsTheTimerUntilSuperseded(t *testing.T) {
	var b backoffTimer
	delay, gen := b.arm()
	if delay != jobNotificationRetryInitialDelay || !b.active {
		t.Fatalf("first arm = (%v, active %v), want the initial delay armed", delay, b.active)
	}
	if !b.fire(gen) || b.active {
		t.Fatalf("a current firing must own the timer and leave it unarmed")
	}

	_, gen = b.arm()
	b.reset()
	if b.fire(gen) {
		t.Fatalf("a firing armed before a reset must be ignored")
	}
	if b.active || b.delay != jobNotificationRetryInitialDelay {
		t.Fatalf("after reset: active %v delay %v, want unarmed at the initial delay", b.active, b.delay)
	}

	_, stale := b.arm()
	b.disarm()
	_, current := b.arm()
	if b.fire(stale) || !b.fire(current) {
		t.Fatalf("only the newest arm may fire after a disarm and re-arm")
	}
}

func TestBackoffTimerDisarmIfArmedLeavesAnIdleTimerAlone(t *testing.T) {
	var b backoffTimer
	b.disarmIfArmed()
	if b.generation != 0 {
		t.Fatalf("generation = %d, want an idle timer untouched", b.generation)
	}
	b.arm()
	b.disarmIfArmed()
	if b.generation != 2 || b.active {
		t.Fatalf("armed timer: generation %d active %v, want it disarmed", b.generation, b.active)
	}
}

func TestBackoffTimerSettleDoublesToCeilingAndResetsWhenDone(t *testing.T) {
	const ceiling = 5 * time.Second
	var b backoffTimer
	delay := jobNotificationRetryInitialDelay
	for range 10 {
		b.settle(true, delay, ceiling)
		delay = b.delay
		if delay > ceiling {
			t.Fatalf("delay %v exceeds the ceiling %v", delay, ceiling)
		}
	}
	if b.delay != ceiling {
		t.Fatalf("delay = %v, want saturation at %v", b.delay, ceiling)
	}
	b.settle(false, b.delay, ceiling)
	if b.delay != jobNotificationRetryInitialDelay {
		t.Fatalf("delay = %v, want a rail with no pending work back at the initial delay", b.delay)
	}
}

// The stable-delegate rail doubles the delay current at fire time, not the one
// the timer was armed with: a reset that landed while the timer was armed
// holds. The other rails double the armed delay.
func TestBackoffTimerSettleFromArmedDelayVersusCurrentDelay(t *testing.T) {
	var armedDelayRail, currentDelayRail backoffTimer
	armedDelayRail.delay = 4 * time.Second
	currentDelayRail.delay = 4 * time.Second
	armed, _ := armedDelayRail.arm()
	currentDelayRail.arm()

	armedDelayRail.delay = jobNotificationRetryInitialDelay // a delay reset while armed
	currentDelayRail.delay = jobNotificationRetryInitialDelay

	armedDelayRail.settle(true, armed, jobNotificationRetryMaxDelay)
	currentDelayRail.settle(true, currentDelayRail.delay, jobNotificationRetryMaxDelay)
	if armedDelayRail.delay != jobNotificationRetryMaxDelay {
		t.Fatalf("armed-delay doubling = %v, want %v", armedDelayRail.delay, jobNotificationRetryMaxDelay)
	}
	if want := 2 * jobNotificationRetryInitialDelay; currentDelayRail.delay != want {
		t.Fatalf("fire-time doubling = %v, want the reset to hold at %v", currentDelayRail.delay, want)
	}
}
