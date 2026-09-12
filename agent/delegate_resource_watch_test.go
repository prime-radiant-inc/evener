package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
)

const expectedStableDelegateWatchSourceKind watchSourceKind = 3

func TestStableDelegateWatch_TypedSessionShellAndDelegateSources(t *testing.T) {
	tests := []struct {
		source string
		kind   watchSourceKind
	}{
		{source: "self", kind: watchSourceSelfSession},
		{source: "job_shell", kind: watchSourceConcreteJob},
		{source: "dlg_source", kind: expectedStableDelegateWatchSourceKind},
	}
	for _, test := range tests {
		t.Run(test.source, func(t *testing.T) {
			a, err := watchArgsFromToolArgs(map[string]any{
				"operation": "create",
				"source":    test.source,
				"events":    []any{"communicate"},
			})
			if err != nil {
				t.Fatalf("decode typed source %q: %v", test.source, err)
			}
			got, err := normalizeWatchSource(a.Source)
			if err != nil {
				t.Fatalf("normalize typed source %q: %v", test.source, err)
			}
			if got.Kind != test.kind || got.Public != test.source {
				t.Fatalf("typed source %q = %#v, want kind %d and public identity", test.source, got, test.kind)
			}
		})
	}
}

func TestStableDelegateWatch_DelegateReceiverIsImplicit(t *testing.T) {
	a := requireStableDelegateWatchArgs(t)
	if a.ReceiverSessionID != "" || a.ReceiverDelegateID != "" || a.Send != nil {
		t.Fatalf("public stable watch decoded receiver routing: %#v", a)
	}
	for _, forbidden := range []string{"send", "receiver_session_id", "receiver_delegate_id"} {
		args := map[string]any{
			"operation": "create",
			"source":    "dlg_source",
			"events":    []any{"communicate"},
		}
		args[forbidden] = map[string]any{"to": "dlg_other"}
		if forbidden != "send" {
			args[forbidden] = "dlg_other"
		}
		if _, err := watchArgsFromToolArgs(args); err == nil {
			t.Fatalf("public watch accepted explicit %s", forbidden)
		}
	}
	fixture := newStableWatchRuntimeFixture(t, nil)
	cfg := fixture.onlyWatchConfig(t)
	if cfg.receiverSessionID != fixture.root.ID() || cfg.receiverDelegateID != "" || !cfg.stableReceiver {
		t.Fatalf("runtime-derived stable receiver = session:%q delegate:%q stable:%t", cfg.receiverSessionID, cfg.receiverDelegateID, cfg.stableReceiver)
	}
	if public := watchResultFromConfig(cfg, false); public.Send != nil {
		t.Fatalf("public stable watch exposed internal receiver routing: %#v", public.Send)
	}
}

func TestStableDelegateWatch_ParentRequiresLeaseEdgeAndPersistedGrant(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime := delegateRuntime{owner: root}
	args := delegateArgs{Task: "persist parent watch grant", WatchParent: true}
	selection, err := root.selectSubagentModel(context.Background(), args.Model, args.AgentType)
	if err != nil {
		t.Fatalf("select model: %v", err)
	}
	toolNameCeiling := root.stableDelegateEffectiveToolNameCeiling(selection, args, "")
	descriptor, _, err := runtime.describe(context.Background(), args, args.Task, "", nil, selection, toolNameCeiling)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if binding, err := root.delegateController.ResolveParentWatchSource(delegateActor{lease: &started.lease}); err != nil || binding.runtime != root {
		t.Fatalf("resolve persisted parent watch grant = %#v, %v", binding, err)
	}
	raw, err := os.ReadFile(filepath.Join(jobsDir(root.stateDir, root.ID()), "delegates.jsonl"))
	if err != nil {
		t.Fatalf("read delegate store: %v", err)
	}
	if !strings.Contains(string(raw), `"parent_watch_granted":true`) {
		t.Fatalf("committed descriptor omitted parent watch grant: %s", raw)
	}
	if _, _, err := root.delegateController.FailCommittedStart(
		started.lease,
		delegatePermanentStartFailure(errors.New("test complete"), "construction_failed"),
		"construction_failed",
		nil,
	); err != nil {
		t.Fatalf("finish descriptor-only start: %v", err)
	}
}

func TestStableDelegateWatch_ParentGrantIsNonTransitive(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime := delegateRuntime{owner: root}
	describe := func(watchParent bool) map[string]any {
		t.Helper()
		args := delegateArgs{Task: "grant scope", WatchParent: watchParent}
		selection, err := root.selectSubagentModel(context.Background(), args.Model, args.AgentType)
		if err != nil {
			t.Fatalf("select model: %v", err)
		}
		toolNameCeiling := root.stableDelegateEffectiveToolNameCeiling(selection, args, "")
		descriptor, _, err := runtime.describe(context.Background(), args, args.Task, "", nil, selection, toolNameCeiling)
		if err != nil {
			t.Fatalf("describe: %v", err)
		}
		raw, err := json.Marshal(descriptor)
		if err != nil {
			t.Fatalf("marshal descriptor: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("unmarshal descriptor: %v", err)
		}
		return fields
	}
	granted := describe(true)
	ungranted := describe(false)
	if granted["parent_watch_granted"] != true {
		t.Fatalf("explicit grant descriptor = %#v", granted)
	}
	if _, inherited := ungranted["parent_watch_granted"]; inherited {
		t.Fatalf("ungranted descendant inherited parent watch capability: %#v", ungranted)
	}
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	parent := &Session{id: "child-dlg_parent"}
	c.mu.Lock()
	c.durable["dlg_parent"].Descriptor.ParentWatchGranted = true
	c.live["dlg_parent"].runtime = parent
	c.live["dlg_parent"].binding.runtime = parent
	c.mu.Unlock()
	childLease := delegateLease{delegateID: "dlg_child", generation: 1}
	if _, err := c.ResolveParentWatchSource(delegateActor{lease: &childLease}); err == nil {
		t.Fatal("child inherited its parent's parent-watch grant")
	}
	c.mu.Lock()
	c.durable["dlg_child"].Descriptor.ParentWatchGranted = true
	c.mu.Unlock()
	if binding, err := c.ResolveParentWatchSource(delegateActor{lease: &childLease}); err != nil || binding.runtime != parent {
		t.Fatalf("exact child parent-watch edge = %#v, %v", binding, err)
	}
}

func TestStableDelegateWatch_PreservesFiltersEveryCoalescingAndBudget(t *testing.T) {
	_ = requireStableDelegateWatchArgs(t)
	jm := newStableWatchTestJobManager(t)
	installWatchBelowValidation(t, jm, watchArgs{
		Source: "dlg_source",
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.tool"},
		Every:  2,
		EventFilter: &watchEventFilter{
			ToolName: "read_file",
			Status:   "ok",
		},
		Send: &watchSendArgs{To: "dlg_receiver"},
	})
	for range 4 {
		onSessionEventKD(jm, events.EventToolCallEnd, events.ToolCallEndData{ToolName: "read_file", Output: "ok"})
	}
	deliveries := jm.pendingWatchSendDeliveries(nil)
	if len(deliveries) != 1 {
		t.Fatalf("stable watch pending slots = %d, want one latest-frame slot", len(deliveries))
	}
	state := deliveries[0].state
	if state.UpdateSeq != 2 || state.CoalescedCount != 1 {
		t.Fatalf("stable watch coalescing = seq %d count %d, want 2/1", state.UpdateSeq, state.CoalescedCount)
	}
	cfg := deliveries[0].cfg
	jm.mu.Lock()
	cfg.conditionFires = watchDeliveryBudget
	crossed := jm.recordWatchDeliveryLocked(cfg)
	jm.mu.Unlock()
	if !crossed {
		t.Fatal("stable watch did not preserve the delivery budget")
	}
}

func TestStableDelegateWatch_PreservesListInspectAndClear(t *testing.T) {
	_ = requireStableDelegateWatchArgs(t)
	jm := newStableWatchTestJobManager(t)
	result, err := jm.configureWatch(watchArgs{
		Source:            "dlg_source",
		Target:            runtimeMessageAliasCaller,
		Events:            []string{"communicate"},
		ReceiverSessionID: "receiver-session",
	})
	if err != nil {
		t.Fatalf("configure stable watch: %v", err)
	}
	listed := jm.watchListToolResultForReceiver("receiver-session", "")
	if listed.Count != 1 || listed.Watches[0].WatchID != result.WatchID || listed.Watches[0].Source != "dlg_source" {
		t.Fatalf("stable watch list = %#v", listed)
	}
	inspected, found := jm.inspectReceiverWatchByID(result.WatchID, "receiver-session", "")
	if !found || inspected.Source != "dlg_source" {
		t.Fatalf("stable watch inspect = %#v found=%t", inspected, found)
	}
	cleared, err := jm.clearReceiverWatchByID(result.WatchID, "receiver-session", "")
	if err != nil || cleared.Watching {
		t.Fatalf("stable watch clear = %#v err=%v", cleared, err)
	}
	fixture := newStableWatchRuntimeBase(t, nil)
	result, err = fixture.sourceJM.configureWatch(watchArgs{
		Source:               "dlg_source",
		Target:               runtimeMessageAliasCaller,
		Events:               []string{"communicate"},
		ReceiverSessionID:    "receiver-session",
		ReceiverDelegateID:   "dlg_receiver",
		StableReceiver:       true,
		ReceiverSendInternal: true,
		SourceDelegateID:     "dlg_source",
		SourceGeneration:     1,
	})
	if err != nil {
		t.Fatalf("configure delegate-owned stable watch: %v", err)
	}
	watcher := &Session{
		id:                    "receiver-session",
		owningDelegateID:      "dlg_receiver",
		delegateController:    fixture.controller,
		delegateRootSessionID: fixture.root.ID(),
		jobManager:            fixture.rootJM,
	}
	listed = watcher.watchListToolResultWithDescendantReceivers(jobWatchListToolResult{})
	if listed.Count != 1 || listed.Watches[0].WatchID != result.WatchID {
		t.Fatalf("delegate-owned stable watch list = %#v", listed)
	}
	if inspected, found = watcher.inspectDescendantReceiverWatchByID(result.WatchID); !found || inspected.Source != "dlg_source" {
		t.Fatalf("delegate-owned stable watch inspect = %#v found=%t", inspected, found)
	}
	if cleared, found, err := watcher.clearStableReceiverWatchByID(result.WatchID); err != nil || !found || cleared.Watching {
		t.Fatalf("delegate-owned stable watch clear = %#v found=%t err=%v", cleared, found, err)
	}
}

func TestStableDelegateWatch_EnqueueFsyncPrecedesCursorAdvance(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	originalAppend := fixture.sourceJM.appendEvent
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if event.Kind != jobstore.EventWatchSendPending {
			return originalAppend(event)
		}
		close(entered)
		<-release
		return originalAppend(event)
	}
	done := make(chan struct{})
	go func() {
		onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "durable frame"})
		close(done)
	}()
	<-entered
	fixture.sourceJM.mu.Lock()
	pendingBeforeFsync := len(fixture.onlyWatchConfig(t).pending)
	fixture.sourceJM.mu.Unlock()
	if pendingBeforeFsync != 0 {
		close(release)
		<-done
		t.Fatalf("watch cursor advanced before pending-frame fsync: pending=%d", pendingBeforeFsync)
	}
	fixture.controller.mu.Lock()
	enqueuesBeforeFsync := len(fixture.controller.watchEnqueues)
	deliveriesBeforeFsync := len(fixture.controller.watchDeliveries)
	fixture.controller.mu.Unlock()
	if enqueuesBeforeFsync != 1 || deliveriesBeforeFsync != 0 {
		close(release)
		<-done
		t.Fatalf("receipt phase before source fsync = enqueue:%d delivery:%d, want 1/0", enqueuesBeforeFsync, deliveriesBeforeFsync)
	}
	close(release)
	<-done
	if pending := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(pending) != 1 {
		t.Fatalf("durable pending frames = %d, want 1", len(pending))
	} else if pending[0].state.SourceDelegateID != "dlg_source" || pending[0].state.SourceDelegateGeneration != 1 {
		t.Fatalf("durable stable source binding = %q/%d, want dlg_source/1", pending[0].state.SourceDelegateID, pending[0].state.SourceDelegateGeneration)
	}
	fixture.controller.mu.Lock()
	enqueuesAfterFsync := len(fixture.controller.watchEnqueues)
	deliveriesAfterFsync := len(fixture.controller.watchDeliveries)
	fixture.controller.mu.Unlock()
	if enqueuesAfterFsync != 0 || deliveriesAfterFsync != 1 {
		t.Fatalf("receipt phase after source fsync = enqueue:%d delivery:%d, want 0/1", enqueuesAfterFsync, deliveriesAfterFsync)
	}
}

func TestStableDelegateWatch_ControllerReceiptRunsAfterJobManagerUnlock(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	boundaries := 0
	fixture.sourceJM.watchReceiptBoundary = func() {
		boundaries++
		if !fixture.sourceJM.watchPersistMu.TryLock() {
			t.Fatal("stable watch entered the delegate controller while holding the job-manager persistence lock")
		}
		fixture.sourceJM.watchPersistMu.Unlock()
	}

	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "lock order"})
	if boundaries != 2 {
		t.Fatalf("controller receipt boundaries = %d, want enqueue admission and completion", boundaries)
	}
}

func TestStableDelegateWatch_ReceiverFsyncPrecedesDeliveredAck(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "receiver barrier"})
	pending := fixture.requireOnePending(t)
	fs.arm()
	done := make(chan error, 1)
	go func() {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		done <- err
	}()
	<-fs.syncEntered
	eventsBefore, err := fixture.sourceJM.store.LoadEvents()
	if err != nil {
		t.Fatalf("load watch journal: %v", err)
	}
	for _, event := range eventsBefore {
		if event.Kind == jobstore.EventWatchSendDelivered {
			fs.release()
			<-done
			t.Fatal("source acknowledgement preceded receiver durability barrier")
		}
	}
	if fold, err := readDelegateAttentionFold(fixture.rootTranscriptPath, fixture.root.ID()); err != nil {
		fs.release()
		<-done
		t.Fatalf("fold receiver transcript at fsync barrier: %v", err)
	} else if _, ok := fold.content[stableWatchAttentionID(pending.state)]; !ok {
		fs.release()
		<-done
		t.Fatal("receiver attention was not present at its durability barrier")
	}
	fs.release()
	if err := <-done; err != nil {
		t.Fatalf("deliver watch frame: %v", err)
	}
	if pending := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(pending) != 0 {
		t.Fatalf("source cursor remained pending after receiver fsync: %#v", pending)
	}
}

func TestStableDelegateWatch_SourceAcknowledgementWaitsForRootWakeArm(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "ordered receiver wake"})
	ackEntered := make(chan struct{}, 1)
	releaseAck := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseAck)
		}
	}()
	originalAppend := fixture.sourceJM.appendEvent
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventWatchSendDelivered {
			ackEntered <- struct{}{}
			<-releaseAck
		}
		return originalAppend(event)
	}
	wakes := make(chan struct{}, 1)
	fixture.root.SetNotifyFunc(func() { wakes <- struct{}{} })
	done := make(chan error, 1)
	go func() {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		done <- err
	}()
	<-ackEntered
	select {
	case <-wakes:
	default:
		close(releaseAck)
		released = true
		<-done
		t.Fatal("source acknowledgement began before root attention was armed")
	}
	if receipt := fixture.sourceJM.stableWatchReceipt(fixture.requireOnePending(t).state.DeliveryID); receipt == nil {
		close(releaseAck)
		released = true
		<-done
		t.Fatal("source acknowledgement released its receipt before durable settlement")
	}
	close(releaseAck)
	released = true
	if err := <-done; err != nil {
		t.Fatalf("deliver watch frame: %v", err)
	}
}

func TestStableDelegateWatch_LaterCoalescedUpdateSurvivesEarlierAck(t *testing.T) {
	key := jobstore.WatchSendKey{
		VisibleSessionID:        "source-session",
		WatchID:                 "watch-stable",
		WatchTarget:             "dlg_source",
		ResolvedWatchedIdentity: "dlg_source",
		ResolvedSendTo:          "dlg_receiver",
		WatchGeneration:         "wg_stable",
	}
	events := []jobstore.Event{
		{Kind: jobstore.EventWatchSendPending, Seq: 1, WatchSend: &jobstore.WatchSendState{Key: key, DeliveryID: "wd_old", UpdateSeq: 1, Frame: "old"}},
		{Kind: jobstore.EventWatchSendPending, Seq: 2, WatchSend: &jobstore.WatchSendState{Key: key, DeliveryID: "wd_new", UpdateSeq: 2, Frame: "new"}},
		{Kind: jobstore.EventWatchSendDelivered, Seq: 3, WatchSend: &jobstore.WatchSendState{Key: key, DeliveryID: "wd_old", UpdateSeq: 1}},
	}
	got := jobstore.FoldWatchSends(events).Pending[key]
	if got == nil || got.DeliveryID != "wd_new" || got.UpdateSeq != 2 || got.Frame != "new" {
		t.Fatalf("later coalesced update lost after earlier ack: %#v", got)
	}
}

func TestStableDelegateWatch_CoalescingRetainsInflightReceiverReceipt(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "old frame"})
	old := fixture.requireOnePending(t).state
	wakes := make(chan struct{}, 1)
	fixture.root.SetNotifyFunc(func() { wakes <- struct{}{} })
	fs.arm()
	done := make(chan error, 1)
	go func() {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		done <- err
	}()
	<-fs.syncEntered
	released := false
	defer func() {
		if !released {
			fs.release()
		}
	}()

	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "new frame"})
	newer := fixture.requireOnePending(t).state
	fixture.controller.mu.Lock()
	liveReceipts := len(fixture.controller.watchDeliveries)
	fixture.controller.mu.Unlock()
	if liveReceipts != 2 {
		t.Fatalf("controller delivery receipts during coalescing = %d, want old in-flight plus replacement", liveReceipts)
	}
	result, _, _, err := fixture.controller.StopSubtree(rootDelegateActor(fixture.root.ID()), "dlg_source")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	stop := fixture.controller.stopForResult(result)
	if got := len(stop.watchDeliveries); got != 2 {
		t.Fatalf("stop captured watch delivery receipts = %d, want 2", got)
	}

	fs.release()
	released = true
	if err := <-done; err != nil {
		t.Fatalf("release in-flight receiver append: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("durable coalesced receiver attention was stranded after receipt release")
	}
	eventsLog := loadJobStoreEvents(t, fixture.sourceJM)
	delivered := 0
	for _, event := range eventsLog {
		if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend != nil && event.WatchSend.DeliveryID == old.DeliveryID && event.WatchSend.UpdateSeq == old.UpdateSeq {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("old source acknowledgements before receiver wake = %d, want exactly 1", delivered)
	}
	pending, err := fixture.sourceJM.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("load watch sends after old acknowledgement: %v", err)
	}
	current := pending.Pending[newer.Key]
	if current == nil || current.DeliveryID != newer.DeliveryID || current.UpdateSeq != newer.UpdateSeq {
		t.Fatalf("newer coalesced cursor after old acknowledgement = %#v, want delivery %q seq %d", current, newer.DeliveryID, newer.UpdateSeq)
	}
	fixture.root.attentionMu.Lock()
	_, armed := fixture.root.rootAttentionWakeIDs[stableWatchAttentionID(old)]
	fixture.root.attentionMu.Unlock()
	if !armed {
		t.Fatalf("coalesced receiver attention %q was not armed", stableWatchAttentionID(old))
	}
}

func TestStableDelegateWatch_SupersededAckFailureRetainsArmedAttentionBeforeCurrentCursor(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	wakes := make(chan struct{}, 2)
	fixture.root.SetNotifyFunc(func() { wakes <- struct{}{} })
	old, newer, originalAppend := seedSupersededStableWatchAckFailure(t, fixture, fs)

	select {
	case <-wakes:
	default:
		t.Fatal("failed old source acknowledgement did not retain already-armed receiver attention")
	}
	if receipt := fixture.sourceJM.stableWatchReceipt(old.DeliveryID); receipt == nil {
		t.Fatal("failed old source acknowledgement released its delivery receipt")
	}
	if !fixture.source.sessionWorkPending() {
		t.Fatal("failed old source acknowledgement disappeared from session work-pending")
	}

	newAckEntered := make(chan struct{}, 1)
	releaseNewAck := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseNewAck)
		}
	}()
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, newer) {
			newAckEntered <- struct{}{}
			<-releaseNewAck
		}
		return originalAppend(event)
	}
	done := make(chan error, 1)
	go func() {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		done <- err
	}()
	<-newAckEntered

	eventsAtNewAck := loadJobStoreEvents(t, fixture.sourceJM)
	oldAcknowledgements := countExactWatchSendEvents(eventsAtNewAck, jobstore.EventWatchSendDelivered, old)
	oldReceipt := fixture.sourceJM.stableWatchReceipt(old.DeliveryID)
	newReceipt := fixture.sourceJM.stableWatchReceipt(newer.DeliveryID)
	folded, err := fixture.sourceJM.store.LoadWatchSends()
	if err != nil {
		close(releaseNewAck)
		released = true
		<-done
		t.Fatalf("load watch sends at newer acknowledgement: %v", err)
	}
	current := folded.Pending[newer.Key]
	fixture.root.attentionMu.Lock()
	_, oldArmed := fixture.root.rootAttentionWakeIDs[stableWatchAttentionID(old)]
	fixture.root.attentionMu.Unlock()
	close(releaseNewAck)
	released = true
	if err := <-done; err != nil {
		t.Fatalf("redrain stable watch sends: %v", err)
	}

	if oldAcknowledgements != 1 {
		t.Fatalf("old source acknowledgements before current cursor = %d, want exactly 1", oldAcknowledgements)
	}
	if oldReceipt != nil {
		t.Fatal("old delivery receipt remained held after exact acknowledgement retry")
	}
	if newReceipt == nil {
		t.Fatal("current delivery receipt released before its blocked acknowledgement")
	}
	if current == nil || current.DeliveryID != newer.DeliveryID || current.UpdateSeq != newer.UpdateSeq {
		t.Fatalf("newer cursor during old acknowledgement retry = %#v, want delivery %q seq %d", current, newer.DeliveryID, newer.UpdateSeq)
	}
	if !oldArmed {
		t.Fatalf("old attention %q was not armed after exact acknowledgement retry", stableWatchAttentionID(old))
	}
}

func TestStableDelegateWatch_ConcurrentRedrainsClaimSupersededAckOnce(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	old, newer, originalAppend := seedSupersededStableWatchAckFailure(t, fixture, fs)
	cfg := fixture.onlyWatchConfig(t)
	fixture.sourceJM.mu.Lock()
	deliveriesBefore := cfg.deliveries
	fixture.sourceJM.mu.Unlock()

	oldAckEntered := make(chan struct{}, 2)
	releaseOldAck := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseOldAck)
		}
		fixture.sourceJM.appendEvent = originalAppend
	}()
	stopAfterRetry := errors.New("stop after exact retry")
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, old) {
			oldAckEntered <- struct{}{}
			<-releaseOldAck
			return originalAppend(event)
		}
		if exactWatchSendEvent(event, jobstore.EventWatchSendPending, newer) {
			return stopAfterRetry
		}
		return originalAppend(event)
	}
	drain := func(done chan<- error) {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		done <- err
	}
	firstDone := make(chan error, 1)
	go drain(firstDone)
	<-oldAckEntered
	secondDone := make(chan error, 1)
	go drain(secondDone)
	secondFinished := false
	var secondErr error
	select {
	case <-oldAckEntered:
	case secondErr = <-secondDone:
		secondFinished = true
	}
	close(releaseOldAck)
	released = true
	firstErr := <-firstDone
	if !secondFinished {
		secondErr = <-secondDone
	}
	if !errors.Is(firstErr, stopAfterRetry) || secondErr != nil {
		t.Fatalf("concurrent redrain errors = first:%v second:%v, want first current delivery stopped and second claimed", firstErr, secondErr)
	}

	eventsLog := loadJobStoreEvents(t, fixture.sourceJM)
	if got := countExactWatchSendEvents(eventsLog, jobstore.EventWatchSendDelivered, old); got != 1 {
		t.Fatalf("concurrent old source acknowledgements = %d, want exactly 1", got)
	}
	fixture.sourceJM.mu.Lock()
	deliveriesAfter := cfg.deliveries
	fixture.sourceJM.mu.Unlock()
	if deliveriesAfter != deliveriesBefore+1 {
		t.Fatalf("concurrent old delivery budget = %d, want %d", deliveriesAfter, deliveriesBefore+1)
	}
	if receipt := fixture.sourceJM.stableWatchReceipt(old.DeliveryID); receipt != nil {
		t.Fatal("old delivery receipt remained held after claimed acknowledgement")
	}
	folded, err := fixture.sourceJM.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("load newer cursor after concurrent retry: %v", err)
	}
	current := folded.Pending[newer.Key]
	if current == nil || current.DeliveryID != newer.DeliveryID || current.UpdateSeq != newer.UpdateSeq {
		t.Fatalf("newer cursor after concurrent retry = %#v, want delivery %q seq %d", current, newer.DeliveryID, newer.UpdateSeq)
	}
}

func TestStableDelegateWatch_RetryAddedDuringClaimSettlesBeforeCurrentCursor(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	retryA, staleB, originalAppend := seedSupersededStableWatchAckFailure(t, fixture, fs)
	cfg := fixture.onlyWatchConfig(t)

	retryAAckEntered := make(chan struct{}, 1)
	releaseRetryAAck := make(chan struct{})
	currentAckEntered := make(chan struct{}, 1)
	releaseCurrentAck := make(chan struct{})
	releasedRetryA := false
	releasedCurrent := false
	defer func() {
		if !releasedRetryA {
			close(releaseRetryAAck)
		}
		if !releasedCurrent {
			close(releaseCurrentAck)
		}
		fixture.sourceJM.appendEvent = originalAppend
	}()
	staleBAckFailure := errors.New("second stale source acknowledgement failed")
	var staleBAckFailed atomic.Bool
	var current jobstore.WatchSendState
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, retryA) {
			retryAAckEntered <- struct{}{}
			<-releaseRetryAAck
		}
		if exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, staleB) && staleBAckFailed.CompareAndSwap(false, true) {
			return staleBAckFailure
		}
		if exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, current) {
			currentAckEntered <- struct{}{}
			<-releaseCurrentAck
		}
		return originalAppend(event)
	}

	fs.rearm()
	staleBDone := make(chan error, 1)
	go func() {
		_, err := fixture.sourceJM.deliverStableWatchSend(cfg, staleB)
		staleBDone <- err
	}()
	<-fs.syncEntered
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "current frame"})
	current = fixture.requireOnePending(t).state

	drainDone := make(chan error, 1)
	go func() {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		drainDone <- err
	}()
	<-retryAAckEntered
	fs.release()
	if err := <-staleBDone; !errors.Is(err, staleBAckFailure) {
		close(releaseRetryAAck)
		releasedRetryA = true
		<-drainDone
		t.Fatalf("second stale delivery error = %v, want %v", err, staleBAckFailure)
	}
	close(releaseRetryAAck)
	releasedRetryA = true
	<-currentAckEntered

	eventsAtCurrentAck := loadJobStoreEvents(t, fixture.sourceJM)
	staleBAcknowledgements := countExactWatchSendEvents(eventsAtCurrentAck, jobstore.EventWatchSendDelivered, staleB)
	staleBReceipt := fixture.sourceJM.stableWatchReceipt(staleB.DeliveryID)
	close(releaseCurrentAck)
	releasedCurrent = true
	if err := <-drainDone; err != nil {
		t.Fatalf("drain after retry added during claim: %v", err)
	}

	if staleBAcknowledgements != 1 {
		t.Fatalf("second stale source acknowledgements before current cursor = %d, want exactly 1", staleBAcknowledgements)
	}
	if staleBReceipt != nil {
		t.Fatal("second stale delivery receipt remained held at current cursor acknowledgement")
	}
}

func TestStableDelegateWatch_RestartRepairsReceiverDurableSourceUnacked(t *testing.T) {
	fixture := newStableWatchRuntimeFixture(t, nil)
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "crash repair"})
	pending := fixture.requireOnePending(t)
	if !pending.state.StableReceiver || pending.state.ReceiverSessionID != fixture.root.ID() {
		t.Fatalf("stable pending identity = %#v", pending.state)
	}
	sourceAckFailure := errors.New("source delivered acknowledgement failed")
	originalAppend := fixture.sourceJM.appendEvent
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventWatchSendDelivered {
			return sourceAckFailure
		}
		return originalAppend(event)
	}
	if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, ""); !errors.Is(err, sourceAckFailure) {
		t.Fatalf("first delivery error = %v, want %v", err, sourceAckFailure)
	}
	if got := countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(pending.state)); got != 1 {
		t.Fatalf("receiver attention count after failed source ack = %d, want 1", got)
	}
	fixture.sourceJM.releaseStableWatchReceipt(pending.state.DeliveryID)
	if err := fixture.sourceJM.closeStoreOnly(); err != nil {
		t.Fatalf("close source journal: %v", err)
	}
	fixture.sourceJM = nil
	if err := fixture.root.closeAttachedTranscript(); err != nil {
		t.Fatalf("close receiver transcript: %v", err)
	}
	if err := fixture.controller.store.Close(); err != nil {
		t.Fatalf("close delegate controller: %v", err)
	}
	seedBootstrapControllerJournal(t, fixture)
	fresh := &Session{
		id:       fixture.root.ID(),
		stateDir: fixture.controller.stateDir,
		cfg: SessionConfig{
			MaxConcurrentDelegateTurns: 2,
		},
		state: SessionIdle,
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap stable watch repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.delegateController.store.Close() })
	if got := countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(pending.state)); got != 1 {
		t.Fatalf("receiver attention count after restart repair = %d, want 1", got)
	}
	restarted, err := jobstore.Open(filepath.Join(jobsDir(fixture.controller.stateDir, fixture.source.ID()), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open repaired source journal: %v", err)
	}
	defer restarted.Close()
	folded, err := restarted.LoadWatchSends()
	if err != nil {
		t.Fatalf("fold repaired source journal: %v", err)
	}
	if len(folded.Pending) != 0 {
		t.Fatalf("source pending after restart repair = %#v", folded.Pending)
	}

	t.Run("cold arm failure retains source", func(t *testing.T) {
		fixture := newStableWatchRuntimeBase(t, nil)
		seedDelegateControllerIdle(t, fixture.controller, "dlg_receiver", "dlg_source")
		receiverID := fixture.controller.durable["dlg_receiver"].Descriptor.ChildSessionID
		receiverPath := transcriptPath(fixture.controller.stateDir, receiverID)
		writer, err := transcript.NewWriter(receiverPath, transcript.Header{SessionID: receiverID})
		if err != nil {
			t.Fatalf("create cold watch receiver transcript: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close cold watch receiver transcript: %v", err)
		}
		if _, err := fixture.sourceJM.configureWatch(watchArgs{
			Source:               "dlg_source",
			Target:               runtimeMessageAliasCaller,
			Events:               []string{"communicate"},
			ReceiverSessionID:    receiverID,
			ReceiverDelegateID:   "dlg_receiver",
			StableReceiver:       true,
			ReceiverSendInternal: true,
			SourceDelegateID:     "dlg_source",
			SourceGeneration:     1,
		}); err != nil {
			t.Fatalf("configure cold receiver watch: %v", err)
		}
		onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "cold arm retry"})
		pending := fixture.requireOnePending(t)
		if err := fixture.controller.store.Close(); err != nil {
			t.Fatalf("close controller before cold arm: %v", err)
		}
		if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, ""); err == nil {
			t.Fatal("cold watch arm unexpectedly succeeded with closed delegate journal")
		}
		if got := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(got) != 1 || got[0].state.DeliveryID != pending.state.DeliveryID {
			t.Fatalf("failed cold watch arm source = %#v, want exact pending delivery", got)
		}
		if receipt := fixture.sourceJM.stableWatchReceipt(pending.state.DeliveryID); receipt == nil {
			t.Fatal("failed cold watch arm released its delivery receipt")
		}
		reopened, err := delegatestore.Open(fixture.controllerStorePath)
		if err != nil {
			t.Fatalf("reopen delegate journal: %v", err)
		}
		restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
			store:         reopened,
			rootRuntime:   fixture.root,
			rootSessionID: fixture.root.ID(),
			stateDir:      fixture.controller.stateDir,
			turnLimit:     2,
			driveLimit:    1,
			now:           fixture.controller.now,
		})
		if err != nil {
			_ = reopened.Close()
			t.Fatalf("restart delegate controller: %v", err)
		}
		t.Cleanup(func() { _ = restarted.store.Close() })
		fixture.root.delegateController = restarted
		fixture.source.delegateController = restarted
		fixture.sourceJM.delegateController = restarted
		if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, ""); err != nil {
			t.Fatalf("retry cold watch arm: %v", err)
		}
		if got := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(got) != 0 {
			t.Fatalf("retried cold watch source remained pending: %#v", got)
		}
		restarted.mu.Lock()
		armed := restarted.durable["dlg_receiver"].NeedsAttention
		restarted.mu.Unlock()
		if !armed {
			t.Fatal("retried cold watch attention was not journaled")
		}
	})
}

func TestStableDelegateWatch_RestartRepairsSupersededReceiverDurableSourceUnacked(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	old, newer, _ := seedSupersededStableWatchAckFailure(t, fixture, fs)
	if got := countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(old)); got != 1 {
		t.Fatalf("old receiver attention count after failed source ack = %d, want 1", got)
	}
	if got := countExactWatchSendEvents(loadJobStoreEvents(t, fixture.sourceJM), jobstore.EventWatchSendDelivered, old); got != 0 {
		t.Fatalf("old source acknowledgements before restart = %d, want 0", got)
	}
	fixture.sourceJM.releaseStableWatchReceipt(old.DeliveryID)
	fixture.sourceJM.releaseStableWatchReceipt(newer.DeliveryID)
	if err := fixture.sourceJM.closeStoreOnly(); err != nil {
		t.Fatalf("close source journal: %v", err)
	}
	fixture.sourceJM = nil
	if err := fixture.root.closeAttachedTranscript(); err != nil {
		t.Fatalf("close receiver transcript: %v", err)
	}
	if err := fixture.controller.store.Close(); err != nil {
		t.Fatalf("close delegate controller: %v", err)
	}
	seedBootstrapControllerJournal(t, fixture)
	fresh := &Session{
		id:       fixture.root.ID(),
		stateDir: fixture.controller.stateDir,
		cfg: SessionConfig{
			MaxConcurrentDelegateTurns: 2,
		},
		state: SessionIdle,
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap superseded stable watch repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.delegateController.store.Close() })
	if got := countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(old)); got != 1 {
		t.Fatalf("old receiver attention count after restart repair = %d, want 1", got)
	}
	if got := countAttentionEntries(t, fixture.rootTranscriptPath, stableWatchAttentionID(newer)); got != 1 {
		t.Fatalf("new receiver attention count after restart repair = %d, want 1", got)
	}
	restarted, err := jobstore.Open(filepath.Join(jobsDir(fixture.controller.stateDir, fixture.source.ID()), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open repaired source journal: %v", err)
	}
	defer restarted.Close()
	eventsLog, err := restarted.LoadEvents()
	if err != nil {
		t.Fatalf("load repaired source journal: %v", err)
	}
	if got := countExactWatchSendEvents(eventsLog, jobstore.EventWatchSendDelivered, old); got != 1 {
		t.Fatalf("old source acknowledgements after restart repair = %d, want exactly 1", got)
	}
	if got := countExactWatchSendEvents(eventsLog, jobstore.EventWatchSendDelivered, newer); got != 1 {
		t.Fatalf("new source acknowledgements after restart repair = %d, want exactly 1", got)
	}
}

func TestStableDelegateWatch_StopFencesAndDrainsBothReceiptClasses(t *testing.T) {
	t.Run("live receipts", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, c, "dlg_source", "")
		enqueue, err := c.BeginWatchEnqueue("dlg_source", 1, "", "wd_stop", 1, false)
		if err != nil {
			t.Fatalf("BeginWatchEnqueue: %v", err)
		}
		result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_source")
		if err != nil {
			t.Fatalf("StopSubtree: %v", err)
		}
		if _, err := c.BeginWatchEnqueue("dlg_source", 1, "", "wd_late", 1, false); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("watch enqueue admitted after stop: %v", err)
		}
		delivery, err := c.CompleteWatchEnqueue(enqueue)
		if err != nil {
			t.Fatalf("CompleteWatchEnqueue: %v", err)
		}
		if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_source", generation: 1}, delegateFinish{}); err != nil {
			t.Fatalf("FinishGeneration: %v", err)
		}
		if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
			t.Fatalf("Reconcile with delivery receipt: %v", err)
		}
		select {
		case <-result.done:
			t.Fatal("stable source stop completed before delivery receipt drained")
		default:
		}
		if err := c.CompleteWatchDelivery(delivery); err != nil {
			t.Fatalf("CompleteWatchDelivery: %v", err)
		}
		if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
			t.Fatalf("Reconcile after receipt drain: %v", err)
		}
		select {
		case <-result.done:
		default:
			t.Fatal("stable source stop remained pending after both receipt classes drained")
		}
	})

	t.Run("restart cleanup", func(t *testing.T) {
		fixture := newStableWatchRuntimeBase(t, nil)
		seedDelegateControllerRunning(t, fixture.controller, "dlg_receiver", "dlg_source")
		receiverID := "child-dlg_receiver"
		receiver := &Session{
			id:                    receiverID,
			stateDir:              fixture.controller.stateDir,
			delegateController:    fixture.controller,
			delegateRootSessionID: fixture.root.ID(),
			owningDelegateID:      "dlg_receiver",
			state:                 SessionIdle,
		}
		receiverPath := transcriptPath(fixture.controller.stateDir, receiverID)
		receiverWriter, err := transcript.NewWriter(receiverPath, transcript.Header{SessionID: receiverID})
		if err != nil {
			t.Fatalf("new receiver transcript: %v", err)
		}
		receiver.attachTranscript(receiverWriter)
		fixture.controller.mu.Lock()
		fixture.controller.live["dlg_receiver"].runtime = receiver
		fixture.controller.live["dlg_receiver"].binding.runtime = receiver
		fixture.controller.mu.Unlock()
		if _, err := fixture.sourceJM.configureWatch(watchArgs{
			Source:               "dlg_source",
			Target:               runtimeMessageAliasCaller,
			Events:               []string{"communicate"},
			ReceiverSessionID:    receiverID,
			ReceiverDelegateID:   "dlg_receiver",
			StableReceiver:       true,
			ReceiverSendInternal: true,
			SourceDelegateID:     "dlg_source",
			SourceGeneration:     1,
		}); err != nil {
			t.Fatalf("configure nested stable watch: %v", err)
		}
		onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "stop cleanup"})
		pending := fixture.requireOnePending(t)
		if _, _, _, err := fixture.controller.StopSubtree(rootDelegateActor(fixture.root.ID()), "dlg_source"); err != nil {
			t.Fatalf("StopSubtree: %v", err)
		}
		ackFailure := errors.New("stop cleanup source ack failed")
		originalAppend := fixture.sourceJM.appendEvent
		fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
			if event.Kind == jobstore.EventWatchSendDelivered {
				return ackFailure
			}
			return originalAppend(event)
		}
		if _, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, ""); !errors.Is(err, ackFailure) {
			t.Fatalf("deliver before crash = %v, want %v", err, ackFailure)
		}
		attentionID := stableWatchAttentionID(pending.state)
		fixture.sourceJM.releaseStableWatchReceipt(pending.state.DeliveryID)
		if err := fixture.sourceJM.closeStoreOnly(); err != nil {
			t.Fatalf("close source journal: %v", err)
		}
		fixture.sourceJM = nil
		if err := receiver.closeAttachedTranscript(); err != nil {
			t.Fatalf("close receiver transcript: %v", err)
		}
		if err := fixture.root.closeAttachedTranscript(); err != nil {
			t.Fatalf("close root transcript: %v", err)
		}
		if err := fixture.controller.store.Close(); err != nil {
			t.Fatalf("close controller: %v", err)
		}
		seedBootstrapControllerJournal(t, fixture)
		fresh := &Session{
			id:       fixture.root.ID(),
			stateDir: fixture.controller.stateDir,
			cfg:      SessionConfig{MaxConcurrentDelegateTurns: 2},
			state:    SessionIdle,
		}
		if err := fresh.bootstrapDelegateResources(); err != nil {
			t.Fatalf("bootstrap stopped stable watch: %v", err)
		}
		t.Cleanup(func() { _ = fresh.delegateController.store.Close() })
		fresh.delegateController.mu.Lock()
		stopPending := fresh.delegateController.stop != nil
		fresh.delegateController.mu.Unlock()
		if stopPending {
			t.Fatal("restored stop remained pending after stable watch cleanup")
		}
		fold, err := readDelegateAttentionFold(receiverPath, receiverID)
		if err != nil {
			t.Fatalf("fold stopped receiver attention: %v", err)
		}
		if fold.resolutions[attentionID] != delegateAttentionDiscarded {
			t.Fatalf("stopped receiver resolution = %q, want %q", fold.resolutions[attentionID], delegateAttentionDiscarded)
		}
		repaired, err := jobstore.Open(filepath.Join(jobsDir(fixture.controller.stateDir, fixture.source.ID()), "jobs.jsonl"))
		if err != nil {
			t.Fatalf("open stopped source journal: %v", err)
		}
		defer repaired.Close()
		watchSends, err := repaired.LoadWatchSends()
		if err != nil {
			t.Fatalf("fold stopped source journal: %v", err)
		}
		if len(watchSends.Pending) != 0 {
			t.Fatalf("stopped source cursors remained pending: %#v", watchSends.Pending)
		}
	})
}

func TestStableDelegateWatch_TerminalFramePrecedesEndNotice(t *testing.T) {
	_ = requireStableDelegateWatchArgs(t)
	jm := newStableWatchTestJobManager(t)
	installWatchBelowValidation(t, jm, watchArgs{
		Source: "dlg_source",
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: "dlg_receiver"},
	})
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "terminal frame", EndTurn: true})
	deliveries := jm.pendingWatchSendDeliveries(nil)
	if len(deliveries) != 1 || !strings.Contains(deliveries[0].state.Frame, "terminal frame") {
		t.Fatalf("terminal watch frame = %#v", deliveries)
	}
	stored, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load watch events: %v", err)
	}
	if indexWatchEvent(stored, jobstore.EventWatchSendPending) < 0 {
		t.Fatal("terminal frame was not durable before watch teardown")
	}
}

func TestStableDelegateWatch_RestartCancellationEmitsEndNotice(t *testing.T) {
	_ = requireStableDelegateWatchArgs(t)
	stateDir := t.TempDir()
	jm, err := newJobManagerNoSync(stateDir, "source-session", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new source job manager: %v", err)
	}
	result, err := jm.configureWatch(watchArgs{
		Source:               "dlg_source",
		Target:               runtimeMessageAliasCaller,
		Events:               []string{"communicate"},
		ReceiverSessionID:    "receiver-session",
		ReceiverDelegateID:   "dlg_receiver",
		StableReceiver:       true,
		ReceiverSendInternal: true,
		SourceDelegateID:     "dlg_source",
		SourceGeneration:     1,
	})
	if err != nil {
		t.Fatalf("configure stable watch: %v", err)
	}
	if err := jm.closeStoreOnly(); err != nil {
		t.Fatalf("close source job manager: %v", err)
	}
	restarted, err := newJobManagerNoSync(stateDir, "source-session", func(jobNotification) {})
	if err != nil {
		t.Fatalf("restart source job manager: %v", err)
	}
	t.Cleanup(func() { _ = restarted.closeStoreOnly() })
	controller, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerRunning(t, controller, "dlg_source", "")
	seedDelegateControllerRunning(t, controller, "dlg_receiver", "")
	if _, err := controller.FinishGeneration(delegateLease{delegateID: "dlg_source", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish watched generation before restart notice: %v", err)
	}
	restarted.delegateController = controller
	if err := restarted.noticeUnrestoredWatchEnds(); err != nil {
		t.Fatalf("notice unrestored watch end: %v", err)
	}
	record, err := restarted.store.LoadWatchSends()
	if err != nil {
		t.Fatalf("load restart end notice: %v", err)
	}
	if len(record.Pending) != 1 {
		t.Fatalf("restart end notices = %#v, want one", record.Pending)
	}
	for _, pending := range record.Pending {
		if !pending.EndNotice || !strings.Contains(pending.Frame, "watch ended") || !strings.Contains(pending.Frame, "dlg_source") {
			t.Fatalf("restart end notice for %s = %#v", result.WatchID, pending)
		}
		if !pending.StableReceiver || pending.ReceiverSessionID != "receiver-session" || pending.ReceiverDelegateID != "dlg_receiver" || pending.SourceDelegateID != "dlg_source" || pending.SourceDelegateGeneration != 1 {
			t.Fatalf("restart end notice lost stable routing identity: %#v", pending)
		}
	}
}

func TestStableDelegateWatch_LegacyDelegateJobRowFailsClosed(t *testing.T) {
	events := []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "job_legacy", Type: jobstore.JobType(delegateResourceType)},
		{Kind: jobstore.EventWatchRegistered, WatchID: "watch_legacy", Watch: &jobstore.WatchEvent{Target: "job_legacy"}},
	}
	legacy := map[string]struct{}{"job_legacy": {}}
	watchID, found := firstLegacyDelegateWatch(events, legacy)
	if !found || watchID != "watch_legacy" {
		t.Fatalf("legacy delegate watch preflight = %q/%t", watchID, found)
	}
}

func requireStableDelegateWatchArgs(t *testing.T) watchArgs {
	t.Helper()
	a, err := watchArgsFromToolArgs(map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"communicate"},
	})
	if err != nil {
		t.Fatalf("stable delegate watch source rejected: %v", err)
	}
	return a
}

func newStableWatchTestJobManager(t *testing.T) *jobManager {
	t.Helper()
	jm, err := newJobManager(t.TempDir(), "source-session", func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	t.Cleanup(func() { _ = jm.closeStoreOnly() })
	return jm
}

func indexWatchEvent(events []jobstore.Event, kind jobstore.EventKind) int {
	for i, event := range events {
		if event.Kind == kind {
			return i
		}
	}
	return -1
}

type stableWatchRuntimeFixture struct {
	controller          *delegateTreeController
	root                *Session
	source              *Session
	rootJM              *jobManager
	sourceJM            *jobManager
	rootTranscriptPath  string
	controllerStorePath string
}

func newStableWatchRuntimeFixture(t *testing.T, rootFS afero.Fs) *stableWatchRuntimeFixture {
	t.Helper()
	fixture := newStableWatchRuntimeBase(t, rootFS)
	if _, err := jobWatchToolWithContext(context.Background(), fixture.root, map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"communicate"},
	}, 4096); err != nil {
		t.Fatalf("create stable delegate watch: %v", err)
	}
	return fixture
}

func newStableWatchRuntimeBase(t *testing.T, rootFS afero.Fs) *stableWatchRuntimeFixture {
	t.Helper()
	controller, controllerStorePath := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerRunning(t, controller, "dlg_source", "")
	rootID := "root-session"
	sourceID := "child-dlg_source"
	rootJM, err := newJobManager(controller.stateDir, rootID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new root job manager: %v", err)
	}
	sourceJM, err := newJobManager(controller.stateDir, sourceID, func(jobNotification) {})
	if err != nil {
		_ = rootJM.closeStoreOnly()
		t.Fatalf("new source job manager: %v", err)
	}
	root := &Session{
		id:                    rootID,
		stateDir:              controller.stateDir,
		delegateController:    controller,
		delegateRootSessionID: rootID,
		jobManager:            rootJM,
		state:                 SessionIdle,
	}
	source := &Session{
		id:                    sourceID,
		stateDir:              controller.stateDir,
		delegateController:    controller,
		delegateRootSessionID: rootID,
		owningDelegateID:      "dlg_source",
		jobManager:            sourceJM,
		state:                 SessionIdle,
	}
	rootJM.delegateController = controller
	sourceJM.delegateController = controller
	path := transcriptPath(controller.stateDir, rootID)
	var writer *transcript.Writer
	if rootFS == nil {
		writer, err = transcript.NewWriter(path, transcript.Header{SessionID: rootID})
	} else {
		writer, err = transcript.NewWriterWithFS(rootFS, path, transcript.Header{SessionID: rootID})
	}
	if err != nil {
		_ = sourceJM.closeStoreOnly()
		_ = rootJM.closeStoreOnly()
		t.Fatalf("new root transcript: %v", err)
	}
	root.attachTranscript(writer)
	controller.mu.Lock()
	controller.rootRuntime = root
	controller.live["dlg_source"].runtime = source
	controller.live["dlg_source"].binding.runtime = source
	controller.mu.Unlock()
	fixture := &stableWatchRuntimeFixture{
		controller: controller, root: root, source: source,
		rootJM: rootJM, sourceJM: sourceJM, rootTranscriptPath: path,
		controllerStorePath: controllerStorePath,
	}
	t.Cleanup(func() {
		if fixture.sourceJM != nil {
			_ = fixture.sourceJM.closeStoreOnly()
		}
		_ = fixture.rootJM.closeStoreOnly()
		_ = fixture.root.closeAttachedTranscript()
	})
	return fixture
}

func seedBootstrapControllerJournal(t *testing.T, fixture *stableWatchRuntimeFixture) {
	t.Helper()
	controllerBytes, err := os.ReadFile(fixture.controllerStorePath)
	if err != nil {
		t.Fatalf("read delegate controller journal: %v", err)
	}
	path := filepath.Join(jobsDir(fixture.controller.stateDir, fixture.root.ID()), "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create bootstrap controller directory: %v", err)
	}
	if err := os.WriteFile(path, controllerBytes, 0o644); err != nil {
		t.Fatalf("seed bootstrap controller journal: %v", err)
	}
}

func (f *stableWatchRuntimeFixture) onlyWatchConfig(t *testing.T) *watchConfig {
	t.Helper()
	for _, cfg := range f.sourceJM.watches {
		return cfg
	}
	t.Fatal("stable watch config missing")
	return nil
}

func (f *stableWatchRuntimeFixture) requireOnePending(t *testing.T) pendingWatchSendDelivery {
	t.Helper()
	pending := f.sourceJM.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 {
		t.Fatalf("stable watch pending deliveries = %d, want 1", len(pending))
	}
	return pending[0]
}

func seedSupersededStableWatchAckFailure(t *testing.T, fixture *stableWatchRuntimeFixture, fs *attentionSyncBarrierFS) (jobstore.WatchSendState, jobstore.WatchSendState, func(jobstore.Event) error) {
	t.Helper()
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "old frame"})
	old := fixture.requireOnePending(t).state
	sourceAckFailure := errors.New("old source delivered acknowledgement failed")
	originalAppend := fixture.sourceJM.appendEvent
	failed := false
	fixture.sourceJM.appendEvent = func(event jobstore.Event) error {
		if !failed && exactWatchSendEvent(event, jobstore.EventWatchSendDelivered, old) {
			failed = true
			return sourceAckFailure
		}
		return originalAppend(event)
	}
	fs.arm()
	done := make(chan error, 1)
	go func() {
		_, err := fixture.source.drainJobManagerWatchSends(context.Background(), fixture.sourceJM, "")
		done <- err
	}()
	<-fs.syncEntered
	released := false
	defer func() {
		if !released {
			fs.release()
		}
	}()
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "new frame"})
	newer := fixture.requireOnePending(t).state
	fs.release()
	released = true
	if err := <-done; !errors.Is(err, sourceAckFailure) {
		t.Fatalf("superseded delivery error = %v, want %v", err, sourceAckFailure)
	}
	fixture.sourceJM.appendEvent = originalAppend
	return old, newer, originalAppend
}

func exactWatchSendEvent(event jobstore.Event, kind jobstore.EventKind, state jobstore.WatchSendState) bool {
	return event.Kind == kind && event.WatchSend != nil && event.WatchSend.DeliveryID == state.DeliveryID && event.WatchSend.UpdateSeq == state.UpdateSeq
}

func countExactWatchSendEvents(events []jobstore.Event, kind jobstore.EventKind, state jobstore.WatchSendState) int {
	count := 0
	for _, event := range events {
		if exactWatchSendEvent(event, kind, state) {
			count++
		}
	}
	return count
}

func countAttentionEntries(t *testing.T, path, attentionID string) int {
	t.Helper()
	count := 0
	for _, entry := range readAttentionTranscriptEntries(t, path) {
		if entry.Turn.AttentionID == attentionID {
			count++
		}
	}
	return count
}
