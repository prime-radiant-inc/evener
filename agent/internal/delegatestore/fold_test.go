package delegatestore

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFoldUsesOneAggregatePerStableDelegate(t *testing.T) {
	events := sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("first")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
		startedEvent("dlg_alpha", 2, TriggerOwnerInput),
	)

	state, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if len(state) != 1 {
		t.Fatalf("delegate aggregates = %d, want 1", len(state))
	}
	got := state["dlg_alpha"]
	if got == nil || got.Generation != 2 || got.Phase != PhaseRunning {
		t.Fatalf("aggregate = %#v, want generation 2 running", got)
	}
}

func TestApplyRejectsSequenceGapAndPayloadMismatch(t *testing.T) {
	_, err := Fold([]Event{{
		Seq:        2,
		Kind:       EventDelegateCreated,
		DelegateID: "dlg_alpha",
		Created:    &DelegateCreated{Descriptor: testDescriptor("dlg_alpha", "")},
	}})
	if err == nil || !strings.Contains(err.Error(), "sequence 2, want 1") {
		t.Fatalf("Fold sequence error = %v, want sequence gap", err)
	}

	state := make(State)
	event := createdEvent("dlg_alpha", "")
	event.RunStarted = &RunStarted{Generation: 1, Trigger: TriggerOwnerInput}
	if err := Apply(state, event); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("Apply payload mismatch error = %v, want payload mismatch", err)
	}
	if len(state) != 0 {
		t.Fatalf("state mutated after rejected payload: %#v", state)
	}
}

func TestApplyRejectsStaleGeneration(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
	)
	want := stateJSON(t, state)

	err := Apply(state, finishedEvent("dlg_alpha", 2, OutcomeCompleted, DispositionReported, "", nil))
	if err == nil || !strings.Contains(err.Error(), "generation 2") {
		t.Fatalf("Apply stale generation error = %v", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after stale generation:\n got %s\nwant %s", got, want)
	}
}

func TestApplyStopWinsOverPreparedNormalFinish(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("normal result")),
		stopRequestedEvent("dlg_alpha"),
	)
	event := finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil)
	event.RunFinished.Outcome.Reason = "normal_completion"
	if err := Apply(state, event); err != nil {
		t.Fatalf("Apply finish after stop: %v", err)
	}

	got := state["dlg_alpha"]
	if got.Phase != PhaseStopping || got.CurrentRunOpen {
		t.Fatalf("aggregate phase/open = %s/%t, want stopping/false", got.Phase, got.CurrentRunOpen)
	}
	if got.LatestOutcome == nil || got.LatestOutcome.Status != OutcomeStopped || got.LatestOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("latest outcome = %#v, want stopped_by_parent", got.LatestOutcome)
	}
	if got.PreparedTerminal == nil || string(got.PreparedTerminal.Message) != `"normal result"` {
		t.Fatalf("prepared terminal = %#v, want retained diagnostic packet", got.PreparedTerminal)
	}
}

func TestApplyStopCompletionDiscardsInternalDeliveriesAndRetainsExternal(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		startedEvent("dlg_parent", 1, TriggerOwnerInput),
		preparedEvent("dlg_parent", 1, reportedPacket("for root")),
		finishedEvent("dlg_parent", 1, OutcomeCompleted, DispositionReported, "dlg_parent/delivery/1", nil),
		createdEvent("dlg_child", "dlg_parent"),
		startedEvent("dlg_child", 1, TriggerOwnerInput),
		preparedEvent("dlg_child", 1, reportedPacket("for parent")),
		finishedEvent("dlg_child", 1, OutcomeCompleted, DispositionReported, "dlg_child/delivery/1", nil),
		stopRequestedEvent("dlg_parent"),
	)
	requestSeq := state["dlg_parent"].PendingStopSeq
	if err := Apply(state, Event{
		Kind:                 EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_parent",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: requestSeq},
	}); err != nil {
		t.Fatalf("Apply stop completion: %v", err)
	}

	if got := state["dlg_parent"].PendingDeliveries; len(got) != 1 || got[0].DeliveryID != "dlg_parent/delivery/1" {
		t.Fatalf("external deliveries = %#v, want parent delivery retained", got)
	}
	if got := state["dlg_child"].PendingDeliveries; len(got) != 0 {
		t.Fatalf("internal deliveries = %#v, want discarded", got)
	}
}

func TestApplyResumabilityClosureIsMonotonic(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_alpha", ""))
	if err := Apply(state, Event{
		Kind:               EventDelegateResumabilityClosed,
		DelegateID:         "dlg_alpha",
		ResumabilityClosed: &ResumabilityClosed{Reason: "isolation_disposed"},
	}); err != nil {
		t.Fatalf("Apply first closure: %v", err)
	}
	want := stateJSON(t, state)
	err := Apply(state, Event{
		Kind:               EventDelegateResumabilityClosed,
		DelegateID:         "dlg_alpha",
		ResumabilityClosed: &ResumabilityClosed{Reason: "different_reason"},
	})
	if err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Fatalf("Apply second closure error = %v, want monotonic refusal", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after second closure:\n got %s\nwant %s", got, want)
	}
}

func TestApplyKeepsTwoUnacknowledgedDeliveriesInOrder(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("first")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
		startedEvent("dlg_alpha", 2, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 2, reportedPacket("second")),
		finishedEvent("dlg_alpha", 2, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/2", nil),
	)

	got := state["dlg_alpha"].PendingDeliveries
	if len(got) != 2 || got[0].DeliveryID != "dlg_alpha/delivery/1" || got[1].DeliveryID != "dlg_alpha/delivery/2" {
		t.Fatalf("pending deliveries = %#v, want generation order", got)
	}
}

func TestApplyCompletedNoActionProjectsCompleted(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerAttention),
	)
	if err := Apply(state, finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionCompletedNoAction, "", nil)); err != nil {
		t.Fatalf("Apply completed-no-action finish: %v", err)
	}

	got := state["dlg_alpha"]
	if got.LatestOutcome == nil || got.LatestOutcome.Status != OutcomeCompleted {
		t.Fatalf("latest outcome = %#v, want completed", got.LatestOutcome)
	}
	if len(got.PendingDeliveries) != 0 {
		t.Fatalf("pending deliveries = %#v, want none", got.PendingDeliveries)
	}
}

func TestApplyAllowsOnlyOnePendingStopSequence(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		createdEvent("dlg_beta", ""),
		stopRequestedEvent("dlg_alpha"),
	)
	want := stateJSON(t, state)

	secondStop := stopRequestedEvent("dlg_beta")
	secondStop.Seq = 4
	err := Apply(state, secondStop)
	if err == nil || !strings.Contains(err.Error(), "pending subtree stop") {
		t.Fatalf("second stop error = %v, want one-pending-stop refusal", err)
	}
	if got := stateJSON(t, state); got != want {
		t.Fatalf("state mutated after second stop:\n got %s\nwant %s", got, want)
	}
}

func TestApplyProjectionRevisionIncrementsOnlyAffectedDelegate(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		createdEvent("dlg_other", ""),
	)
	beforeParent := state["dlg_parent"].ProjectionRevision
	beforeChild := state["dlg_child"].ProjectionRevision
	beforeOther := state["dlg_other"].ProjectionRevision

	stop := stopRequestedEvent("dlg_parent")
	stop.Seq = 4
	if err := Apply(state, stop); err != nil {
		t.Fatalf("Apply stop request: %v", err)
	}
	if got := state["dlg_parent"].ProjectionRevision; got != beforeParent+1 {
		t.Fatalf("parent revision = %d, want %d", got, beforeParent+1)
	}
	if got := state["dlg_child"].ProjectionRevision; got != beforeChild+1 {
		t.Fatalf("child revision = %d, want %d", got, beforeChild+1)
	}
	if got := state["dlg_other"].ProjectionRevision; got != beforeOther {
		t.Fatalf("unaffected revision = %d, want %d", got, beforeOther)
	}
}

func TestFoldReconstructsProjectionRevisionAcrossRestart(t *testing.T) {
	events := sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		preparedEvent("dlg_alpha", 1, reportedPacket("done")),
		finishedEvent("dlg_alpha", 1, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/1", nil),
	)
	want, err := Fold(events)
	if err != nil {
		t.Fatalf("first Fold: %v", err)
	}
	got, err := Fold(events)
	if err != nil {
		t.Fatalf("restart Fold: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restart state differs:\n got %#v\nwant %#v", got, want)
	}
	if got["dlg_alpha"].ProjectionRevision != 4 {
		t.Fatalf("projection revision = %d, want 4", got["dlg_alpha"].ProjectionRevision)
	}
}

func TestApplyRunOpenDistinguishesUnfinishedAndSettledStopping(t *testing.T) {
	state := applyEvents(t,
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		stopRequestedEvent("dlg_alpha"),
	)
	if !state["dlg_alpha"].CurrentRunOpen {
		t.Fatal("current run is closed before exact finish")
	}
	requestSeq := state["dlg_alpha"].PendingStopSeq
	if err := Apply(state, finishedEvent("dlg_alpha", 1, OutcomeStopped, DispositionTerminalError, "dlg_alpha/delivery/1", stoppedPacket())); err != nil {
		t.Fatalf("Apply stopped finish: %v", err)
	}
	if state["dlg_alpha"].CurrentRunOpen {
		t.Fatal("current run remains open after exact finish")
	}
	if state["dlg_alpha"].Phase != PhaseStopping {
		t.Fatalf("phase = %s, want stopping until stop completion", state["dlg_alpha"].Phase)
	}
	if err := Apply(state, Event{
		Kind:                 EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_alpha",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: requestSeq},
	}); err != nil {
		t.Fatalf("Apply stop completion: %v", err)
	}
	if state["dlg_alpha"].Phase != PhaseIdle {
		t.Fatalf("phase = %s, want idle after stop completion", state["dlg_alpha"].Phase)
	}
}

func sequence(events ...Event) []Event {
	for i := range events {
		events[i].Seq = uint64(i + 1)
	}
	return events
}

func applyEvents(t *testing.T, events ...Event) State {
	t.Helper()
	state := make(State)
	for i := range events {
		if events[i].Seq == 0 {
			events[i].Seq = uint64(i + 1)
		}
		if err := Apply(state, events[i]); err != nil {
			t.Fatalf("Apply event %d (%s): %v", i+1, events[i].Kind, err)
		}
	}
	return state
}

func createdEvent(id, parentID string) Event {
	return Event{
		Kind:       EventDelegateCreated,
		DelegateID: id,
		Created:    &DelegateCreated{Descriptor: testDescriptor(id, parentID)},
	}
}

func startedEvent(id string, generation uint64, trigger RunTrigger) Event {
	return Event{
		Kind:       EventDelegateRunStarted,
		DelegateID: id,
		RunStarted: &RunStarted{
			Generation: generation,
			Trigger:    trigger,
			StartedAt:  time.Date(2026, 8, 12, 10, int(generation), 0, 0, time.UTC),
		},
	}
}

func preparedEvent(id string, generation uint64, packet TerminalPacket) Event {
	return Event{
		Kind:       EventDelegateTerminalPrepared,
		DelegateID: id,
		TerminalPrepared: &TerminalPrepared{
			Generation: generation,
			Packet:     packet,
		},
	}
}

func finishedEvent(id string, generation uint64, status OutcomeStatus, disposition RunDisposition, deliveryID string, packet *TerminalPacket) Event {
	return Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: id,
		RunFinished: &RunFinished{
			Generation:  generation,
			Outcome:     Outcome{Status: status, EndedAt: time.Date(2026, 8, 12, 11, int(generation), 0, 0, time.UTC)},
			Disposition: disposition,
			DeliveryID:  deliveryID,
			Packet:      packet,
		},
	}
}

func stopRequestedEvent(id string) Event {
	return Event{
		Kind:                 EventDelegateSubtreeStopRequested,
		DelegateID:           id,
		SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: id},
	}
}

func reportedPacket(message string) TerminalPacket {
	return TerminalPacket{
		Kind:             PacketReported,
		Message:          json.RawMessage(`"` + message + `"`),
		StructuredResult: json.RawMessage(`{"answer":true}`),
	}
}

func stoppedPacket() *TerminalPacket {
	return &TerminalPacket{
		Kind:    PacketTerminalError,
		Message: json.RawMessage(`"stopped by parent"`),
	}
}

func testDescriptor(id, parentID string) Descriptor {
	return Descriptor{
		ChildSessionID:   "session_" + id,
		TranscriptRef:    "transcript:" + id,
		OwnerSessionID:   "root_session",
		ParentDelegateID: parentID,
		Task:             "task for " + id,
		Description:      "description for " + id,
		AgentType:        "worker",
		Resumable:        true,
	}
}

func stateJSON(t *testing.T, state State) string {
	t.Helper()
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return string(b)
}
