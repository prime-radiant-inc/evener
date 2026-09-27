package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
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

// settleObserverStop completes the pending stop of the observer delegate. The
// seeded generation has no run loop, so the test finishes it by hand exactly as
// the settled-stop tests do; the stop reconcile driver exits once stop.done
// closes.
func settleObserverStop(t *testing.T, f *stableWatchRuntimeFixture) {
	t.Helper()
	stop := awaitDelegateStopAdmission(t, f.controller)
	if _, err := f.controller.FinishGeneration(delegateLease{delegateID: "dlg_observer", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish observer generation: %v", err)
	}
	<-stop.done
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
	settleObserverStop(t, f)
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
	// Capture the stop at admission before finishing the generation — the
	// pattern the retention tests use (delegate_resource_retention_stop_test.go).
	stop := awaitDelegateStopAdmission(t, f.controller)
	if _, err := f.controller.FinishGeneration(delegateLease{delegateID: "dlg_observer", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish observer generation: %v", err)
	}
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
	settleObserverStop(t, f)
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
	stop := awaitDelegateStopAdmission(t, f.controller)
	// Clear the watch between admission and settle: the settled result must
	// not report it.
	if _, err := f.rootJM.clearReceiverWatchByID(onlyWatchIDIn(t, f.rootJM), "child-dlg_observer", "dlg_observer"); err != nil {
		t.Fatalf("clear observer watch mid-stop: %v", err)
	}
	if _, err := f.controller.FinishGeneration(delegateLease{delegateID: "dlg_observer", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish observer generation: %v", err)
	}
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
	settleObserverStop(t, f)
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
	t.Parallel()
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

// installSessionKeyedWatch installs a session-keyed receiver watch (an empty
// receiverDelegateID — the exact shape configureDescendantReceiverWatch stamps)
// into the supplied manager, keyed to receiverSessionID.
func installSessionKeyedWatch(t *testing.T, jm *jobManager, receiverSessionID string) string {
	t.Helper()
	result, err := jm.configureWatch(watchArgs{
		Source:               "parent",
		Target:               runtimeMessageAliasCaller,
		Events:               []string{"communicate"},
		ReceiverSessionID:    receiverSessionID,
		ReceiverDelegateID:   "",
		StableReceiver:       true,
		ReceiverSendInternal: true,
	})
	if err != nil {
		t.Fatalf("install session-keyed receiver watch: %v", err)
	}
	return result.WatchID
}

// runtimeSessionFor builds a runtime session wired to the fixture's controller,
// matching the delegate-seeded session shape, for direct clear invocations.
func runtimeSessionFor(f *stableWatchRuntimeFixture, sessionID, delegateID string) *Session {
	return &Session{
		id:                    sessionID,
		stateDir:              f.controller.stateDir,
		delegateController:    f.controller,
		delegateRootSessionID: f.root.ID(),
		owningDelegateID:      delegateID,
		jobManager:            f.sourceJM,
		state:                 SessionIdle,
	}
}

// TestLiveWatchesDeliveringToDelegateIncludesSessionKeyed pins Symptom 1 at the
// inventory seam: a watch keyed to the stop target's child session with an empty
// delegate id is armed and delivering and must be listed.
func TestLiveWatchesDeliveringToDelegateIncludesSessionKeyed(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installSessionKeyedWatch(t, f.rootJM, "child-dlg_observer")

	rows := f.root.liveWatchesDeliveringToDelegate("dlg_observer")
	if len(rows) != 1 || rows[0].ID != watchID {
		t.Fatalf("session-keyed delivery rows = %#v, want id=%s", rows, watchID)
	}
}

// TestJobStopReportsSessionKeyedLiveWatches pins Symptom 1 end to end: job_stop
// reports the session-keyed watch that survives the stop of the delegate whose
// session is its receiver.
func TestJobStopReportsSessionKeyedLiveWatches(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installSessionKeyedWatch(t, f.rootJM, "child-dlg_observer")

	state := stableJobStopState(t, stopObserverDelegate(t, f.root, map[string]any{
		"target": "dlg_observer",
	}))
	if len(state.LiveWatches) != 1 || state.LiveWatches[0].ID != watchID {
		t.Fatalf("session-keyed live watches = %#v, want id=%s", state.LiveWatches, watchID)
	}
	settleObserverStop(t, f)
}

// TestJobWatchClearSessionKeyedReceiverSession pins the receiver-session half of
// Symptom 2: the session that is the watch's receiver may clear its own
// session-keyed watch even when it is held in another session's manager.
func TestJobWatchClearSessionKeyedReceiverSession(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installSessionKeyedWatch(t, f.rootJM, "child-dlg_observer")

	receiver := runtimeSessionFor(f, "child-dlg_observer", "dlg_observer")
	if _, err := jobWatchToolWithContext(context.Background(), receiver, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096); err != nil {
		t.Fatalf("receiver-session clear: %v", err)
	}
	if _, _, found := f.rootJM.watchReceiverIdentity(watchID); found {
		t.Fatal("session-keyed watch survived the receiver session's clear")
	}
}

// TestJobWatchClearSessionKeyedRoot pins the root half of Symptom 2: the root
// session may clear a session-keyed watch installed by a descendant.
func TestJobWatchClearSessionKeyedRoot(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installSessionKeyedWatch(t, f.rootJM, "child-dlg_observer")

	if _, err := jobWatchToolWithContext(context.Background(), f.root, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096); err != nil {
		t.Fatalf("root clear: %v", err)
	}
	if _, _, found := f.rootJM.watchReceiverIdentity(watchID); found {
		t.Fatal("session-keyed watch survived the root's clear")
	}
}

// TestJobWatchClearSessionKeyedSiblingRefused pins the authority boundary for
// session-keyed watches: a sibling may not clear another delegate's session
// watch.
func TestJobWatchClearSessionKeyedSiblingRefused(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installSessionKeyedWatch(t, f.rootJM, "child-dlg_observer")

	sibling := runtimeSessionFor(f, "child-dlg_source", "dlg_source")
	_, err := jobWatchToolWithContext(context.Background(), sibling, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096)
	if err == nil || !strings.Contains(err.Error(), "may not clear") {
		t.Fatalf("sibling clear err = %v, want explicit refusal", err)
	}
	if _, _, found := f.rootJM.watchReceiverIdentity(watchID); !found {
		t.Fatal("session-keyed watch was cleared by a sibling")
	}
}

// TestWatchClearAuthorityReceiverKeyModel pins the unified receiver-key
// authority matrix: delegate-keyed receivers keep their ancestor/root rule and
// session-keyed receivers admit the receiver session itself, root, and the
// receiver delegate's ancestors only.
func TestWatchClearAuthorityReceiverKeyModel(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_sibling", "")
	cases := []struct {
		name                              string
		actorSession, actorDelegate       string
		receiverSession, receiverDelegate string
		want                              bool
	}{
		{"session-keyed: receiver session itself", "child-dlg_child", "dlg_child", "child-dlg_child", "", true},
		{"session-keyed: root", "root-session", "", "child-dlg_child", "", true},
		{"session-keyed: ancestor delegate", "child-dlg_parent", "dlg_parent", "child-dlg_child", "", true},
		{"session-keyed: sibling delegate", "child-dlg_sibling", "dlg_sibling", "child-dlg_child", "", false},
		{"session-keyed: non-root non-delegate", "other-session", "", "child-dlg_child", "", false},
		{"session-keyed: empty receiver session", "child-dlg_child", "dlg_child", "", "", false},
		{"session-keyed: unknown receiver session", "child-dlg_child", "dlg_child", "orphan-session", "", false},
		{"delegate-keyed: self", "child-dlg_child", "dlg_child", "child-dlg_child", "dlg_child", true},
		{"delegate-keyed: ancestor", "child-dlg_parent", "dlg_parent", "child-dlg_child", "dlg_child", true},
		{"delegate-keyed: sibling", "child-dlg_sibling", "dlg_sibling", "child-dlg_child", "dlg_child", false},
		{"delegate-keyed: root", "root-session", "", "child-dlg_child", "dlg_child", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.watchClearAuthority(tc.actorSession, tc.actorDelegate, tc.receiverSession, tc.receiverDelegate); got != tc.want {
				t.Fatalf("watchClearAuthority(%q, %q, %q, %q) = %v, want %v",
					tc.actorSession, tc.actorDelegate, tc.receiverSession, tc.receiverDelegate, got, tc.want)
			}
		})
	}
}

// TestSubtreeReceiverKeysForDelegateIncludesSessionKey pins the inventory half:
// each subtree member contributes its delegate-keyed and session-keyed keys.
func TestSubtreeReceiverKeysForDelegateIncludesSessionKey(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 2)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_target")

	got := make(map[receiverWatchKey]struct{})
	for _, key := range c.subtreeReceiverKeysForDelegate("dlg_target") {
		got[key] = struct{}{}
	}
	want := []receiverWatchKey{
		{sessionID: "child-dlg_target", delegateID: "dlg_target"},
		{sessionID: "child-dlg_target"},
		{sessionID: "child-dlg_child", delegateID: "dlg_child"},
		{sessionID: "child-dlg_child"},
	}
	if len(got) != len(want) {
		t.Fatalf("subtree receiver keys = %v, want %v", got, want)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("subtree receiver keys missing %v: got %v", key, got)
		}
	}
}

// attachNestedWatchHolder registers an ordinary (non-delegate) child session
// under parent with its own job manager — the holder a
// configureDescendantReceiverWatch lands in when resolveDescendantJobOwner walks
// liveSubagentSessions. Returns the child so a test can arm a watch in its
// manager or assert on it.
func attachNestedWatchHolder(t *testing.T, f *stableWatchRuntimeFixture, parent *Session, sessionID string) *Session {
	t.Helper()
	jm, err := newJobManager(f.controller.stateDir, sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("nested job manager: %v", err)
	}
	nested := &Session{
		id:                    sessionID,
		stateDir:              f.controller.stateDir,
		delegateController:    f.controller,
		delegateRootSessionID: f.root.ID(),
		jobManager:            jm,
		state:                 SessionIdle,
	}
	if parent.subagents == nil {
		parent.subagents = newSubagentManager(func(events.EventKind, events.EventData) {}, 4)
	}
	parent.subagents.mu.Lock()
	parent.subagents.subs[sessionID] = &subagent{id: sessionID, sess: nested, running: true}
	parent.subagents.mu.Unlock()
	t.Cleanup(func() {
		parent.subagents.mu.Lock()
		delete(parent.subagents.subs, sessionID)
		parent.subagents.mu.Unlock()
		_ = jm.closeStoreOnly()
	})
	return nested
}

// TestLiveWatchesDeliveringToDelegateIncludesNestedHolder pins the roborev
// finding that a session-keyed watch held in an ordinary nested subagent's
// manager (a real configureDescendantReceiverWatch destination) was missing from
// the stop inventory.
func TestLiveWatchesDeliveringToDelegateIncludesNestedHolder(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	observer := seedObserverDelegate(t, f)
	nested := attachNestedWatchHolder(t, f, observer, "nested-observer")
	watchID := installSessionKeyedWatch(t, nested.jobManager, "child-dlg_observer")

	rows := f.root.liveWatchesDeliveringToDelegate("dlg_observer")
	if len(rows) != 1 || rows[0].ID != watchID {
		t.Fatalf("nested-holder delivery rows = %#v, want id=%s", rows, watchID)
	}
}

// TestJobWatchClearSessionKeyedNestedHolder pins the clear half of the same
// finding: the receiver session's clear must route to the ordinary nested
// subagent manager that holds the watch.
func TestJobWatchClearSessionKeyedNestedHolder(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	observer := seedObserverDelegate(t, f)
	nested := attachNestedWatchHolder(t, f, observer, "nested-observer")
	watchID := installSessionKeyedWatch(t, nested.jobManager, "child-dlg_observer")

	receiver := runtimeSessionFor(f, "child-dlg_observer", "dlg_observer")
	if _, err := jobWatchToolWithContext(context.Background(), receiver, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096); err != nil {
		t.Fatalf("nested-holder clear: %v", err)
	}
	if _, _, found := nested.jobManager.watchReceiverIdentity(watchID); found {
		t.Fatal("nested-holder session-keyed watch survived the receiver session's clear")
	}
}

// TestJobWatchListInspectSessionKeyedReceiverShape pins the roborev finding that
// list and inspect queried only the delegate-keyed receiver shape and so could
// not discover a session-keyed watch for the receiver session.
func TestJobWatchListInspectSessionKeyedReceiverShape(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	watchID := installSessionKeyedWatch(t, f.rootJM, "child-dlg_observer")

	receiver := runtimeSessionFor(f, "child-dlg_observer", "dlg_observer")
	list := receiver.watchListToolResultWithDescendantReceivers(receiver.jobManager.watchListToolResult())
	found := false
	for _, row := range list.Watches {
		if row.WatchID == watchID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list rows = %#v, want session-keyed %s", list.Watches, watchID)
	}

	inspect, ok := receiver.inspectDescendantReceiverWatchByID(watchID)
	if !ok || !inspect.Watching || inspect.WatchID != watchID {
		t.Fatalf("inspect = %#v, ok=%v, want watching %s", inspect, ok, watchID)
	}
}

// TestWatchListToolResultForReceiverShapesKeepsHistoryLatestFirst pins the
// roborev note that formatting the two receiver-key shapes in separate passes
// and concatenating them can place an older history entry ahead of a newer one.
func TestWatchListToolResultForReceiverShapesKeepsHistoryLatestFirst(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.mu.Lock()
	jm.watchHistory = append(jm.watchHistory,
		watchHistoryEntry{
			id: "older-delegate", source: "parent", target: "job_a",
			receiverSessionID: "S", receiverDelegateID: "dlg_x",
			endReason: "cleared", endedAt: time.Unix(100, 0).UTC(),
		},
		watchHistoryEntry{
			id: "newer-session", source: "parent", target: "job_b",
			receiverSessionID: "S",
			endReason:         "cleared", endedAt: time.Unix(200, 0).UTC(),
		},
	)
	jm.mu.Unlock()

	got := jm.watchListToolResultForReceiverShapes("S", "dlg_x")
	if len(got.RecentWatches) != 2 {
		t.Fatalf("recent watches = %#v, want both receiver-key shapes", got.RecentWatches)
	}
	if got.RecentWatches[0].WatchID != "newer-session" || got.RecentWatches[1].WatchID != "older-delegate" {
		t.Fatalf("recent watch order = [%s, %s], want [newer-session, older-delegate] (latest first)",
			got.RecentWatches[0].WatchID, got.RecentWatches[1].WatchID)
	}
}

// TestLiveWatchesDeliveringToDelegateIncludesNestedReceiver pins the roborev
// finding that receiver keys came only from delegate ChildSessionIDs, so a
// session-keyed watch whose receiver is an ordinary nested subagent inside the
// stopped subtree never appeared in the stop inventory.
func TestLiveWatchesDeliveringToDelegateIncludesNestedReceiver(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	observer := seedObserverDelegate(t, f)
	attachNestedWatchHolder(t, f, observer, "nested-observer")
	watchID := installSessionKeyedWatch(t, f.rootJM, "nested-observer")

	rows := f.root.liveWatchesDeliveringToDelegate("dlg_observer")
	if len(rows) != 1 || rows[0].ID != watchID {
		t.Fatalf("nested-receiver delivery rows = %#v, want id=%s", rows, watchID)
	}
}

// TestJobWatchClearSessionKeyedNestedReceiver pins the authority half of the
// same finding: the delegate whose runtime subtree contains the nested receiver
// session must be able to clear its watch.
func TestJobWatchClearSessionKeyedNestedReceiver(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	observer := seedObserverDelegate(t, f)
	attachNestedWatchHolder(t, f, observer, "nested-observer")
	watchID := installSessionKeyedWatch(t, f.rootJM, "nested-observer")

	parent := runtimeSessionFor(f, "child-dlg_observer", "dlg_observer")
	if _, err := jobWatchToolWithContext(context.Background(), parent, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096); err != nil {
		t.Fatalf("nested-receiver clear: %v", err)
	}
	if _, _, found := f.rootJM.watchReceiverIdentity(watchID); found {
		t.Fatal("nested-receiver watch survived the anchoring delegate's clear")
	}
}

// TestWatchListToolResultWithDescendantReceiversOrdersAndCapsHistory pins the
// roborev finding that recent-watch rows from several managers were concatenated
// unsorted and beyond the ring cap.
func TestWatchListToolResultWithDescendantReceiversOrdersAndCapsHistory(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	observer := seedObserverDelegate(t, f)
	nested := attachNestedWatchHolder(t, f, observer, "nested-observer")
	receiver := runtimeSessionFor(f, "child-dlg_observer", "dlg_observer")

	seedHistory := func(jm *jobManager, entries ...watchHistoryEntry) {
		jm.mu.Lock()
		jm.watchHistory = append(jm.watchHistory, entries...)
		jm.mu.Unlock()
	}
	seedHistory(f.rootJM, watchHistoryEntry{
		id: "older", source: "parent", target: "job_a",
		receiverSessionID: "child-dlg_observer", receiverDelegateID: "dlg_observer",
		endReason: "cleared", endedAt: time.Unix(100, 0).UTC(),
	})
	extra := make([]watchHistoryEntry, 0, watchHistoryCap)
	for i := range watchHistoryCap {
		extra = append(extra, watchHistoryEntry{
			id: fmt.Sprintf("extra-%02d", i), source: "parent", target: "job_x",
			receiverSessionID: "child-dlg_observer",
			endReason:         "cleared", endedAt: time.Unix(int64(200+i), 0).UTC(),
		})
	}
	seedHistory(nested.jobManager, extra...)

	rows := receiver.watchListToolResultWithDescendantReceivers(receiver.jobManager.watchListToolResult())
	if len(rows.RecentWatches) != watchHistoryCap {
		t.Fatalf("recent watches = %d, want the %d-row cap", len(rows.RecentWatches), watchHistoryCap)
	}
	if rows.RecentWatches[0].WatchID != "extra-15" {
		t.Fatalf("latest recent row = %s, want extra-15", rows.RecentWatches[0].WatchID)
	}
}

// bindDelegateRuntime points a seeded delegate's live entry at its runtime
// session, so the controller sees it as a live runtime.
func bindDelegateRuntime(t *testing.T, controller *delegateTreeController, delegateID string, runtime *Session) {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if live := controller.live[delegateID]; live != nil {
		live.runtime = runtime
	}
}

// TestDelegateAnchoringSessionPicksNearestDelegate pins the roborev finding that
// anchoring a session-keyed receiver picked an arbitrary delegate: delegate
// runtimes are tracked as subagents of their parent, so a session appeared in
// every ancestor delegate's runtime subtree and map iteration decided the
// anchor.
func TestDelegateAnchoringSessionPicksNearestDelegate(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedDelegateControllerRunning(t, f.controller, "dlg_a", "")
	a := attachNestedWatchHolder(t, f, f.root, "child-dlg_a")
	bindDelegateRuntime(t, f.controller, "dlg_a", a)
	seedDelegateControllerRunning(t, f.controller, "dlg_b", "dlg_a")
	b := attachNestedWatchHolder(t, f, a, "child-dlg_b")
	bindDelegateRuntime(t, f.controller, "dlg_b", b)
	attachNestedWatchHolder(t, f, b, "s-child")

	for i := range 200 {
		if got := f.controller.delegateAnchoringSession("s-child"); got != "dlg_b" {
			t.Fatalf("iteration %d: anchoring delegate = %q, want the nearest delegate dlg_b", i, got)
		}
	}
}

// TestJobWatchClearSessionKeyedNearestDelegate pins the user-visible effect:
// the intermediate delegate B clears its own subagent's session-keyed watch.
func TestJobWatchClearSessionKeyedNearestDelegate(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedDelegateControllerRunning(t, f.controller, "dlg_a", "")
	a := attachNestedWatchHolder(t, f, f.root, "child-dlg_a")
	bindDelegateRuntime(t, f.controller, "dlg_a", a)
	seedDelegateControllerRunning(t, f.controller, "dlg_b", "dlg_a")
	b := attachNestedWatchHolder(t, f, a, "child-dlg_b")
	bindDelegateRuntime(t, f.controller, "dlg_b", b)
	attachNestedWatchHolder(t, f, b, "s-child")
	watchID := installSessionKeyedWatch(t, f.rootJM, "s-child")

	actor := runtimeSessionFor(f, "child-dlg_b", "dlg_b")
	if _, err := jobWatchToolWithContext(context.Background(), actor, map[string]any{
		"operation": "clear",
		"watch_id":  watchID,
	}, 4096); err != nil {
		t.Fatalf("nearest-delegate clear: %v", err)
	}
	if _, _, found := f.rootJM.watchReceiverIdentity(watchID); found {
		t.Fatal("watch survived the nearest delegate's clear")
	}
}

// TestWatchListToolResultWithDescendantReceiversDoesNotDuplicateLocal pins the
// roborev finding that the local manager's rows were merged twice: the holder
// whose manager produced `local` must be skipped.
func TestWatchListToolResultWithDescendantReceiversDoesNotDuplicateLocal(t *testing.T) {
	f := newStableWatchRuntimeBase(t, nil)
	seedObserverDelegate(t, f)
	observer := runtimeSessionFor(f, "child-dlg_observer", "dlg_observer")
	// Give the observer its OWN manager, so a receiver row is both visible in
	// `local` and reachable again through the holder scan.
	observerJM, err := newJobManager(f.controller.stateDir, observer.ID(), func(jobNotification) {})
	if err != nil {
		t.Fatalf("observer job manager: %v", err)
	}
	t.Cleanup(func() { _ = observerJM.closeStoreOnly() })
	observer.jobManager = observerJM
	if _, err := observerJM.configureWatch(watchArgs{
		Source:               "parent",
		Target:               runtimeMessageAliasCaller,
		Events:               []string{"communicate"},
		ReceiverSessionID:    observer.ID(),
		ReceiverDelegateID:   "dlg_observer",
		StableReceiver:       true,
		ReceiverSendInternal: true,
	}); err != nil {
		t.Fatalf("install local receiver watch: %v", err)
	}

	rows := observer.watchListToolResultWithDescendantReceivers(observerJM.watchListToolResult())
	if len(rows.Watches) != 1 {
		t.Fatalf("list rows = %d (%#v), want 1 (no local/descendant duplicate)", len(rows.Watches), rows.Watches)
	}
}
