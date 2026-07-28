package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

const clientMutationMethodQueue = "turn/queue"

const (
	clientMutationMethodSteer   = "turn/steer"
	clientMutationMethodDrain   = "turn/drainAsSteer"
	clientMutationMethodPromote = "turn/promoteQueuedAsSteer"
	clientMutationMethodCancel  = "turn/cancelQueued"
)

type clientMutationTranscriptItems struct {
	StableTurnID string
	User         bool
	Failure      bool
}

func (s *Session) ensureClientMutationStore() error {
	s.clientMutationsInitMu.Lock()
	defer s.clientMutationsInitMu.Unlock()
	if s.clientMutations != nil {
		return nil
	}
	store, err := newClientMutationStore(s.stateDir, s.id)
	if err != nil {
		return err
	}
	s.clientMutations = store
	return nil
}

func (s *Session) clientMutationQueue(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodQueue, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	request.Preconditions.ExpectedTurnID = params.ExpectedTurnID

	var response appwire.TurnQueueResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		if params.ExpectedTurnID != "" && snapshot.ActiveTurnID != params.ExpectedTurnID {
			rejectClientMutation(record, appwire.Conflict("turn is not active"))
			return nil
		}
		if len(params.Input) == 0 {
			rejectClientMutation(record, appwire.InvalidParams("input is required"))
			return nil
		}
		if s.cfg.MaxTurns > 0 && snapshot.AcceptedTurns+reservedClientMutationTurns(snapshot) >= uint64(s.cfg.MaxTurns) {
			rejectClientMutation(record, appwire.Conflict((&budgetExhaustionError{
				Budget: exhaustedBudgetTurns, Limit: s.cfg.MaxTurns, Resumable: true,
			}).Error()))
			return nil
		}
		snapshot.NextQueueEntrySequence++
		entryID := fmt.Sprintf("queue_%d", snapshot.NextQueueEntrySequence)
		record.StableQueueEntryIDs = []string{entryID}
		record.ExecutionState = "accepted"
		snapshot.BudgetReservations[params.ClientMutationID] = clientMutationBudgetReservation{Slots: 1}
		return nil
	}, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		response = appwire.TurnQueueResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied)}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		snapshot.InputQueue = append(snapshot.InputQueue, clientMutationQueueEntry{
			ID:               record.StableQueueEntryIDs[0],
			ClientMutationID: record.ClientMutationID,
			Input:            cloneClientMutationInput(params.Input),
		})
		snapshot.QueueRevision++
		applyClientMutationRecord(record, result)
		return nil
	})
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnQueueResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		return queueResponseFromRecord(s.ID(), lookup.Record, appwire.MutationDispositionReplayed)
	}
	s.reflectDurableInputQueue()
	return response, nil
}

func rejectClientMutation(record *clientMutationRecord, err error) {
	wireErr, ok := err.(appwire.WireError)
	if !ok {
		wireErr = appwire.Conflict(err.Error())
	}
	data, _ := wireErr.Data.(appwire.ErrorData)
	data.ClientMutationID = record.ClientMutationID
	data.MutationOutcome = appwire.MutationOutcomeNotAccepted
	data.RetryDisposition = appwire.RetryDispositionNone
	record.OperationState = clientMutationOperationRejected
	record.ExecutionState = "rejected"
	record.ProjectionState = appwire.MutationProjectionRemoved
	record.Payload = nil
	record.Rejection = &clientMutationRejection{
		Code:    wireErr.Code,
		Message: wireErr.Message,
		Data:    data,
	}
}

func clientMutationRejectionError(record clientMutationRecord) error {
	if record.Rejection == nil {
		return appwire.InternalError("client mutation rejection is missing")
	}
	return appwire.WireError{
		Code:    record.Rejection.Code,
		Message: record.Rejection.Message,
		Data:    record.Rejection.Data,
	}
}

func reservedClientMutationTurns(snapshot *clientMutationSnapshot) uint64 {
	var reserved uint64
	for _, reservation := range snapshot.BudgetReservations {
		reserved += reservation.Slots
	}
	return reserved
}

func (s *Session) claimDirectClientMutationTurn(acceptedTurnsFloor uint64) error {
	if err := s.ensureClientMutationStore(); err != nil {
		return err
	}
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.AcceptedTurns < acceptedTurnsFloor {
			snapshot.AcceptedTurns = acceptedTurnsFloor
		}
		if s.cfg.MaxTurns > 0 && snapshot.AcceptedTurns+reservedClientMutationTurns(snapshot) >= uint64(s.cfg.MaxTurns) {
			return &budgetExhaustionError{
				Budget:    exhaustedBudgetTurns,
				Limit:     s.cfg.MaxTurns,
				Resumable: false,
			}
		}
		snapshot.AcceptedTurns++
		return nil
	})
}

func queueResponseFromRecord(threadID string, record clientMutationRecord, disposition appwire.MutationDisposition) (appwire.TurnQueueResponse, error) {
	var response appwire.TurnQueueResponse
	if len(record.Result) != 0 {
		if err := json.Unmarshal(record.Result, &response); err != nil {
			return appwire.TurnQueueResponse{}, fmt.Errorf("decode queue mutation result: %w", err)
		}
	}
	response.Receipt.ClientMutationID = record.ClientMutationID
	response.Receipt.Disposition = disposition
	response.Receipt.ThreadID = threadID
	response.Receipt.QueueEntryIDs = append([]string(nil), record.StableQueueEntryIDs...)
	response.Receipt.ProjectionState = record.ProjectionState
	return response, nil
}

func (s *Session) reflectDurableInputQueue() {
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	snapshot := s.clientMutations.snapshot()
	if snapshot.QueueRevision < s.publishedQueueRevision {
		return
	}
	s.publishedQueueRevision = snapshot.QueueRevision
	queue := make([]queuedInput, 0, len(snapshot.InputQueue))
	for _, entry := range snapshot.InputQueue {
		queue = append(queue, queuedInputFromClientMutation(entry))
	}
	s.mu.Lock()
	s.inputQueue = queue
	data := s.queueChangedDataLocked()
	data.Revision = snapshot.QueueRevision
	data.ClientMutationIDs = make([]string, len(snapshot.InputQueue))
	for i, entry := range snapshot.InputQueue {
		data.ClientMutationIDs[i] = entry.ClientMutationID
	}
	s.mu.Unlock()
	s.emit(events.EventQueueChanged, data)
}

func queuedInputFromClientMutation(entry clientMutationQueueEntry) queuedInput {
	var queued queuedInput
	queued.ID = entry.ID
	for _, item := range entry.Input {
		switch item.Type {
		case "text":
			queued.Text += item.Text
		case "image":
			queued.Images = append(queued.Images, ImageAttachment{MediaType: item.MediaType, Data: append([]byte(nil), item.Data...), Name: item.Name})
		}
	}
	return queued
}

func clientMutationInput(text string, images []ImageAttachment) []appwire.InputItem {
	input := make([]appwire.InputItem, 0, 1+len(images))
	if text != "" {
		input = append(input, appwire.InputItem{Type: "text", Text: text})
	}
	for _, image := range images {
		input = append(input, appwire.InputItem{
			Type:      "image",
			MediaType: image.MediaType,
			Data:      append([]byte(nil), image.Data...),
			Name:      image.Name,
		})
	}
	return input
}

func (s *Session) clientMutationSteer(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodSteer, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	request.Preconditions.ExpectedTurnID = params.ExpectedTurnID
	var response appwire.TurnSteerResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		if params.ExpectedTurnID != "" && snapshot.ActiveTurnID != params.ExpectedTurnID {
			rejectClientMutation(record, appwire.Conflict("turn is not active"))
			return nil
		}
		if len(params.Input) == 0 {
			rejectClientMutation(record, appwire.InvalidParams("input is required"))
			return nil
		}
		reserveClientMutationTurnID(snapshot, record)
		record.ExecutionState = "accepted"
		return nil
	}, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		response = appwire.TurnSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied)}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		addPendingSteering(snapshot, record, params.Input)
		applyClientMutationRecord(record, result)
		return nil
	})
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnSteerResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		var replayed appwire.TurnSteerResponse
		if err := replayClientMutationResult(lookup.Record, &replayed); err != nil {
			return replayed, err
		}
		replayed.Receipt.Disposition = appwire.MutationDispositionReplayed
		replayed.Receipt.ProjectionState = lookup.Record.ProjectionState
		return replayed, nil
	}
	s.reflectDurableClientSteering()
	return response, nil
}

func (s *Session) clientMutationDrain(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodDrain, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	request.Preconditions.ExpectedTurnID = params.ExpectedTurnID
	request.Preconditions.ExpectedQueueRevision = &params.ExpectedQueueRevision
	var response appwire.TurnDrainAsSteerResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		if params.ExpectedTurnID != "" && snapshot.ActiveTurnID != params.ExpectedTurnID {
			rejectClientMutation(record, appwire.Conflict("turn is not active"))
			return nil
		}
		if snapshot.QueueRevision != params.ExpectedQueueRevision {
			rejectClientMutation(record, appwire.Conflict("queue revision changed"))
			return nil
		}
		if len(snapshot.InputQueue) == 0 && len(params.Input) == 0 {
			rejectClientMutation(record, appwire.Conflict("queue is empty"))
			return nil
		}
		for _, entry := range snapshot.InputQueue {
			if clientMutationQueueEntryReserved(snapshot, entry.ID) {
				rejectClientMutation(record, appwire.Conflict("queue contains an entry reserved by another mutation"))
				return nil
			}
		}
		reserveClientMutationTurnID(snapshot, record)
		record.StableQueueEntryIDs = clientMutationQueueIDs(snapshot.InputQueue)
		record.ExecutionState = "accepted"
		return nil
	}, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		entries, remaining, foundAll := takeClientMutationQueueEntries(snapshot.InputQueue, record.StableQueueEntryIDs)
		if !foundAll {
			rejectClientMutation(record, appwire.Conflict("reserved queue entries are no longer available"))
			return nil
		}
		for _, entry := range entries {
			removeQueuedMutationSource(snapshot, entry, "transformed")
		}
		steeringInput := combineClientMutationInputs(entries, params.Input)
		snapshot.InputQueue = remaining
		snapshot.QueueRevision++
		response = appwire.TurnDrainAsSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied)}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		addPendingSteering(snapshot, record, steeringInput)
		applyClientMutationRecord(record, result)
		return nil
	})
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnDrainAsSteerResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		var replayed appwire.TurnDrainAsSteerResponse
		if err := replayClientMutationResult(lookup.Record, &replayed); err != nil {
			return replayed, err
		}
		replayed.Receipt.Disposition = appwire.MutationDispositionReplayed
		replayed.Receipt.ProjectionState = lookup.Record.ProjectionState
		return replayed, nil
	}
	s.reflectDurableInputQueue()
	s.reflectDurableClientSteering()
	return response, nil
}

func combineClientMutationInputs(entries []clientMutationQueueEntry, extra []appwire.InputItem) []appwire.InputItem {
	texts := make([]string, 0, len(entries)+1)
	var images []ImageAttachment
	for _, entry := range entries {
		queued := queuedInputFromClientMutation(entry)
		if strings.TrimSpace(queued.Text) != "" {
			texts = append(texts, queued.Text)
		}
		images = append(images, queued.Images...)
	}
	if len(extra) > 0 {
		queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: extra})
		if strings.TrimSpace(queued.Text) != "" {
			texts = append(texts, queued.Text)
		}
		images = append(images, queued.Images...)
	}
	return clientMutationInput(strings.Join(texts, "\n\n"), images)
}

func (s *Session) clientMutationPromote(params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodPromote, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	request.Preconditions.ExpectedTurnID = params.ExpectedTurnID
	request.Preconditions.ExpectedEntryID = params.ExpectedEntryID
	var response appwire.TurnPromoteQueuedAsSteerResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		if params.ExpectedTurnID != "" && snapshot.ActiveTurnID != params.ExpectedTurnID {
			rejectClientMutation(record, appwire.Conflict("turn is not active"))
			return nil
		}
		if params.Index < 0 || params.Index >= len(snapshot.InputQueue) {
			rejectClientMutation(record, appwire.Conflict(fmt.Sprintf("promote: queue index %d out of range (depth %d)", params.Index, len(snapshot.InputQueue))))
			return nil
		}
		entry := snapshot.InputQueue[params.Index]
		if params.ExpectedEntryID != "" && entry.ID != params.ExpectedEntryID {
			rejectClientMutation(record, appwire.Conflict(fmt.Sprintf("promote: queue entry at index %d no longer matches the snapshot (queue changed)", params.Index)))
			return nil
		}
		if clientMutationQueueEntryReserved(snapshot, entry.ID) {
			rejectClientMutation(record, appwire.Conflict("queue entry is reserved by another mutation"))
			return nil
		}
		reserveClientMutationTurnID(snapshot, record)
		record.StableQueueEntryIDs = []string{entry.ID}
		record.ExecutionState = "accepted"
		return nil
	}, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		index := clientMutationQueueEntryIndex(snapshot.InputQueue, record.StableQueueEntryIDs[0])
		if index < 0 {
			rejectClientMutation(record, appwire.Conflict("reserved queue entry is no longer available"))
			return nil
		}
		entry := snapshot.InputQueue[index]
		snapshot.InputQueue = append(snapshot.InputQueue[:index], snapshot.InputQueue[index+1:]...)
		snapshot.QueueRevision++
		removeQueuedMutationSource(snapshot, entry, "transformed")
		response = appwire.TurnPromoteQueuedAsSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied)}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		addPendingSteering(snapshot, record, entry.Input)
		applyClientMutationRecord(record, result)
		return nil
	})
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		var replayed appwire.TurnPromoteQueuedAsSteerResponse
		if err := replayClientMutationResult(lookup.Record, &replayed); err != nil {
			return replayed, err
		}
		replayed.Receipt.Disposition = appwire.MutationDispositionReplayed
		replayed.Receipt.ProjectionState = lookup.Record.ProjectionState
		return replayed, nil
	}
	s.reflectDurableInputQueue()
	s.reflectDurableClientSteering()
	return response, nil
}

func (s *Session) clientMutationCancel(params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodCancel, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	request.Preconditions.ExpectedEntryID = params.ExpectedEntryID
	var response appwire.TurnCancelQueuedResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if params.Index < 0 || params.Index >= len(snapshot.InputQueue) {
			rejectClientMutation(record, appwire.Conflict(fmt.Sprintf("cancel: queue index %d out of range (depth %d)", params.Index, len(snapshot.InputQueue))))
			return nil
		}
		entry := snapshot.InputQueue[params.Index]
		if params.ExpectedEntryID != "" && entry.ID != params.ExpectedEntryID {
			rejectClientMutation(record, appwire.Conflict(fmt.Sprintf("cancel: queue entry at index %d no longer matches the snapshot (queue changed)", params.Index)))
			return nil
		}
		if clientMutationQueueEntryReserved(snapshot, entry.ID) {
			rejectClientMutation(record, appwire.Conflict("queue entry is reserved by another mutation"))
			return nil
		}
		record.StableQueueEntryIDs = []string{entry.ID}
		record.ExecutionState = "accepted"
		return nil
	}, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		index := clientMutationQueueEntryIndex(snapshot.InputQueue, record.StableQueueEntryIDs[0])
		if index < 0 {
			rejectClientMutation(record, appwire.Conflict("reserved queue entry is no longer available"))
			return nil
		}
		entry := snapshot.InputQueue[index]
		queued := queuedInputFromClientMutation(entry)
		response = appwire.TurnCancelQueuedResponse{
			RemovedText:   queued.Text,
			RemovedImages: len(queued.Images),
			Receipt:       mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied),
		}
		response.Receipt.ProjectionState = appwire.MutationProjectionRemoved
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		snapshot.InputQueue = append(snapshot.InputQueue[:index], snapshot.InputQueue[index+1:]...)
		snapshot.QueueRevision++
		removeQueuedMutationSource(snapshot, entry, "canceled")
		record.OperationState = clientMutationOperationTerminal
		record.ExecutionState = "canceled"
		record.ProjectionState = appwire.MutationProjectionRemoved
		record.Payload = nil
		record.Result = append(json.RawMessage(nil), result...)
		return nil
	})
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnCancelQueuedResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		var replayed appwire.TurnCancelQueuedResponse
		if err := replayClientMutationResult(lookup.Record, &replayed); err != nil {
			return replayed, err
		}
		replayed.Receipt.Disposition = appwire.MutationDispositionReplayed
		replayed.Receipt.ProjectionState = lookup.Record.ProjectionState
		return replayed, nil
	}
	s.reflectDurableInputQueue()
	return response, nil
}

func reserveClientMutationTurnID(snapshot *clientMutationSnapshot, record *clientMutationRecord) {
	snapshot.NextTurnSequence++
	record.StableTurnID = fmt.Sprintf("turn_%d", snapshot.NextTurnSequence)
}

func mutationReceipt(threadID string, record clientMutationRecord, disposition appwire.MutationDisposition) appwire.MutationReceipt {
	return appwire.MutationReceipt{
		ClientMutationID: record.ClientMutationID,
		Disposition:      disposition,
		ThreadID:         threadID,
		TurnID:           record.StableTurnID,
		QueueEntryIDs:    append([]string(nil), record.StableQueueEntryIDs...),
		ProjectionState:  appwire.MutationProjectionReflected,
	}
}

func applyClientMutationRecord(record *clientMutationRecord, result json.RawMessage) {
	record.OperationState = clientMutationOperationApplied
	record.ExecutionState = "accepted"
	record.ProjectionState = appwire.MutationProjectionReflected
	record.Result = append(json.RawMessage(nil), result...)
}

func addPendingSteering(snapshot *clientMutationSnapshot, record *clientMutationRecord, input []appwire.InputItem) {
	snapshot.PendingExecutions[record.ClientMutationID] = appwire.PendingMutation{
		ClientMutationID: record.ClientMutationID,
		Method:           record.Method,
		Input:            cloneClientMutationInput(input),
		ExecutionState:   "accepted",
		TurnID:           record.StableTurnID,
		ProjectionState:  appwire.MutationProjectionReflected,
	}
	snapshot.SteeringOrder = append(snapshot.SteeringOrder, record.ClientMutationID)
}

func replayClientMutationResult(record clientMutationRecord, response any) error {
	if len(record.Result) == 0 {
		return appwire.InternalError("client mutation result is missing")
	}
	if err := json.Unmarshal(record.Result, response); err != nil {
		return fmt.Errorf("decode client mutation result: %w", err)
	}
	return nil
}

func clientMutationQueueIDs(entries []clientMutationQueueEntry) []string {
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return ids
}

func removeQueuedMutationSource(snapshot *clientMutationSnapshot, entry clientMutationQueueEntry, executionState string) {
	delete(snapshot.BudgetReservations, entry.ClientMutationID)
	record, ok := snapshot.Journal[entry.ClientMutationID]
	if !ok {
		return
	}
	record.OperationState = clientMutationOperationTerminal
	record.ExecutionState = executionState
	record.ProjectionState = appwire.MutationProjectionRemoved
	record.Payload = nil
	snapshot.Journal[entry.ClientMutationID] = record
}

func clientMutationQueueEntryReserved(snapshot *clientMutationSnapshot, entryID string) bool {
	for _, record := range snapshot.Journal {
		if record.OperationState != clientMutationOperationInFlight {
			continue
		}
		for _, reservedID := range record.StableQueueEntryIDs {
			if reservedID == entryID {
				return true
			}
		}
	}
	return false
}

func clientMutationQueueEntryIndex(entries []clientMutationQueueEntry, id string) int {
	for i, entry := range entries {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

func takeClientMutationQueueEntries(
	queue []clientMutationQueueEntry,
	ids []string,
) (selected []clientMutationQueueEntry, remaining []clientMutationQueueEntry, foundAll bool) {
	selected = make([]clientMutationQueueEntry, 0, len(ids))
	selectedIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		index := clientMutationQueueEntryIndex(queue, id)
		if index < 0 {
			return nil, queue, false
		}
		selected = append(selected, queue[index])
		selectedIDs[id] = struct{}{}
	}
	remaining = make([]clientMutationQueueEntry, 0, len(queue)-len(selected))
	for _, entry := range queue {
		if _, selected := selectedIDs[entry.ID]; !selected {
			remaining = append(remaining, entry)
		}
	}
	return selected, remaining, true
}

func (s *Session) reflectDurableClientSteering() {
	s.queueEventsMu.Lock()
	defer s.queueEventsMu.Unlock()
	snapshot := s.clientMutations.snapshot()
	client := clientSteeringFromSnapshot(snapshot)
	s.mu.Lock()
	daemon := s.steeringQueue[:0]
	for _, entry := range s.steeringQueue {
		if entry.ClientMutationID == "" {
			daemon = append(daemon, entry)
		}
	}
	s.steeringQueue = append(daemon, client...)
	s.mu.Unlock()
}

func clientSteeringFromSnapshot(snapshot clientMutationSnapshot) []steeringMessage {
	client := make([]steeringMessage, 0, len(snapshot.SteeringOrder))
	for _, id := range snapshot.SteeringOrder {
		pending, ok := snapshot.PendingExecutions[id]
		if !ok || pending.ExecutionState != "accepted" {
			continue
		}
		queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
		client = append(client, steeringMessage{
			Text:             queued.Text,
			Images:           queued.Images,
			Source:           events.SteeringSourceUser,
			ClientMutationID: id,
			StableTurnID:     pending.TurnID,
		})
	}
	return client
}

// restoreDurableClientMutationQueues projects the mutation snapshot into the
// runtime queues before a restored Session becomes visible. It emits nothing:
// reconnect projections read the already-restored fields.
func (s *Session) restoreDurableClientMutationQueues() {
	incorporated := make(map[string]string, len(s.restoredClientMutationTurns))
	for id, turnID := range s.restoredClientMutationTurns {
		incorporated[id] = turnID
	}
	for _, turn := range s.history {
		if turn.ClientMutationID != "" {
			incorporated[turn.ClientMutationID] = turn.StableTurnID
		}
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.AcceptedTurns < uint64(s.turns) {
			snapshot.AcceptedTurns = uint64(s.turns)
		}
		for id, pending := range snapshot.PendingExecutions {
			record := snapshot.Journal[id]
			if pending.ExecutionState == "failureRecording" {
				continue
			}
			if stableTurnID, ok := incorporated[id]; ok && stableTurnID == pending.TurnID {
				record.ExecutionState = "incorporated"
				record.ProjectionState = appwire.MutationProjectionReflected
				if pending.Method == clientMutationMethodQueue || pending.Method == clientMutationMethodStart {
					pending.ExecutionState = "incorporated"
					pending.ProjectionState = appwire.MutationProjectionReflected
					snapshot.PendingExecutions[id] = pending
					snapshot.Journal[id] = record
				} else {
					record.OperationState = clientMutationOperationTerminal
					record.Payload = nil
					snapshot.Journal[id] = record
					delete(snapshot.PendingExecutions, id)
					removeClientMutationSteeringOrder(snapshot, id)
				}
				continue
			}
			if pending.ExecutionState != "claimed" {
				continue
			}
			if pending.Method == clientMutationMethodQueue {
				if len(pending.QueueEntryIDs) != 1 {
					return fmt.Errorf("claimed queue mutation %q has %d queue entry IDs", id, len(pending.QueueEntryIDs))
				}
				snapshot.InputQueue = append([]clientMutationQueueEntry{{
					ID:               pending.QueueEntryIDs[0],
					ClientMutationID: id,
					Input:            cloneClientMutationInput(pending.Input),
				}}, snapshot.InputQueue...)
				if snapshot.AcceptedTurns > 0 {
					snapshot.AcceptedTurns--
				}
				snapshot.BudgetReservations[id] = clientMutationBudgetReservation{TurnID: pending.TurnID, Slots: 1}
				delete(snapshot.PendingExecutions, id)
				record.ProjectionState = appwire.MutationProjectionReflected
			} else {
				pending.ExecutionState = "accepted"
				snapshot.PendingExecutions[id] = pending
				if pending.Method == clientMutationMethodStart {
					if snapshot.AcceptedTurns > 0 {
						snapshot.AcceptedTurns--
					}
					snapshot.BudgetReservations[id] = clientMutationBudgetReservation{
						TurnID: pending.TurnID,
						Slots:  1,
					}
				}
			}
			record.ExecutionState = "accepted"
			snapshot.Journal[id] = record
		}
		snapshot.QueueRevision++
		return nil
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("restore client mutations failed: %v", err)})
	}
	snapshot := s.clientMutations.snapshot()
	s.inputQueue = make([]queuedInput, 0, len(snapshot.InputQueue))
	for _, entry := range snapshot.InputQueue {
		s.inputQueue = append(s.inputQueue, queuedInputFromClientMutation(entry))
	}
	s.publishedQueueRevision = snapshot.QueueRevision
	s.steeringQueue = append(s.steeringQueue, clientSteeringFromSnapshot(snapshot)...)
}

func (s *Session) markClaimedUserTranscriptIncorporated(clientMutationID string) error {
	if clientMutationID == "" {
		return nil
	}
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		if pending.Method != clientMutationMethodQueue && pending.Method != clientMutationMethodStart {
			return fmt.Errorf("client mutation %q is %q, want user input", clientMutationID, pending.Method)
		}
		record, ok := snapshot.Journal[clientMutationID]
		if !ok {
			return fmt.Errorf("claimed client mutation %q has no journal record", clientMutationID)
		}
		record.ExecutionState = "incorporated"
		record.ProjectionState = appwire.MutationProjectionReflected
		pending.ExecutionState = "incorporated"
		pending.ProjectionState = appwire.MutationProjectionReflected
		snapshot.Journal[clientMutationID] = record
		snapshot.PendingExecutions[clientMutationID] = pending
		return nil
	})
}

func (s *Session) clientMutationUserTranscriptIncorporated(clientMutationID, stableTurnID string) bool {
	if clientMutationID == "" || s.clientMutations == nil {
		return false
	}
	pending, ok := s.clientMutations.snapshot().PendingExecutions[clientMutationID]
	return ok &&
		(pending.Method == clientMutationMethodQueue || pending.Method == clientMutationMethodStart) &&
		pending.ExecutionState == "incorporated" &&
		pending.TurnID == stableTurnID
}

func (s *Session) beginClientMutationFailure(clientMutationID string, cause error) error {
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok || pending.Method != clientMutationMethodStart {
			return fmt.Errorf("client start %q is not pending", clientMutationID)
		}
		record := snapshot.Journal[clientMutationID]
		record.ExecutionState = "failureRecording"
		record.Failure = &clientMutationFailure{Message: cause.Error()}
		pending.ExecutionState = "failureRecording"
		snapshot.Journal[clientMutationID] = record
		snapshot.PendingExecutions[clientMutationID] = pending
		return nil
	})
}

func (s *Session) recoverClientMutationFailures() error {
	if s == nil || s.clientMutations == nil {
		return nil
	}
	snapshot := s.clientMutations.snapshot()
	for id, pending := range snapshot.PendingExecutions {
		record := snapshot.Journal[id]
		if pending.Method != clientMutationMethodStart ||
			pending.ExecutionState != "failureRecording" ||
			record.Failure == nil {
			continue
		}
		if err := s.recordClientMutationFailure(id, pending, *record.Failure); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) recordClientMutationFailure(
	clientMutationID string,
	pending appwire.PendingMutation,
	failure clientMutationFailure,
) error {
	items := s.clientMutationTranscriptItems(clientMutationID, pending.TurnID)
	queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
	if !items.User {
		if err := s.clientMutationFailureFault("before_user"); err != nil {
			return err
		}
		turn := schema.NewTurn(schema.TurnUserInput, buildUserInputMessage(queued.Text, queued.Images))
		turn.ClientMutationID = clientMutationID
		turn.StableTurnID = pending.TurnID
		if err := s.writeTranscriptDurable(turn); err != nil {
			return fmt.Errorf("append failed client start input: %w", err)
		}
		s.mu.Lock()
		s.history = append(s.history, turn)
		s.mu.Unlock()
		if err := s.clientMutationFailureFault("after_user"); err != nil {
			return err
		}
	}
	if !items.Failure {
		if err := s.clientMutationFailureFault("before_failure"); err != nil {
			return err
		}
		info := &schema.TurnFailureInfo{Message: failure.Message}
		turn := schema.NewTurn(schema.TurnFailure, llm.System(failure.Message))
		turn.ClientMutationID = clientMutationID
		turn.StableTurnID = pending.TurnID
		turn.Error = info
		if err := s.writeTranscriptDurable(turn); err != nil {
			return fmt.Errorf("append failed client start diagnostic: %w", err)
		}
		s.mu.Lock()
		s.history = append(s.history, turn)
		s.mu.Unlock()
		if err := s.clientMutationFailureFault("after_failure"); err != nil {
			return err
		}
	}
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		record := snapshot.Journal[clientMutationID]
		record.OperationState = clientMutationOperationTerminal
		record.ExecutionState = "failed"
		record.ProjectionState = appwire.MutationProjectionReflected
		record.Payload = nil
		snapshot.Journal[clientMutationID] = record
		delete(snapshot.PendingExecutions, clientMutationID)
		if snapshot.ActiveTurnID == pending.TurnID {
			snapshot.ActiveTurnID = ""
		}
		return nil
	})
}

func (s *Session) clientMutationTranscriptItems(clientMutationID, stableTurnID string) clientMutationTranscriptItems {
	items := s.restoredClientMutationItems[clientMutationID]
	if items.StableTurnID != "" && items.StableTurnID != stableTurnID {
		items = clientMutationTranscriptItems{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if turn.ClientMutationID != clientMutationID || turn.StableTurnID != stableTurnID {
			continue
		}
		items.StableTurnID = stableTurnID
		switch turn.Kind {
		case schema.TurnUserInput:
			items.User = true
		case schema.TurnFailure:
			items.Failure = true
		}
	}
	return items
}

func (s *Session) clientMutationFailureFault(boundary string) error {
	if s.clientMutationFailureRecoveryFault == nil {
		return nil
	}
	return s.clientMutationFailureRecoveryFault(boundary)
}

func (s *Session) finalizeIncorporatedSteering(clientMutationID string) error {
	if clientMutationID == "" {
		return nil
	}
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		if pending.Method == clientMutationMethodQueue {
			return fmt.Errorf("queued input %q cannot finalize at transcript append", clientMutationID)
		}
		record := snapshot.Journal[clientMutationID]
		record.OperationState = clientMutationOperationTerminal
		record.ExecutionState = "incorporated"
		record.ProjectionState = appwire.MutationProjectionReflected
		record.Payload = nil
		snapshot.Journal[clientMutationID] = record
		delete(snapshot.PendingExecutions, clientMutationID)
		removeClientMutationSteeringOrder(snapshot, clientMutationID)
		return nil
	})
}

// completeClientMutationTurn is the lifecycle completion hook for Task 4.
// Queue transcript incorporation retains the payload and stable turn identity;
// only durable terminal completion may remove the runnable execution.
func (s *Session) completeClientMutationTurn(clientMutationID string) error {
	return s.completeClientMutationTurnWithState(clientMutationID, "terminal")
}

func (s *Session) completeClientMutationInterruptedTurn(clientMutationID string) error {
	return s.completeClientMutationTurnWithState(clientMutationID, "interrupted")
}

func (s *Session) completeClientMutationTurnWithState(clientMutationID, executionState string) error {
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		if pending.ExecutionState != "incorporated" {
			return nil
		}
		if pending.Method != clientMutationMethodQueue && pending.Method != clientMutationMethodStart {
			return fmt.Errorf("client mutation %q is not a user turn", clientMutationID)
		}
		record := snapshot.Journal[clientMutationID]
		record.OperationState = clientMutationOperationTerminal
		record.ExecutionState = executionState
		if snapshot.InterruptFence != nil && snapshot.InterruptFence.ExpectedTurnID == pending.TurnID {
			record.ExecutionState = "interrupted"
		}
		record.ProjectionState = appwire.MutationProjectionReflected
		record.Payload = nil
		snapshot.Journal[clientMutationID] = record
		delete(snapshot.PendingExecutions, clientMutationID)
		if snapshot.ActiveTurnID == pending.TurnID {
			snapshot.ActiveTurnID = ""
		}
		if snapshot.InterruptFence != nil && snapshot.InterruptFence.ExpectedTurnID == pending.TurnID {
			return finalizeClientMutationInterrupt(snapshot, s.ID())
		}
		return nil
	})
}

func removeClientMutationSteeringOrder(snapshot *clientMutationSnapshot, clientMutationID string) {
	for i, id := range snapshot.SteeringOrder {
		if id == clientMutationID {
			snapshot.SteeringOrder = append(snapshot.SteeringOrder[:i], snapshot.SteeringOrder[i+1:]...)
			return
		}
	}
}

func (s *Session) appendClientMutationTranscript(turn schema.Turn) error {
	if s.clientMutationTranscriptAppend != nil {
		return s.clientMutationTranscriptAppend(turn)
	}
	return s.writeTranscriptDurable(turn)
}
