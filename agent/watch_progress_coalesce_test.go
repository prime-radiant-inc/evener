package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

// A progress-interval tick that carries no new output must not wake the model.
// These tests pin the ruling on issue #501's tick-coalescing half: the watch
// keeps its cadence and stays armed, but a tick with nothing new to say skips
// the model-bearing delivery until there is something to act on.
//
// Every tick here is driven synchronously through fireProgressTick, the same
// call the ticker goroutine makes, on an injected fake clock that never
// advances — so the watch's own background ticker parks and the test owns the
// cadence. Deliveries are counted through jm.enqueue, the seam the existing
// progress-tick tests use, and through cfg.deliveries, which is the count
// job_list reports.

// coalesceHarness arms a concrete-job progress watch on a parked fake clock.
type coalesceHarness struct {
	jm       *jobManager
	key      watchKey
	cfg      *watchConfig
	jobID    string
	output   *jobstore.OutputStore
	notified []jobNotification
	start    time.Time
}

func newCoalesceHarness(t *testing.T) *coalesceHarness {
	t.Helper()
	jm := newTestJM(t)
	h := &coalesceHarness{jm: jm, start: time.Unix(1_700_000_000, 0).UTC()}
	// The fake clock leaves the watch's background progress timer parked; every
	// tick below does exactly the work that goroutine would do.
	jm.clock = agenttest.NewFakeClockAt(h.start)
	freezeClockAt(jm, h.start)
	jm.enqueue = func(n jobNotification) { h.notified = append(h.notified, n) }

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	h.jobID = rec.JobID
	jm.mu.Lock()
	h.output = jm.running[rec.JobID].output
	jm.mu.Unlock()

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatalf("watch %s is not installed", res.WatchID)
	}
	h.key, h.cfg = key, cfg
	return h
}

// tick fires one progress tick at start+seconds and reports whether the ticker
// is still armed.
func (h *coalesceHarness) tick(t *testing.T, seconds int) bool {
	t.Helper()
	freezeClockAt(h.jm, h.start.Add(time.Duration(seconds)*time.Second))
	return h.jm.fireProgressTick(h.key, h.cfg)
}

// produce appends output to the watched job, the growth a tick looks for.
func (h *coalesceHarness) produce(t *testing.T, text string) {
	t.Helper()
	if _, err := h.jm.appendJobOutput(h.jobID, h.output, []byte(text)); err != nil {
		t.Fatalf("append output: %v", err)
	}
}

func (h *coalesceHarness) deliveries() int {
	h.jm.mu.Lock()
	defer h.jm.mu.Unlock()
	return h.cfg.deliveries
}

func (h *coalesceHarness) installed() bool {
	h.jm.mu.Lock()
	defer h.jm.mu.Unlock()
	return h.jm.watches[h.key] == h.cfg
}

func (h *coalesceHarness) lastClockFire() time.Time {
	h.jm.mu.Lock()
	defer h.jm.mu.Unlock()
	return h.cfg.lastClockFire
}

// The central claim: with no new output, the second and third ticks deliver
// nothing while the first still delivers. The watch stays armed throughout and
// its cadence keeps advancing, so it is coalescing, not going inert.
func TestProgressTickCoalescesTicksWithNoNewOutput(t *testing.T) {
	t.Parallel()
	h := newCoalesceHarness(t)

	// First value: the very first tick delivers even though the job has
	// produced nothing. The model must see the output's content at least once.
	if !h.tick(t, 1) {
		t.Fatal("the first progress tick retired the watch, want it armed")
	}
	if len(h.notified) != 1 || h.deliveries() != 1 {
		t.Fatalf("first tick: notifications=%d deliveries=%d, want one of each",
			len(h.notified), h.deliveries())
	}

	// Two empty ticks. Each still fires and still keeps the ticker armed.
	for _, second := range []int{2, 3} {
		if !h.tick(t, second) {
			t.Fatalf("tick at +%ds retired the watch, want it armed and coalesced", second)
		}
		if got := h.deliveries(); got != 1 {
			t.Fatalf("after the tick at +%ds: deliveries=%d, want 1 (an empty tick must not deliver)", second, got)
		}
		if got := len(h.notified); got != 1 {
			t.Fatalf("after the tick at +%ds: notifications=%d, want 1 (an empty tick must not wake the model)", second, got)
		}
	}

	// Liveness: the watch is still registered ...
	if !h.installed() {
		t.Fatal("a coalesced tick cleared its watch, want it still armed")
	}
	// ... and the coalesced tick is recorded as a fire on the cadence it kept.
	if got, want := h.lastClockFire(), h.start.Add(3*time.Second); !got.Equal(want) {
		t.Fatalf("last clock fire = %v, want the coalesced tick at %v", got, want)
	}
}

// The boundary in the other direction: the tick immediately after new output
// delivers, and the next empty tick after that coalesces again because the
// delivered tick moved the baseline.
func TestProgressTickDeliversAfterNewOutput(t *testing.T) {
	t.Parallel()
	h := newCoalesceHarness(t)

	if !h.tick(t, 1) {
		t.Fatal("the first progress tick retired the watch")
	}
	if !h.tick(t, 2) {
		t.Fatal("an empty tick retired the watch")
	}
	if got := h.deliveries(); got != 1 {
		t.Fatalf("deliveries before any output = %d, want 1", got)
	}

	h.produce(t, "still booting\n")
	if !h.tick(t, 3) {
		t.Fatal("the tick after new output retired the watch")
	}
	if got := h.deliveries(); got != 2 {
		t.Fatalf("deliveries after new output = %d, want 2: the tick carried new bytes", got)
	}

	// The delivering tick is the new baseline, so a quiet tick coalesces again.
	if !h.tick(t, 4) {
		t.Fatal("the quiet tick after a delivering tick retired the watch")
	}
	if got := h.deliveries(); got != 2 {
		t.Fatalf("deliveries after the following empty tick = %d, want 2", got)
	}
}

// A target that is stopping is a state transition, never a quiet interval. The
// tick that observes it delivers even though the job wrote nothing.
func TestProgressTickNeverCoalescesAStoppingTarget(t *testing.T) {
	t.Parallel()
	h := newCoalesceHarness(t)

	if !h.tick(t, 1) {
		t.Fatal("the first progress tick retired the watch")
	}
	if !h.tick(t, 2) {
		t.Fatal("an empty tick retired the watch")
	}
	if got := h.deliveries(); got != 1 {
		t.Fatalf("deliveries before the stop = %d, want 1", got)
	}

	if _, err := h.jm.stop(h.jobID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !h.tick(t, 3) {
		t.Fatal("the tick on a stopping target retired the watch")
	}
	if got := h.deliveries(); got != 2 {
		t.Fatalf("deliveries after the stop = %d, want 2: a stopping target is a state change, not an empty tick", got)
	}
}

// The pure gate, pinned directly: a snapshot that is quiet in every other
// respect still fires when the target is in transition, and coalesces when it
// is not. This is the terminal/stop transition claim at the decision core, where
// it cannot be masked by a rail.
func TestDecideProgressTickTransitionIsNeverCoalesced(t *testing.T) {
	t.Parallel()
	quiet := progressTickSnapshot{
		stillRegistered:     true,
		targetRunning:       true,
		tickFired:           true,
		targetOutputBytes:   64,
		lastTickOutputBytes: 64,
	}
	if dec := decideProgressTick(quiet); !dec.coalesce || dec.fire {
		t.Fatalf("quiet tick decision = %+v, want coalesce with no fire", dec)
	}
	if dec := decideProgressTick(quiet); !dec.keepAlive {
		t.Fatalf("quiet tick decision = %+v, want the ticker kept alive", dec)
	}

	transitioning := quiet
	transitioning.targetTransitioning = true
	dec := decideProgressTick(transitioning)
	if !dec.fire || dec.coalesce || !dec.recordBudget {
		t.Fatalf("transitioning tick decision = %+v, want a delivered notification", dec)
	}

	// The first tick is never coalesced, whatever the output baseline says.
	first := quiet
	first.tickFired = false
	if dec := decideProgressTick(first); !dec.fire || dec.coalesce {
		t.Fatalf("first tick decision = %+v, want a delivery", dec)
	}

	// A timer's cadence is time, not output: it keeps delivering.
	timer := quiet
	timer.timer = true
	if dec := decideProgressTick(timer); !dec.fire || dec.coalesce {
		t.Fatalf("timer tick decision = %+v, want a delivery", dec)
	}

	// A session target has no output stream to grow.
	session := quiet
	session.sessionTarget = true
	session.targetRunning = false
	if dec := decideProgressTick(session); !dec.fire || dec.coalesce {
		t.Fatalf("session tick decision = %+v, want a delivery", dec)
	}
}

// A send-routed progress watch coalesces on the same rule: a quiet tick records
// no send delivery, so the rail has nothing new to hand the observer.
func TestProgressTickSendRailCoalescesWithNoNewOutput(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	start := time.Unix(1_700_000_000, 0).UTC()
	jm.clock = agenttest.NewFakeClockAt(start)
	freezeClockAt(jm, start)

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	installWatchBelowValidation(t, jm, watchArgs{
		Target:             rec.JobID,
		ProgressIntervalMS: minWatchProgressIntervalMS,
		Send:               &watchSendArgs{To: "dlg_obs", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, jm)
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}

	// A non-same-watch provenance so the tick is not suppressed on the send path.
	jm.mu.Lock()
	jm.running[rec.JobID].rec.Provenance = provenance.Clone(nil)
	jm.mu.Unlock()

	tick := func(seconds int) bool {
		freezeClockAt(jm, start.Add(time.Duration(seconds)*time.Second))
		return jm.fireProgressTick(key, cfg)
	}
	if !tick(1) {
		t.Fatal("the first send tick retired the watch")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending sends after the first tick = %d, want 1", len(pending))
	}
	for _, second := range []int{2, 3} {
		if !tick(second) {
			t.Fatalf("the send tick at +%ds retired the watch", second)
		}
		if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
			t.Fatalf("pending sends after the empty tick at +%ds = %d, want 1", second, len(pending))
		}
	}
}

// A coalesced tick must not swallow the job's terminal transition: the watch is
// swept when the job finishes, and the model still hears that the job ended.
func TestCoalescedProgressTickStillEndsWithTheJob(t *testing.T) {
	t.Parallel()
	h := newCoalesceHarness(t)

	if !h.tick(t, 1) {
		t.Fatal("the first progress tick retired the watch")
	}
	if !h.tick(t, 2) {
		t.Fatal("an empty tick retired the watch")
	}
	before := len(h.notified)

	code := 0
	if err := h.jm.finalize(h.jobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if h.installed() {
		t.Fatal("the watch survived its target going terminal")
	}
	if len(h.notified) != before+1 {
		t.Fatalf("notifications after finalize = %d, want %d: the terminal transition must reach the model",
			len(h.notified), before+1)
	}
	if last := h.notified[len(h.notified)-1]; last.Kind == jobNotificationKindWatch {
		t.Fatalf("last notification = %+v, want the job terminal notification, not a watch tick", last)
	}
}
