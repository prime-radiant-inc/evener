package agent

import (
	"fmt"
	"strings"
	"testing"
)

// seedLiveWatches fills the manager's live-watch map with count distinct
// watches built by the real config constructor.
//
// The guard under test counts entries in that map, and a fresh registration
// needs a key of its own: 32 real registrations would need 32 distinct live job
// targets, so the prefix is seeded and the boundary itself is driven through
// configureWatch.
func seedLiveWatches(t *testing.T, jm *jobManager, count int) {
	t.Helper()
	type seeded struct {
		key watchKey
		cfg *watchConfig
	}
	rows := make([]seeded, 0, count)
	for i := range count {
		target := fmt.Sprintf("job_live_cap_seed_%02d", i)
		cfg, err := newWatchConfig(watchArgs{Target: target, Events: []string{"assistant.tool"}}, frozenTestTime, "")
		if err != nil {
			t.Fatalf("seed watch %d: %v", i, err)
		}
		rows = append(rows, seeded{key: watchKey{VisibleSessionID: jm.sessionID, Target: target}, cfg: cfg})
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, row := range rows {
		jm.watches[row.key] = row.cfg
	}
}

func liveWatchCount(jm *jobManager) int {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return len(jm.watches)
}

func callerEventWatch() watchArgs {
	return watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"assistant.tool"}}
}

// Every live watch row is projected onto AppWire for the hub, which clones and
// fingerprints the rows and only then caps what it displays. An unbounded live
// set therefore carried an unbounded watch payload on every roster probe, so
// registration refuses a key that would grow the set past the cap, the same way
// the timer path already refuses its ninth timer.
func TestConfigureWatch_LiveWatchCapRefusesAFreshKey(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedLiveWatches(t, jm, maxLiveWatches)
	_, err := jm.configureWatch(callerEventWatch())
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("too many watches (%d live); clear one first", maxLiveWatches)) {
		t.Fatalf("create past the cap: err = %v", err)
	}
	if live := liveWatchCount(jm); live != maxLiveWatches {
		t.Fatalf("a refused create must leave the live set alone: live = %d", live)
	}
}

// The cap counts every live watch, timers included: with the timer count itself
// under its own cap, a fresh timer is refused by the watch cap, not the timer
// cap.
func TestConfigureWatch_LiveWatchCapCountsTimersToo(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedLiveWatches(t, jm, maxLiveWatches)
	_, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("too many watches (%d live); clear one first", maxLiveWatches)) {
		t.Fatalf("fresh timer past the cap: err = %v", err)
	}
}

// Re-registering a key the manager already holds is not growth, so the cap must
// not refuse it. Refusing here would strand every session that reaches the cap
// on a watch whose condition it can no longer change.
func TestConfigureWatch_LiveWatchCapAdmitsTheExistingKey(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedLiveWatches(t, jm, maxLiveWatches-1)
	first, err := jm.configureWatch(callerEventWatch())
	if err != nil {
		t.Fatalf("create to the cap: %v", err)
	}
	if live := liveWatchCount(jm); live != maxLiveWatches {
		t.Fatalf("setup must reach the cap: live = %d", live)
	}
	again, err := jm.configureWatch(callerEventWatch())
	if err != nil {
		t.Fatalf("identical re-registration at the cap: %v", err)
	}
	if again.WatchID != first.WatchID {
		t.Fatalf("identical re-registration must return the same watch: %q vs %q", again.WatchID, first.WatchID)
	}
	changed := callerEventWatch()
	changed.Note = "changed"
	if _, err := jm.configureWatch(changed); err != nil {
		t.Fatalf("replacing an existing key at the cap: %v", err)
	}
	if live := liveWatchCount(jm); live != maxLiveWatches {
		t.Fatalf("an existing key must not grow the live set: live = %d", live)
	}
}

// Clearing is not growth either, and the slot a clear frees admits a fresh key.
func TestConfigureWatch_LiveWatchCapAdmitsClearAndFreesTheSlot(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedLiveWatches(t, jm, maxLiveWatches-1)
	if _, err := jm.configureWatch(callerEventWatch()); err != nil {
		t.Fatalf("create to the cap: %v", err)
	}
	clearArgs := callerEventWatch()
	clearArgs.Clear = true
	if _, err := jm.configureWatch(clearArgs); err != nil {
		t.Fatalf("clear at the cap: %v", err)
	}
	if live := liveWatchCount(jm); live != maxLiveWatches-1 {
		t.Fatalf("clear must free its slot: live = %d", live)
	}
	if _, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60}); err != nil {
		t.Fatalf("fresh key after a clear: %v", err)
	}
	if live := liveWatchCount(jm); live != maxLiveWatches {
		t.Fatalf("the freed slot must admit one watch: live = %d", live)
	}
}
