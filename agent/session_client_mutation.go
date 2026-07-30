package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/serf/appwire"
)

var (
	errClientMutationMismatch = errors.New("client mutation ID reused with a different method or payload")
	errClientMutationOwner    = errors.New("client mutation owner is no longer active")
)

// NormalizeClientMutationError converts Session mutation failures into the
// structured AppWire contract at the daemon boundary. Existing WireErrors are
// already authoritative. A reused ID with different content is a protocol
// error; any other unclassified failure means the journal could not prove the
// outcome and retry must remain blocked until persistence recovers.
func NormalizeClientMutationError(clientMutationID string, err error) error {
	if err == nil {
		return nil
	}
	var wire appwire.WireError
	if errors.As(err, &wire) {
		return err
	}
	if errors.Is(err, errClientMutationMismatch) {
		return appwire.InvalidRequest(err.Error())
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: err.Error(),
		Data: appwire.ErrorData{
			SerfErrorInfo:    appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionBlocked,
			Cause:            "persistenceUnavailable",
		},
	}
}

const (
	clientMutationMethodStart     = "turn/start"
	clientMutationMethodInterrupt = "turn/interrupt"
)

type clientMutationOperationState string

const (
	clientMutationOperationInFlight clientMutationOperationState = "inFlight"
	clientMutationOperationApplied  clientMutationOperationState = "applied"
	clientMutationOperationRejected clientMutationOperationState = "rejected"
	clientMutationOperationTerminal clientMutationOperationState = "terminal"
)

type clientMutationDisposition string

const (
	clientMutationDispositionReserved clientMutationDisposition = "reserved"
	clientMutationDispositionReplayed clientMutationDisposition = "replayed"
	clientMutationDispositionJoined   clientMutationDisposition = "joined"
)

type clientMutationPreconditions struct {
	ExpectedTurnID        string  `json:"expected_turn_id,omitempty"`
	ExpectedEntryID       string  `json:"expected_entry_id,omitempty"`
	ExpectedQueueRevision *uint64 `json:"expected_queue_revision,omitempty"`
}

type clientMutationRejection struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    appwire.ErrorData `json:"data"`
}

type clientMutationRecord struct {
	ClientMutationID    string                          `json:"client_mutation_id"`
	Method              string                          `json:"method"`
	Payload             json.RawMessage                 `json:"payload,omitempty"`
	Preconditions       clientMutationPreconditions     `json:"preconditions,omitzero"`
	StableTurnID        string                          `json:"stable_turn_id,omitempty"`
	StableQueueEntryIDs []string                        `json:"stable_queue_entry_ids,omitempty"`
	PayloadHash         string                          `json:"payload_hash"`
	OperationState      clientMutationOperationState    `json:"operation_state"`
	ExecutionState      string                          `json:"execution_state"`
	ProjectionState     appwire.MutationProjectionState `json:"projection_state"`
	Result              json.RawMessage                 `json:"result,omitempty"`
	Rejection           *clientMutationRejection        `json:"rejection,omitempty"`
	Failure             *clientMutationFailure          `json:"failure,omitempty"`
	AttemptGeneration   uint64                          `json:"attempt_generation"`
}

type clientMutationFailure struct {
	Message string `json:"message"`
}

type clientMutationQueueEntry struct {
	ID               string              `json:"id"`
	ClientMutationID string              `json:"client_mutation_id"`
	Input            []appwire.InputItem `json:"input"`
}

type clientMutationBudgetReservation struct {
	TurnID string `json:"turn_id"`
	Slots  uint64 `json:"slots"`
}

type clientMutationInterruptFence struct {
	ClientMutationID string `json:"client_mutation_id"`
	ExpectedTurnID   string `json:"expected_turn_id"`
}

type clientMutationSnapshot struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	// ActiveTurnID is the sole durable authority used by retry-safe mutation
	// preconditions. Task 4 owns lifecycle writes; queue and steering
	// transitions only compare it while holding this store's serializer.
	ActiveTurnID           string                                     `json:"active_turn_id,omitempty"`
	AcceptedTurns          uint64                                     `json:"accepted_turns"`
	Journal                map[string]clientMutationRecord            `json:"journal"`
	InputQueue             []clientMutationQueueEntry                 `json:"input_queue"`
	QueueRevision          uint64                                     `json:"queue_revision"`
	NextTurnSequence       uint64                                     `json:"next_turn_sequence"`
	NextQueueEntrySequence uint64                                     `json:"next_queue_entry_sequence"`
	BudgetReservations     map[string]clientMutationBudgetReservation `json:"budget_reservations"`
	InterruptFence         *clientMutationInterruptFence              `json:"interrupt_fence,omitempty"`
	PendingExecutions      map[string]appwire.PendingMutation         `json:"pending_executions"`
	SteeringOrder          []string                                   `json:"steering_order,omitempty"`
}

type clientMutationRequest struct {
	ClientMutationID string
	Method           string
	Payload          json.RawMessage
	PayloadHash      string
	Preconditions    clientMutationPreconditions
}

// clientMutationPrepare atomically claims stable IDs, sequence numbers, and
// reserved resources for an unseen mutation. The store invokes it only while
// holding its serializer and before the mutation's first snapshot write.
type clientMutationPrepare func(*clientMutationSnapshot, *clientMutationRecord) error
type clientMutationEffect func(*clientMutationSnapshot, *clientMutationRecord) error

func newClientMutationRequest(method, id string, payload any) (clientMutationRequest, error) {
	if method == "" {
		return clientMutationRequest{}, errors.New("client mutation method is required")
	}
	if id == "" {
		return clientMutationRequest{}, errors.New("client mutation ID is required")
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return clientMutationRequest{}, fmt.Errorf("marshal client mutation payload: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return clientMutationRequest{
		ClientMutationID: id,
		Method:           method,
		Payload:          canonical,
		PayloadHash:      hex.EncodeToString(sum[:]),
	}, nil
}

// AcceptClientMutationStart durably owns a complete start intent before
// notifying the lifecycle runner. A lost response or refused wake leaves the
// accepted pending execution in the journal, and an identical retry returns
// the same stable turn rather than creating another logical turn.
func (s *Session) AcceptClientMutationStart(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Input = input.Items
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodStart, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}

	var response appwire.TurnStartResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
			return nil
		}
		if len(params.Input) == 0 {
			rejectClientMutation(record, appwire.InvalidParams("input is required"))
			return nil
		}
		if snapshot.ActiveTurnID != "" {
			rejectClientMutation(record, appwire.Conflict("turn is already active"))
			return nil
		}
		if s.cfg.MaxTurns > 0 && snapshot.AcceptedTurns+reservedClientMutationTurns(snapshot) >= uint64(s.cfg.MaxTurns) {
			rejectClientMutation(record, appwire.Conflict((&budgetExhaustionError{
				Budget: exhaustedBudgetTurns, Limit: s.cfg.MaxTurns, Resumable: true,
			}).Error()))
			return nil
		}
		reserveClientMutationTurnID(snapshot, record)
		record.ExecutionState = "accepted"
		snapshot.BudgetReservations[record.ClientMutationID] = clientMutationBudgetReservation{
			TurnID: record.StableTurnID,
			Slots:  1,
		}
		return nil
	}, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		response = appwire.TurnStartResponse{
			Turn: appwire.Turn{
				ID:     record.StableTurnID,
				Status: appwire.TurnStatusInProgress,
			},
			Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method)),
		}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		snapshot.ActiveTurnID = record.StableTurnID
		snapshot.PendingExecutions[record.ClientMutationID] = appwire.PendingMutation{
			ClientMutationID: record.ClientMutationID,
			Method:           record.Method,
			Input:            cloneClientMutationInput(params.Input),
			ExecutionState:   "accepted",
			TurnID:           record.StableTurnID,
			ProjectionState:  acceptedClientMutationProjection(record.Method),
		}
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
		return nil
	})
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnStartResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		if err := replayClientMutationResult(lookup.Record, &response); err != nil {
			return appwire.TurnStartResponse{}, err
		}
		response.Receipt.Disposition = appwire.MutationDispositionReplayed
		response.Receipt.ProjectionState = lookup.Record.ProjectionState
	}
	if pending, ok := s.clientMutations.snapshot().PendingExecutions[params.ClientMutationID]; ok && pending.ExecutionState == "accepted" {
		s.wakeClientMutationStart()
	}
	return response, nil
}

// claimClientMutationStart returns the next user turn owned by the durable
// start lifecycle. In addition to accepted starts, restore may expose a queued
// turn that crashed after claim under the same stable turn identity.
func (s *Session) claimClientMutationStart() (queuedInput, bool, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return queuedInput{}, false, err
	}
	var claimed queuedInput
	claimedQueue := false
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		for id, pending := range snapshot.PendingExecutions {
			if pending.Method != clientMutationMethodStart ||
				(pending.ExecutionState != "accepted" && pending.ExecutionState != "incorporated") {
				continue
			}
			if snapshot.InterruptFence != nil && snapshot.InterruptFence.ExpectedTurnID == pending.TurnID {
				continue
			}
			record, ok := snapshot.Journal[id]
			if !ok {
				return fmt.Errorf("accepted client start %q has no journal record", id)
			}
			if pending.ExecutionState == "accepted" {
				record.ExecutionState = "claimed"
				pending.ExecutionState = "claimed"
				delete(snapshot.BudgetReservations, id)
				snapshot.AcceptedTurns++
				// No transcript item exists for this claim yet, so it must
				// report pending, not reflected. When ExecutionState was
				// already "incorporated" (a crash-recovery reclaim of a start
				// whose transcript append already landed), this branch is
				// skipped and the reflected state markClaimedUserTranscriptIncorporated
				// set earlier is left untouched.
				record.ProjectionState = acceptedClientMutationProjection(record.Method)
				pending.ProjectionState = acceptedClientMutationProjection(record.Method)
			}
			snapshot.Journal[id] = record
			snapshot.PendingExecutions[id] = pending
			claimed = queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
			claimed.ClientMutationID = id
			claimed.StableTurnID = pending.TurnID
			return nil
		}
		for id, pending := range snapshot.PendingExecutions {
			if pending.Method != clientMutationMethodQueue ||
				pending.ExecutionState != "incorporated" ||
				pending.TurnID == "" ||
				snapshot.ActiveTurnID != pending.TurnID {
				continue
			}
			claimed = queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
			if len(pending.QueueEntryIDs) == 1 {
				claimed.ID = pending.QueueEntryIDs[0]
			}
			claimed.ClientMutationID = id
			claimed.StableTurnID = pending.TurnID
			return nil
		}
		if len(snapshot.InputQueue) == 0 {
			return nil
		}
		entry := snapshot.InputQueue[0]
		record, ok := snapshot.Journal[entry.ClientMutationID]
		if !ok ||
			record.Method != clientMutationMethodQueue ||
			record.StableTurnID == "" ||
			snapshot.ActiveTurnID != record.StableTurnID {
			return nil
		}
		record.ExecutionState = "claimed"
		record.ProjectionState = acceptedClientMutationProjection(record.Method)
		snapshot.Journal[entry.ClientMutationID] = record
		snapshot.PendingExecutions[entry.ClientMutationID] = appwire.PendingMutation{
			ClientMutationID: entry.ClientMutationID,
			Method:           record.Method,
			Input:            cloneClientMutationInput(entry.Input),
			ExecutionState:   "claimed",
			TurnID:           record.StableTurnID,
			QueueEntryIDs:    []string{entry.ID},
			ProjectionState:  acceptedClientMutationProjection(record.Method),
		}
		snapshot.InputQueue = snapshot.InputQueue[1:]
		snapshot.QueueRevision++
		delete(snapshot.BudgetReservations, entry.ClientMutationID)
		snapshot.AcceptedTurns++
		claimed = queuedInputFromClientMutation(entry)
		claimed.ClientMutationID = entry.ClientMutationID
		claimed.StableTurnID = record.StableTurnID
		claimedQueue = true
		return nil
	})
	if err != nil {
		return queuedInput{}, false, err
	}
	if claimedQueue {
		s.reflectDurableInputQueue()
	}
	return claimed, claimed.ClientMutationID != "", nil
}

// ProcessClientMutationStart claims and processes the next durable start using
// the payload and identity stored by AcceptClientMutationStart.
func (s *Session) ProcessClientMutationStart(ctx context.Context, onRunnable func()) (string, bool, error) {
	if !s.hasRunnableClientMutationStart() {
		return "", false, nil
	}
	if onRunnable != nil {
		onRunnable()
	}
	claimed, ok, err := s.claimClientMutationStart()
	if err != nil || !ok {
		return "", ok, err
	}
	ctx = withQueuedClientMutation(ctx, claimed)
	result, err := s.ProcessInputKind(ctx, claimed.Text, claimed.Images, EntryUserInput)
	return result, true, err
}

// InterruptClientMutation persists an interrupt fence under the lifecycle
// serializer, then releases it before cancellation and runner waiting. The
// runner may finalize the fence from completeClientMutationTurnWithState; if it
// does not, recovery finalizes it after the wait returns.
func (s *Session) InterruptClientMutation(
	ctx context.Context,
	params appwire.TurnInterruptParams,
	cancelAndWait func(),
) (appwire.TurnInterruptResponse, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodInterrupt, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	request.Preconditions.ExpectedTurnID = params.ExpectedTurnID
	lookup, err := s.clientMutations.reservePrepared(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if params.ExpectedTurnID == "" {
			rejectClientMutation(record, appwire.InvalidParams("expectedTurnId is required"))
			return nil
		}
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is already pending"))
			return nil
		}
		if snapshot.ActiveTurnID != params.ExpectedTurnID {
			rejectClientMutation(record, appwire.Conflict("turn is not active"))
			return nil
		}
		record.StableTurnID = params.ExpectedTurnID
		record.ExecutionState = "interruptRequested"
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: params.ClientMutationID,
			ExpectedTurnID:   params.ExpectedTurnID,
		}
		return nil
	})
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnInterruptResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		s.clientMutations.clearInterruptCallbackCompleted(params.ClientMutationID)
		return interruptResponseFromRecord(lookup.Record, appwire.MutationDispositionReplayed)
	}
	if lookup.Disposition == clientMutationDispositionJoined {
		if s.clientMutationInterruptJoined != nil {
			s.clientMutationInterruptJoined()
		}
		select {
		case <-lookup.OwnerDone:
			return s.InterruptClientMutation(ctx, params, cancelAndWait)
		case <-ctx.Done():
			return appwire.TurnInterruptResponse{}, ctx.Err()
		}
	}
	if lookup.Lease == nil {
		return appwire.TurnInterruptResponse{}, appwire.InternalError("interrupt mutation owner is missing")
	}
	defer lookup.Lease.Release()

	if cancelAndWait != nil && !s.clientMutations.interruptCallbackCompleted(params.ClientMutationID) {
		cancelAndWait()
		current, terminal, err := s.clientMutations.markInterruptCallbackCompleted(lookup.Lease)
		if err != nil {
			return appwire.TurnInterruptResponse{}, err
		}
		if terminal {
			s.clientMutations.clearInterruptCallbackCompleted(params.ClientMutationID)
			return interruptResponseFromRecord(current, appwire.MutationDispositionApplied)
		}
	}
	current := s.clientMutations.snapshot().Journal[params.ClientMutationID]
	if current.OperationState == clientMutationOperationTerminal {
		s.clientMutations.clearInterruptCallbackCompleted(params.ClientMutationID)
		return interruptResponseFromRecord(current, appwire.MutationDispositionApplied)
	}
	if err := s.clientMutations.update(lookup.Lease, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if err := finalizeClientMutationInterrupt(snapshot, s.ID()); err != nil {
			return err
		}
		terminal, ok := snapshot.Journal[record.ClientMutationID]
		if !ok {
			return fmt.Errorf("interrupt mutation %q has no terminal journal record", record.ClientMutationID)
		}
		*record = terminal
		return nil
	}); err != nil {
		current := s.clientMutations.snapshot().Journal[params.ClientMutationID]
		if current.OperationState == clientMutationOperationTerminal {
			s.clientMutations.clearInterruptCallbackCompleted(params.ClientMutationID)
			return interruptResponseFromRecord(current, appwire.MutationDispositionApplied)
		}
		return appwire.TurnInterruptResponse{}, err
	}
	s.clientMutations.clearInterruptCallbackCompleted(params.ClientMutationID)
	return interruptResponseFromRecord(
		s.clientMutations.snapshot().Journal[params.ClientMutationID],
		appwire.MutationDispositionApplied,
	)
}

func interruptResponseFromRecord(
	record clientMutationRecord,
	disposition appwire.MutationDisposition,
) (appwire.TurnInterruptResponse, error) {
	var response appwire.TurnInterruptResponse
	if err := replayClientMutationResult(record, &response); err != nil {
		return response, err
	}
	response.Receipt.Disposition = disposition
	response.Receipt.ProjectionState = record.ProjectionState
	return response, nil
}

func finalizeClientMutationInterrupt(snapshot *clientMutationSnapshot, threadID string) error {
	fence := snapshot.InterruptFence
	if fence == nil {
		return nil
	}
	record, ok := snapshot.Journal[fence.ClientMutationID]
	if !ok {
		return fmt.Errorf("interrupt fence %q has no journal record", fence.ClientMutationID)
	}
	for id, pending := range snapshot.PendingExecutions {
		if pending.TurnID != fence.ExpectedTurnID {
			continue
		}
		target, ok := snapshot.Journal[id]
		if !ok {
			return fmt.Errorf("interrupt target %q has no journal record", id)
		}
		target.OperationState = clientMutationOperationTerminal
		target.ExecutionState = "interrupted"
		// reflected, not removed, and the choice is deliberate. An interrupt
		// ends the turn these inputs were accepted into; it does not un-accept
		// them, and the transcript keeps whatever they produced before the
		// cancel landed. removed means "this input is gone, drop your optimistic
		// copy and show nothing", which would be a lie about input the session
		// did act on. Both states retire the optimistic copy the same way in the
		// browser, so nothing observable pins this today -- the difference is
		// what the daemon is asserting, and it is asserting the input was
		// incorporated.
		target.ProjectionState = appwire.MutationProjectionReflected
		target.Payload = nil
		snapshot.Journal[id] = target
		delete(snapshot.PendingExecutions, id)
		delete(snapshot.BudgetReservations, id)
	}
	record.StableTurnID = fence.ExpectedTurnID
	response := appwire.TurnInterruptResponse{
		Receipt: mutationReceipt(threadID, record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method)),
	}
	result, err := json.Marshal(response)
	if err != nil {
		return err
	}
	record.OperationState = clientMutationOperationTerminal
	record.ExecutionState = "interrupted"
	// The durable record and the serialized receipt above must come from the
	// same helper. interruptResponseFromRecord overwrites the deserialized
	// receipt with this field on every replay, so a literal here would make
	// the receipt's own projection state dead and let the two drift silently.
	record.ProjectionState = acceptedClientMutationProjection(record.Method)
	record.Payload = nil
	record.Result = result
	snapshot.Journal[fence.ClientMutationID] = record
	snapshot.ActiveTurnID = ""
	snapshot.InterruptFence = nil
	return nil
}

func (s *Session) recoverClientMutationInterrupt() error {
	if s == nil || s.clientMutations == nil {
		return nil
	}
	if s.clientMutations.snapshot().InterruptFence == nil {
		return nil
	}
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		return finalizeClientMutationInterrupt(snapshot, s.ID())
	})
}

func (s *Session) returnClaimedClientMutationStart(clientMutationID string) error {
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok || pending.Method != clientMutationMethodStart || pending.ExecutionState != "claimed" {
			return nil
		}
		record := snapshot.Journal[clientMutationID]
		record.ExecutionState = "accepted"
		pending.ExecutionState = "accepted"
		snapshot.Journal[clientMutationID] = record
		snapshot.PendingExecutions[clientMutationID] = pending
		if snapshot.AcceptedTurns > 0 {
			snapshot.AcceptedTurns--
		}
		snapshot.BudgetReservations[clientMutationID] = clientMutationBudgetReservation{
			TurnID: pending.TurnID,
			Slots:  1,
		}
		return nil
	})
	if err == nil {
		s.wakeClientMutationStart()
	}
	return err
}

// SetClientMutationStartWakeFunc installs the runner wake seam. Accepted work
// restored before the runner exists is woken immediately after installation.
func (s *Session) SetClientMutationStartWakeFunc(wake func()) {
	s.mu.Lock()
	s.clientMutationStartWake = wake
	s.mu.Unlock()
	if wake != nil && s.hasRunnableClientMutationStart() {
		wake()
	}
}

func (s *Session) wakeClientMutationStart() {
	s.mu.Lock()
	wake := s.clientMutationStartWake
	s.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (s *Session) hasRunnableClientMutationStart() bool {
	if s == nil || s.clientMutations == nil {
		return false
	}
	snapshot := s.clientMutations.snapshot()
	for _, pending := range snapshot.PendingExecutions {
		if pending.Method == clientMutationMethodStart &&
			(pending.ExecutionState == "accepted" || pending.ExecutionState == "incorporated") {
			return true
		}
		if pending.Method == clientMutationMethodQueue &&
			pending.ExecutionState == "incorporated" &&
			pending.TurnID != "" &&
			snapshot.ActiveTurnID == pending.TurnID {
			return true
		}
	}
	if len(snapshot.InputQueue) > 0 {
		record := snapshot.Journal[snapshot.InputQueue[0].ClientMutationID]
		return record.Method == clientMutationMethodQueue &&
			record.StableTurnID != "" &&
			snapshot.ActiveTurnID == record.StableTurnID
	}
	return false
}

type clientMutationFaults struct {
	AfterReservation           func() error
	BeforeEffectSnapshotRename func() error
	AfterEffectSnapshotRename  func() error
}

type clientMutationOwner struct {
	token             uint64
	attemptGeneration uint64
	done              chan struct{}
}

type clientMutationStore struct {
	// mu is the store serializer: one read-modify-write at a time, held across
	// the durable write so a second mutation cannot interleave between
	// validation and commit.
	mu sync.Mutex
	// stateMu guards the committed generation alone and is never held across a
	// durable write. It exists so snapshot(), a pure read of the last committed
	// generation, does not queue behind another mutation's fsync -- the daemon
	// projects that snapshot into thread/read while it holds the AppWire
	// projection gate, where a stall blocks the whole session event bridge.
	stateMu                     sync.RWMutex
	fs                          afero.Fs
	stateDir                    string
	sessionID                   string
	state                       clientMutationSnapshot
	owners                      map[string]clientMutationOwner
	interruptCallbacksCompleted map[string]struct{}
	nextOwnerToken              uint64
	faults                      clientMutationFaults
}

type clientMutationLease struct {
	store             *clientMutationStore
	clientMutationID  string
	token             uint64
	attemptGeneration uint64
}

func (l *clientMutationLease) Release() {
	if l == nil || l.store == nil {
		return
	}
	l.store.release(l)
}

type clientMutationLookup struct {
	Disposition            clientMutationDisposition
	Record                 clientMutationRecord
	Lease                  *clientMutationLease
	OwnerDone              <-chan struct{}
	OwnerAttemptGeneration uint64
}

func newClientMutationStore(stateDir, sessionID string) (*clientMutationStore, error) {
	return newClientMutationStoreFS(afero.NewOsFs(), stateDir, sessionID, clientMutationFaults{})
}

func newClientMutationStoreFS(fs afero.Fs, stateDir, sessionID string, faults clientMutationFaults) (*clientMutationStore, error) {
	state, err := loadClientMutationSnapshotFS(fs, stateDir, sessionID)
	if err != nil {
		return nil, err
	}
	return &clientMutationStore{
		fs:                          fs,
		stateDir:                    stateDir,
		sessionID:                   sessionID,
		state:                       state,
		owners:                      make(map[string]clientMutationOwner),
		interruptCallbacksCompleted: make(map[string]struct{}),
		faults:                      faults,
	}, nil
}

func (s *clientMutationStore) reserve(request clientMutationRequest) (clientMutationLookup, error) {
	return s.reservePrepared(request, nil)
}

// executeAtomic is the Task 3 transition primitive. It deliberately keeps the
// store serializer from journal lookup through precondition validation,
// reservation persistence, effect persistence, and publication. Queue and
// steering mutations must not use reservePrepared followed by update: that
// split is appropriate for long-running execution ownership, but would let a
// different queue mutation invalidate an index, entry ID, turn, revision, or
// budget between validation and effect.
func (s *clientMutationStore) executeAtomic(
	request clientMutationRequest,
	prepare clientMutationPrepare,
	effect clientMutationEffect,
) (clientMutationLookup, error) {
	if err := validateClientMutationRequest(request); err != nil {
		return clientMutationLookup{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.state.Journal[request.ClientMutationID]
	if exists {
		if current.Method != request.Method || current.PayloadHash != request.PayloadHash {
			return clientMutationLookup{}, errClientMutationMismatch
		}
		switch current.OperationState {
		case clientMutationOperationApplied, clientMutationOperationRejected, clientMutationOperationTerminal:
			return clientMutationLookup{
				Disposition: clientMutationDispositionReplayed,
				Record:      cloneClientMutationRecord(current),
			}, nil
		case clientMutationOperationInFlight:
			// An in-flight record with no serializer holder is a crash-recovery
			// takeover. Stable IDs and reservations already live in the snapshot;
			// prepare must not run again.
		default:
			return clientMutationLookup{}, fmt.Errorf("client mutation %q has invalid operation state %q", request.ClientMutationID, current.OperationState)
		}
	}

	if !exists {
		next := cloneClientMutationSnapshot(s.state)
		current = clientMutationRecord{
			ClientMutationID: request.ClientMutationID,
			Method:           request.Method,
			Payload:          append(json.RawMessage(nil), request.Payload...),
			PayloadHash:      request.PayloadHash,
			Preconditions:    cloneClientMutationPreconditions(request.Preconditions),
			OperationState:   clientMutationOperationInFlight,
			ExecutionState:   "pending",
			ProjectionState:  appwire.MutationProjectionPending,
		}
		if prepare != nil {
			if err := prepare(&next, &current); err != nil {
				return clientMutationLookup{}, err
			}
		}
		current.AttemptGeneration++
		next.Journal[request.ClientMutationID] = current
		next = cloneClientMutationSnapshot(next)
		current = next.Journal[request.ClientMutationID]
		if _, err := saveClientMutationSnapshotFS(s.fs, s.stateDir, s.sessionID, next, clientMutationWriteReservation, s.faults); err != nil {
			return clientMutationLookup{}, err
		}
		s.commitStateLocked(next)
		if current.OperationState != clientMutationOperationInFlight {
			return clientMutationLookup{
				Disposition: clientMutationDispositionReserved,
				Record:      cloneClientMutationRecord(current),
			}, nil
		}
		if s.faults.AfterReservation != nil {
			if err := s.faults.AfterReservation(); err != nil {
				return clientMutationLookup{}, err
			}
		}
	} else {
		next := cloneClientMutationSnapshot(s.state)
		current.AttemptGeneration++
		next.Journal[request.ClientMutationID] = current
		if _, err := saveClientMutationSnapshotFS(s.fs, s.stateDir, s.sessionID, next, clientMutationWriteReservation, s.faults); err != nil {
			return clientMutationLookup{}, err
		}
		s.commitStateLocked(next)
		if s.faults.AfterReservation != nil {
			if err := s.faults.AfterReservation(); err != nil {
				return clientMutationLookup{}, err
			}
		}
	}

	next := cloneClientMutationSnapshot(s.state)
	current = next.Journal[request.ClientMutationID]
	if effect != nil {
		if err := effect(&next, &current); err != nil {
			return clientMutationLookup{}, err
		}
	}
	next.Journal[request.ClientMutationID] = current
	next = cloneClientMutationSnapshot(next)
	if err := validateClientMutationSnapshot(next, s.sessionID); err != nil {
		return clientMutationLookup{}, err
	}
	renamed, err := saveClientMutationSnapshotFS(s.fs, s.stateDir, s.sessionID, next, clientMutationWriteEffect, s.faults)
	if renamed {
		s.commitStateLocked(next)
	}
	if err != nil {
		return clientMutationLookup{}, err
	}
	if !renamed {
		return clientMutationLookup{}, errors.New("client mutation snapshot was not committed")
	}
	return clientMutationLookup{
		Disposition: clientMutationDispositionReserved,
		Record:      cloneClientMutationRecord(current),
	}, nil
}

func (s *clientMutationStore) reservePrepared(
	request clientMutationRequest,
	prepare clientMutationPrepare,
) (clientMutationLookup, error) {
	if err := validateClientMutationRequest(request); err != nil {
		return clientMutationLookup{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.state.Journal[request.ClientMutationID]
	if exists {
		if current.Method != request.Method || current.PayloadHash != request.PayloadHash {
			return clientMutationLookup{}, errClientMutationMismatch
		}
		switch current.OperationState {
		case clientMutationOperationApplied, clientMutationOperationRejected, clientMutationOperationTerminal:
			return clientMutationLookup{
				Disposition: clientMutationDispositionReplayed,
				Record:      cloneClientMutationRecord(current),
			}, nil
		case clientMutationOperationInFlight:
			if owner, ok := s.owners[request.ClientMutationID]; ok &&
				owner.attemptGeneration == current.AttemptGeneration {
				return clientMutationLookup{
					Disposition:            clientMutationDispositionJoined,
					Record:                 cloneClientMutationRecord(current),
					OwnerDone:              owner.done,
					OwnerAttemptGeneration: owner.attemptGeneration,
				}, nil
			}
		default:
			return clientMutationLookup{}, fmt.Errorf("client mutation %q has invalid operation state %q", request.ClientMutationID, current.OperationState)
		}
	}

	next := cloneClientMutationSnapshot(s.state)
	record := current
	if !exists {
		record = clientMutationRecord{
			ClientMutationID: request.ClientMutationID,
			Method:           request.Method,
			Payload:          append(json.RawMessage(nil), request.Payload...),
			PayloadHash:      request.PayloadHash,
			Preconditions:    cloneClientMutationPreconditions(request.Preconditions),
			OperationState:   clientMutationOperationInFlight,
			ExecutionState:   "pending",
			ProjectionState:  appwire.MutationProjectionPending,
		}
		if prepare != nil {
			if err := prepare(&next, &record); err != nil {
				return clientMutationLookup{}, err
			}
		}
	}
	record.AttemptGeneration++
	next.Journal[request.ClientMutationID] = record
	next = cloneClientMutationSnapshot(next)
	record = next.Journal[request.ClientMutationID]
	if _, err := saveClientMutationSnapshotFS(s.fs, s.stateDir, s.sessionID, next, clientMutationWriteReservation, s.faults); err != nil {
		return clientMutationLookup{}, err
	}
	s.commitStateLocked(next)

	if record.OperationState != clientMutationOperationInFlight {
		return clientMutationLookup{
			Disposition: clientMutationDispositionReserved,
			Record:      cloneClientMutationRecord(record),
		}, nil
	}

	s.nextOwnerToken++
	owner := clientMutationOwner{
		token:             s.nextOwnerToken,
		attemptGeneration: record.AttemptGeneration,
		done:              make(chan struct{}),
	}
	s.owners[request.ClientMutationID] = owner
	lease := &clientMutationLease{
		store:             s,
		clientMutationID:  request.ClientMutationID,
		token:             owner.token,
		attemptGeneration: owner.attemptGeneration,
	}
	if s.faults.AfterReservation != nil {
		if err := s.faults.AfterReservation(); err != nil {
			s.releaseOwnerLocked(lease)
			return clientMutationLookup{}, err
		}
	}
	return clientMutationLookup{
		Disposition: clientMutationDispositionReserved,
		Record:      cloneClientMutationRecord(record),
		Lease:       lease,
	}, nil
}

func (s *clientMutationStore) update(lease *clientMutationLease, mutate func(*clientMutationSnapshot, *clientMutationRecord) error) error {
	if lease == nil || lease.store != s {
		return errClientMutationOwner
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.owners[lease.clientMutationID]
	if !ok || owner.token != lease.token || owner.attemptGeneration != lease.attemptGeneration {
		return errClientMutationOwner
	}
	defer s.releaseOwnerLocked(lease)

	next := cloneClientMutationSnapshot(s.state)
	record, ok := next.Journal[lease.clientMutationID]
	if !ok || record.AttemptGeneration != lease.attemptGeneration ||
		record.OperationState != clientMutationOperationInFlight {
		return errClientMutationOwner
	}
	if mutate != nil {
		if err := mutate(&next, &record); err != nil {
			return err
		}
	}
	next.Journal[lease.clientMutationID] = record
	next = cloneClientMutationSnapshot(next)
	if err := validateClientMutationSnapshot(next, s.sessionID); err != nil {
		return err
	}

	renamed, err := saveClientMutationSnapshotFS(s.fs, s.stateDir, s.sessionID, next, clientMutationWriteEffect, s.faults)
	if renamed {
		s.commitStateLocked(next)
	}
	if err != nil {
		return err
	}
	if !renamed {
		return errors.New("client mutation snapshot was not committed")
	}
	return nil
}

func (s *clientMutationStore) interruptCallbackCompleted(clientMutationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.interruptCallbacksCompleted[clientMutationID]
	return ok
}

func (s *clientMutationStore) markInterruptCallbackCompleted(
	lease *clientMutationLease,
) (clientMutationRecord, bool, error) {
	if lease == nil || lease.store != s {
		return clientMutationRecord{}, false, errClientMutationOwner
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.owners[lease.clientMutationID]
	if !ok || owner.token != lease.token || owner.attemptGeneration != lease.attemptGeneration {
		return clientMutationRecord{}, false, errClientMutationOwner
	}
	record, ok := s.state.Journal[lease.clientMutationID]
	if !ok || record.AttemptGeneration != lease.attemptGeneration {
		return clientMutationRecord{}, false, errClientMutationOwner
	}
	if record.OperationState == clientMutationOperationTerminal {
		return cloneClientMutationRecord(record), true, nil
	}
	if record.OperationState != clientMutationOperationInFlight {
		return clientMutationRecord{}, false, errClientMutationOwner
	}
	s.interruptCallbacksCompleted[lease.clientMutationID] = struct{}{}
	return cloneClientMutationRecord(record), false, nil
}

func (s *clientMutationStore) clearInterruptCallbackCompleted(clientMutationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.interruptCallbacksCompleted, clientMutationID)
}

// commitStateLocked publishes a generation that is already durable. The caller
// holds mu for the whole read-modify-write; this narrows the window a reader
// can contend on to the assignment itself.
func (s *clientMutationStore) commitStateLocked(next clientMutationSnapshot) {
	s.stateMu.Lock()
	s.state = next
	s.stateMu.Unlock()
}

// snapshot returns the last committed generation. It takes only stateMu, so it
// answers while another mutation is still writing its own generation to disk --
// the answer is the same either way, since an uncommitted generation is not
// state yet.
func (s *clientMutationStore) snapshot() clientMutationSnapshot {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return cloneClientMutationSnapshot(s.state)
}

func (s *clientMutationStore) mutate(mutate func(*clientMutationSnapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneClientMutationSnapshot(s.state)
	if err := mutate(&next); err != nil {
		return err
	}
	next = cloneClientMutationSnapshot(next)
	if err := validateClientMutationSnapshot(next, s.sessionID); err != nil {
		return err
	}
	renamed, err := saveClientMutationSnapshotFS(s.fs, s.stateDir, s.sessionID, next, clientMutationWriteEffect, s.faults)
	if renamed {
		s.commitStateLocked(next)
	}
	if err != nil {
		return err
	}
	if !renamed {
		return errors.New("client mutation snapshot was not committed")
	}
	return nil
}

func (s *clientMutationStore) release(lease *clientMutationLease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseOwnerLocked(lease)
}

func (s *clientMutationStore) releaseOwnerLocked(lease *clientMutationLease) {
	owner, ok := s.owners[lease.clientMutationID]
	if !ok || owner.token != lease.token || owner.attemptGeneration != lease.attemptGeneration {
		return
	}
	delete(s.owners, lease.clientMutationID)
	close(owner.done)
}

func validateClientMutationRequest(request clientMutationRequest) error {
	if request.ClientMutationID == "" {
		return errors.New("client mutation ID is required")
	}
	if request.Method == "" {
		return errors.New("client mutation method is required")
	}
	if len(request.Payload) == 0 || !json.Valid(request.Payload) {
		return errors.New("client mutation payload must be valid JSON")
	}
	sum := sha256.Sum256(request.Payload)
	if request.PayloadHash != hex.EncodeToString(sum[:]) {
		return errors.New("client mutation payload hash does not match payload")
	}
	return nil
}

func cloneClientMutationSnapshot(src clientMutationSnapshot) clientMutationSnapshot {
	dst := src
	dst.Journal = make(map[string]clientMutationRecord, len(src.Journal))
	for id, record := range src.Journal {
		dst.Journal[id] = cloneClientMutationRecord(record)
	}
	dst.InputQueue = make([]clientMutationQueueEntry, len(src.InputQueue))
	for i, entry := range src.InputQueue {
		dst.InputQueue[i] = entry
		dst.InputQueue[i].Input = cloneClientMutationInput(entry.Input)
	}
	dst.BudgetReservations = make(map[string]clientMutationBudgetReservation, len(src.BudgetReservations))
	maps.Copy(dst.BudgetReservations, src.BudgetReservations)
	if src.InterruptFence != nil {
		fence := *src.InterruptFence
		dst.InterruptFence = &fence
	}
	dst.PendingExecutions = make(map[string]appwire.PendingMutation, len(src.PendingExecutions))
	for id, pending := range src.PendingExecutions {
		pending.Input = cloneClientMutationInput(pending.Input)
		pending.QueueEntryIDs = append([]string(nil), pending.QueueEntryIDs...)
		dst.PendingExecutions[id] = pending
	}
	dst.SteeringOrder = append([]string(nil), src.SteeringOrder...)
	return dst
}

func cloneClientMutationRecord(src clientMutationRecord) clientMutationRecord {
	dst := src
	dst.Payload = append(json.RawMessage(nil), src.Payload...)
	dst.Preconditions = cloneClientMutationPreconditions(src.Preconditions)
	dst.StableQueueEntryIDs = append([]string(nil), src.StableQueueEntryIDs...)
	dst.Result = append(json.RawMessage(nil), src.Result...)
	if src.Rejection != nil {
		rejection := *src.Rejection
		dst.Rejection = &rejection
	}
	if src.Failure != nil {
		failure := *src.Failure
		dst.Failure = &failure
	}
	return dst
}

func cloneClientMutationPreconditions(src clientMutationPreconditions) clientMutationPreconditions {
	dst := src
	if src.ExpectedQueueRevision != nil {
		revision := *src.ExpectedQueueRevision
		dst.ExpectedQueueRevision = &revision
	}
	return dst
}

func cloneClientMutationInput(src []appwire.InputItem) []appwire.InputItem {
	dst := make([]appwire.InputItem, len(src))
	for i, item := range src {
		dst[i] = item
		dst[i].Data = append([]byte(nil), item.Data...)
		if item.Metadata != nil {
			dst[i].Metadata = make(map[string]string, len(item.Metadata))
			maps.Copy(dst[i].Metadata, item.Metadata)
		}
	}
	return dst
}
