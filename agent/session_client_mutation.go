package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/serf/appwire"
)

var (
	errClientMutationMismatch = errors.New("client mutation ID reused with a different method or payload")
	errClientMutationOwner    = errors.New("client mutation owner is no longer active")
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
	ExpectedTurnID        string  `json:"expectedTurnId,omitempty"`
	ExpectedEntryID       string  `json:"expectedEntryId,omitempty"`
	ExpectedQueueRevision *uint64 `json:"expectedQueueRevision,omitempty"`
}

type clientMutationRejection struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    appwire.ErrorData `json:"data"`
}

type clientMutationRecord struct {
	ClientMutationID    string                          `json:"clientMutationId"`
	Method              string                          `json:"method"`
	Payload             json.RawMessage                 `json:"payload,omitempty"`
	Preconditions       clientMutationPreconditions     `json:"preconditions,omitempty"`
	StableTurnID        string                          `json:"stableTurnId,omitempty"`
	StableQueueEntryIDs []string                        `json:"stableQueueEntryIds,omitempty"`
	PayloadHash         string                          `json:"payloadHash"`
	OperationState      clientMutationOperationState    `json:"operationState"`
	ExecutionState      string                          `json:"executionState"`
	ProjectionState     appwire.MutationProjectionState `json:"projectionState"`
	Result              json.RawMessage                 `json:"result,omitempty"`
	Rejection           *clientMutationRejection        `json:"rejection,omitempty"`
	AttemptGeneration   uint64                          `json:"attemptGeneration"`
}

type clientMutationQueueEntry struct {
	ID               string              `json:"id"`
	ClientMutationID string              `json:"clientMutationId"`
	Input            []appwire.InputItem `json:"input"`
}

type clientMutationBudgetReservation struct {
	TurnID string `json:"turnId"`
	Slots  uint64 `json:"slots"`
}

type clientMutationInterruptFence struct {
	ClientMutationID string `json:"clientMutationId"`
	ExpectedTurnID   string `json:"expectedTurnId"`
}

type clientMutationSnapshot struct {
	Version   int    `json:"version"`
	SessionID string `json:"sessionId"`
	// ActiveTurnID is the sole durable authority used by retry-safe mutation
	// preconditions. Task 4 owns lifecycle writes; queue and steering
	// transitions only compare it while holding this store's serializer.
	ActiveTurnID           string                                     `json:"activeTurnId,omitempty"`
	AcceptedTurns          uint64                                     `json:"acceptedTurns"`
	Journal                map[string]clientMutationRecord            `json:"journal"`
	InputQueue             []clientMutationQueueEntry                 `json:"inputQueue"`
	QueueRevision          uint64                                     `json:"queueRevision"`
	NextTurnSequence       uint64                                     `json:"nextTurnSequence"`
	NextQueueEntrySequence uint64                                     `json:"nextQueueEntrySequence"`
	BudgetReservations     map[string]clientMutationBudgetReservation `json:"budgetReservations"`
	InterruptFence         *clientMutationInterruptFence              `json:"interruptFence,omitempty"`
	PendingExecutions      map[string]appwire.PendingMutation         `json:"pendingExecutions"`
	SteeringOrder          []string                                   `json:"steeringOrder,omitempty"`
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
	mu             sync.Mutex
	fs             afero.Fs
	stateDir       string
	sessionID      string
	state          clientMutationSnapshot
	owners         map[string]clientMutationOwner
	nextOwnerToken uint64
	faults         clientMutationFaults
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
		fs:        fs,
		stateDir:  stateDir,
		sessionID: sessionID,
		state:     state,
		owners:    make(map[string]clientMutationOwner),
		faults:    faults,
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
		s.state = next
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
		s.state = next
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
		s.state = next
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
	s.state = next

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
		s.state = next
	}
	if err != nil {
		return err
	}
	if !renamed {
		return errors.New("client mutation snapshot was not committed")
	}
	return nil
}

func (s *clientMutationStore) snapshot() clientMutationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		s.state = next
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
	for id, reservation := range src.BudgetReservations {
		dst.BudgetReservations[id] = reservation
	}
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
			for key, value := range item.Metadata {
				dst[i].Metadata[key] = value
			}
		}
	}
	return dst
}
