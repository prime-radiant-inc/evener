package delegatestore

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// attentionQueuedEvent, attentionResolvedEvent and attentionHeldEvent build the
// three durable queue events the controller journals. They live here so every
// test in this file names the payload shape once.
func attentionQueuedEvent(delegateID, attentionID string, committed bool, admittedGeneration uint64) Event {
	return Event{
		Kind:       EventDelegateAttentionQueued,
		DelegateID: delegateID,
		AttentionQueued: &AttentionQueued{
			AttentionID:        attentionID,
			Committed:          committed,
			AdmittedGeneration: admittedGeneration,
		},
	}
}

func attentionResolvedEvent(delegateID, attentionID string) Event {
	return Event{
		Kind:              EventDelegateAttentionResolved,
		DelegateID:        delegateID,
		AttentionResolved: &AttentionResolved{AttentionID: attentionID},
	}
}

func attentionHeldEvent(delegateID, attentionID string, generation uint64) Event {
	return Event{
		Kind:          EventDelegateAttentionHeld,
		DelegateID:    delegateID,
		AttentionHeld: &AttentionHeld{AttentionID: attentionID, Generation: generation},
	}
}

func attentionReleaseEvent(delegateID string, generation uint64) Event {
	return Event{
		Kind:          EventDelegateAttentionHeld,
		DelegateID:    delegateID,
		AttentionHeld: &AttentionHeld{Generation: generation},
	}
}

// attentionTestState folds events and asserts every step left the exported
// projection in agreement with the queue, the invariant every new arm must
// hold inside one Apply.
func attentionTestState(t *testing.T, events ...Event) State {
	t.Helper()
	for i := range events {
		events[i].Seq = uint64(i + 1)
	}
	state, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	for id, aggregate := range state {
		if want := len(aggregate.PendingAttention) != 0 && AttentionProjectionEligible(state, id); aggregate.NeedsAttention != want {
			t.Fatalf("delegate %s NeedsAttention=%t, queue holds %d rows, eligible=%t", id, aggregate.NeedsAttention, len(aggregate.PendingAttention), AttentionProjectionEligible(state, id))
		}
	}
	return state
}

func attentionRows(aggregate *Aggregate) []string {
	rows := make([]string, 0, len(aggregate.PendingAttention))
	for _, row := range aggregate.PendingAttention {
		rows = append(rows, row.AttentionID)
	}
	return rows
}

// TestPendingAttentionQueueIsIdempotentAndOutOfOrder pins the queue's two
// differences from PendingDeliveries: a repeated identity updates the row
// instead of appending, and a resolution removes a row from the middle of the
// queue.
func TestPendingAttentionQueueIsIdempotentAndOutOfOrder(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_one", ""),
		attentionQueuedEvent("dlg_one", "att_a", true, 0),
		attentionQueuedEvent("dlg_one", "att_b", false, 0),
		attentionQueuedEvent("dlg_one", "att_a", true, 0),
	)
	aggregate := state["dlg_one"]
	if got := attentionRows(aggregate); !reflect.DeepEqual(got, []string{"att_a", "att_b"}) {
		t.Fatalf("queue = %v, want [att_a att_b]", got)
	}
	if !aggregate.PendingAttention[0].Committed || aggregate.PendingAttention[1].Committed {
		t.Fatalf("committed flags = %t,%t, want true,false", aggregate.PendingAttention[0].Committed, aggregate.PendingAttention[1].Committed)
	}
	if !aggregate.NeedsAttention {
		t.Fatal("queued attention did not raise NeedsAttention in the same Apply")
	}
	revision := aggregate.ProjectionRevision

	state = attentionTestState(t,
		createdEvent("dlg_one", ""),
		attentionQueuedEvent("dlg_one", "att_a", true, 0),
		attentionQueuedEvent("dlg_one", "att_b", false, 0),
		attentionQueuedEvent("dlg_one", "att_a", true, 0),
		attentionResolvedEvent("dlg_one", "att_a"),
	)
	aggregate = state["dlg_one"]
	if got := attentionRows(aggregate); !reflect.DeepEqual(got, []string{"att_b"}) {
		t.Fatalf("queue after out-of-order resolve = %v, want [att_b]", got)
	}
	if !aggregate.NeedsAttention {
		t.Fatal("resolving one of two rows lowered NeedsAttention")
	}
	state = attentionTestState(t,
		createdEvent("dlg_one", ""),
		attentionQueuedEvent("dlg_one", "att_a", true, 0),
		attentionQueuedEvent("dlg_one", "att_b", false, 0),
		attentionQueuedEvent("dlg_one", "att_a", true, 0),
		attentionResolvedEvent("dlg_one", "att_a"),
		attentionResolvedEvent("dlg_one", "att_b"),
		attentionResolvedEvent("dlg_one", "att_b"),
	)
	aggregate = state["dlg_one"]
	if len(aggregate.PendingAttention) != 0 || aggregate.NeedsAttention {
		t.Fatalf("empty queue projection = rows:%v needs:%t, want empty/false", attentionRows(aggregate), aggregate.NeedsAttention)
	}
	if aggregate.ProjectionRevision <= revision {
		t.Fatalf("projection revision %d did not advance past %d", aggregate.ProjectionRevision, revision)
	}
}

// TestPendingAttentionHoldAndRelease pins the hold's two forms: a set stamps
// one row, and a release clears every hold through its generation while
// advancing the durable release floor a cold restore reads.
func TestPendingAttentionHoldAndRelease(t *testing.T) {
	t.Parallel()
	settled := []Event{
		createdEvent("dlg_held", ""),
		startedEvent("dlg_held", 1, TriggerOwnerInput),
		preparedEvent("dlg_held", 1, reportedPacket("done")),
		finishedEvent("dlg_held", 1, OutcomeCompleted, DispositionReported, "dlg_held/delivery/1", nil),
		attentionQueuedEvent("dlg_held", "att_late", true, 0),
		attentionQueuedEvent("dlg_held", "att_early", true, 0),
	}
	state := attentionTestState(t, append(settled,
		attentionHeldEvent("dlg_held", "att_late", 1),
		attentionHeldEvent("dlg_held", "att_early", 1),
	)...)
	aggregate := state["dlg_held"]
	for _, row := range aggregate.PendingAttention {
		if row.HeldThrough != 1 {
			t.Fatalf("row %s HeldThrough=%d, want 1", row.AttentionID, row.HeldThrough)
		}
	}
	// A held row still raises the flag, exactly as the split-state controller
	// did: the transcript still owes the row and a cold restore must re-present
	// it. Only lifecycle eligibility can lower the projection while a row
	// remains.
	if !aggregate.NeedsAttention {
		t.Fatal("a held row lowered NeedsAttention, but a restart rebuilt it as true")
	}
	// A fresh wake admitted at the held generation supersedes the quiet.
	state = attentionTestState(t, append(settled,
		attentionHeldEvent("dlg_held", "att_late", 1),
		attentionHeldEvent("dlg_held", "att_early", 1),
		attentionQueuedEvent("dlg_held", "att_fresh", true, 1),
	)...)
	for _, row := range state["dlg_held"].PendingAttention {
		if row.HeldThrough != 0 {
			t.Fatalf("fresh wake left row %s held through %d", row.AttentionID, row.HeldThrough)
		}
	}
	// The explicit release form does the same and advances the floor.
	state = attentionTestState(t, append(settled,
		attentionHeldEvent("dlg_held", "att_late", 1),
		attentionHeldEvent("dlg_held", "att_early", 1),
		attentionReleaseEvent("dlg_held", 1),
	)...)
	aggregate = state["dlg_held"]
	if aggregate.AttentionHoldReleasedGeneration != 1 {
		t.Fatalf("release floor = %d, want 1", aggregate.AttentionHoldReleasedGeneration)
	}
	for _, row := range aggregate.PendingAttention {
		if row.HeldThrough != 0 {
			t.Fatalf("release left row %s held through %d", row.AttentionID, row.HeldThrough)
		}
	}
}

// TestPendingAttentionLifecycleRecomputesProjection drives the stop, close and
// resumability arms through the same helper: each one ends eligibility and
// lowers NeedsAttention in the same Apply that moves the lifecycle state, while
// the queue rows stay for the stop's own resolution.
func TestPendingAttentionLifecycleRecomputesProjection(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_stop", ""),
		attentionQueuedEvent("dlg_stop", "att_one", true, 0),
		stopRequestedEvent("dlg_stop"),
	)
	aggregate := state["dlg_stop"]
	if aggregate.Phase != PhaseStopping || aggregate.NeedsAttention {
		t.Fatalf("stopping projection = phase:%v needs:%t, want %v/false", aggregate.Phase, aggregate.NeedsAttention, PhaseStopping)
	}
	if got := attentionRows(aggregate); !reflect.DeepEqual(got, []string{"att_one"}) {
		t.Fatalf("stop request dropped the queued row: %v", got)
	}

	state = attentionTestState(t,
		createdEvent("dlg_closed", ""),
		attentionQueuedEvent("dlg_closed", "att_one", true, 0),
		stopRequestedEvent("dlg_closed"),
		Event{Kind: EventDelegateSubtreeStopCompleted, DelegateID: "dlg_closed", SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 3}},
		Event{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_closed", ResumabilityClosed: &ResumabilityClosed{Reason: "closed"}},
	)
	aggregate = state["dlg_closed"]
	if aggregate.NeedsAttention || aggregate.Resumable {
		t.Fatalf("closed projection = needs:%t resumable:%t, want false/false", aggregate.NeedsAttention, aggregate.Resumable)
	}
	if got := attentionRows(aggregate); !reflect.DeepEqual(got, []string{"att_one"}) {
		t.Fatalf("close dropped the queued row: %v", got)
	}
	// A queued event after the close still folds, and the projection stays
	// lowered because eligibility, not the queue length, decides it.
	state = attentionTestState(t,
		createdEvent("dlg_closed", ""),
		stopRequestedEvent("dlg_closed"),
		Event{Kind: EventDelegateSubtreeStopCompleted, DelegateID: "dlg_closed", SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 2}},
		Event{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_closed", ResumabilityClosed: &ResumabilityClosed{Reason: "closed"}},
		attentionQueuedEvent("dlg_closed", "att_late", true, 0),
	)
	if state["dlg_closed"].NeedsAttention {
		t.Fatal("a row queued after close raised NeedsAttention")
	}
}

// TestPendingAttentionAncestorFenceCoversDescendants pins the ancestor half of
// the projection: a closed ancestor lowers its descendants' flags in the same
// Apply, without discarding their rows.
func TestPendingAttentionAncestorFenceCoversDescendants(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_parent", ""),
		createdEvent("dlg_child", "dlg_parent"),
		attentionQueuedEvent("dlg_child", "att_child", true, 0),
		Event{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_parent", ResumabilityClosed: &ResumabilityClosed{Reason: "closed"}},
	)
	child := state["dlg_child"]
	if child.NeedsAttention || !child.Resumable {
		t.Fatalf("fenced child projection = needs:%t resumable:%t, want false/true", child.NeedsAttention, child.Resumable)
	}
	if got := attentionRows(child); !reflect.DeepEqual(got, []string{"att_child"}) {
		t.Fatalf("fence dropped the child's row: %v", got)
	}
}

// TestLegacyAttentionEventsStillLoadAndFold keeps the read-only compatibility
// contract: a journal written before the durable queue carries
// delegate_attention_changed and delegate_attention_hold_released, and a
// current binary must still decode it (DisallowUnknownFields) and fold the same
// projection it wrote.
func TestLegacyAttentionEventsStillLoadAndFold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	log := "{\"version\":1}\n" +
		"{\"events\":[" +
		"{\"kind\":\"delegate_created\",\"seq\":1,\"delegate_id\":\"dlg_legacy\",\"created\":{\"descriptor\":{\"child_session_id\":\"child_legacy\",\"transcript_ref\":\"local:child_legacy\",\"owner_session_id\":\"root\",\"task\":\"legacy task\",\"agent_type\":\"general\",\"tool_name_ceiling\":[\"communicate\"],\"resumable\":true}}}," +
		"{\"kind\":\"delegate_run_started\",\"seq\":2,\"delegate_id\":\"dlg_legacy\",\"run_started\":{\"generation\":1,\"trigger\":\"owner_input\",\"started_at\":\"2026-01-01T00:00:00Z\"}}," +
		"{\"kind\":\"delegate_terminal_prepared\",\"seq\":3,\"delegate_id\":\"dlg_legacy\",\"terminal_prepared\":{\"generation\":1,\"packet\":{\"kind\":\"reported\",\"message\":\"done\"}}}," +
		"{\"kind\":\"delegate_run_finished\",\"seq\":4,\"delegate_id\":\"dlg_legacy\",\"run_finished\":{\"generation\":1,\"outcome\":{\"status\":\"completed\",\"ended_at\":\"2026-01-01T00:00:01Z\"},\"disposition\":\"reported\",\"delivery_id\":\"dlg_legacy/delivery/1\"}}," +
		"{\"kind\":\"delegate_attention_changed\",\"seq\":5,\"delegate_id\":\"dlg_legacy\",\"attention_changed\":{\"needs_attention\":true}}," +
		"{\"kind\":\"delegate_attention_hold_released\",\"seq\":6,\"delegate_id\":\"dlg_legacy\",\"attention_hold_released\":{\"generation\":1}}" +
		"]}\n"
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy journal: %v", err)
	}
	events, err := store.Load()
	if err != nil {
		_ = store.Close()
		t.Fatalf("Load legacy journal: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	state, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold legacy journal: %v", err)
	}
	aggregate := state["dlg_legacy"]
	if aggregate == nil {
		t.Fatal("legacy delegate missing from folded state")
	}
	if !aggregate.NeedsAttention {
		t.Fatal("legacy needs_attention event folded false")
	}
	if aggregate.AttentionHoldReleasedGeneration != 1 {
		t.Fatalf("legacy hold release floor = %d, want 1", aggregate.AttentionHoldReleasedGeneration)
	}
	// The legacy boolean stays readable but is not a row: a later queue change
	// re-derives the projection from the durable queue alone.
	queued := attentionQueuedEvent("dlg_legacy", "att_new", true, 0)
	queued.Seq = events[len(events)-1].Seq + 1
	next, err := Fold(append(events, queued))
	if err != nil {
		t.Fatalf("Fold with queued row: %v", err)
	}
	if got := attentionRows(next["dlg_legacy"]); !reflect.DeepEqual(got, []string{"att_new"}) {
		t.Fatalf("queue after legacy fold = %v, want [att_new]", got)
	}
}
