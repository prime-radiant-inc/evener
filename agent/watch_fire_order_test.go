package agent

import (
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// The condition-fire rails run the same breaker offer, but each orders its
// effects differently, and that order decides what the model sees in its first
// wake: a frame must be pending before its watch is torn down, and the cleared
// notification either rides out with the matched notifications or follows them.

const (
	stepNotify      = "notify"
	stepSendPending = "append:" + string(jobstore.EventWatchSendPending)
	stepCleared     = "append:" + string(jobstore.EventWatchCleared)
)

type fireStepTrace struct {
	mu    sync.Mutex
	steps []string
}

func (f *fireStepTrace) add(step string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, step)
}

// notifiesAroundTeardown reports how many notifications were queued before the
// first frame was recorded and after the watch-cleared record landed, and
// whether the frame was recorded before the clear. The notifications between
// the two belong to the send rail's own kick and are not part of the order.
func (f *fireStepTrace) notifiesAroundTeardown() (beforeSends, afterClear int, sentBeforeClear bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pending, cleared := slices.Index(f.steps, stepSendPending), slices.Index(f.steps, stepCleared)
	if pending < 0 || cleared < 0 {
		return -1, -1, false
	}
	for _, step := range f.steps[:pending] {
		if step == stepNotify {
			beforeSends++
		}
	}
	for _, step := range f.steps[cleared:] {
		if step == stepNotify {
			afterClear++
		}
	}
	return beforeSends, afterClear, pending < cleared
}

func traceFireSteps(jm *jobManager) *fireStepTrace {
	trace := &fireStepTrace{}
	enqueue := jm.enqueue
	jm.enqueue = func(n jobNotification) {
		trace.add(stepNotify)
		if enqueue != nil {
			enqueue(n)
		}
	}
	appendEvent := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		trace.add("append:" + string(event.Kind))
		return appendEvent(event)
	}
	appendEvents := jm.appendEvents
	jm.appendEvents = func(batch []jobstore.Event) error {
		for _, event := range batch {
			trace.add("append:" + string(event.Kind))
		}
		return appendEvents(batch)
	}
	return trace
}

// installNearBudgetWatches installs a send watch one fire from crossing the
// budget and a no-send watch on the same target whose match produces a
// notification of its own.
func installNearBudgetWatches(t *testing.T, jm *jobManager, args watchArgs) (sendCfg, notifyCfg *watchConfig) {
	t.Helper()
	sendArgs := args
	sendArgs.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"}
	installWatchBelowValidation(t, jm, sendArgs)
	installWatchBelowValidation(t, jm, args)
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if key.SendTo == "" {
			notifyCfg = cfg
		} else {
			cfg.conditionFires = watchDeliveryBudget - 1
			sendCfg = cfg
		}
	}
	if sendCfg == nil || notifyCfg == nil {
		t.Fatal("expected one send watch and one notify watch")
	}
	return sendCfg, notifyCfg
}

func createFireOrderJob(t *testing.T, jm *jobManager) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "fire order fixture"})
	if err != nil {
		t.Fatalf("create output job: %v", err)
	}
	return rec
}

func TestSessionEventRailRecordsSendsThenClearsThenEnqueues(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	trace := traceFireSteps(jm)
	installNearBudgetWatches(t, jm, watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})

	onSessionEventKD(jm, events.EventCommunicate, nil)

	// Both the matched notification and the cleared notification leave after the
	// clear is durable, in one batch.
	before, after, sentBeforeClear := trace.notifiesAroundTeardown()
	if before != 0 || after != 2 || !sentBeforeClear {
		t.Fatalf("event rail: notifications before sends=%d after clear=%d, sends before clear=%t; want 0, 2, true", before, after, sentBeforeClear)
	}
}

func TestOutputRailEnqueuesThenRecordsSendsThenClears(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec := createFireOrderJob(t, jm)
	trace := traceFireSteps(jm)
	installNearBudgetWatches(t, jm, watchArgs{Target: rec.JobID, OutputMatch: "hit"})

	jm.feedJobOutput(rec.JobID, []byte("hit\n"), 4)

	// The matched notification leaves first; the cleared notification follows the
	// clear on its own.
	before, after, sentBeforeClear := trace.notifiesAroundTeardown()
	if before != 1 || after != 1 || !sentBeforeClear {
		t.Fatalf("output rail: notifications before sends=%d after clear=%d, sends before clear=%t; want 1, 1, true", before, after, sentBeforeClear)
	}
}

func TestOutputRailStopsTheChunkAtTheBudgetCrossingMatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec := createFireOrderJob(t, jm)
	installWatchBelowValidation(t, jm, watchArgs{Target: rec.JobID, OutputMatch: "hit"})
	var cfg *watchConfig
	jm.mu.Lock()
	for _, c := range jm.watches {
		cfg = c
	}
	const remaining = 3
	cfg.conditionFires = watchDeliveryBudget - remaining
	jm.mu.Unlock()

	chunk := []byte("hit\nhit\nhit\nhit\nhit\nhit\nhit\nhit\n")
	jm.feedJobOutput(rec.JobID, chunk, int64(len(chunk)))

	jm.mu.Lock()
	fires, deliveries, live := cfg.conditionFires, cfg.deliveries, len(jm.watches)
	jm.mu.Unlock()
	if fires != watchDeliveryBudget || deliveries != remaining || live != 0 {
		t.Fatalf("after one over-long chunk: conditionFires=%d deliveries=%d liveWatches=%d, want %d fires, %d deliveries and the watch cleared",
			fires, deliveries, live, watchDeliveryBudget, remaining)
	}
}
