package agent

import (
	"testing"
	"time"
)

// The next-fire instant is derived ONLY from data the daemon already holds: the
// interval, the install instant, and (for a repeating ticker) the newest
// delivery instant in the ring. Output/event/condition watches carry none.
func TestWatchCadencesDeriveNextFireForClockCadencesOnly(t *testing.T) {
	created := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return created.Add(time.Duration(seconds) * time.Second) }
	fmtTime := func(tm time.Time) string { return tm.Format(time.RFC3339Nano) }

	cases := []struct {
		name string
		cfg  *watchConfig
		want []WatchCadenceInfo
	}{
		{
			name: "unfired one-shot after is armed-at plus its seconds",
			cfg: &watchConfig{
				timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600_000,
				createdAt: created,
			},
			want: []WatchCadenceInfo{{Kind: "after", Seconds: 600, DerivedNextFireAt: fmtTime(at(600))}},
		},
		{
			name: "unfired repeating timer is armed-at plus its interval",
			cfg: &watchConfig{
				timer: true, timerSeconds: 300, progressIntervalMS: 300_000,
				createdAt: created,
			},
			want: []WatchCadenceInfo{{Kind: "every", Seconds: 300, DerivedNextFireAt: fmtTime(at(300))}},
		},
		{
			name: "a fired repeating timer advances from its newest delivery",
			cfg: &watchConfig{
				timer: true, timerSeconds: 300, progressIntervalMS: 300_000,
				createdAt: created, deliveryTimes: []time.Time{at(300), at(600)},
			},
			want: []WatchCadenceInfo{{Kind: "every", Seconds: 300, DerivedNextFireAt: fmtTime(at(900))}},
		},
		{
			name: "a progress watch advances from its newest delivery",
			cfg: &watchConfig{
				progressIntervalMS: 1_000, createdAt: created,
				deliveryTimes: []time.Time{at(1), at(2)},
			},
			want: []WatchCadenceInfo{{Kind: "progress", Seconds: 1, DerivedNextFireAt: fmtTime(at(3))}},
		},
		{
			name: "a one-shot that already fired carries no next fire",
			cfg: &watchConfig{
				timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600_000,
				createdAt: created, firedPendingEnd: true, deliveryTimes: []time.Time{at(600)},
			},
			want: []WatchCadenceInfo{{Kind: "after", Seconds: 600}},
		},
		{
			name: "an output watch has no schedule",
			cfg:  &watchConfig{outputMatch: "ready"},
			want: []WatchCadenceInfo{{Kind: "output"}},
		},
		{
			name: "an event watch has no schedule",
			cfg:  &watchConfig{wildcardEvents: true},
			want: []WatchCadenceInfo{{Kind: "events"}},
		},
		{
			name: "an output watch with a clock trigger dates only the clock row",
			cfg: &watchConfig{
				outputMatch: "ready", progressIntervalMS: 1_000, createdAt: created,
				deliveryTimes: []time.Time{at(1)},
			},
			want: []WatchCadenceInfo{
				{Kind: "output"},
				{Kind: "progress", Seconds: 1, DerivedNextFireAt: fmtTime(at(2))},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := watchCadencesOf(tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("watchCadencesOf = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("cadence[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
