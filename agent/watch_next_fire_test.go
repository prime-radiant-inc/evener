package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// The next-fire instant is derived ONLY from data the daemon already holds: the
// interval, the install instant, and (for a repeating ticker) the newest CLOCK
// fire. The delivery ring is not a clock history - it holds output matches and
// event fires too - so it never anchors the derivation. Output/event/condition
// watches carry none.
func TestWatchCadencesDeriveNextFireForClockCadencesOnly(t *testing.T) {
	t.Parallel()
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
			name: "a fired repeating timer advances from its newest clock fire",
			cfg: &watchConfig{
				timer: true, timerSeconds: 300, progressIntervalMS: 300_000,
				createdAt: created, lastClockFire: at(600),
			},
			want: []WatchCadenceInfo{{Kind: "every", Seconds: 300, DerivedNextFireAt: fmtTime(at(900))}},
		},
		{
			name: "a progress watch advances from its newest clock fire",
			cfg: &watchConfig{
				progressIntervalMS: 1_000, createdAt: created,
				lastClockFire: at(2),
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
			// A delivery newer than the newest clock fire - an output match -
			// must not move the clock cadence's date.
			name: "a delivery after the newest clock fire does not move the clock row",
			cfg: &watchConfig{
				outputMatch: "ready", progressIntervalMS: 1_000, createdAt: created,
				lastClockFire: at(1), deliveryTimes: []time.Time{at(1), at(30)},
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

// TestWatchCadencesDeriveNextFireFromTheClockFireNotAnyDelivery pins the
// derivation's anchor. A watch may legally combine output_match with
// progress_interval_ms, and both fires count a delivery, so the newest entry in
// the delivery ring is not necessarily a clock fire. The progress cadence's
// derived next fire must advance from the newest CLOCK fire; reading the ring
// would shift it to an unrelated output match (or suppress the label when the
// match instant is recent). Before the fix this test fails: the newest ring
// entry is the match, so the derived instant lands one interval after the match
// instead of one interval after the tick.
func TestWatchCadencesDeriveNextFireFromTheClockFireNotAnyDelivery(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	start := time.Unix(1_700_000_000, 0).UTC()
	// The fake clock leaves the watch's background progress timer inert; the
	// tick below is driven synchronously, doing exactly that goroutine's work.
	jm.clock = agenttest.NewFakeClockAt(start)
	freezeClockAt(jm, start)

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	res, err := jm.configureWatch(watchArgs{
		Operation:          "create",
		Source:             rec.JobID,
		Target:             rec.JobID,
		OutputMatch:        "ready",
		ProgressIntervalMS: minWatchProgressIntervalMS,
	})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatalf("watch %s is not installed", res.WatchID)
	}

	// One clock tick at +60s: the progress cadence's newest real fire.
	tick := start.Add(60 * time.Second)
	freezeClockAt(jm, tick)
	if !jm.fireProgressTick(key, cfg) {
		t.Fatal("the progress tick ended the watch")
	}

	// An output match lands later, at +90s. Both deliveries are real, but only
	// the tick is a clock fire, so only the tick may anchor the cadence.
	match := start.Add(90 * time.Second)
	freezeClockAt(jm, match)
	chunk := []byte("ready\n")
	jm.feedJobOutput(rec.JobID, chunk, int64(len(chunk)))

	jm.mu.Lock()
	ring := append([]time.Time(nil), cfg.deliveryTimes...)
	lastClockFire := cfg.lastClockFire
	jm.mu.Unlock()
	// The scenario under test: the newest ring entry is the MATCH, not the tick.
	if len(ring) < 2 || !ring[len(ring)-1].Equal(match) {
		t.Fatalf("delivery ring = %v, want the output match at %v as the newest entry", ring, match)
	}
	// The tick stamped the clock's own history, and the match did not move it.
	if !lastClockFire.Equal(tick) {
		t.Fatalf("last clock fire = %v, want the progress tick at %v", lastClockFire, tick)
	}

	var got string
	for _, cadence := range watchCadencesOf(cfg) {
		if cadence.Kind == "progress" {
			got = cadence.DerivedNextFireAt
		}
	}
	want := tick.Add(time.Duration(minWatchProgressIntervalMS) * time.Millisecond).Format(time.RFC3339Nano)
	if got != want {
		t.Fatalf("progress derived next fire = %q, want %q (the tick at %v, not the match at %v)", got, want, tick, match)
	}
}

// A send-routed one-shot fires its single tick (stamping lastClockFire) but
// counts no delivery until its frame settles, and firedPendingEnd is not set
// while the durable teardown is still pending. In that window the old guard
// (firedPendingEnd || len(deliveryTimes) > 0) was false, so the derivation
// returned lastClockFire.Add(interval) -- a future instant for a watch that will
// never fire again. Before the fix this test fails: the pending-settlement case
// asserts the empty string and gets the phantom future instant back. A non-zero
// lastClockFire must read as "already fired" for a one-shot, and a one-shot must
// never advance from lastClockFire; an unfired one-shot and a repeating watch
// keep their own behavior.
func TestWatchDerivedNextFireOneShotFiredPendingSettlementHasNoNextFire(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	fmtTime := func(tm time.Time) string { return tm.Format(time.RFC3339Nano) }

	// Fired one-shot, settlement still pending: lastClockFire set, delivery ring
	// empty, firedPendingEnd not yet set.
	firedPendingSettlement := &watchConfig{
		timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600_000,
		createdAt: created, lastClockFire: created.Add(600 * time.Second),
	}
	got := watchCadencesOf(firedPendingSettlement)
	if len(got) != 1 || got[0].Kind != "after" {
		t.Fatalf("cadences = %+v, want one after cadence", got)
	}
	if got[0].DerivedNextFireAt != "" {
		t.Fatalf("derived next fire = %q, want empty: a fired one-shot has no next fire", got[0].DerivedNextFireAt)
	}

	// Not yet fired: install instant + interval.
	unfired := &watchConfig{
		timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600_000,
		createdAt: created,
	}
	got = watchCadencesOf(unfired)
	wantUnfired := fmtTime(created.Add(600 * time.Second))
	if len(got) != 1 || got[0].DerivedNextFireAt != wantUnfired {
		t.Fatalf("unfired one-shot derived next fire = %+v, want %q", got, wantUnfired)
	}

	// A repeating watch still advances from lastClockFire.
	repeating := &watchConfig{
		timer: true, timerSeconds: 300, progressIntervalMS: 300_000,
		createdAt: created, lastClockFire: created.Add(600 * time.Second),
	}
	got = watchCadencesOf(repeating)
	wantRepeating := fmtTime(created.Add(900 * time.Second))
	if len(got) != 1 || got[0].DerivedNextFireAt != wantRepeating {
		t.Fatalf("repeating derived next fire = %+v, want %q", got, wantRepeating)
	}
}

// A one-shot that delivered by a CONDITION has fired: one-shot means fire once,
// whatever fires it. The delivery ring is what marks that in the window before
// the durable teardown writes firedPendingEnd, so the derived next fire and the
// armed state must both read it as spent -- a countdown next to "armed" for a
// watch that will never fire again is the overstatement this pins against.
func TestWatchOneShotFiredByConditionIsSpent(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	fired := &watchConfig{
		timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600_000,
		createdAt: created, deliveries: 1,
		deliveryTimes: []time.Time{created.Add(30 * time.Second)},
	}
	got := watchCadencesOf(fired)
	if len(got) != 1 || got[0].Kind != "after" || got[0].DerivedNextFireAt != "" {
		t.Fatalf("cadences = %+v, want one after cadence with no next fire", got)
	}
	if info := watchStatusInfoFromConfig(fired); info.Active {
		t.Fatalf("one-shot fired by a condition reports armed: %+v", info)
	}

	// Still waiting: neither signal set, so it is armed and dated.
	waiting := &watchConfig{
		timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600_000,
		createdAt: created,
	}
	if info := watchStatusInfoFromConfig(waiting); !info.Active {
		t.Fatalf("unfired one-shot reports spent: %+v", info)
	}
	if cadences := watchCadencesOf(waiting); len(cadences) != 1 || cadences[0].DerivedNextFireAt == "" {
		t.Fatalf("unfired one-shot cadences = %+v, want a derived next fire", cadences)
	}
}

// A repeating cadence advances from the LATER of its install instant and its
// newest clock fire. A clock that steps backwards, or a fire stamped before the
// install instant, must not move the next fire earlier -- an instant in the past
// is one the UI deliberately refuses to show.
func TestWatchRepeatingNextFireNeverAdvancesFromAnOlderClockFire(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	fmtTime := func(tm time.Time) string { return tm.Format(time.RFC3339Nano) }
	skewed := &watchConfig{
		timer: true, timerSeconds: 300, progressIntervalMS: 300_000,
		createdAt: created, lastClockFire: created.Add(-120 * time.Second),
	}
	got := watchCadencesOf(skewed)
	want := fmtTime(created.Add(300 * time.Second))
	if len(got) != 1 || got[0].DerivedNextFireAt != want {
		t.Fatalf("derived next fire = %+v, want %q (the install instant, not the older fire)", got, want)
	}
}
