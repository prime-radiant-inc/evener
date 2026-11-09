package agent

import (
	"testing"
	"time"
)

// TestJobManagerHasArmedWatch covers the armed-watch predicate over fixtures
// seeded on a frozen-clock manager: an empty manager has nothing armed, a
// repeating event watch and an armed timer both count, and each single
// one-shot spent signal reads as not armed. Every row asserts through the
// locking wrapper so the table also covers the lock discipline.
func TestJobManagerHasArmedWatch(t *testing.T) {
	t.Parallel()

	t.Run("empty manager has no armed watch", func(t *testing.T) {
		t.Parallel()
		jm := newTestJM(t)
		if jm.hasArmedWatch() {
			t.Fatal("hasArmedWatch = true on an empty manager, want false")
		}
	})

	t.Run("repeating event watch is armed", func(t *testing.T) {
		t.Parallel()
		jm := newTestJM(t)
		if _, err := jm.configureWatch(watchArgs{Target: "*", Events: []string{"job.notification"}}); err != nil {
			t.Fatalf("configureWatch: %v", err)
		}
		if !jm.hasArmedWatch() {
			t.Fatal("hasArmedWatch = false with a repeating event watch, want true")
		}
	})

	t.Run("armed timer is armed", func(t *testing.T) {
		t.Parallel()
		jm := newTestJM(t)
		// The tool layer always sends Source: "self" with a timer; the manager's
		// validation rejects a timer without it.
		if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600}); err != nil {
			t.Fatalf("configureWatch: %v", err)
		}
		if !jm.hasArmedWatch() {
			t.Fatal("hasArmedWatch = false with an armed timer, want true")
		}
	})

	oneShotSpent := map[string]*watchConfig{
		"firedPendingEnd signal": {oneShot: true, firedPendingEnd: true},
		"lastClockFire signal":   {oneShot: true, lastClockFire: time.Now()},
		"deliveryTimes signal":   {oneShot: true, deliveryTimes: []time.Time{time.Now()}},
	}
	for name, cfg := range oneShotSpent {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			jm := newTestJM(t)
			jm.mu.Lock()
			jm.watches[watchKey{Target: "*"}] = cfg
			jm.mu.Unlock()
			if jm.hasArmedWatch() {
				t.Fatalf("hasArmedWatch = true with a spent one-shot (%s), want false", name)
			}
		})
	}
}
