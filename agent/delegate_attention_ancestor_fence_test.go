package agent

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func closeDelegateControllerResumability(t *testing.T, c *delegateTreeController, id, reason string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:               delegatestore.EventDelegateResumabilityClosed,
		DelegateID:         id,
		ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: reason},
	}); err != nil {
		t.Fatalf("close delegate %s resumability: %v", id, err)
	}
}

// TestDelegateAttentionWake_ClosedAncestorRefusesReserveAndNextIdle reproduces
// the confirmed probe: a permanently closed ancestor (turn-budget exhaustion)
// must fence attention wakes for every descendant, exactly as admitLeaseLocked
// fences running leases. Before the fix ReserveAttention admitted against the
// target's own aggregate only and committed a generation whose every model
// request would fail target_busy forever.
func TestDelegateAttentionWake_ClosedAncestorRefusesReserveAndNextIdle(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_grandchild", "dlg_child")
	child := &Session{}
	grandchild := &Session{}
	c.mu.Lock()
	c.live["dlg_child"] = &delegateLiveState{runtime: child}
	c.live["dlg_grandchild"] = &delegateLiveState{runtime: grandchild}
	c.mu.Unlock()
	const childAttentionID = "delegate:fenced-child"
	const grandchildAttentionID = "delegate:fenced-grandchild"
	if !c.noteDelegateAttention("dlg_child", childAttentionID) {
		t.Fatal("note child attention")
	}
	if !c.noteDelegateAttention("dlg_grandchild", grandchildAttentionID) {
		t.Fatal("note grandchild attention")
	}

	closeDelegateControllerResumability(t, c, "dlg_parent", "turn_budget_exhausted")

	if _, err := c.ReserveAttention(child, childAttentionID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveAttention under closed parent = %v, want target_busy", err)
	}
	if _, err := c.ReserveAttention(grandchild, grandchildAttentionID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveAttention under closed grandparent = %v, want target_busy", err)
	}
	if delegateID, attentionID, pending := c.nextIdleDelegateAttention(); pending {
		t.Fatalf("nextIdleDelegateAttention offered %s/%s under a closed ancestor", delegateID, attentionID)
	}
	c.mu.Lock()
	childGeneration := c.durable["dlg_child"].Generation
	grandchildGeneration := c.durable["dlg_grandchild"].Generation
	c.mu.Unlock()
	if childGeneration != 0 || grandchildGeneration != 0 {
		t.Fatalf("generations under closed ancestor = child:%d grandchild:%d, want none committed", childGeneration, grandchildGeneration)
	}
}

// TestDelegateAttentionWake_CommitStartRechecksAncestorFence covers the
// accept/closure race: once transcript consumption is admitted, permanent
// ancestor closure waits for the exact attention start batch.
func TestDelegateAttentionWake_CommitStartRechecksAncestorFence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	child := &Session{}
	c.mu.Lock()
	c.live["dlg_child"] = &delegateLiveState{runtime: child}
	c.mu.Unlock()
	const attentionID = "delegate:raced-wake"
	if !c.noteDelegateAttention("dlg_child", attentionID) {
		t.Fatal("note child attention")
	}
	attachDelegateAttentionTranscriptForTest(t, c, child, "dlg_child", attentionID)

	reservation, err := c.ReserveAttention(child, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention with open ancestor: %v", err)
	}
	if err := child.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept attention before ancestor closes: %v", err)
	}
	c.mu.Lock()
	closureStarted := make(chan struct{})
	closureDone := make(chan error, 1)
	go func() {
		close(closureStarted)
		_, closeErr := c.CloseResumability(rootDelegateActor("root-session"), "dlg_parent", "turn_budget_exhausted")
		closureDone <- closeErr
	}()
	<-closureStarted
	c.mu.Unlock()
	runtime.Gosched()

	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("accepted CommitStart lost to ancestor closure: %v", err)
	}
	if err := <-closureDone; err != nil {
		t.Fatalf("CloseResumability after accepted start: %v", err)
	}
	c.mu.Lock()
	generation := c.durable["dlg_child"].Generation
	reservations := len(c.reservations)
	trigger := c.runStarts[started.lease]
	parentResumable := c.durable["dlg_parent"].Resumable
	c.mu.Unlock()
	if generation != 1 || trigger != delegatestore.TriggerAttention {
		t.Fatalf("accepted generation = %d trigger %q, want exact 1/attention RunStarted", generation, trigger)
	}
	if reservations != 0 {
		t.Fatalf("reservations after accepted commit = %d, want released", reservations)
	}
	if parentResumable {
		t.Fatal("ancestor closure did not commit after accepted attention start")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, "child-dlg_child"), "child-dlg_child")
	if err != nil {
		t.Fatalf("read accepted attention marker: %v", err)
	}
	if fold.resumeGenerations[attentionID] != started.lease.generation {
		t.Fatalf("accepted marker generation = %d, want %d", fold.resumeGenerations[attentionID], started.lease.generation)
	}
}

// TestDelegateAttentionWake_StoppingAncestorParksAttentionForLaterDelivery pins
// the transient case: a pending subtree stop over the ancestor parks the
// attention -- no wake, no resolution, no escalation -- and once the stop
// completes with the ancestor still resumable the exact same attention is
// deliverable again.
func TestDelegateAttentionWake_StoppingAncestorParksAttentionForLaterDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	child := &Session{}
	c.mu.Lock()
	c.live["dlg_child"] = &delegateLiveState{runtime: child}
	c.mu.Unlock()
	const attentionID = "delegate:parked-wake"
	if !c.noteDelegateAttention("dlg_child", attentionID) {
		t.Fatal("note child attention")
	}

	c.mu.Lock()
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID:           "dlg_parent",
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: "dlg_parent"},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append subtree stop request: %v", err)
	}
	requestSeq := appended[0].Seq

	if _, err := c.ReserveAttention(child, attentionID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveAttention during ancestor stop = %v, want target_busy", err)
	}
	if delegateID, _, pending := c.nextIdleDelegateAttention(); pending {
		t.Fatalf("nextIdleDelegateAttention offered %s during ancestor stop", delegateID)
	}
	c.mu.Lock()
	_, parked := c.attentionWakeIDs["dlg_child"][attentionID]
	c.mu.Unlock()
	if !parked {
		t.Fatal("transient ancestor stop dropped the parked attention")
	}
	if plans := c.permanentlyFencedDelegateAttention(); len(plans) != 0 {
		t.Fatalf("transient ancestor stop offered escalations %#v, want none", plans)
	}

	c.mu.Lock()
	_, err = c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_parent",
		SubtreeStopCompleted: &delegatestore.SubtreeStopCompleted{RequestSeq: requestSeq},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append subtree stop completion: %v", err)
	}

	delegateID, gotAttentionID, pending := c.nextIdleDelegateAttention()
	if !pending || delegateID != "dlg_child" || gotAttentionID != attentionID {
		t.Fatalf("post-stop attention = delegate:%q attention:%q pending:%t, want parked attention deliverable", delegateID, gotAttentionID, pending)
	}
	reservation, err := c.ReserveAttention(child, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention after ancestor resumed: %v", err)
	}
	attachDelegateAttentionTranscriptForTest(t, c, child, "dlg_child", attentionID)
	if err := child.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept attention after ancestor resumed: %v", err)
	}
	if _, err := c.CommitStart(reservation); err != nil {
		t.Fatalf("CommitStart after ancestor resumed: %v", err)
	}
	c.mu.Lock()
	generation := c.durable["dlg_child"].Generation
	trigger := c.durable["dlg_child"].Trigger
	c.mu.Unlock()
	if generation != 1 || trigger != delegatestore.TriggerAttention {
		t.Fatalf("post-stop wake generation = %d trigger %q, want 1/attention", generation, trigger)
	}
}

// TestDelegateAttentionWake_PermanentClosedAncestorEscalatesToRootOnce drives
// the hot path end to end: a resident root supervises an idle grandchild whose
// pending attention sits under a parent delegate that closed permanently. The
// wake must be refused and the exact original message transferred to the root
// under its original attention ID, with the source durably resolved -- replays
// append nothing new.
func TestDelegateAttentionWake_PermanentClosedAncestorEscalatesToRootOnce(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	grandchildDelegateID := identifier.MustNewDelegateID()
	grandchildSessionID := identifier.MustNewSessionID()

	store, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("open fixture delegate store: %v", err)
	}
	events, err := store.Load()
	if err != nil {
		t.Fatalf("load fixture delegate events: %v", err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("fold fixture delegate events: %v", err)
	}
	descriptor := cloneDelegateStartDescriptor(state[fixture.delegateID].Descriptor)
	descriptor.ChildSessionID = grandchildSessionID
	descriptor.TranscriptRef = encodeRef("", grandchildSessionID)
	descriptor.ParentDelegateID = fixture.delegateID
	_, _, err = store.AppendBatch(state, []delegatestore.Event{
		{
			Kind:       delegatestore.EventDelegateCreated,
			TS:         time.Unix(1_700_000_300, 0).UTC(),
			DelegateID: grandchildDelegateID,
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		},
		{
			Kind:       delegatestore.EventDelegateAttentionChanged,
			TS:         time.Unix(1_700_000_301, 0).UTC(),
			DelegateID: grandchildDelegateID,
			AttentionChanged: &delegatestore.DelegateAttentionChanged{
				NeedsAttention: true,
			},
		},
	})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append grandchild delegate: %v", err)
	}
	grandchildMeta := fixture.meta
	grandchildMeta.ID = grandchildSessionID
	grandchildMeta.ParentSessionID = fixture.childID
	grandchildMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(fixture.stateDir, grandchildMeta); err != nil {
		t.Fatalf("save grandchild session meta: %v", err)
	}
	const attentionID = "delegate:undeliverable-under-closed-parent"
	writer, err := transcript.NewWriter(transcriptPath(fixture.stateDir, grandchildSessionID), transcript.Header{
		SessionID:       grandchildSessionID,
		ParentSessionID: fixture.childID,
		Task:            descriptor.Task,
		ProfileID:       descriptor.ResolvedProfileID,
		Model:           descriptor.ResolvedModel,
		WorkingDir:      fixture.workspace,
		Depth:           2,
	})
	if err != nil {
		t.Fatalf("create grandchild transcript: %v", err)
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User("undelivered grandchild message"))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		t.Fatalf("append grandchild attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close grandchild transcript: %v", err)
	}

	root := restoreSupervisionRoot(t, fixture, nil)
	root.delegateController.mu.Lock()
	childRevision := root.delegateController.durable[grandchildDelegateID].ProjectionRevision
	var published []delegateUpdatePlan
	root.delegateController.emitUpdate = func(plan delegateUpdatePlan) { published = append(published, plan) }
	root.delegateController.mu.Unlock()
	plans, err := root.delegateController.CloseResumability(rootDelegateActor(root.ID()), fixture.delegateID, "turn_budget_exhausted")
	if err != nil {
		t.Fatalf("close parent resumability: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("publish parent closure: %v", err)
	}
	root.delegateController.mu.Lock()
	childAggregate := root.delegateController.durable[grandchildDelegateID]
	root.delegateController.mu.Unlock()
	if childAggregate.NeedsAttention || childAggregate.ProjectionRevision != childRevision+1 {
		t.Fatalf("closed-ancestor child projection = attention:%t revision:%d, want false/%d", childAggregate.NeedsAttention, childAggregate.ProjectionRevision, childRevision+1)
	}
	childPublished := false
	for _, update := range published {
		for _, row := range update.rows {
			if row.id == grandchildDelegateID && !row.needsAttention && row.revision == childRevision+1 {
				childPublished = true
			}
		}
	}
	if !childPublished {
		t.Fatalf("closed-ancestor child update was not published: %#v", published)
	}

	root.drivePendingStableDelegateAttention()

	grandchildFold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, grandchildSessionID), grandchildSessionID)
	if err != nil {
		t.Fatalf("read grandchild attention fold: %v", err)
	}
	if got := grandchildFold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("fenced grandchild attention resolution = %q, want discarded", got)
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.meta.ID), fixture.meta.ID)
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	escalations := 0
	for _, id := range rootFold.order {
		if id == attentionID {
			escalations++
		}
	}
	if escalations != 1 {
		t.Fatalf("root transferred attention entries = %d, want exactly one", escalations)
	}
	if got := rootFold.content[attentionID].Text(); got != "undelivered grandchild message" {
		t.Fatalf("root transferred attention content = %q, want the original message", got)
	}
	if _, resolved := rootFold.resolutions[attentionID]; resolved {
		t.Fatal("root transferred attention arrived pre-resolved")
	}
	if !root.hasPendingRootDelegateAttention() {
		t.Fatal("root escalation notification was not armed for the root model")
	}
	if delegateID, _, pending := root.delegateController.nextIdleDelegateAttention(); pending {
		t.Fatalf("nextIdleDelegateAttention still offers %s after escalation", delegateID)
	}
	root.delegateController.mu.Lock()
	generation := root.delegateController.durable[grandchildDelegateID].Generation
	root.delegateController.mu.Unlock()
	if generation != 0 {
		t.Fatalf("fenced grandchild committed generation %d, want none", generation)
	}

	// Replays are no-ops: the hot driver again and the cold restart repair both
	// find the attention already resolved.
	root.drivePendingStableDelegateAttention()
	if err := repairPermanentlyUnreachableDelegateAttention(root.delegateController); err != nil {
		t.Fatalf("replay cold repair: %v", err)
	}
	rootFold, err = readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.meta.ID), fixture.meta.ID)
	if err != nil {
		t.Fatalf("re-read root attention fold: %v", err)
	}
	escalations = 0
	for _, id := range rootFold.order {
		if id == attentionID {
			escalations++
		}
	}
	if escalations != 1 {
		t.Fatalf("root transferred attention entries after replay = %d, want exactly one", escalations)
	}
}

// TestStableDelegateAttention_ColdRestoreEscalatesFencedDescendantOnce covers
// the cold variant: restart with a permanently closed ancestor and an idle
// descendant holding pending attention. Bootstrap must transfer the original
// message to the root transcript under its original identity, resolve the
// source, arm no wake (no retry loop), and a second bootstrap over the same
// durable state must not duplicate anything.
func TestStableDelegateAttention_ColdRestoreEscalatesFencedDescendantOnce(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", true)
	closeDelegateControllerResumability(t, c, "dlg_parent", "turn_budget_exhausted")
	const attentionID = "delegate:cold-fenced-child"
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_parent"), "child-dlg_parent")
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	countEscalations := func() (int, string) {
		t.Helper()
		rootFold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, "root-session"), "root-session")
		if err != nil {
			t.Fatalf("read root attention fold: %v", err)
		}
		count := 0
		content := ""
		for _, id := range rootFold.order {
			if id == attentionID {
				count++
				content = rootFold.content[id].Text()
			}
		}
		return count, content
	}

	fresh := newAttentionRepairRoot(c.stateDir, nil)
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap fenced descendant repair: %v", err)
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read fenced child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("cold fenced child resolution = %q, want discarded", got)
	}
	count, content := countEscalations()
	if count != 1 {
		t.Fatalf("cold root transfers = %d, want exactly one", count)
	}
	if content != "attention" {
		t.Fatalf("cold transferred content = %q, want the original message", content)
	}
	if delegateID, _, pending := fresh.delegateController.nextIdleDelegateAttention(); pending {
		t.Fatalf("cold restore armed fenced attention for %s", delegateID)
	}
	if fresh.delegateController.hasPendingDelegateAttention() {
		t.Fatal("cold restore left fenced attention pending, feeding an unbounded retry loop")
	}
	if err := fresh.closeOwnedDelegateStore(); err != nil {
		t.Fatalf("close first bootstrap store: %v", err)
	}

	replay := newAttentionRepairRoot(c.stateDir, nil)
	if err := replay.bootstrapDelegateResources(); err != nil {
		t.Fatalf("replay bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = replay.closeOwnedDelegateStore() })
	count, _ = countEscalations()
	if count != 1 {
		t.Fatalf("root transfers after replay bootstrap = %d, want exactly one", count)
	}
	if replay.delegateController.hasPendingDelegateAttention() {
		t.Fatal("replay bootstrap re-armed resolved fenced attention")
	}
}

// TestStableDelegateAttention_EscalationCrashWindowSurvivesCascadeAndGrowth
// pins the crash window BETWEEN the root transfer and the source resolution:
// the transfer for attention A is durable in the root transcript, the source
// is still pending, a further ancestor closes (the causally-linked cascade
// that moved the nearest fenced ancestor), and a second attention B arrives at
// the fenced child. Replay bootstrap must complete -- the per-attention
// identity and content are immutable, so A replays as a no-op and B gets its
// own transfer -- with exactly one root copy of each and both sources
// resolved. The batch-hash design failed this permanently.
func TestStableDelegateAttention_EscalationCrashWindowSurvivesCascadeAndGrowth(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 4, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_grand", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "dlg_grand", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", true)
	const attentionA = "delegate:crash-window-a"
	const attentionB = "delegate:crash-window-b"
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	rootPath := transcriptPath(c.stateDir, "root-session")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionA)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_grand"), "child-dlg_grand")
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_parent"), "child-dlg_parent")
	writeEmptyAttentionTranscript(t, rootPath, "root-session")

	// The grandparent closes; escalation of A transfers to the root and then
	// crashes before the source resolution becomes durable.
	closeDelegateControllerResumability(t, c, "dlg_grand", "turn_budget_exhausted")
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read child attention before partial transfer: %v", err)
	}
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen(rootPath, "root-session", attentionA, childFold.content[attentionA], time.Unix(200, 0).UTC(), transcript.OpenWriterForSession); err != nil {
		t.Fatalf("simulate partially completed transfer: %v", err)
	}
	// Before the restart, the cascade continues (the parent under the closed
	// grandparent closes too) and a second attention reaches the fenced child.
	closeDelegateControllerResumability(t, c, "dlg_parent", "turn_budget_exhausted")
	appendDelegateAttentionTurn(t, childPath, "child-dlg_child", attentionB)
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fresh := newAttentionRepairRoot(c.stateDir, nil)
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("replay bootstrap across the crash window: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	rootFold, err := readDelegateAttentionFold(rootPath, "root-session")
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	counts := map[string]int{}
	for _, id := range rootFold.order {
		counts[id]++
	}
	if counts[attentionA] != 1 || counts[attentionB] != 1 {
		t.Fatalf("root transfers after crash-window replay = %v, want exactly one of each", counts)
	}
	childFold, err = readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read child attention after replay: %v", err)
	}
	if got := childFold.resolutions[attentionA]; got != delegateAttentionDiscarded {
		t.Fatalf("crash-window attention %s resolution = %q, want discarded", attentionA, got)
	}
	if got := childFold.resolutions[attentionB]; got != delegateAttentionDiscarded {
		t.Fatalf("late attention %s resolution = %q, want discarded", attentionB, got)
	}
	if fresh.delegateController.hasPendingDelegateAttention() {
		t.Fatal("crash-window replay left fenced attention pending")
	}
}

// TestDelegateAttentionWake_AncestorStopFenceParksUncoveredChild reaches the
// ancestor fence's transient branch itself: the fold accepts a delegate
// created under a parent whose subtree stop is already pending (this
// controller's API refuses that ordering, so the state arrives only through a
// restored journal), leaving a child whose own PendingStopSeq is clear under a
// stopping ancestor. The fence must park the wake -- and must NOT classify the
// stop as permanent, so no escalation fires -- and the attention delivers
// normally once the stop completes.
func TestDelegateAttentionWake_AncestorStopFenceParksUncoveredChild(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	c.mu.Lock()
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID:           "dlg_parent",
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: "dlg_parent"},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append subtree stop request: %v", err)
	}
	requestSeq := appended[0].Seq
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	child := &Session{}
	c.mu.Lock()
	c.live["dlg_child"] = &delegateLiveState{runtime: child}
	childPendingStop := c.durable["dlg_child"].PendingStopSeq
	c.mu.Unlock()
	if childPendingStop != 0 {
		t.Fatalf("child pending stop seq = %d, want 0 so only the ancestor fence can park it", childPendingStop)
	}
	const attentionID = "delegate:uncovered-child-wake"
	if !c.noteDelegateAttention("dlg_child", attentionID) {
		t.Fatal("note child attention")
	}

	if _, err := c.ReserveAttention(child, attentionID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveAttention under stopping ancestor = %v, want target_busy", err)
	}
	if delegateID, _, pending := c.nextIdleDelegateAttention(); pending {
		t.Fatalf("nextIdleDelegateAttention offered %s under a stopping ancestor", delegateID)
	}
	if plans := c.permanentlyFencedDelegateAttention(); len(plans) != 0 {
		t.Fatalf("stopping ancestor classified as permanent: %#v", plans)
	}

	c.mu.Lock()
	_, err = c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_parent",
		SubtreeStopCompleted: &delegatestore.SubtreeStopCompleted{RequestSeq: requestSeq},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("append subtree stop completion: %v", err)
	}
	delegateID, gotAttentionID, pending := c.nextIdleDelegateAttention()
	if !pending || delegateID != "dlg_child" || gotAttentionID != attentionID {
		t.Fatalf("post-stop attention = delegate:%q attention:%q pending:%t, want the parked wake deliverable", delegateID, gotAttentionID, pending)
	}
	reservation, err := c.ReserveAttention(child, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention after ancestor stop completed: %v", err)
	}
	attachDelegateAttentionTranscriptForTest(t, c, child, "dlg_child", attentionID)
	if err := child.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept attention after ancestor stop completed: %v", err)
	}
	if _, err := c.CommitStart(reservation); err != nil {
		t.Fatalf("CommitStart after ancestor stop completed: %v", err)
	}
}
