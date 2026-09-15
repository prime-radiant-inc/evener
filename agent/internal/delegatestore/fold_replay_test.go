package delegatestore

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"
)

// referenceTransactionalApply freezes the pre-optimization Apply algorithm.
// In particular it does not call the replay helper under test. Repeated calls
// are the independent reference for complete state, revisions, and errors.
func referenceTransactionalApply(state State, event Event) error {
	if state == nil {
		return errors.New("delegate state is nil")
	}
	if err := validateEventEnvelope(event); err != nil {
		return err
	}
	next, err := cloneState(state)
	if err != nil {
		return err
	}
	before := make(map[string]publicProjection, len(state))
	for id, aggregate := range state {
		if aggregate == nil {
			return fmt.Errorf("delegate %q aggregate is nil", id)
		}
		before[id] = aggregate.publicProjection()
	}
	if err := applyEvent(next, event); err != nil {
		return err
	}
	for id, aggregate := range next {
		previous, existed := state[id]
		if !existed {
			aggregate.ProjectionRevision = 1
			continue
		}
		aggregate.ProjectionRevision = previous.ProjectionRevision
		if !reflect.DeepEqual(before[id], aggregate.publicProjection()) {
			aggregate.ProjectionRevision++
		}
	}
	for id := range state {
		delete(state, id)
	}
	maps.Copy(state, next)
	return nil
}

func replayReference(events []Event) (State, error) {
	state := make(State)
	for i, event := range events {
		if event.Seq != uint64(i+1) {
			return nil, fmt.Errorf("delegate event sequence %d, want %d", event.Seq, i+1)
		}
		if err := referenceTransactionalApply(state, event); err != nil {
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
		t.Run(fmt.Sprint(end), func(t *testing.T) {
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
	before, err := cloneState(state)
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
		t.Fatalf("replay allocations=%g; unchanged transactional reference=%g; unpublished replay should avoid transaction copies", got, reference)
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
	if err := referenceTransactionalApply(want, event); err != nil {
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
