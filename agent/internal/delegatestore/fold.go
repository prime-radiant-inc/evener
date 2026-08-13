package delegatestore

import (
	"encoding/json"
	"fmt"
	"reflect"

	"primeradiant.com/serf/agent/provenance"
)

func Fold(events []Event) (State, error) {
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

func Apply(state State, event Event) error {
	if state == nil {
		return fmt.Errorf("delegate state is nil")
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
	for id, aggregate := range next {
		state[id] = aggregate
	}
	return nil
}

func applyEvent(state State, event Event) error {
	switch event.Kind {
	case EventDelegateCreated:
		return applyCreated(state, event)
	case EventDelegateRunStarted:
		return applyRunStarted(state, event)
	case EventDelegateTerminalPrepared:
		return applyTerminalPrepared(state, event)
	case EventDelegateRunFinished:
		return applyRunFinished(state, event)
	case EventDelegateResumabilityClosed:
		return applyResumabilityClosed(state, event)
	case EventDelegateSubtreeStopRequested:
		return applySubtreeStopRequested(state, event)
	case EventDelegateSubtreeStopCompleted:
		return applySubtreeStopCompleted(state, event)
	case EventDelegateDeliveryAcknowledged:
		return applyDeliveryAcknowledged(state, event)
	default:
		return fmt.Errorf("unknown delegate event kind %q", event.Kind)
	}
}

func applyCreated(state State, event Event) error {
	if state[event.DelegateID] != nil {
		return fmt.Errorf("delegate %q already exists", event.DelegateID)
	}
	descriptor := event.Created.Descriptor
	if err := validateDescriptor(descriptor); err != nil {
		return fmt.Errorf("delegate_created: %w", err)
	}
	if descriptor.ParentDelegateID != "" && state[descriptor.ParentDelegateID] == nil {
		return fmt.Errorf("delegate_created: parent %q does not exist", descriptor.ParentDelegateID)
	}
	state[event.DelegateID] = &Aggregate{
		DelegateID: event.DelegateID,
		Descriptor: descriptor,
		Phase:      PhaseIdle,
		Resumable:  descriptor.Resumable,
	}
	if !descriptor.Resumable {
		state[event.DelegateID].Phase = PhaseClosed
	}
	return nil
}

func applyRunStarted(state State, event Event) error {
	aggregate, err := requireAggregate(state, event.DelegateID)
	if err != nil {
		return err
	}
	payload := event.RunStarted
	if payload.Generation != aggregate.Generation+1 {
		return fmt.Errorf("delegate %q run generation %d, want %d", event.DelegateID, payload.Generation, aggregate.Generation+1)
	}
	if aggregate.Phase != PhaseIdle {
		return fmt.Errorf("delegate %q phase %q cannot start", event.DelegateID, aggregate.Phase)
	}
	if !aggregate.Resumable {
		return fmt.Errorf("delegate %q is not resumable", event.DelegateID)
	}
	if aggregate.PendingStopSeq != 0 {
		return fmt.Errorf("delegate %q belongs to pending subtree stop %d", event.DelegateID, aggregate.PendingStopSeq)
	}
	if !validRunTrigger(payload.Trigger) {
		return fmt.Errorf("delegate %q has invalid run trigger %q", event.DelegateID, payload.Trigger)
	}
	if payload.StartedAt.IsZero() {
		return fmt.Errorf("delegate %q run start time is zero", event.DelegateID)
	}
	aggregate.Generation = payload.Generation
	aggregate.Trigger = payload.Trigger
	aggregate.Phase = PhaseRunning
	aggregate.CurrentRunOpen = true
	aggregate.PreparedTerminal = nil
	aggregate.RunStartedAt = payload.StartedAt
	aggregate.LatestActivityAt = payload.StartedAt
	return nil
}

func applyTerminalPrepared(state State, event Event) error {
	aggregate, err := requireExactOpenRun(state, event.DelegateID, event.TerminalPrepared.Generation)
	if err != nil {
		return err
	}
	if aggregate.Phase != PhaseRunning {
		return fmt.Errorf("delegate %q phase %q cannot prepare terminal", event.DelegateID, aggregate.Phase)
	}
	if err := validateTerminalPacket(event.TerminalPrepared.Packet); err != nil {
		return fmt.Errorf("delegate %q terminal packet: %w", event.DelegateID, err)
	}
	aggregate.PreparedTerminal = cloneTerminalPacket(&event.TerminalPrepared.Packet)
	aggregate.Phase = PhaseSettling
	return nil
}

func applyRunFinished(state State, event Event) error {
	payload := event.RunFinished
	aggregate, err := requireExactOpenRun(state, event.DelegateID, payload.Generation)
	if err != nil {
		return err
	}
	if aggregate.Phase != PhaseRunning && aggregate.Phase != PhaseSettling && aggregate.Phase != PhaseStopping {
		return fmt.Errorf("delegate %q phase %q cannot finish", event.DelegateID, aggregate.Phase)
	}
	if !validOutcomeStatus(payload.Outcome.Status) {
		return fmt.Errorf("delegate %q has invalid outcome %q", event.DelegateID, payload.Outcome.Status)
	}
	if payload.Outcome.EndedAt.IsZero() {
		return fmt.Errorf("delegate %q finish time is zero", event.DelegateID)
	}
	if !validDisposition(payload.Disposition) {
		return fmt.Errorf("delegate %q has invalid disposition %q", event.DelegateID, payload.Disposition)
	}

	stopping := aggregate.Phase == PhaseStopping
	packet := payload.Packet
	if packet == nil && aggregate.PreparedTerminal != nil {
		packet = aggregate.PreparedTerminal
	}
	if stopping {
		outcome := payload.Outcome
		outcome.Status = OutcomeStopped
		outcome.Reason = "stopped_by_parent"
		aggregate.LatestOutcome = &outcome
		if payload.DeliveryID != "" && !ownerCoveredByStop(state, aggregate) {
			if payload.Packet == nil || payload.Packet.Kind != PacketTerminalError {
				packet = &TerminalPacket{Kind: PacketTerminalError, Message: json.RawMessage(`"stopped by parent"`)}
			}
			if err := appendDelivery(aggregate, payload.DeliveryID, payload.Generation, packet); err != nil {
				return err
			}
		}
	} else {
		if err := validateFinishPacket(aggregate, payload, packet); err != nil {
			return err
		}
		outcome := payload.Outcome
		aggregate.LatestOutcome = &outcome
		if payload.Disposition != DispositionCompletedNoAction {
			if err := appendDelivery(aggregate, payload.DeliveryID, payload.Generation, packet); err != nil {
				return err
			}
		}
		aggregate.PreparedTerminal = nil
		if aggregate.Resumable {
			aggregate.Phase = PhaseIdle
		} else {
			aggregate.Phase = PhaseClosed
		}
	}
	aggregate.CurrentRunOpen = false
	aggregate.LatestActivityAt = payload.Outcome.EndedAt
	return nil
}

func applyResumabilityClosed(state State, event Event) error {
	aggregate, err := requireAggregate(state, event.DelegateID)
	if err != nil {
		return err
	}
	if event.ResumabilityClosed.Reason == "" {
		return fmt.Errorf("delegate %q resumability close reason is empty", event.DelegateID)
	}
	if !aggregate.Resumable {
		return fmt.Errorf("delegate %q resumability is already closed", event.DelegateID)
	}
	aggregate.Resumable = false
	aggregate.NotResumableReason = event.ResumabilityClosed.Reason
	if aggregate.Phase == PhaseIdle {
		aggregate.Phase = PhaseClosed
	}
	return nil
}

func applySubtreeStopRequested(state State, event Event) error {
	if event.Seq == 0 {
		return fmt.Errorf("delegate subtree stop request sequence is zero")
	}
	payload := event.SubtreeStopRequested
	if payload.TargetDelegateID != event.DelegateID {
		return fmt.Errorf("delegate subtree stop target %q does not match delegate %q", payload.TargetDelegateID, event.DelegateID)
	}
	if _, err := requireAggregate(state, payload.TargetDelegateID); err != nil {
		return err
	}
	for _, aggregate := range state {
		if aggregate.PendingStopSeq != 0 {
			return fmt.Errorf("pending subtree stop %d already exists", aggregate.PendingStopSeq)
		}
	}
	for id, aggregate := range state {
		if !isDelegateOrDescendant(state, id, payload.TargetDelegateID) {
			continue
		}
		aggregate.PendingStopSeq = event.Seq
		if aggregate.Phase != PhaseClosed {
			aggregate.Phase = PhaseStopping
		}
	}
	return nil
}

func applySubtreeStopCompleted(state State, event Event) error {
	payload := event.SubtreeStopCompleted
	if payload.RequestSeq == 0 {
		return fmt.Errorf("delegate subtree stop completion request sequence is zero")
	}
	target, err := requireAggregate(state, event.DelegateID)
	if err != nil {
		return err
	}
	if target.PendingStopSeq != payload.RequestSeq {
		return fmt.Errorf("delegate %q pending stop sequence %d, want %d", event.DelegateID, target.PendingStopSeq, payload.RequestSeq)
	}
	covered := make(map[string]bool)
	for id, aggregate := range state {
		if aggregate.PendingStopSeq != payload.RequestSeq {
			continue
		}
		if aggregate.CurrentRunOpen {
			return fmt.Errorf("delegate %q current run is still open", id)
		}
		covered[id] = true
	}
	if len(covered) == 0 {
		return fmt.Errorf("delegate subtree stop %d has no members", payload.RequestSeq)
	}

	for _, aggregate := range state {
		kept := aggregate.PendingDeliveries[:0]
		for _, delivery := range aggregate.PendingDeliveries {
			if covered[delivery.OwnerDelegateID] {
				continue
			}
			kept = append(kept, delivery)
		}
		aggregate.PendingDeliveries = kept
	}
	for id := range covered {
		aggregate := state[id]
		aggregate.PendingStopSeq = 0
		aggregate.PreparedTerminal = nil
		if aggregate.Resumable {
			aggregate.Phase = PhaseIdle
		} else {
			aggregate.Phase = PhaseClosed
		}
	}
	return nil
}

func applyDeliveryAcknowledged(state State, event Event) error {
	aggregate, err := requireAggregate(state, event.DelegateID)
	if err != nil {
		return err
	}
	deliveryID := event.DeliveryAcknowledged.DeliveryID
	if deliveryID == "" {
		return fmt.Errorf("delegate %q delivery acknowledgement id is empty", event.DelegateID)
	}
	if len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].DeliveryID != deliveryID {
		return fmt.Errorf("delegate %q delivery %q is not the pending head", event.DelegateID, deliveryID)
	}
	aggregate.PendingDeliveries = append([]PendingDelivery(nil), aggregate.PendingDeliveries[1:]...)
	return nil
}

func validateEventEnvelope(event Event) error {
	if event.DelegateID == "" {
		return fmt.Errorf("delegate event %q has empty delegate id", event.Kind)
	}
	payloads := []bool{
		event.Created != nil,
		event.RunStarted != nil,
		event.TerminalPrepared != nil,
		event.RunFinished != nil,
		event.ResumabilityClosed != nil,
		event.SubtreeStopRequested != nil,
		event.SubtreeStopCompleted != nil,
		event.DeliveryAcknowledged != nil,
	}
	count := 0
	for _, present := range payloads {
		if present {
			count++
		}
	}
	matching := false
	switch event.Kind {
	case EventDelegateCreated:
		matching = event.Created != nil
	case EventDelegateRunStarted:
		matching = event.RunStarted != nil
	case EventDelegateTerminalPrepared:
		matching = event.TerminalPrepared != nil
	case EventDelegateRunFinished:
		matching = event.RunFinished != nil
	case EventDelegateResumabilityClosed:
		matching = event.ResumabilityClosed != nil
	case EventDelegateSubtreeStopRequested:
		matching = event.SubtreeStopRequested != nil
	case EventDelegateSubtreeStopCompleted:
		matching = event.SubtreeStopCompleted != nil
	case EventDelegateDeliveryAcknowledged:
		matching = event.DeliveryAcknowledged != nil
	default:
		return fmt.Errorf("unknown delegate event kind %q", event.Kind)
	}
	if count != 1 || !matching {
		return fmt.Errorf("delegate event %q payload does not match kind", event.Kind)
	}
	return nil
}

func validateDescriptor(descriptor Descriptor) error {
	switch {
	case descriptor.ChildSessionID == "":
		return fmt.Errorf("child session id is empty")
	case descriptor.TranscriptRef == "":
		return fmt.Errorf("transcript ref is empty")
	case descriptor.OwnerSessionID == "":
		return fmt.Errorf("owner session id is empty")
	case descriptor.Task == "":
		return fmt.Errorf("task is empty")
	case descriptor.AgentType == "":
		return fmt.Errorf("agent type is empty")
	case len(descriptor.ResultSchema) > 0 && !json.Valid(descriptor.ResultSchema):
		return fmt.Errorf("result schema is not valid JSON")
	default:
		return nil
	}
}

func validateTerminalPacket(packet TerminalPacket) error {
	if packet.Kind != PacketReported && packet.Kind != PacketTerminalError {
		return fmt.Errorf("invalid kind %q", packet.Kind)
	}
	if len(packet.Message) == 0 || !json.Valid(packet.Message) {
		return fmt.Errorf("message is not valid JSON")
	}
	if len(packet.StructuredResult) > 0 && !json.Valid(packet.StructuredResult) {
		return fmt.Errorf("structured result is not valid JSON")
	}
	if len(packet.Metadata) > 0 && !json.Valid(packet.Metadata) {
		return fmt.Errorf("metadata is not valid JSON")
	}
	return nil
}

func validateFinishPacket(aggregate *Aggregate, finish *RunFinished, packet *TerminalPacket) error {
	if finish.Disposition == DispositionCompletedNoAction {
		if aggregate.Trigger != TriggerAttention {
			return fmt.Errorf("delegate %q completed_no_action requires attention trigger", aggregate.DelegateID)
		}
		if finish.Outcome.Status != OutcomeCompleted {
			return fmt.Errorf("delegate %q completed_no_action outcome is %q", aggregate.DelegateID, finish.Outcome.Status)
		}
		if finish.DeliveryID != "" || finish.Packet != nil || aggregate.PreparedTerminal != nil {
			return fmt.Errorf("delegate %q completed_no_action cannot carry terminal delivery", aggregate.DelegateID)
		}
		return nil
	}
	if packet == nil {
		return fmt.Errorf("delegate %q finish has no terminal packet", aggregate.DelegateID)
	}
	if err := validateTerminalPacket(*packet); err != nil {
		return fmt.Errorf("delegate %q finish packet: %w", aggregate.DelegateID, err)
	}
	if finish.Disposition == DispositionReported && packet.Kind != PacketReported {
		return fmt.Errorf("delegate %q reported finish has packet kind %q", aggregate.DelegateID, packet.Kind)
	}
	if finish.Disposition == DispositionTerminalError && packet.Kind != PacketTerminalError {
		return fmt.Errorf("delegate %q terminal-error finish has packet kind %q", aggregate.DelegateID, packet.Kind)
	}
	if finish.DeliveryID == "" {
		return fmt.Errorf("delegate %q finish delivery id is empty", aggregate.DelegateID)
	}
	return nil
}

func appendDelivery(aggregate *Aggregate, deliveryID string, generation uint64, packet *TerminalPacket) error {
	if deliveryID == "" || packet == nil {
		return fmt.Errorf("delegate %q finish requires delivery id and packet", aggregate.DelegateID)
	}
	wantID := fmt.Sprintf("%s/delivery/%d", aggregate.DelegateID, generation)
	if deliveryID != wantID {
		return fmt.Errorf("delegate %q delivery id %q, want %q", aggregate.DelegateID, deliveryID, wantID)
	}
	for _, pending := range aggregate.PendingDeliveries {
		if pending.DeliveryID == deliveryID {
			return fmt.Errorf("delegate %q delivery %q already exists", aggregate.DelegateID, deliveryID)
		}
	}
	aggregate.PendingDeliveries = append(aggregate.PendingDeliveries, PendingDelivery{
		DeliveryID:      deliveryID,
		Generation:      generation,
		OwnerDelegateID: aggregate.Descriptor.ParentDelegateID,
		Packet:          *cloneTerminalPacket(packet),
	})
	return nil
}

func requireAggregate(state State, id string) (*Aggregate, error) {
	aggregate := state[id]
	if aggregate == nil {
		return nil, fmt.Errorf("delegate %q does not exist", id)
	}
	return aggregate, nil
}

func requireExactOpenRun(state State, id string, generation uint64) (*Aggregate, error) {
	aggregate, err := requireAggregate(state, id)
	if err != nil {
		return nil, err
	}
	if generation != aggregate.Generation {
		return nil, fmt.Errorf("delegate %q generation %d is stale; current generation is %d", id, generation, aggregate.Generation)
	}
	if !aggregate.CurrentRunOpen {
		return nil, fmt.Errorf("delegate %q generation %d is already finished", id, generation)
	}
	return aggregate, nil
}

func ownerCoveredByStop(state State, aggregate *Aggregate) bool {
	ownerID := aggregate.Descriptor.ParentDelegateID
	return ownerID != "" && state[ownerID] != nil && state[ownerID].PendingStopSeq == aggregate.PendingStopSeq
}

func isDelegateOrDescendant(state State, id, target string) bool {
	for current := id; current != ""; {
		if current == target {
			return true
		}
		aggregate := state[current]
		if aggregate == nil {
			return false
		}
		current = aggregate.Descriptor.ParentDelegateID
	}
	return false
}

func validRunTrigger(trigger RunTrigger) bool {
	return trigger == TriggerInitial || trigger == TriggerOwnerInput || trigger == TriggerAttention
}

func validOutcomeStatus(status OutcomeStatus) bool {
	switch status {
	case OutcomeCompleted, OutcomeFailed, OutcomeExhausted, OutcomeCancelled, OutcomeStopped:
		return true
	default:
		return false
	}
}

func validDisposition(disposition RunDisposition) bool {
	return disposition == DispositionReported || disposition == DispositionTerminalError || disposition == DispositionCompletedNoAction
}

type publicProjection struct {
	Descriptor         Descriptor
	Phase              Phase
	Resumable          bool
	NotResumableReason string
	LatestActivityAt   string
	LatestOutcome      *Outcome
}

func (aggregate *Aggregate) publicProjection() publicProjection {
	return publicProjection{
		Descriptor:         aggregate.Descriptor,
		Phase:              aggregate.Phase,
		Resumable:          aggregate.Resumable,
		NotResumableReason: aggregate.NotResumableReason,
		LatestActivityAt:   aggregate.LatestActivityAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		LatestOutcome:      cloneOutcome(aggregate.LatestOutcome),
	}
}

func cloneState(state State) (State, error) {
	clone := make(State, len(state))
	for id, aggregate := range state {
		if aggregate == nil {
			return nil, fmt.Errorf("delegate %q aggregate is nil", id)
		}
		clone[id] = cloneAggregate(aggregate)
	}
	return clone, nil
}

func cloneAggregate(aggregate *Aggregate) *Aggregate {
	clone := *aggregate
	clone.Descriptor = cloneDescriptor(aggregate.Descriptor)
	clone.PreparedTerminal = cloneTerminalPacket(aggregate.PreparedTerminal)
	clone.LatestOutcome = cloneOutcome(aggregate.LatestOutcome)
	clone.PendingDeliveries = make([]PendingDelivery, len(aggregate.PendingDeliveries))
	for i := range aggregate.PendingDeliveries {
		clone.PendingDeliveries[i] = aggregate.PendingDeliveries[i]
		clone.PendingDeliveries[i].Packet = *cloneTerminalPacket(&aggregate.PendingDeliveries[i].Packet)
	}
	return &clone
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	clone := descriptor
	clone.FrozenToolNames = append([]string(nil), descriptor.FrozenToolNames...)
	clone.FrozenSkillNames = append([]string(nil), descriptor.FrozenSkillNames...)
	clone.FrozenSkillBodies = append([]string(nil), descriptor.FrozenSkillBodies...)
	clone.ResultSchema = append(json.RawMessage(nil), descriptor.ResultSchema...)
	clone.ExplicitToolGrants = append([]string(nil), descriptor.ExplicitToolGrants...)
	clone.Provenance = provenance.Clone(descriptor.Provenance)
	if descriptor.Sandbox != nil {
		sandbox := *descriptor.Sandbox
		sandbox.DenylistAdd = append([]string(nil), descriptor.Sandbox.DenylistAdd...)
		sandbox.DenylistRemove = append([]string(nil), descriptor.Sandbox.DenylistRemove...)
		sandbox.ExtraWritableRoots = append([]string(nil), descriptor.Sandbox.ExtraWritableRoots...)
		sandbox.ExtraReadRoots = append([]string(nil), descriptor.Sandbox.ExtraReadRoots...)
		if descriptor.Sandbox.Network != nil {
			network := *descriptor.Sandbox.Network
			sandbox.Network = &network
		}
		clone.Sandbox = &sandbox
	}
	return clone
}

func cloneTerminalPacket(packet *TerminalPacket) *TerminalPacket {
	if packet == nil {
		return nil
	}
	clone := *packet
	clone.Message = append(json.RawMessage(nil), packet.Message...)
	clone.StructuredResult = append(json.RawMessage(nil), packet.StructuredResult...)
	clone.Warnings = append([]string(nil), packet.Warnings...)
	clone.Metadata = append(json.RawMessage(nil), packet.Metadata...)
	if packet.StructuredResultValid != nil {
		valid := *packet.StructuredResultValid
		clone.StructuredResultValid = &valid
	}
	return &clone
}

func cloneOutcome(outcome *Outcome) *Outcome {
	if outcome == nil {
		return nil
	}
	clone := *outcome
	return &clone
}
