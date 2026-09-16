package delegatestore

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

// replayReference is the semantic contract Fold must match: a replay that
// repeatedly applies the shipped public Apply. Apply is now touched-only — it
// clones, diffs and, on a rejected event, restores exactly the aggregates an
// event can reach — so this reference is the real contract, not the full-clone
// strawman the branch first compared against. Fold may only differ in mutating
// its own unpublished state in place instead of through that transaction copy.
func replayReference(events []Event) (State, error) {
	state := make(State)
	for i, event := range events {
		if event.Seq != uint64(i+1) {
			return nil, fmt.Errorf("delegate event sequence %d, want %d", event.Seq, i+1)
		}
		if err := Apply(state, event); err != nil {
			return nil, fmt.Errorf("delegate event %d: %w", event.Seq, err)
		}
	}
	return state, nil
}

func replayRegressionEvents() []Event {
	packet := reportedPacket("opaque-result")
	events := []Event{
		createdEventWithReferenceDescriptor("dlg_parent"),
		createdEvent("dlg_child", "dlg_parent"),
		createdEvent("dlg_other", ""),
		startedEvent("dlg_parent", 1, TriggerOwnerInput),
		preparedEvent("dlg_parent", 1, packet),
		finishedEvent("dlg_parent", 1, OutcomeCompleted, DispositionReported, "dlg_parent/delivery/1", nil),
		{Kind: EventDelegateDeliveryAcknowledged, DelegateID: "dlg_parent", DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: "dlg_parent/delivery/1"}},
		{Kind: EventDelegateAttentionChanged, DelegateID: "dlg_child", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true}},
		startedEvent("dlg_child", 1, TriggerOwnerInput),
		stopRequestedEvent("dlg_parent"),
		finishedEvent("dlg_child", 1, OutcomeStopped, DispositionTerminalError, "", stoppedPacket()),
		{Kind: EventDelegateSubtreeStopCompleted, DelegateID: "dlg_parent", SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 10}},
		{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_other", ResumabilityClosed: &ResumabilityClosed{Reason: "opaque-reason"}},
	}
	for i := range events {
		events[i].Seq = uint64(i + 1)
	}
	return events
}

func TestFoldReplayMatchesTransactionalReference(t *testing.T) {
	events := replayRegressionEvents()
	for end := 0; end <= len(events); end++ {
		t.Run(strconv.Itoa(end), func(t *testing.T) {
			want, err := replayReference(events[:end])
			if err != nil {
				t.Fatalf("reference fixture: %v", err)
			}
			got, err := Fold(events[:end])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("replay state/revisions differ from transactional reference")
			}
		})
	}
	for _, mutate := range []struct {
		name   string
		change func([]Event)
	}{
		{"sequence", func(events []Event) { events[3].Seq++ }},
		{"envelope", func(events []Event) { events[3].RunStarted = nil }},
		{"generation", func(events []Event) { events[3].RunStarted.Generation++ }},
		{"late stop failure", func(events []Event) { events[10].RunFinished.ObserverCallbackDelivered = true }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			events := replayRegressionEvents()
			mutate.change(events)
			want, wantErr := replayReference(events)
			got, gotErr := Fold(events)
			if wantErr == nil || gotErr == nil || wantErr.Error() != gotErr.Error() || got != nil || want != nil {
				t.Fatalf("got error=%v stateNil=%t; reference error=%v", gotErr, got == nil, wantErr)
			}
		})
	}
}

func TestFoldReplayOwnsInputAndIndependentResults(t *testing.T) {
	events := replayRegressionEvents()[:6]
	first, err := Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	want, err := replayReference(events)
	if err != nil {
		t.Fatal(err)
	}
	first["dlg_parent"].Descriptor.Config.SkillsDirs[0] = "changed"
	first["dlg_parent"].LatestPacket.Message[1] = 'X'
	first["dlg_parent"].PendingDeliveries[0].Packet.StructuredResult[1] = 'X'
	if !reflect.DeepEqual(second, want) {
		t.Fatal("separate fold results share mutable storage")
	}
	after, err := replayReference(events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatal("fold result aliases input event storage")
	}
	events[0].Created.Descriptor.ToolNameCeiling[0] = "changed"
	events[4].TerminalPrepared.Packet.Message[1] = 'Y'
	if !reflect.DeepEqual(second, want) {
		t.Fatal("input mutation changed accepted replay state")
	}
}

func TestApplyLateFailureStillAtomic(t *testing.T) {
	events := replayRegressionEvents()
	state, err := replayReference(events[:10])
	if err != nil {
		t.Fatal(err)
	}
	// cloneState is not a faithful snapshot for this comparison: it normalizes a
	// nil PendingDeliveries slice to an empty one, while the Apply reference
	// (correctly) leaves untouched aggregates' slices nil. An independent replay
	// of the same events is a nil-faithful value copy to diff against.
	before, err := replayReference(events[:10])
	if err != nil {
		t.Fatal(err)
	}
	bad := events[10]
	bad.RunFinished.ObserverCallbackDelivered = true
	if err := Apply(state, bad); err == nil {
		t.Fatal("invalid stopped observer callback accepted")
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("failed Apply published partial mutation")
	}
}

func TestFoldReplayAvoidsTransactionalCopies(t *testing.T) {
	events := replayRegressionEvents()
	run := func(fold func([]Event) (State, error)) float64 {
		return testing.AllocsPerRun(1, func() {
			if _, err := fold(events); err != nil {
				t.Fatal(err)
			}
		})
	}
	reference := run(replayReference)
	got := run(Fold)
	if got >= reference {
		t.Fatalf("replay allocations=%g; Apply replay=%g; unpublished replay should avoid the per-event transaction copies Apply makes", got, reference)
	}
}

func TestApplyProjectionRevisionUsesOriginalState(t *testing.T) {
	seed := []Event{createdEvent("dlg_target", "")}
	seed[0].Seq = 1
	got, err := replayReference(seed)
	if err != nil {
		t.Fatal(err)
	}
	want, err := replayReference(seed)
	if err != nil {
		t.Fatal(err)
	}
	// Seed independently, then install a caller-owned nonnil empty slice.
	// Cloning after this assignment would erase the boundary under test.
	got["dlg_target"].Descriptor.FrozenSkillNames = []string{}
	want["dlg_target"].Descriptor.FrozenSkillNames = []string{}
	beforeRevision := want["dlg_target"].ProjectionRevision
	event := Event{Seq: 2, Kind: EventDelegateAttentionChanged, DelegateID: "dlg_target", AttentionChanged: &DelegateAttentionChanged{NeedsAttention: false}}
	if err := Apply(want, event); err != nil {
		t.Fatal(err)
	}
	if err := Apply(got, event); err != nil {
		t.Fatal(err)
	}
	if want["dlg_target"].ProjectionRevision != beforeRevision+1 {
		t.Fatal("reference did not observe descriptor normalization")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Apply differs from reference: revision=%d, want %d; complete states equal=false", got["dlg_target"].ProjectionRevision, want["dlg_target"].ProjectionRevision)
	}
}
