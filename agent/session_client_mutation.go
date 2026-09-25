package agent

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
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
	if _, ok := errors.AsType[appwire.WireError](err); ok {
		return err
	}
	if errors.Is(err, errClientMutationMismatch) {
		return appwire.InvalidRequest(err.Error())
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: err.Error(),
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
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

// clientMutationPreconditions records what a mutation was conditioned on, so a
// retry can be recognised as the same request. No turn id appears here because
// control is session-scoped: it applies to whatever the session is running, and
// the only preconditions left name a real object rather than a moment in time.
type clientMutationPreconditions struct {
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
	// SteeringKind is accepted atomically with a typed pending notification.
	// Reconstruction restores it onto the delivered steering entry.
	SteeringKind string `json:"steering_kind,omitempty"`
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

// clientMutationInterruptFence names the turn an accepted interrupt is ending,
// which every other durable transition compares itself against so a turn being
// cancelled cannot also be steered, queued against, or completed normally.
//
// ExpectedTurnID is the id the interrupt ACTUALLY cancelled, not the one the
// client asked for: a session-scoped interrupt supplies no target and takes the
// durable name instead. It is empty when the running turn had no durable name —
// a real state, not a missing value. Nothing then matches the fence, which is
// right: cancellation is what ends such a turn, and no accepted input exists to
// mark interrupted.
type clientMutationInterruptFence struct {
	ClientMutationID string `json:"client_mutation_id"`
	ExpectedTurnID   string `json:"expected_turn_id"`
}

type clientMutationPendingExecutions map[string]appwire.PendingMutation

type clientMutationSnapshot struct {
	// HumanNote is absent until canonical authority has been established; empty is a saved clear.
	HumanNote *string `json:"human_note,omitempty"`
	Version   int     `json:"version"`
	SessionID string  `json:"session_id"`
	// ActiveTurnID is the sole durable authority used by retry-safe mutation
	// preconditions, and it names the turn that is RUNNING — not merely one a
	// client mutation reserved. Queue and steering transitions only compare it.
	//
	// Five runtime sites SET it, each while holding this store's serializer:
	// AcceptClientMutationStart and claimClientMutationStart for turns a client
	// asked for, popQueueHead for one claimed off the input queue,
	// claimSteeringCarrierInput (session_client_mutation_queue.go) for the turn
	// a steer is handed to, and mintRunningTurnID (session_active_turn.go) for
	// the turns the agent starts for itself — a goal continuation and a
	// notification wake — which have no mutation to name them and would
	// otherwise publish an id these preconditions reject. The accept side names it only when the slot is FREE:
	// a running turn keeps its name, so a follow-up accepted behind the turn a
	// dead process left running cannot steal the slot — which would aim a Stop
	// at the wrong turn — nor clear the guard it was admitted through. The claim
	// side names it UNCONDITIONALLY, and is the authority: a claim happens at a
	// turn boundary and deterministically takes the oldest pending start, so the
	// turn it names is the one about to run, while naming only a free slot would
	// let a follow-up accepted into the slot a settling turn just released keep
	// the name while the older claimed turn ran unfenced.
	//
	// Four runtime sites CLEAR it, also serialized, and the list matters more
	// than it looks, because
	// releaseRunningTurnID's compare-and-clear exists to survive them:
	// completeClientMutationTurnWithState and failClientMutationTurn clear the
	// id of the turn they settle; releaseRunningTurnID clears only an id it
	// wrote; and finalizeClientMutationInterrupt clears UNCONDITIONALLY. That
	// last one is why an unconditional release would be wrong — an interrupt can
	// free the slot while a turn is still unwinding, a turn/start can claim it,
	// and the unwinding turn would then wipe that mutation's compare-and-commit
	// target.
	//
	// One more write is NOT serialized and is not a runtime write at all:
	// forgetRunningTurnNoOneOwns normalizes the value during load, before the
	// store is serving anyone. Counting it among the runtime writers would
	// mislead a concurrency audit in the other direction.
	//
	// It does not survive the process: loadClientMutationSnapshotFS drops a
	// value no pending execution owns, because a turn that was running when
	// the daemon died is not running now, and an id nothing can settle would
	// reject every later turn/start forever.
	ActiveTurnID  string                          `json:"active_turn_id,omitempty"`
	AcceptedTurns uint64                          `json:"accepted_turns"`
	Journal       map[string]clientMutationRecord `json:"journal"`
	InputQueue    []clientMutationQueueEntry      `json:"input_queue"`
	// QueueHeld parks the input queue after a Stop: the messages stay where they
	// are and nothing claims them until the user asks for one to run ("Stop
	// should cancel execution and wait", kata wms7).
	//
	// Durable because the queue it parks is durable: a daemon that restarts must
	// not treat parked messages as work to resume. There is deliberately no
	// process-local mirror -- the first attempt at this kata had one and the
	// review found it drifting on three of four writers, across restarts, and on
	// a rejected promote.
	QueueHeld bool `json:"queue_held,omitempty"`
	// SteeringHeld parks pending user steering after a Stop, mirroring QueueHeld
	// (issue #174): a Stop that lands at a turn boundary is Applied, and without
	// this gate the still-OPEN steering rail restarts the session and delivers the
	// steer to the model anyway. The steer never moves storage -- it stays in
	// PendingExecutions/SteeringOrder where it already durably lives, so causal
	// provenance is never at risk by construction (issue #146, Option C — park in
	// place). Delivery stays a steering injection ("redirect"), not a converted
	// new instruction.
	//
	// Durable for the same reason QueueHeld is: a daemon that restarts must not
	// treat the parked steer as work to resume. Cleared on the same
	// user-initiated-run triggers that clear QueueHeld (turn/start, turn/queue,
	// turn/drainAsSteer, turn/promoteQueuedAsSteer), and deliberately NOT cleared
	// on cancel-queued.
	SteeringHeld           bool                                       `json:"steering_held,omitempty"`
	QueueRevision          uint64                                     `json:"queue_revision"`
	NextTurnSequence       uint64                                     `json:"next_turn_sequence"`
	NextQueueEntrySequence uint64                                     `json:"next_queue_entry_sequence"`
	BudgetReservations     map[string]clientMutationBudgetReservation `json:"budget_reservations"`
	InterruptFence         *clientMutationInterruptFence              `json:"interrupt_fence,omitempty"`
	PendingExecutions      clientMutationPendingExecutions            `json:"pending_executions"`
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
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	defer release()
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
		// A non-empty slot refuses a new start, with one exception: the turn this
		// session inherited from a dead process, and only once it is actually
		// RUNNING again (see recoveredTurnRunning). That inherited turn is the
		// session's own recovered work, so the caller's prompt belongs behind it
		// and the send settles with an applied receipt instead of being returned
		// as an unaccepted mutation. While it is still merely accepted -- restore
		// resets a claimed start to accepted so the runner can reclaim it -- the
		// refusal stays: admitting a follow-up in that window makes two starts
		// simultaneously claimable, which is what produced the review's
		// ownership, ordering and Stop-targeting findings. A turn this process
		// started keeps the refusal too, because the web composer's routing
		// contract reads "turn is already active" as the answer to a turn/start
		// while a turn is active. The serve loop runs one turn at a time and
		// processes a wake only after the current turn returns, so this accepted
		// start is claimed after the recovered turn finishes.
		if snapshot.ActiveTurnID != "" && !s.recoveredTurnRunning(snapshot) {
			rejectClientMutation(record, appwire.Conflict("turn is already active"))
			return nil
		}
		if s.cfg.MaxTurns > 0 && snapshot.AcceptedTurns+reservedClientMutationTurns(snapshot) >= uint64(s.cfg.MaxTurns) {
			rejectClientMutation(record, appwire.Conflict((&budgetExhaustionError{
				Budget: exhaustedBudgetTurns, Limit: s.cfg.MaxTurns, Resumable: true,
			}).Error()))
			return nil
		}
		// The user is speaking again, so the wait a Stop started is over. Their
		// new turn runs first and the drain loop takes the parked messages after
		// it, which is the ordinary queue behaviour they were promised (wms7).
		snapshot.QueueHeld = false
		// A user-initiated run releases the parked steer too: the opening turn
		// will drain the steering queue at its boundary, so the steer the Stop
		// parked is delivered into it (issue #174).
		snapshot.SteeringHeld = false
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
		// Name the new turn ONLY when the slot is free. The slot names the turn
		// that is RUNNING, and a running turn keeps its name: an accepted
		// follow-up admitted behind the recovered turn has not run yet, so
		// repointing the slot at it would aim a Stop at the wrong turn and clear
		// the guard the follow-up was admitted through. claimClientMutationStart
		// names the follow-up when it is claimed, and there it is authoritative.
		if snapshot.ActiveTurnID == "" {
			snapshot.ActiveTurnID = record.StableTurnID
		}
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

// recoveredStartTurnID returns the durable ActiveTurnID when a client
// turn/start owns it, and "" otherwise. restoreDurableClientMutationQueues
// captures this into Session.recoveredTurnID, so the recovered turn it names is
// a recovered USER turn only: a queue-origin turn leaves it empty, and no
// follow-up start is admitted behind a turn the claim path would never take.
func (s *Session) recoveredStartTurnID() string {
	snapshot := s.clientMutations.snapshot()
	if snapshot.ActiveTurnID == "" {
		return ""
	}
	for _, pending := range snapshot.PendingExecutions {
		if pending.Method == clientMutationMethodStart && pending.TurnID == snapshot.ActiveTurnID {
			return snapshot.ActiveTurnID
		}
	}
	return ""
}

// recoveredTurnRunning reports whether the slot names the inherited recovered
// turn AND that turn has actually started running again. It is the second half
// of the admission precondition in AcceptClientMutationStart.
//
// "Running again" is ExecutionState "claimed" or "incorporated", and both are
// needed. Restore rewinds a claimed start to "accepted" so the runner can
// reclaim it, so a reclaimed start is "claimed" -- and an inherited turn whose
// user transcript entry had already landed before the crash is "incorporated"
// instead: restore only rewinds "claimed", and the runner's reclaim of an
// incorporated start leaves the mark as it found it. Either way the inherited
// turn runs again; only "accepted" is the pre-reclaim window in which a
// follow-up must stay refused.
func (s *Session) recoveredTurnRunning(snapshot *clientMutationSnapshot) bool {
	if s.recoveredTurnID == "" || snapshot.ActiveTurnID != s.recoveredTurnID {
		return false
	}
	for _, pending := range snapshot.PendingExecutions {
		if pending.Method != clientMutationMethodStart || pending.TurnID != snapshot.ActiveTurnID {
			continue
		}
		return pending.ExecutionState == "claimed" || pending.ExecutionState == "incorporated"
	}
	return false
}

// claimableClientMutationStartIDs returns the mutation ids of the starts the
// claim path may claim, in the order the user spoke them: the inherited
// recovered turn first, then each follow-up in reserved turn sequence order.
//
// Turn ids are appwire.ClientMutationTurnID(sequence) ("turn_m<N>"), reserved
// monotonically from snapshot.NextTurnSequence, so the numeric suffix orders
// them exactly. That order is the order the user spoke: the lowest sequence is
// the oldest prompt. The sort is the whole point -- PendingExecutions is a map,
// and ranging it directly gives no order at all, so the serve loop could claim
// and run a follow-up before the turn the user spoke first. Both the claim path
// (which takes the head as the one start it claims) and the runnable predicate
// (which must name that same head) call this, so the two can never disagree.
func (s *Session) claimableClientMutationStartIDs(snapshot *clientMutationSnapshot) []string {
	startIDs := make([]string, 0, len(snapshot.PendingExecutions))
	for id, pending := range snapshot.PendingExecutions {
		if pending.Method != clientMutationMethodStart ||
			(pending.ExecutionState != "accepted" && pending.ExecutionState != "incorporated") {
			continue
		}
		startIDs = append(startIDs, id)
	}
	slices.SortFunc(startIDs, func(a, b string) int {
		aID := snapshot.PendingExecutions[a].TurnID
		bID := snapshot.PendingExecutions[b].TurnID
		// The inherited turn is the user's OLDER prompt -- it was already
		// accepted, and spoken, when the process died -- so it runs before
		// every follow-up admitted behind it, whatever the two ids look
		// like. It cannot be ordered by a reserved sequence: the id a
		// resume carries is a legacy "turn_11" spelling as often as it is
		// "turn_m<N>", and a legacy id has no sequence to compare, which
		// would sort it LAST and run the newest prompt first. Prioritising
		// it here is what makes recovery order independent of the id
		// spelling. The rest keep the sequence order below.
		// Prioritise the inherited turn ONLY when its id carries no reserved
		// sequence to compare -- the legacy "turn_11" spelling this exists for.
		// When it parses as turn_m<N> the sequence comparison below already
		// orders it: a genuinely inherited running turn was reserved before any
		// follow-up admitted behind it, so sequence order puts it first anyway.
		// Prioritising it unconditionally would reorder prompts after a crash:
		// once the recovered turn completes, the accept side may name a NEWER
		// follow-up in the freed slot (accept names the slot only when free), and
		// a process that dies before the claim re-names it restores that NEWER
		// turn as the inherited one -- which must not outrank the older follow-up
		// still pending accepted.
		if inherited := s.recoveredTurnID; inherited != "" {
			if _, inheritedParses := clientMutationStartSequence(inherited); !inheritedParses {
				aInherited := aID == inherited
				bInherited := bID == inherited
				if aInherited != bInherited {
					if aInherited {
						return -1
					}
					return 1
				}
			}
		}
		aSeq, aOK := clientMutationStartSequence(aID)
		bSeq, bOK := clientMutationStartSequence(bID)
		switch {
		case aOK && bOK:
			if order := cmp.Compare(aSeq, bSeq); order != 0 {
				return order
			}
		case aOK != bOK:
			// An id that does not parse -- a hand-written or legacy name --
			// has no sequence to order by, so it sorts last.
			if aOK {
				return -1
			}
			return 1
		}
		// Deterministic tie-break: equal sequences cannot happen (a
		// sequence is reserved once) and two unparsed ids have no order of
		// their own, so the mutation id decides rather than the map.
		return strings.Compare(a, b)
	})
	return startIDs
}

// claimClientMutationStart returns the next user turn owned by the durable
// start lifecycle. In addition to accepted starts, restore may expose a queued
// turn that crashed after claim under the same stable turn identity.
//
// No start is claimed while an interrupt fence exists (see the guard below), and
// runnableClientMutationStartTurnID reports the same rule: the start path must
// not arm for, or wake on, a claim this call would refuse. The queue branches
// are not fenced.
func (s *Session) claimClientMutationStart() (queuedInput, bool, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return queuedInput{}, false, err
	}
	defer release()
	if err := s.ensureClientMutationStore(); err != nil {
		return queuedInput{}, false, err
	}
	if s.cfg.testOnly.clientMutationStartClaiming != nil {
		s.cfg.testOnly.clientMutationStartClaiming()
	}
	// The writer is sampled under s.mu here, before the serializer takes
	// clientMutations.mu; the refusal is read inside using only the writer's
	// own lock, so the serializer never waits on s.mu.
	writer := s.attachedTranscript()
	var claimed queuedInput
	claimedQueue := false
	err = s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		// No start is claimed while an interrupt fence exists. The fence names
		// the turn a Stop is ending and stays set until that Stop finalizes, and
		// finalization retires the fenced turn ALONE. A start claimed behind the
		// fenced turn in that window is stranded: the Stop's cancellation unwinds
		// the fenced turn, finalization never reaches the follow-up, and the
		// claimed start is left with no runner to run it and no interrupt to
		// release it. Leaving every start pending until the fence is cleared --
		// the fence lives only while a Stop is in flight -- keeps the follow-up
		// claimable once the Stop settles, and the interrupt path wakes it then.
		// The queue branches below are unaffected; only start claims are refused.
		if snapshot.InterruptFence == nil {
			// Take the FIRST of the claimable starts in the order the user spoke
			// them. The selection and its ordering rationale live in
			// claimableClientMutationStartIDs, shared with the runnable predicate
			// so the turn this call claims is exactly the turn that predicate
			// names.
			startIDs := s.claimableClientMutationStartIDs(snapshot)
			if len(startIDs) > 0 {
				// Exactly one start is claimed per call, and the helper already
				// selected the oldest, so this takes the head explicitly rather
				// than looping over a slice whose body always returns.
				id := startIDs[0]
				pending := snapshot.PendingExecutions[id]
				record, ok := snapshot.Journal[id]
				if !ok {
					return fmt.Errorf("accepted client start %q has no journal record", id)
				}
				// The refusal is decided where a candidate is selected and a claimed
				// input is about to be returned -- never before claimability is
				// known, so a store with nothing to claim stays quiet. It covers an
				// incorporated recovery entry as well as a fresh claim: both are
				// announced as a running turn, and one announced on a poisoned
				// transcript is refused by the turn gate behind a phantom running
				// notification.
				if refusal := refuseOnUnhealthyTranscript(writer); refusal != nil {
					return refusal
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
				// The claim is the AUTHORITY on the slot, so it names this turn
				// UNCONDITIONALLY. The accept-side rule names a turn only when the
				// slot is free, because an accepted-but-unrun follow-up must not
				// steal the running turn's name; that rule alone is not enough.
				// After a running turn releases the slot, a later follow-up can be
				// accepted into the now-free slot before the serve loop claims the
				// OLDEST pending start, so the slot would name that follow-up. A
				// claim happens at a turn boundary and the sort above
				// deterministically selects the oldest pending start, so the turn
				// being named here is the one about to run -- never a concurrently
				// running turn -- and leaving the slot pointed at the follow-up
				// would aim a Stop at the wrong turn while the claimed turn ran
				// unfenced. A Stop fences the claimed turn.
				snapshot.ActiveTurnID = pending.TurnID
				snapshot.Journal[id] = record
				snapshot.PendingExecutions[id] = pending
				claimed = queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
				claimed.ClientMutationID = id
				claimed.StableTurnID = pending.TurnID
				return nil
			}
		}
		for id, pending := range snapshot.PendingExecutions {
			if pending.Method != clientMutationMethodQueue ||
				pending.ExecutionState != "incorporated" ||
				pending.TurnID == "" ||
				snapshot.ActiveTurnID != pending.TurnID {
				continue
			}
			// Same refusal as the start branch: this incorporated queued turn
			// would be announced as running too.
			if refusal := refuseOnUnhealthyTranscript(writer); refusal != nil {
				return refusal
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
		if refusal := refuseOnUnhealthyTranscript(writer); refusal != nil {
			return refusal
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

// clientMutationStartSequence parses the reserved turn sequence out of
// appwire.ClientMutationTurnID's "turn_m<N>" spelling, so accepted starts can
// be claimed in the order they were reserved. It reports false for any id that
// is not one of these, which callers order last rather than treat as sequence 0.
func clientMutationStartSequence(turnID string) (uint64, bool) {
	digits, ok := strings.CutPrefix(turnID, "turn_m")
	if !ok || digits == "" {
		return 0, false
	}
	sequence, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return sequence, true
}

// ClientMutationStartPhase says which side of the durable claim a
// ProcessClientMutationStart callback is reporting.
type ClientMutationStartPhase int

const (
	// ClientMutationStartArmed reports that the turn is about to be claimed.
	// The daemon wires cancellation here, so a turn/interrupt that finalizes
	// the claimed start before it is published still finds a runner to cancel.
	ClientMutationStartArmed ClientMutationStartPhase = iota
	// ClientMutationStartClaimed reports that the claim committed. The daemon
	// may now publish the running turn.
	ClientMutationStartClaimed
)

// ProcessClientMutationStart claims and processes the next durable start using
// the payload and identity stored by AcceptClientMutationStart.
//
// onRunnable, when non-nil, is called twice: once with
// ClientMutationStartArmed before the claim, so the daemon can wire
// cancellation to a turn that is about to exist, and once with
// ClientMutationStartClaimed after the claim commits, when publishing the
// running turn is safe. It is never called with ClientMutationStartClaimed for
// a claim that refused, so a refused claim never announces a running turn.
func (s *Session) ProcessClientMutationStart(ctx context.Context, onRunnable func(turnID string, phase ClientMutationStartPhase)) (string, bool, error) {
	// The serve loop calls this as an idle probe after every message it
	// processes, so most calls find nothing to run. Such a probe is not
	// admitted work and must not take a retirement lease: beginning one
	// restarts the idle interval, which would move the deadline from the
	// settlement to whenever the probe happened to run. Returning without a
	// lease is safe because a start accepted after this check wakes the loop,
	// which probes again, and until it is claimed the pending start is itself
	// retirement evidence (retirementInputBlockers). A start that is runnable
	// here is re-checked under the lease below.
	if _, runnable := s.runnableClientMutationStartTurnID(); !runnable {
		return "", false, nil
	}
	release, admissionErr := s.beginRetirementMutation("turn")
	if admissionErr != nil {
		return "", false, admissionErr
	}
	defer release()
	turnID, runnable := s.runnableClientMutationStartTurnID()
	if !runnable {
		return "", false, nil
	}
	if err := s.refuseBeforeClaimingOnUnhealthyTranscript(); err != nil {
		return "", false, err
	}
	// Arm before the claim. Publishing is what must wait for the claim, but
	// cancellation cannot: a Stop that finalizes the claimed start in the
	// window before publication has nothing to cancel otherwise, and the start
	// then runs despite having been stopped.
	if onRunnable != nil {
		onRunnable(turnID, ClientMutationStartArmed)
	}
	claimed, ok, err := s.claimClientMutationStart()
	if err != nil || !ok {
		return "", ok, err
	}
	// Announce only once the claim has committed. A poisoning that lands
	// between the pre-check above and the claim makes the claim refuse, and a
	// turn announced for it is a phantom: the daemon has published a running
	// turn and wired cancellation to an id that will never run.
	//
	// The claim's own refusal is decided before its commit, so a poisoning can
	// still land between the two. The announcement is therefore made under the
	// transcript's write door rather than after a check outside it: a poisoning
	// append records the poison while holding that door, so the announce is
	// ordered against every poisoning -- either it is already visible and
	// nothing is announced, or it lands after the announce and the turn meets
	// the ordinary mid-run refusal, with the claim handed back below.
	announceRefusal := s.attachedTranscript().WhileHealthy(func() {
		if onRunnable != nil {
			onRunnable(claimed.StableTurnID, ClientMutationStartClaimed)
		}
	})
	if announceRefusal != nil {
		if giveBackErr := s.returnUnrunStartClaim(claimed); giveBackErr != nil {
			return "", false, errors.Join(errWhileHealthyRefuses(announceRefusal), fmt.Errorf("return claimed input: %w", giveBackErr))
		}
		return "", false, errWhileHealthyRefuses(announceRefusal)
	}
	if s.cfg.testOnly.clientMutationStartAnnounced != nil {
		s.cfg.testOnly.clientMutationStartAnnounced()
	}
	ctx = withQueuedClientMutation(ctx, claimed)
	// Prepare the claimed input's skill selection at actual consumption: one
	// atomic group tied to this input's durable identity, all-or-nothing
	// before any dependent work dispatches.
	ctx = s.contextWithSelectedSkills(ctx, claimed)
	result, err := s.ProcessInputKind(ctx, claimed.Text, claimed.Images, EntryUserInput)
	// The claim above spends a turn of the budget and the turn loop's gate can
	// refuse after it, when poisoning lands in between. Give the claim back
	// rather than leave it spent on a turn that never ran.
	//
	// Keyed on the transcript's own refusals -- poisoned or closed -- and nothing
	// wider, deliberately. Every other pre-incorporation failure either unwinds
	// where it happened or is reclaimed by startup recovery, and a wider key
	// would return a claim whose turn is already recorded in the transcript — an
	// incorporation marking that fails after the entry is durable would run the
	// start twice. A close can land after the claim and announce, so it owes the
	// same give-back the poisoned case gets.
	if transcriptRefusedClaim(err) {
		if returnErr := s.returnUnrunStartClaim(claimed); returnErr != nil {
			err = errors.Join(err, fmt.Errorf("return claimed input: %w", returnErr))
		}
	}
	// A run that failed before this turn's prompt was recorded leaves the claim
	// with nothing to finish it in this process: only the transcript's own
	// refusals are given back above, so any other such failure keeps the start
	// claimed and the serve loop carries on to the next input. For the turn this
	// session INHERITED from a dead process that is not survivable: the claim
	// selection offers only accepted/incorporated starts, so the recovered turn
	// is skipped, the follow-up admitted behind it is claimed next, and naming
	// the slot unconditionally moves ActiveTurnID off the recovered turn -- which
	// restart recovery then replays AFTER the newer prompt, out of order. Hand
	// the recovered turn's claim back instead, so it returns to accepted and the
	// recovered-first ordering in claimableClientMutationStartIDs re-claims it
	// before the follow-up.
	//
	// Keyed on the RECOVERED turn and on its prompt not being recorded -- never
	// on the error's type. The recorded test is clientMutationTranscriptHolds,
	// not the store's incorporation mark: the user-input turn is durably appended
	// BEFORE that mark is written, so a mark write that fails leaves a pending the
	// store still calls "claimed" whose turn is already on disk. Handing that one
	// back would re-claim it, append the same user turn again and run the prompt's
	// model round twice -- exactly what the transcript-refusal give-back above is
	// safe from because it fires only when the append itself was refused.
	//
	// Three things hold the branch shut: the transcript does not already hold the
	// turn (the fact above); an ordinary turn this process started is not the
	// recovered turn, so its claim is left exactly as it was; and the give-back is
	// bounded to ONCE per recovered turn (recoveredTurnClaimReturned), because the
	// wake it sends drives an immediate retry. One in-process retry is for a
	// failure that may be transient; a second consecutive failure of the same turn
	// is deferred to restart recovery instead of spinning on it.
	//
	// A give-back that did NOT commit does not spend it: the claim is still
	// exactly where it was, so the retry is still owed. Nothing can spin on the
	// unspent flag either -- returnClaimedClientMutationStart wakes the runner
	// only when its own write succeeded, so a failed return sends no wake.
	//
	// A transcript refusal is excluded outright: the give-back above already
	// returned that claim, so this branch would be a no-op that still spent the
	// retry on nothing. That case belongs to the transcript-refusal branch alone.
	if err != nil && claimed.StableTurnID == s.recoveredTurnID &&
		!transcriptRefusedClaim(err) &&
		!s.recoveredTurnClaimReturned &&
		!s.clientMutationTranscriptHolds(claimed.ClientMutationID, claimed.StableTurnID) {
		if returnErr := s.returnUnrunStartClaim(claimed); returnErr != nil {
			err = errors.Join(err, fmt.Errorf("return claimed input: %w", returnErr))
		} else {
			s.recoveredTurnClaimReturned = true
		}
	}
	return result, true, err
}

// sessionIsQuiesced is the agent's one answer to "has this session stopped
// doing anything", and it is the precondition every session-scoped control
// mutation gets to consult. Control mutations name no turn (appwire v3 dropped
// expectedTurnId from all of them), so an id the client happens to hold is
// never the question; whether there is work to act on is.
//
// It takes TWO facts because there is a window where each is false on its own
// and the session is nonetheless working:
//
//   - running: the session has entered SessionProcessing, or has work queued
//     that will resume it without the user (Session.WireState).
//   - claimedTurnID: a turn accepted by turn/start and claimed by the drain
//     loop, which has not completed. Between the claim and SessionProcessing
//     lies every turn's PRE-TURN WORK -- slash-command expansion runs an inline
//     shell span there, seconds wide -- and throughout it the DAEMON reports the
//     thread active while the SESSION still reports itself settled. Stop was
//     refused for the whole of that window, to users looking straight at a Stop
//     button (kata vewa).
//
// The daemon reports active there off its own `processing` flag, which it holds
// across the whole of an input rather than only the model round. An earlier
// version of this comment blamed a turn reservation; that was wrong, and
// measurably so -- 12 of 12 samples inside the window on a live stack showed
// processing=true with no reservation set. The disagreement is between two
// different objects' idea of "running", not between a reservation and a flag.
//
// The two facts are sampled separately, and must be: WireState reaches for locks
// the mutation-store serializer may never wait on, so it cannot be read from
// inside the serialized callback that reads the snapshot. This function is where
// that constraint is written down rather than left as an anonymous condition.
//
// The daemon's own answer (server/appwire_runtime.go's appStatus) is this same
// predicate projected onto what the daemon can see. They are deliberately the
// same shape: the status the wire publishes, the capabilities published beside
// it, and the precondition that decides whether Stop lands must not be able to
// disagree.
func sessionIsQuiesced(running bool, claimedTurnID string) bool {
	return !running && strings.TrimSpace(claimedTurnID) == ""
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
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	defer release()
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodInterrupt, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	// Half of sessionIsQuiesced, sampled HERE rather than inside the callback
	// below: WireState reaches for the session lock, the input queue and the
	// notification set, and the store serializer must never wait on any of
	// those. The turn can settle between this sample and the fence; a Stop
	// pressed as its turn ended then cancels nothing, which is the outcome it
	// asked for.
	sessionRunning := s.WireState() == string(SessionProcessing)
	lookup, err := s.clientMutations.reservePrepared(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is already pending"))
			return nil
		}
		if sessionIsQuiesced(sessionRunning, snapshot.ActiveTurnID) {
			rejectClientMutation(record, appwire.Conflict("session is not processing"))
			return nil
		}
		// The fence records the turn this interrupt is actually cancelling,
		// which is the durable name of whatever was running -- and the empty
		// name when that turn had none. An interrupt that matches no pending
		// execution is correct there: the cancellation is what ends such a turn,
		// and there is no accepted input to mark interrupted.
		target := snapshot.ActiveTurnID
		record.StableTurnID = target
		record.ExecutionState = "interruptRequested"
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: params.ClientMutationID,
			ExpectedTurnID:   target,
		}
		// Park the queue from the moment the Stop is ACCEPTED, not when the fence
		// finalizes. The claim this Stop cancels is returned to the queue by the
		// runner on its way out and the drain loop is right behind it, so holding
		// only at finalize leaves exactly the window the restart used (wms7).
		snapshot.QueueHeld = true
		// Park pending user steering at the same moment, mirroring QueueHeld
		// (issue #174). Without this gate the steering rail stays OPEN after a
		// Stop lands at a turn boundary: wakeForPendingSteering fires
		// unconditionally below, restarts the session, and delivers the steer to
		// the model anyway. The steer stays in PendingExecutions/SteeringOrder
		// -- it never moves storage, so its causal provenance is never at risk
		// (issue #146, Option C — park in place).
		//
		// Only when there is steering to park, though. A hold armed over an
		// empty rail catches nothing this Stop cancelled and everything the
		// user steers afterwards: that steer is Applied, then parked behind a
		// gate only turn/start, turn/queue, drainAsSteer or promote can clear,
		// so it never reaches the model and never becomes a steering item in
		// the transcript (issue #710). Nothing pending, nothing held.
		//
		// Assigned rather than set, so the hold is a total function of what is
		// actually parked and cannot drift from it. No reachable path accepts
		// a Stop with the hold already armed (a held session reads idle, and
		// an idle session's Stop is refused), so this never clears one today;
		// restore is where a hold persisted by the old unconditional write is
		// released.
		snapshot.SteeringHeld = snapshotHasPendingUserSteering(snapshot)
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
	// Sampled before the update, never inside it: the in-flight set is under
	// s.mu, which the store serializer must not wait on. cancelAndWait has
	// returned, so no turn of this session is appending.
	recordedIDs, recorded := s.recordedSteeringAwaitingMark()
	// Sampled here, outside the serializer below: the transcript scan takes
	// Session.mu, and the store serializer must never wait on it.
	claimedRecorded := s.claimedQueuedRecordedInTranscript()
	returnedClaim := false
	if err := s.clientMutations.update(lookup.Lease, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		returned, finalizeErr := finalizeClientMutationInterrupt(snapshot, s.ID(), recorded, claimedRecorded)
		if finalizeErr != nil {
			return finalizeErr
		}
		returnedClaim = returned
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
	s.steeringMarked(recordedIDs)
	if returnedClaim {
		// The durable queue grew a claimed-but-unrun message back. QueueDepth,
		// QueuePreview and WireState read the process-local copy, so without this
		// the message is durably present and invisible. No wake: this Stop parks
		// the queue, so the returned message waits for the user's next run.
		s.reflectDurableInputQueue()
	}
	// No queued-input wake here, deliberately: the hold above parks the queue
	// until the user asks for something to run, so a kick would find nothing
	// claimable. The steering wake stays; a Stop with pending steering still
	// restarts, and that is kata 1k3m, not this one.
	s.wakeForPendingSteering()
	// A start may be waiting on the fence this Stop just cleared. The claim
	// path refuses every start while an interrupt fence exists, so a follow-up
	// admitted behind the recovered turn waits for finalization -- and nothing
	// else wakes the start path: the queue is parked above, and the steering
	// wake above serves only steering. Without this the follow-up is accepted,
	// durably present, and never runs.
	s.wakeRunnableClientMutationStartAfterFence()
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

// finalizeClientMutationInterrupt settles the fence a Stop left: every user
// turn naming the cancelled turn is retired as interrupted, and pending client
// steering goes through reconcileClientSteering with stopping set: a steer the
// transcript holds (recorded, the caller's sample of the in-flight set) is
// finalized, and every other is parked, the way a queued message a Stop
// returns is (wms7) -- a steer the cancelled turn never recorded, whether a
// carrier claim it landed on before the carrier drained it or an append that
// failed under it, is still accepted in the store and waits for the user's
// next run. Steering entries are never retired here.
// It reports whether it put a claimed queue entry back on the queue. The caller
// owns the reflection that makes that message visible, because only the caller
// knows whether the queue may be woken: a Stop parks the queue, so the interrupt
// path reflects the returned message without waking it.
//
// It is a pure function of the snapshot: everything it needs from the session is
// sampled by the caller BEFORE the store serializer, and handed in. The
// serializer never waits on Session.mu, and the transcript scan that would take
// it is exactly such a wait.
func finalizeClientMutationInterrupt(
	snapshot *clientMutationSnapshot,
	threadID string,
	recorded func(string) string,
	claimedRecorded map[string]bool,
) (returned bool, err error) {
	fence := snapshot.InterruptFence
	if fence == nil {
		return false, nil
	}
	record, ok := snapshot.Journal[fence.ClientMutationID]
	if !ok {
		return false, fmt.Errorf("interrupt fence %q has no journal record", fence.ClientMutationID)
	}
	reconcileClientSteering(snapshot, recorded, true)
	for id, pending := range snapshot.PendingExecutions {
		if pending.TurnID != fence.ExpectedTurnID {
			continue
		}
		if slices.Contains(snapshot.SteeringOrder, id) {
			continue
		}
		target, ok := snapshot.Journal[id]
		if !ok {
			return false, fmt.Errorf("interrupt target %q has no journal record", id)
		}
		// A queue entry claimed and never incorporated did not run: the Stop
		// cancelled a turn that recorded nothing. Retiring it as interrupted
		// would drop a durably accepted message (and its turn id would pin
		// ActiveTurnID), so it goes back to the queue the way
		// completeClientMutationTurnWithState returns a claimed queue turn a
		// cancellation ended. The queue is held by this Stop, so the returned
		// message waits rather than auto-running.
		//
		// "claimed" alone does not mean "never recorded": the user-input turn is
		// written to the transcript before its incorporation mark, so a mark that
		// failed leaves a claimed execution whose turn is already on disk.
		// Requeueing that one would append and run it twice. The transcript
		// itself decides -- not the store's mark, which is exactly what failed --
		// and a recorded claim falls through to the interrupted retirement below.
		// The fact is sampled by the caller, outside this serializer.
		if pending.Method == clientMutationMethodQueue && pending.ExecutionState == "claimed" &&
			!claimedRecorded[id] {
			if returnErr := returnClaimedQueuedMutation(snapshot, id, pending, &target); returnErr != nil {
				return false, returnErr
			}
			target.ExecutionState = "accepted"
			snapshot.Journal[id] = target
			snapshot.QueueRevision++
			returned = true
			continue
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
		return returned, err
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
	return returned, nil
}

func (s *Session) recoverClientMutationInterrupt() error {
	if s == nil || s.clientMutations == nil {
		return nil
	}
	if s.clientMutations.snapshot().InterruptFence == nil {
		return nil
	}
	recordedIDs, recorded := s.recordedSteeringAwaitingMark()
	claimedRecorded := s.claimedQueuedRecordedInTranscript()
	returnedClaim := false
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		returned, finalizeErr := finalizeClientMutationInterrupt(snapshot, s.ID(), recorded, claimedRecorded)
		returnedClaim = returned
		return finalizeErr
	})
	if err == nil {
		s.steeringMarked(recordedIDs)
		if returnedClaim {
			// Recovery is not a Stop, so a message it returns may run: reflect
			// it and wake the runner rather than leave it durably present and
			// invisible.
			s.reflectDurableInputQueue()
			s.wakeForPendingQueuedInput()
		}
		// The fence is cleared now, so a start that was waiting on it can run.
		s.wakeRunnableClientMutationStartAfterFence()
	}
	return err
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

// returnUnrunStartClaim gives back a claim ProcessClientMutationStart took for a
// turn it will not run, by the mutation's own method: a start's claim goes back
// through the start path, and a queued claim -- which claimClientMutationStart
// also serves, from the head of the input queue -- goes back to the queue
// through the queue path. An incorporated entry is left alone: its transcript
// entry already landed, so it is neither spent nor stranded and restart recovery
// owns it. Leaving any other claim in place spends its budget slot and pins the
// active turn until the next restart, out of the queue the turn gate would have
// returned it to.
func (s *Session) returnUnrunStartClaim(claimed queuedInput) error {
	if claimed.ClientMutationID == "" ||
		s.clientMutationUserTranscriptIncorporated(claimed.ClientMutationID, claimed.StableTurnID) {
		return nil
	}
	if pending := s.clientMutations.snapshot().PendingExecutions[claimed.ClientMutationID]; pending.Method == clientMutationMethodQueue {
		return s.completeClientMutationTurn(claimed.ClientMutationID)
	}
	return s.returnClaimedClientMutationStart(claimed.ClientMutationID)
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

// SetPendingUserInputWakeFunc installs the daemon's queued-input wake. Setting it
// after restore immediately wakes a queue the journal already owns, which is
// how a message that survived a crash gets run without waiting for the user to
// do something unrelated.
func (s *Session) SetPendingUserInputWakeFunc(wake func()) {
	s.mu.Lock()
	s.pendingUserInputWake = wake
	s.mu.Unlock()
	// A steer parked by a Stop (SteeringHeld) is not work to resume: waking for
	// it at attach would restart the session and deliver the steer the user
	// just stopped (issue #174, #146 Option C — park in place).
	if wake != nil && (s.QueueDepth() > 0 || s.hasRunnableUserSteering()) {
		// Attach is an external wake: it unparks steering a failed attempt
		// left (Session.steeringParked).
		s.mu.Lock()
		s.steeringParked = false
		s.mu.Unlock()
		wake()
	}
}

func (s *Session) wakePendingUserInput() {
	s.mu.Lock()
	wake := s.pendingUserInputWake
	// Every sender of this wake is an event outside the failed attempt that
	// parked steering -- an accepted client mutation, a store write that
	// landed -- so the wake it sends is the one the parked steer waits for.
	s.steeringParked = false
	s.mu.Unlock()
	if wake != nil {
		wake()
		return
	}
	// No daemon is driving this session (a bare ProcessInput caller, or a
	// subagent). notify is the only wake such a session has, and it is what the
	// queue relied on before this path existed.
	s.notify()
}

func (s *Session) wakeClientMutationStart() {
	s.mu.Lock()
	wake := s.clientMutationStartWake
	s.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// wakeRunnableClientMutationStartAfterFence wakes the start path when an
// interrupt fence that has just been cleared leaves a start claimable.
//
// The claim path refuses every start while a fence exists, so a follow-up
// admitted behind the recovered turn waits for the Stop to finalize. Nothing
// else wakes the start path -- the Stop parks the input queue, and
// wakeForPendingSteering serves only steering -- so without this the accepted
// follow-up is durably present and never runs, the stranding the fence guard
// trades for.
func (s *Session) wakeRunnableClientMutationStartAfterFence() {
	if s.hasRunnableClientMutationStart() {
		s.wakeClientMutationStart()
	}
}

func (s *Session) hasRunnableClientMutationStart() bool {
	_, runnable := s.runnableClientMutationStartTurnID()
	return runnable
}

// runnableClientMutationStartTurnID reports the next turn the start path can run
// and whether it is runnable right now. Its start branch mirrors
// claimClientMutationStart's fence guard exactly: NO start is reported runnable
// while an interrupt fence exists, because none can be claimed then. Reporting
// one would make ProcessClientMutationStart arm cancellation for a claim that
// immediately refuses -- a spurious arm/clear on every wake -- and would tell
// sessionWorkPending, and so WireState, that the session has work to resume
// while the fence blocks its only pending start. The queue branches are not
// fenced: they name a turn the fence did not stop. The post-fence wake
// (wakeRunnableClientMutationStartAfterFence) delivers the held start once the
// fence clears.
//
// The start branch also names exactly the turn the claim path will take: the
// head of the same ordered selection (claimableClientMutationStartIDs), not
// whichever start the map happens to yield first. The two must agree, because
// the id named here is what cancellation is armed for and what the claim then
// takes.
func (s *Session) runnableClientMutationStartTurnID() (string, bool) {
	if s == nil || s.clientMutations == nil {
		return "", false
	}
	snapshot := s.clientMutations.snapshot()
	// Resolve the ordered head once, and only when no fence blocks the claim.
	// startClaimable tracks claimability separately from the head's turn id so a
	// start with an empty turn id still returns ("", false) exactly as before.
	startTurnID := ""
	startClaimable := false
	if snapshot.InterruptFence == nil {
		if startIDs := s.claimableClientMutationStartIDs(&snapshot); len(startIDs) > 0 {
			startTurnID = snapshot.PendingExecutions[startIDs[0]].TurnID
			startClaimable = true
		}
	}
	// The start branch is settled BEFORE the queue branches, because the claim
	// path claims the ordered start head whenever a start is claimable, fence
	// aside. Answering from the map's order instead could name a claimable queue
	// entry while the claim takes a start, arming cancellation for the wrong turn.
	if startClaimable {
		return startTurnID, startTurnID != ""
	}
	for _, pending := range snapshot.PendingExecutions {
		if pending.Method == clientMutationMethodQueue &&
			pending.ExecutionState == "incorporated" &&
			pending.TurnID != "" &&
			snapshot.ActiveTurnID == pending.TurnID {
			return pending.TurnID, true
		}
	}
	if len(snapshot.InputQueue) > 0 {
		record := snapshot.Journal[snapshot.InputQueue[0].ClientMutationID]
		runnable := record.Method == clientMutationMethodQueue &&
			record.StableTurnID != "" &&
			snapshot.ActiveTurnID == record.StableTurnID
		return record.StableTurnID, runnable
	}
	return "", false
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

// raiseTurnSequence makes the store's next reserved turn id exceed
// turn_m<floor>: a transcript can name turns this store never reserved (a
// fork's copied prefix). The raise is in memory; the next mutation persists
// it, and a restart that finds none recomputes the floor from the transcript.
func (s *clientMutationStore) raiseTurnSequence(floor uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.state.NextTurnSequence = max(s.state.NextTurnSequence, floor)
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

// committedHumanNote reads the committed canonical human note under stateMu
// without cloning the journal. The notes cut carries it for lock-free readers;
// the metadata write reads it here so the persisted projection always names the
// store's authority rather than a caller-remembered copy. Absence and a saved
// clear both read as "" — no caller of this accessor distinguishes them.
func (s *clientMutationStore) committedHumanNote() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.state.HumanNote == nil {
		return ""
	}
	return *s.state.HumanNote
}

// steeringOrigins reports the persisted steering provenance of every journal
// record that keeps one, keyed by client mutation id. A steering turn's own
// provenance is its kind, but a turn written before kinds were stamped kept
// none, and only the record of the mutation that wrote it can still say whether
// it was a notes update or an ordinary steer. The reader takes only stateMu and
// does not clone the journal: history escaping runs once per restore over every
// restored steering turn, and the journal can outgrow the rest of the snapshot.
// A record that kept neither field cannot decide anything and is left out, so
// the caller falls back to the turn's own kind and then the write-path text
// shape. Only the record stored under the client mutation id decides, and the
// id's spelling is not evidence of anything.
func (s *clientMutationStore) steeringOrigins() map[string]steeringOrigin {
	if s == nil {
		return nil
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	var origins map[string]steeringOrigin
	for id := range s.state.Journal {
		origin := steeringOriginFromJournal(s.state.Journal, id)
		if origin.kind == "" && origin.method == "" {
			continue
		}
		if origins == nil {
			origins = make(map[string]steeringOrigin, len(s.state.Journal))
		}
		origins[id] = origin
	}
	return origins
}

// queueHeld reads the parked-queue flag without cloning the snapshot.
// sessionWorkPending calls this on every WireState sample, and snapshot()
// deep-copies the whole journal.
func (s *clientMutationStore) queueHeld() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state.QueueHeld
}

// steeringHeld reads the parked-steering flag without cloning the snapshot,
// mirroring queueHeld. wakeForPendingSteering and the busy-state predicates
// consult it so a Stop with pending user steering does not restart the session
// or read as work in progress (issue #174).
func (s *clientMutationStore) steeringHeld() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state.SteeringHeld
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
	if src.HumanNote != nil {
		note := *src.HumanNote
		dst.HumanNote = &note
	}
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
	dst.PendingExecutions = make(clientMutationPendingExecutions, len(src.PendingExecutions))
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
