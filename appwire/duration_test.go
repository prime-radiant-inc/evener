package appwire

import (
	"testing"
	"time"
)

// TestDurationMillisRoundsAPositiveSubMillisecondDurationUp pins the one edge
// that truncation gets wrong: time.Duration.Milliseconds() truncates toward
// zero, and 0 is the value this wire reserves for "disabled", so a positive
// sub-millisecond deadline would otherwise cross the wire looking like an
// unarmed daemon.
func TestDurationMillisRoundsAPositiveSubMillisecondDurationUp(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want int64
	}{
		{"zero stays the disabled value", 0, 0},
		{"a single nanosecond is not disabled", time.Nanosecond, 1},
		{"half a millisecond is not disabled", 500 * time.Microsecond, 1},
		{"just under a millisecond", time.Millisecond - 1, 1},
		{"exactly a millisecond", time.Millisecond, 1},
		{"sub-millisecond remainder truncates", time.Millisecond + 500*time.Microsecond, 1},
		{"a second", time.Second, 1000},
		{"the one-hour default", time.Hour, 3600000},
		{"a negative duration is passed through", -time.Second, -1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DurationMillis(tc.in); got != tc.want {
				t.Fatalf("DurationMillis(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
