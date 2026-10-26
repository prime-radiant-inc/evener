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
