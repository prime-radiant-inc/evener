package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
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
// reserve/commit race: the ancestor closes after ReserveAttention admitted the
// wake. CommitStart must re-check the ancestor chain and refuse instead of
// durably committing an inoperable generation.
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

	reservation, err := c.ReserveAttention(child, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention with open ancestor: %v", err)
	}
	closeDelegateControllerResumability(t, c, "dlg_parent", "turn_budget_exhausted")

	if _, err := c.CommitStart(reservation); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CommitStart after ancestor closed = %v, want target_busy", err)
	}
	c.mu.Lock()
	generation := c.durable["dlg_child"].Generation
	reservations := len(c.reservations)
	c.mu.Unlock()
	if generation != 0 {
		t.Fatalf("committed generation %d under closed ancestor, want none", generation)
	}
	if reservations != 0 {
		t.Fatalf("reservations after refused commit = %d, want released", reservations)
	}
	if turns, drives := c.capacityInUse(); turns != 0 || drives != 0 {
		t.Fatalf("capacity after refused commit = (%d, %d), want released", turns, drives)
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
// wake must be refused, the attention durably resolved, and the root must
// receive exactly one escalation notification -- replays append nothing new.
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
	_, _, err = store.AppendBatch(state, []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateCreated,
		TS:         time.Unix(1_700_000_300, 0).UTC(),
		DelegateID: grandchildDelegateID,
		Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
	}})
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
	closeDelegateControllerResumability(t, root.delegateController, fixture.delegateID, "turn_budget_exhausted")

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
	escalationID := ""
	for _, id := range rootFold.order {
		if strings.HasPrefix(id, "unreachable:"+grandchildDelegateID+":") {
			escalations++
			escalationID = id
		}
	}
	if escalations != 1 {
		t.Fatalf("root escalation notifications = %d, want exactly one", escalations)
	}
	content := rootFold.content[escalationID].Text()
	for _, want := range []string{grandchildDelegateID, fixture.delegateID, "1 undelivered message(s)"} {
		if !strings.Contains(content, want) {
			t.Fatalf("root escalation content %q omits %q", content, want)
		}
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
		if strings.HasPrefix(id, "unreachable:"+grandchildDelegateID+":") {
			escalations++
		}
	}
	if escalations != 1 {
		t.Fatalf("root escalation notifications after replay = %d, want exactly one", escalations)
	}
}

// TestStableDelegateAttention_ColdRestoreEscalatesFencedDescendantOnce covers
// the cold variant: restart with a permanently closed ancestor and an idle
// descendant holding pending attention. Bootstrap must resolve the attention,
// escalate once to the root transcript, arm no wake (no retry loop), and a
// second bootstrap over the same durable state must not duplicate anything.
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
			if strings.HasPrefix(id, "unreachable:dlg_child:") {
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
		t.Fatalf("cold root escalations = %d, want exactly one", count)
	}
	for _, want := range []string{"dlg_child", "dlg_parent", "1 undelivered message(s)"} {
		if !strings.Contains(content, want) {
			t.Fatalf("cold escalation content %q omits %q", content, want)
		}
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
		t.Fatalf("root escalations after replay bootstrap = %d, want exactly one", count)
	}
	if replay.delegateController.hasPendingDelegateAttention() {
		t.Fatal("replay bootstrap re-armed resolved fenced attention")
	}
}
