package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
)

// seedObserverDelegate extends a stableWatchRuntimeFixture-shaped harness with
// a second running delegate whose watch on the parent session survives a stop —
// the #655 scenario. It wires the delegate's runtime session and installs the
// parent-source watch into the ROOT job manager exactly as
// configureStableWatchOnSource does in production (receiver keyed to the child).
func seedObserverDelegate(t *testing.T, f *stableWatchRuntimeFixture) *Session {
	t.Helper()
	seedDelegateControllerRunning(t, f.controller, "dlg_observer", "")
	observer := &Session{
		id:                    "child-dlg_observer",
		stateDir:              f.controller.stateDir,
		delegateController:    f.controller,
		delegateRootSessionID: f.root.ID(),
		owningDelegateID:      "dlg_observer",
		jobManager:            f.sourceJM,
		state:                 SessionIdle,
	}
	f.controller.mu.Lock()
	f.controller.live["dlg_observer"].runtime = observer
	f.controller.live["dlg_observer"].binding.runtime = observer
	f.controller.mu.Unlock()
	return observer
}

// installParentWatchOnObserver installs the observer's watch on the parent
// session into rootJM, mirroring configureStableWatchOnSource
// (session_tools_jobs.go): Source "parent", Target "caller", receiver keyed to
// the child session and delegate, StableReceiver with internal send.
func installParentWatchOnObserver(t *testing.T, f *stableWatchRuntimeFixture) string {
	t.Helper()
	result, err := f.rootJM.configureWatch(watchArgs{
		Source:               "parent",
		Target:               runtimeMessageAliasCaller,
		Events:               []string{"communicate"},
		ReceiverSessionID:    "child-dlg_observer",
		ReceiverDelegateID:   "dlg_observer",
		StableReceiver:       true,
		ReceiverSendInternal: true,
	})
	if err != nil {
		t.Fatalf("install parent-source observer watch: %v", err)
	}
	return result.WatchID
}

// stopObserverDelegate invokes job_stop on the observer delegate.
func stopObserverDelegate(t *testing.T, s *Session, args map[string]any) stableJobStopInvocation {
	t.Helper()
	value, err := jobStopTool(context.Background(), s, args, 4096)
	return stableJobStopInvocation{value: value, err: err}
}

// stopObserverDelegateOutput returns the rendered output of a stop invocation.
func stopObserverDelegateOutput(t *testing.T, invocation stableJobStopInvocation) string {
	t.Helper()
	if invocation.err != nil {
		t.Fatalf("stable job_stop: %v", invocation.err)
	}
	result, ok := invocation.value.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("stable job_stop value = %T, want tool.StateResult", invocation.value)
	}
	return result.Output
}

// TestJobStopReportsLiveWatchesAdmission pins #655's must-have branch: even a
// stop_pending result reports the watches that will survive the stop and keep
// delivering to the stopped delegate, and the rendered output carries the
// inventory header and clear guidance.
func TestJobStopReportsLiveWatchesAdmission(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installParentWatchOnObserver(t, f)

	invocation := stopObserverDelegate(t, f.root, map[string]any{
		"target": "dlg_observer",
	})
	state := stableJobStopState(t, invocation)
	if len(state.LiveWatches) != 1 {
		t.Fatalf("stop_pending live watches = %d, want 1 (state %#v)", len(state.LiveWatches), state)
	}
	row := state.LiveWatches[0]
	if row.ID != watchID || row.Source != "parent" {
		t.Fatalf("live watch row = %#v, want id=%s source=parent", row, watchID)
	}
	if row.Condition == "" {
		t.Fatalf("live watch row condition empty: %#v", row)
	}
	output := stopObserverDelegateOutput(t, invocation)
	if !strings.Contains(output, "live watches: 1 still armed") {
		t.Fatalf("output missing live watches header: %q", output)
	}
	if !strings.Contains(output, watchID) || !strings.Contains(output, `job_watch operation="clear"`) {
		t.Fatalf("output missing clear guidance for %s: %q", watchID, output)
	}
}

// TestJobStopReportsLiveWatchesSettled pins the completed stop: after the
// delegate settles, the result still carries the live-watch inventory.
func TestJobStopReportsLiveWatchesSettled(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installParentWatchOnObserver(t, f)

	result := make(chan stableJobStopInvocation, 1)
	go func() {
		result <- stopObserverDelegate(t, f.root, map[string]any{
			"target":      "dlg_observer",
			"max_wait_ms": 5000,
		})
	}()
	// Wait for durable stop admission before finishing the generation — the
	// pattern the retention tests use (delegate_resource_retention_stop_test.go).
	// (currentDelegateStop itself is unsuitable to poll: it fails the test when
	// the stop is not yet admitted.)
	// TRIPWIRE: admission is a durable fsync + local drive, expected in low
	// hundreds of ms; 5s only absorbs CI scheduling stalls.
	waitForCondition(t, 5*time.Second, "stable stop admission", func() bool {
		f.controller.mu.Lock()
		defer f.controller.mu.Unlock()
		return f.controller.stop != nil
	})
	if _, err := f.controller.FinishGeneration(delegateLease{delegateID: "dlg_observer", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish observer generation: %v", err)
	}
	stop := currentDelegateStop(t, f.controller)
	<-stop.done
	state := stableJobStopState(t, <-result)
	if len(state.LiveWatches) != 1 || state.LiveWatches[0].ID != watchID {
		t.Fatalf("settled live watches = %#v, want id=%s", state.LiveWatches, watchID)
	}
}

// TestJobStopNoLiveWatches pins the negative case: a stopped delegate reports
// no watches when none target it — with a watch delivering to a DIFFERENT
// delegate present, so "correctly empty" is distinguished from "always empty".
func TestJobStopNoLiveWatches(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	// A watch delivering to another delegate must not leak into the inventory.
	if _, err := f.rootJM.configureWatch(watchArgs{
		Source:               "parent",
		Target:               runtimeMessageAliasCaller,
		Events:               []string{"communicate"},
		ReceiverSessionID:    "child-dlg_source",
		ReceiverDelegateID:   "dlg_source",
		StableReceiver:       true,
		ReceiverSendInternal: true,
	}); err != nil {
		t.Fatalf("install other-delegate watch: %v", err)
	}

	state := stableJobStopState(t, stopObserverDelegate(t, f.root, map[string]any{
		"target": "dlg_observer",
	}))
	if len(state.LiveWatches) != 0 {
		t.Fatalf("live watches = %#v, want none", state.LiveWatches)
	}
}

// TestJobStopLiveWatchesSettleRefresh pins the settle-time read: a watch
// cleared while the stop played out is absent from the settled result.
func TestJobStopLiveWatchesSettleRefresh(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	installParentWatchOnObserver(t, f)

	result := make(chan stableJobStopInvocation, 1)
	go func() {
		result <- stopObserverDelegate(t, f.root, map[string]any{
			"target":      "dlg_observer",
			"max_wait_ms": 5000,
		})
	}()
	// TRIPWIRE: admission is a durable fsync + local drive, expected in low
	// hundreds of ms; 5s only absorbs CI scheduling stalls.
	waitForCondition(t, 5*time.Second, "stable stop admission", func() bool {
		f.controller.mu.Lock()
		defer f.controller.mu.Unlock()
		return f.controller.stop != nil
	})
	// Clear the watch between admission and settle: the settled result must
	// not report it.
	if _, err := f.rootJM.clearReceiverWatchByID(onlyWatchIDIn(t, f.rootJM), "child-dlg_observer", "dlg_observer"); err != nil {
		t.Fatalf("clear observer watch mid-stop: %v", err)
	}
	if _, err := f.controller.FinishGeneration(delegateLease{delegateID: "dlg_observer", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish observer generation: %v", err)
	}
	stop := currentDelegateStop(t, f.controller)
	<-stop.done
	state := stableJobStopState(t, <-result)
	if len(state.LiveWatches) != 0 {
		t.Fatalf("settled live watches = %#v, want the cleared watch absent", state.LiveWatches)
	}
}

// TestJobStopLiveWatchesTimeout pins the timed-out wait: a stop with
// max_wait_ms > 0 that does not settle still reports the armed inventory —
// the watches keep delivering while the delegate settles.
func TestJobStopLiveWatchesTimeout(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installParentWatchOnObserver(t, f)

	state := stableJobStopState(t, stopObserverDelegate(t, f.root, map[string]any{
		"target":      "dlg_observer",
		"max_wait_ms": 50, // never settles: the seeded generation has no driver
	}))
	if state.Outcome == "stopped_by_parent" {
		t.Fatal("fixture unexpectedly settled; timeout arm not exercised")
	}
	if len(state.LiveWatches) != 1 || state.LiveWatches[0].ID != watchID {
		t.Fatalf("timed-out live watches = %#v, want id=%s", state.LiveWatches, watchID)
	}
}

// TestJobWatchClearSiblingRefused pins the authority boundary: a sibling
// (non-ancestor, non-receiver) clear is refused with an explicit error, not a
// success-shaped silent no-op.
func TestJobWatchClearSiblingRefused(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installParentWatchOnObserver(t, f)

	sibling := &Session{
		id:                    "child-dlg_source",
		stateDir:              f.controller.stateDir,
		delegateController:    f.controller,
		delegateRootSessionID: f.root.ID(),
		owningDelegateID:      "dlg_source", // sibling of dlg_observer, not an ancestor
		jobManager:            f.sourceJM,
		state:                 SessionIdle,
	}
	_, err := jobWatchToolWithContext(context.Background(), sibling, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096)
	if err == nil || !strings.Contains(err.Error(), "may not clear") {
		t.Fatalf("sibling clear err = %v, want explicit refusal", err)
	}
	if rows := f.rootJM.liveWatchSummariesForReceiver("child-dlg_observer", "dlg_observer"); len(rows) != 1 {
		t.Fatalf("watch was cleared by a sibling: %d rows", len(rows))
	}
}

// TestJobWatchClearNestedParent pins the topology authority admitting a
// nested parent: a delegate whose own child holds the watch may clear it,
// because the receiver is its descendant.
func TestJobWatchClearNestedParent(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installParentWatchOnObserver(t, f)

	parent := &Session{
		id:                    "child-dlg_observer",
		stateDir:              f.controller.stateDir,
		delegateController:    f.controller,
		delegateRootSessionID: f.root.ID(),
		owningDelegateID:      "dlg_observer", // receiver's own parent identity
		jobManager:            f.sourceJM,
		state:                 SessionIdle,
	}
	if _, err := jobWatchToolWithContext(context.Background(), parent, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096); err != nil {
		t.Fatalf("receiver-parent clear: %v", err)
	}
	if rows := f.rootJM.liveWatchSummariesForReceiver("child-dlg_observer", "dlg_observer"); len(rows) != 0 {
		t.Fatalf("watch survived authorized clear: %d rows", len(rows))
	}
}

// onlyWatchIDIn returns the single watch ID installed in a job manager.
func onlyWatchIDIn(t *testing.T, jm *jobManager) string {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("expected exactly one watch, have %d", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		return cfg.watchID
	}
	return ""
}

// TestFormatJobStopLiveWatchesCap pins the render cap: at most 5 rows, then a
// "+N more" line pointing at the JSON state.
func TestFormatJobStopLiveWatchesCap(t *testing.T) {
	stop := jobStopResult{Type: "delegate", LiveWatches: make([]watchListEntry, 7)}
	for i := range stop.LiveWatches {
		stop.LiveWatches[i] = watchListEntry{ID: fmt.Sprintf("watch_%d", i), Source: "parent"}
	}
	out := formatJobStop(stop)
	rows := strings.Count(out, "\n  watch_")
	if rows != 5 {
		t.Fatalf("rendered rows = %d, want 5 (output %q)", rows, out)
	}
	if !strings.Contains(out, "+2 more (see live_watches in state)") {
		t.Fatalf("output missing +2 more line: %q", out)
	}
}

// TestJobWatchClearParentSideReceiverWatch pins the Step 5 fix: the parent's
// job_watch clear actually clears a receiver-keyed watch installed in its own
// job manager (previously a success-shaped silent no-op).
func TestJobWatchClearParentSideReceiverWatch(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installParentWatchOnObserver(t, f)

	value, err := jobWatchToolWithContext(context.Background(), f.root, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096)
	if err != nil {
		t.Fatalf("parent-side clear: %v", err)
	}
	_ = value
	if rows := f.rootJM.liveWatchSummariesForReceiver("child-dlg_observer", "dlg_observer"); len(rows) != 0 {
		t.Fatalf("watch survived parent-side clear: %d rows", len(rows))
	}
}
