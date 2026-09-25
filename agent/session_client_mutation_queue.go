package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
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
	// Steering: the transcript holds a steering turn for the mutation.
	Steering bool
}

// ClientMutationProjection returns the reconstructible retry-safe queue and
// pending-input view from one durable mutation-store generation.
func (s *Session) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	if s == nil || s.clientMutations == nil {
		return appwire.QueueState{}, nil
	}
	snapshot := s.clientMutations.snapshot()
	queue := appwire.QueueState{
		Depth:    len(snapshot.InputQueue),
		Revision: snapshot.QueueRevision,
	}
	if len(snapshot.InputQueue) > 0 {
		queue.Preview = make([]string, len(snapshot.InputQueue))
		queue.IDs = make([]string, len(snapshot.InputQueue))
		queue.ClientMutationIDs = make([]string, len(snapshot.InputQueue))
		queue.Texts = make([]string, len(snapshot.InputQueue))
		queue.SkillNames = make([][]string, len(snapshot.InputQueue))
	}
	pendingByID := make(map[string]appwire.PendingMutation, len(snapshot.PendingExecutions)+len(snapshot.InputQueue))
	for id, pending := range snapshot.PendingExecutions {
		if pending.ExecutionState == "incorporated" &&
			(pending.Method == clientMutationMethodStart || pending.Method == clientMutationMethodQueue) {
			continue
		}
		pending.Input = cloneClientMutationInput(pending.Input)
		pending.QueueEntryIDs = append([]string(nil), pending.QueueEntryIDs...)
		pendingByID[id] = pending
	}
	for i, entry := range snapshot.InputQueue {
		queued := queuedInputFromClientMutation(entry)
		queue.Preview[i] = queuedEntryPreviewLine(queued)
		queue.IDs[i] = entry.ID
		queue.ClientMutationIDs[i] = entry.ClientMutationID
		queue.Texts[i] = queued.Text
		queue.SkillNames[i] = slices.Clone(queued.SkillNames)
		if _, exists := pendingByID[entry.ClientMutationID]; exists {
			continue
		}
		record := snapshot.Journal[entry.ClientMutationID]
		pendingByID[entry.ClientMutationID] = appwire.PendingMutation{
			ClientMutationID: entry.ClientMutationID,
			Method:           record.Method,
			Input:            cloneClientMutationInput(entry.Input),
			ExecutionState:   record.ExecutionState,
			QueueEntryIDs:    []string{entry.ID},
			ProjectionState:  record.ProjectionState,
		}
	}
	pending := make([]appwire.PendingMutation, 0, len(pendingByID))
	for _, mutation := range pendingByID {
		pending = append(pending, mutation)
	}
	slices.SortFunc(pending, func(a, b appwire.PendingMutation) int {
		return strings.Compare(a.ClientMutationID, b.ClientMutationID)
	})
	return queue, pending
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
	store.raiseTurnSequence(s.turnSequenceFloor)
	s.clientMutations = store
	return nil
}

func (s *Session) clientMutationQueue(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	defer release()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Input = input.Items
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodQueue, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}

	var response appwire.TurnQueueResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
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
		response = appwire.TurnQueueResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method))}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		// Queueing another message is re-engaging: release the wait.
		snapshot.QueueHeld = false
		// Re-engaging releases the parked steer too (issue #174).
		snapshot.SteeringHeld = false
		snapshot.InputQueue = append(snapshot.InputQueue, clientMutationQueueEntry{
			ID:               record.StableQueueEntryIDs[0],
			ClientMutationID: record.ClientMutationID,
			Input:            cloneClientMutationInput(params.Input),
		})
		snapshot.QueueRevision++
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
		return nil
	})
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	if lookup.Record.OperationState == clientMutationOperationRejected {
		return appwire.TurnQueueResponse{}, clientMutationRejectionError(lookup.Record)
	}
	if lookup.Disposition == clientMutationDispositionReplayed {
		// A retry of a queue that was accepted but never run -- the process died
		// between the commit and the wake -- must still provoke a run. Replay is
		// idempotent in the store; the wake is what makes it idempotent in
		// effect.
		s.wakeForPendingQueuedInput()
		return queueResponseFromRecord(s.ID(), lookup.Record, appwire.MutationDispositionReplayed)
	}
	s.reflectDurableInputQueue()
	s.wakeForPendingQueuedInput()
	return response, nil
}

// hasPendingUserInputToRun reports whether ProcessPendingUserInput could find
// anything to run, without claiming it or taking a retirement lease. It asks
// the claim path's own questions -- a claimable queue head, or user steering
// that is neither parked nor held and has a carrier to claim -- on the
// committed store generation. It may over-report (the claims under the lease
// re-decide on the generation they commit against) but never under-reports: a
// store not yet opened may hold a durable queue, so it counts as work.
func (s *Session) hasPendingUserInputToRun() bool {
	s.clientMutationsInitMu.Lock()
	store := s.clientMutations
	s.clientMutationsInitMu.Unlock()
	if store == nil {
		return true
	}
	snapshot := store.snapshot()
	if queueHeadClaimable(&snapshot) {
		return true
	}
	if !s.hasPendingUserSteering() || s.steeringParkedNow() || !steeringCarrierRailOpen(&snapshot) {
		return false
	}
	_, turnID := claimableSteeringCarrier(&snapshot, s.steeringInFlightSample())
	return turnID != ""
}

// ProcessPendingUserInput runs input the user has already given but the session
// has not delivered -- a queued message, or steering with no turn to land in --
// and reports whether it ran a turn.
//
// It is the counterpart to ProcessClientMutationStart, and exists for the same
// reason: the entry gate refuses every non-user kind while a question is
// pending, so this work delivered as a notification never reaches the drain
// loop that would run it. The gate is right to refuse autonomous wakes, and
// this is not one -- it is the user speaking.
//
// A queued message enters as EntryUserInput, which is also what makes it
// supersede an unanswered question: someone who types instead of answering has
// moved past it. The drain loop already agrees -- selectDrainNextAction runs
// queued input ahead of everything else while awaiting.
//
// A queued message runs as its own turn. Pending steering has no turn of its
// own to run: it runs as a synthetic queued entry with no content
// (claimSteeringCarrierInput), so it passes the same pending-question gate a
// queued message does (steering is the user speaking too), and its acceptance
// drains the steering the way a queued message's would -- without a user turn
// of its own, a MaxTurns slot, or a transcript append.
//
// onRunnable, when non-nil, is called with the turn id at the moment a claim
// becomes real, so the daemon can publish the running turn and wire
// cancellation to it before the turn starts producing. For steering that id is
// the pending steer's own reserved id, not a freshly minted one, so the id the
// client was told in its Applied receipt is the id that actually runs.
func (s *Session) ProcessPendingUserInput(ctx context.Context, onRunnable func(string)) (string, bool, error) {
	// Wakes are unconditional and coalesce, so one can arrive after the work
	// it was sent for has already run. A wake that finds nothing is not
	// admitted work and must not take a retirement lease: beginning one
	// restarts the idle interval, which would move the deadline from the
	// settlement to whenever the wake happened to run. Returning without a
	// lease is safe because input queued after this check sends its own wake,
	// and until it is claimed queued input and pending steering are
	// themselves retirement evidence (retirementInputBlockers). Input that is
	// present here is claimed under the lease below, which re-decides it.
	if !s.hasPendingUserInputToRun() {
		return "", false, nil
	}
	release, admissionErr := s.beginRetirementMutation("turn")
	if admissionErr != nil {
		return "", false, admissionErr
	}
	defer release()
	if err := s.ensureClientMutationStore(); err != nil {
		return "", false, err
	}
	// Each claim below decides the poisoned-transcript refusal on the same
	// generation it claims on, so nothing here has to read a claimability
	// snapshot the claim might then contradict. A wake that claims nothing --
	// an idle poll, or work a Stop parked in the meantime -- stands down
	// quietly, because answering it with an error would turn every poll into a
	// logged failure for the rest of the session's life; a wake that would
	// claim a poisoned transcript refuses before announcing a turn it cannot
	// record.
	queued, refusal := s.popQueueHeadRefusingPoison()
	if refusal != nil {
		return "", false, refusal
	}
	if !inputHasContent(queued.Text, queued.Images, queued.SkillNames) && s.steeringParkedNow() {
		// A wake the daemon buffered before the last attempt failed -- the
		// steer's own acceptance wake, held while the input that then ran the
		// carrier inline was still running. That attempt spent it; the next
		// wake sender unparks (Session.steeringParked).
		return "", false, nil
	}
	if !inputHasContent(queued.Text, queued.Images, queued.SkillNames) && s.hasPendingUserSteering() {
		carrier, carrierRefusal := s.claimSteeringCarrierInput()
		if carrierRefusal != nil {
			return "", false, carrierRefusal
		}
		if !carrier.SteeringCarrier {
			// Another mutation already owns the active-turn slot, or an
			// interrupt fence is ending one: this wake cannot name itself.
			// Stand down rather than run unaddressable -- the steering stays
			// queued for whichever turn runs next, the same stand-down
			// contract mintRunningTurnID's callers rely on.
			return "", false, nil
		}
		queued = carrier
		// The claim is handed back by the run itself, on every exit: the
		// drain loop after each turn, and the entry gate when it refuses a
		// closed session before the loop (releaseSteeringCarrierClaim). One
		// owner, so nothing here releases it a second time.
	}
	if inputHasContent(queued.Text, queued.Images, queued.SkillNames) || queued.SteeringCarrier {
		// The claims above decided their refusals on the generations they
		// committed against, so a poisoning can still land between a decision and
		// its commit. The announcement is made under the transcript's write door
		// rather than after a check outside it: a poisoning append records the
		// poison while holding that door, so the announce is ordered against every
		// poisoning. Either it is already visible -- nothing is announced, and the
		// claim goes back -- or it lands after the announce, where it is the
		// ordinary refusal the run below meets.
		announceRefusal := s.attachedTranscript().WhileHealthy(func() {
			if onRunnable != nil && queued.StableTurnID != "" {
				onRunnable(queued.StableTurnID)
			}
		})
		if announceRefusal != nil {
			if queued.SteeringCarrier {
				s.releaseSteeringCarrierClaim(queuedClientMutationIdentityOf(queued))
			} else if queued.ClientMutationID != "" {
				if restoreErr := s.completeClientMutationTurn(queued.ClientMutationID); restoreErr != nil {
					return "", false, errors.Join(errWhileHealthyRefuses(announceRefusal), fmt.Errorf("return claimed input: %w", restoreErr))
				}
			}
			return "", false, errWhileHealthyRefuses(announceRefusal)
		}
		ctx = withQueuedClientMutation(ctx, queued)
		// Prepare the claimed input's skill selection at actual consumption:
		// one atomic group tied to this input's durable identity,
		// all-or-nothing before any dependent work dispatches.
		ctx = s.contextWithSelectedSkills(ctx, queued)
		result, err := s.ProcessInputKind(ctx, queued.Text, queued.Images, EntryUserInput)
		// The pop above is durable and the turn loop's gate can refuse after it,
		// when poisoning lands in between. Put the message back rather than
		// leave it in no queue and no transcript; turn completion is the queue's
		// own restore, the same one the environment-append failure uses.
		//
		// Keyed on the transcript's own refusals -- poisoned or closed -- and
		// nothing wider, deliberately. Every other pre-incorporation failure
		// either unwinds where it happened or is reclaimed by startup recovery,
		// and a wider key would re-queue a turn already recorded in the
		// transcript — an incorporation marking that fails after the entry is
		// durable would deliver the message twice. A close can land after the
		// claim and announce, so it owes the same give-back the poisoned case
		// gets; left claimed, its active turn stays pinned until a restart.
		if transcriptRefusedClaim(err) &&
			!s.clientMutationUserTranscriptIncorporated(queued.ClientMutationID, queued.StableTurnID) {
			if restoreErr := s.completeClientMutationTurn(queued.ClientMutationID); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("return queued input: %w", restoreErr))
			}
		}
		return result, true, err
	}
	return "", false, nil
}

// claimSteeringCarrierInput reserves the durable turn identity for a turn whose
// only job is to carry already-accepted user steering, and returns the entry
// that turn runs as: queued input with no content, whose identity is the head
// steer's. It reuses the id that steer already reserved at its own acceptance
// -- reserveClientMutationTurnID, called from clientMutationSteer/Drain/Promote
// -- rather than minting a fresh one, so the id returned in that mutation's
// Applied receipt is the id that actually runs.
//
// It returns a zero carrier when nothing is claimable: an interrupt fence is
// ending a turn, another mutation already holds the active-turn slot, or (a
// benign race with whatever cleared hasPendingUserSteering's answer between the
// caller's check and this call) no user steering is left pending. The caller
// treats every such case the same way -- stand down -- because the steering
// that prompted the wake, if still queued, stays queued for whichever turn runs
// next; nothing is lost by waiting.
//
// It returns a refusal instead when it would have claimed on a poisoned
// transcript. That refusal is decided on the same generation the claim acts on
// (inside the mutate below), so a poisoning or a Stop that lands after the
// caller decided the steer was runnable cannot hand the steer to a turn the
// transcript cannot record.
func (s *Session) claimSteeringCarrierInput() (queuedInput, error) {
	release, admissionErr := s.beginRetirementMutation("input")
	if admissionErr != nil {
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("steering carrier admission failed: %v", admissionErr)})
		return queuedInput{}, nil
	}
	defer release()
	if err := s.ensureClientMutationStore(); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("open client mutation store: %v", err)})
		return queuedInput{}, nil
	}
	// Eligibility is decided on one sample: the store as the serializer holds
	// it, plus the in-flight set taken under s.mu just before -- the serializer
	// never waits on s.mu, and the set changes only on the input goroutine
	// that is calling here.
	inFlight := s.steeringInFlightSample()
	if s.cfg.testOnly.steeringCarrierClaiming != nil {
		s.cfg.testOnly.steeringCarrierClaiming()
	}
	// The writer is sampled under s.mu here, before the serializer takes
	// clientMutations.mu; the claim reads only the writer's own lock inside, so
	// the serializer never waits on s.mu.
	writer := s.attachedTranscript()
	var carrier queuedInput
	err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if !steeringCarrierRailOpen(snapshot) {
			return nil
		}
		if id, turnID := claimableSteeringCarrier(snapshot, inFlight); turnID != "" {
			// The refusal is part of the claim decision, on the generation this
			// claim commits against. Returning it from the mutation is what
			// keeps the refusal from committing anything -- a nil return would
			// save the generation the claim then declined to change.
			if refusal := s.refuseOnUnhealthyTranscript(writer); refusal != nil {
				return refusal
			}
			snapshot.ActiveTurnID = turnID
			carrier = queuedInput{ClientMutationID: id, StableTurnID: turnID, SteeringCarrier: true}
		}
		return nil
	})
	// A refusal is the transcript's, not the store's: distinguish it from a
	// claim write that failed, which parks the steer with a warning instead.
	if transcriptRefusedClaim(err) {
		return queuedInput{}, err
	}
	if err != nil {
		// A refused write parks the steer the way a failed append does: it
		// stays accepted and queued, runnable (hasRunnableUserSteering keeps
		// the session from resting), and nothing here arms a wake of its own
		// -- the next external wake, attach or any accepted client mutation,
		// claims it again. No paced retry. A closed rail or an occupied slot
		// returns nil above.
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("claim steering carrier turn failed: %v; the steering stays queued", err)})
		s.parkSteering()
		return queuedInput{}, nil
	}
	return carrier, nil
}

// steeringCarrierRailOpen reports whether the steering rail is open to a claim
// at all, before asking whether any steer is ready to use it.
//
// A Stop parks pending user steering until the user asks for something to run,
// the same way QueueHeld parks the input queue. Claiming the steering carrier
// while it is parked would hand the steer to a turn the Stop just ended, so it
// is refused -- mirroring popQueueHead's QueueHeld gate (issue #174). The steer
// stays in PendingExecutions/SteeringOrder; nothing moves, so its causal
// provenance is never at risk (issue #146, Option C).
func steeringCarrierRailOpen(snapshot *clientMutationSnapshot) bool {
	return snapshot.InterruptFence == nil && snapshot.ActiveTurnID == "" && !snapshot.SteeringHeld
}

// claimableSteeringCarrier names the first eligible pending steer and the
// reserved turn it already owns, or "" when no steer is ready to carry one. The
// claim above walks the order once through this; nothing else decides carrier
// eligibility, so a caller cannot ask a different question of the same order.
//
// inFlight is the session's steeringInFlight sample: a steer a turn popped and
// is appending, or one whose append landed and whose incorporation write the
// store refused, is accepted in the store and absent from the queue. A carrier
// claimed for it would drain nothing of its own, so it is not eligible.
func claimableSteeringCarrier(snapshot *clientMutationSnapshot, inFlight map[string]string) (clientMutationID, turnID string) {
	for _, id := range snapshot.SteeringOrder {
		pending, exists := snapshot.PendingExecutions[id]
		if !exists || pending.ExecutionState != "accepted" || pending.TurnID == "" {
			continue
		}
		if _, ok := inFlight[id]; ok {
			continue
		}
		return id, pending.TurnID
	}
	return "", ""
}

// carrierSteerOutcome is the carrier's own question after it drained: what
// became of the steer it was claimed for?
type carrierSteerOutcome int

const (
	// carrierSteerDelivered: the transcript holds the steering turn -- the
	// store marked it, or the mark is the only thing missing -- and the model
	// reads it on this turn's request.
	carrierSteerDelivered carrierSteerOutcome = iota
	// carrierSteerUndelivered: the append failed and the steer is back in the
	// queue, accepted.
	carrierSteerUndelivered
	// carrierSteerFailed: the steer's skill selection could not be prepared;
	// the failure is recorded and announced, and there is nothing to carry.
	carrierSteerFailed
)

func (s *Session) carrierSteerOutcome(identity queuedClientMutationIdentity) carrierSteerOutcome {
	if identity.ClientMutationID == "" || s.clientMutations == nil {
		return carrierSteerDelivered
	}
	snapshot := s.clientMutations.snapshot()
	s.mu.Lock()
	recorded, inFlight := s.steeringInFlight[identity.ClientMutationID]
	s.mu.Unlock()
	if recorded == "failed" || snapshot.Journal[identity.ClientMutationID].ExecutionState == "failed" {
		return carrierSteerFailed
	}
	if _, pending := snapshot.PendingExecutions[identity.ClientMutationID]; pending && (!inFlight || recorded == "") {
		return carrierSteerUndelivered
	}
	return carrierSteerDelivered
}

// AcceptClientMutationQueue durably accepts or replays one client-authored
// queued input.
func (s *Session) AcceptClientMutationQueue(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	return s.clientMutationQueue(params)
}

func rejectClientMutation(record *clientMutationRecord, err error) {
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
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

// returnClaimedDirectClientMutationTurn gives back the one turn a direct input's
// claim took. A claim is a unit and a release returns that unit, whoever else
// claimed in between: measuring against the returning caller's own floor instead
// makes the outcome depend on the order two failed inputs unwind in, and the
// order that loses leaves a turn nobody is using counted against MaxTurns for
// the rest of the session. The caller's failure path runs once per claim
// (session_lifecycle.go's environment-append failure branch), and the zero guard
// is the same one returnClaimedClientMutationStart keeps for the queued claims.
func (s *Session) returnClaimedDirectClientMutationTurn() error {
	return s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.AcceptedTurns > 0 {
			snapshot.AcceptedTurns--
		}
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

// reflectDurableInputQueue publishes the durable queue's current state as a
// QUEUE_CHANGED event. consumedClientMutationIDs names entries a caller's own
// transition just consumed (a drain folding the queue into steering) so the
// push carries that one-shot fact alongside the durable snapshot; every other
// caller passes none, and the field is absent on the wire (issue #1704).
func (s *Session) reflectDurableInputQueue(consumedClientMutationIDs ...string) {
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
	data.ClientMutationIDs = clientMutationQueueClientMutationIDs(snapshot.InputQueue)
	s.mu.Unlock()
	data.ConsumedClientMutationIDs = consumedClientMutationIDs
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
		case "skill":
			// Canonical identities only: bodies load from the recorded sources
			// at actual consumption, never from the input wire.
			queued.SkillNames = append(queued.SkillNames, item.Name)
		}
	}
	return queued
}

func clientMutationInput(text string, images []ImageAttachment, skillNames []string) []appwire.InputItem {
	input := make([]appwire.InputItem, 0, 1+len(images)+len(skillNames))
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
	for _, name := range skillNames {
		input = append(input, appwire.InputItem{Type: "skill", Name: name})
	}
	return input
}

func (s *Session) clientMutationSteer(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	defer release()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Input = input.Items
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodSteer, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	var response appwire.TurnSteerResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
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
		response = appwire.TurnSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method))}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		addPendingSteering(snapshot, record, params.Input)
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
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
		// A retry of a steer that was accepted but never delivered -- the
		// process died between the commit and the wake -- must still provoke
		// delivery. Replay is idempotent in the store; the wake is what makes
		// it idempotent in effect.
		s.wakeForPendingSteering()
		return replayed, nil
	}
	s.reflectDurableClientSteering()
	s.wakeForPendingSteering()
	return response, nil
}

// wakeForPendingSteering asks the session to run a turn when steering is
// waiting and nothing is going to pick it up on its own.
//
// Accepting a steer is only half of landing it: steering sitting in a queue
// nobody drains is lost more quietly than the rejection it replaced. The kick
// is unconditional rather than gated on "is a turn running", because that gate
// loses a race it cannot win -- a turn can pass its final steering drain and
// still own the turn id, so a steer arriving in that window would be skipped
// here and then never looked at again.
//
// An unneeded kick is cheap and safe: the wake finds nothing to deliver and
// no-ops, repeated kicks coalesce into one parked send, and a wake that cannot
// name its turn stands down rather than running unaddressable.
func (s *Session) wakeForPendingSteering() {
	// A Stop parks pending user steering behind the SteeringHeld gate (issue
	// #174). Waking here would restart the session and deliver the steer to the
	// model anyway -- exactly the open steering rail the gate exists to close.
	// The steer stays parked in PendingExecutions/SteeringOrder until a
	// user-initiated run trigger clears the gate, so its causal provenance is
	// never at risk (issue #146, Option C — park in place).
	//
	// Only the user's own steering provokes a turn. Daemon-authored steering --
	// the current-task reminder, hook context, a transcript pointer -- is
	// context for whatever turn runs next, and the round loop drains it into
	// that turn. Waking for it would start a turn before the user has said
	// anything, which is both a turn nobody asked for and a turn that can hold
	// the session's turn identity when the opening turn/start arrives.
	s.reconcileRecordedSteering()
	if !s.hasRunnableUserSteering() {
		return
	}
	s.wakePendingUserInput()
}

// wakeForPendingQueuedInput is wakeForPendingSteering's counterpart for
// turn/queue. Control is session-scoped, so a queue is accepted with no turn
// running -- and an idle session is parked awaiting input, so nothing runs what
// was just accepted unless it is asked to.
//
// It is unconditional on turn state for the same reason steering's is: a turn
// can pass its final queue check and still own the turn id, so gating on "is a
// turn running" would skip exactly the arrival that needs the kick. An unneeded
// wake is cheap -- it finds nothing runnable and no-ops, and repeated wakes
// coalesce into one parked send.
func (s *Session) wakeForPendingQueuedInput() {
	if s.QueueDepth() == 0 {
		return
	}
	s.wakePendingUserInput()
}

// AcceptClientMutationSteer durably accepts or replays one client-authored
// steering input.
func (s *Session) AcceptClientMutationSteer(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	return s.clientMutationSteer(params)
}

func (s *Session) clientMutationDrain(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	defer release()
	input, err := appwire.NormalizeMutationInput(params.Input)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	params.Input = input.Items
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodDrain, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	request.Preconditions.ExpectedQueueRevision = &params.ExpectedQueueRevision
	var response appwire.TurnDrainAsSteerResponse
	var consumedClientMutationIDs []string
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
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
		// Draining IS the user asking for the queue to run. Below the effect
		// rejection so a refused drain cannot unpark it.
		snapshot.QueueHeld = false
		// Draining is a user-initiated run, so the parked steer is released too
		// (issue #174).
		snapshot.SteeringHeld = false
		for _, entry := range entries {
			removeQueuedMutationSource(snapshot, entry, "transformed")
		}
		consumedClientMutationIDs = clientMutationQueueClientMutationIDs(entries)
		steeringInput := combineClientMutationInputs(entries, params.Input)
		snapshot.InputQueue = remaining
		snapshot.QueueRevision++
		response = appwire.TurnDrainAsSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method))}
		// Durable, so a replay reports it too: replayClientMutationResult
		// unmarshals this same JSON, and the replay branch below never calls
		// reflectDurableInputQueue to re-announce it any other way.
		response.Receipt.ConsumedClientMutationIDs = consumedClientMutationIDs
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		addPendingSteering(snapshot, record, steeringInput)
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
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
		// A retry of steering that was accepted but never delivered must still
		// provoke delivery; replay is idempotent in the store and useless to the
		// user without the wake.
		s.wakeForPendingSteering()
		return replayed, nil
	}
	s.reflectDurableInputQueue(consumedClientMutationIDs...)
	s.reflectDurableClientSteering()
	s.wakeForPendingSteering()
	return response, nil
}

// AcceptClientMutationDrainAsSteer durably accepts or replays one queued-input
// drain into steering.
func (s *Session) AcceptClientMutationDrainAsSteer(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	return s.clientMutationDrain(params)
}

func combineClientMutationInputs(entries []clientMutationQueueEntry, extra []appwire.InputItem) []appwire.InputItem {
	texts := make([]string, 0, len(entries)+1)
	var images []ImageAttachment
	var skillNames []string
	for _, entry := range entries {
		queued := queuedInputFromClientMutation(entry)
		if strings.TrimSpace(queued.Text) != "" {
			texts = append(texts, queued.Text)
		}
		images = append(images, queued.Images...)
		skillNames = append(skillNames, queued.SkillNames...)
	}
	if len(extra) > 0 {
		queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: extra})
		if strings.TrimSpace(queued.Text) != "" {
			texts = append(texts, queued.Text)
		}
		images = append(images, queued.Images...)
		skillNames = append(skillNames, queued.SkillNames...)
	}
	return clientMutationInput(strings.Join(texts, "\n\n"), images, skillNames)
}

func (s *Session) clientMutationPromote(params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	defer release()
	if err := s.ensureClientMutationStore(); err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	request, err := newClientMutationRequest(clientMutationMethodPromote, params.ClientMutationID, params)
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	request.Preconditions.ExpectedEntryID = params.ExpectedEntryID
	var response appwire.TurnPromoteQueuedAsSteerResponse
	lookup, err := s.clientMutations.executeAtomic(request, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		if snapshot.InterruptFence != nil {
			rejectClientMutation(record, appwire.Conflict("turn interrupt is pending"))
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
		// Promoting is the user picking one to run now. Below the effect
		// rejection so a refused promote cannot unpark it.
		snapshot.QueueHeld = false
		// Promoting is a user-initiated run, so the parked steer is released too
		// (issue #174).
		snapshot.SteeringHeld = false
		entry := snapshot.InputQueue[index]
		snapshot.InputQueue = append(snapshot.InputQueue[:index], snapshot.InputQueue[index+1:]...)
		snapshot.QueueRevision++
		removeQueuedMutationSource(snapshot, entry, "transformed")
		response = appwire.TurnPromoteQueuedAsSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method))}
		result, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return marshalErr
		}
		addPendingSteering(snapshot, record, entry.Input)
		applyClientMutationRecord(record, result, acceptedClientMutationProjection(record.Method))
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
		// A retry of steering that was accepted but never delivered must still
		// provoke delivery; replay is idempotent in the store and useless to the
		// user without the wake.
		s.wakeForPendingSteering()
		return replayed, nil
	}
	s.reflectDurableInputQueue()
	s.reflectDurableClientSteering()
	s.wakeForPendingSteering()
	return response, nil
}

// AcceptClientMutationPromoteQueuedAsSteer durably accepts or replays one
// queued-input promotion into steering.
func (s *Session) AcceptClientMutationPromoteQueuedAsSteer(params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	return s.clientMutationPromote(params)
}

func (s *Session) clientMutationCancel(params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	release, err := s.beginRetirementMutation("input")
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	defer release()
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
			Receipt:       mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method)),
		}
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
	s.wakeForPendingQueuedInput()
	return response, nil
}

// AcceptClientMutationCancelQueued durably accepts or replays one queued-input
// cancellation.
func (s *Session) AcceptClientMutationCancelQueued(params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return s.clientMutationCancel(params)
}

func reserveClientMutationTurnID(snapshot *clientMutationSnapshot, record *clientMutationRecord) {
	snapshot.NextTurnSequence++
	record.StableTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
}

// acceptedClientMutationProjection reports the projection state a mutation
// carries the moment it is accepted. Accepted input is durable, but nothing in
// authoritative state describes it until transcript incorporation, so the
// input-bearing methods report pending: a client told `reflected` here would
// drop its optimistic copy while no authoritative item had replaced it.
// Interrupt and cancel are terminal at acceptance -- their whole effect is
// already carried by state the client can read.
//
// This is the only place the method-to-state mapping lives. Call sites pass the
// result rather than repeating the switch.
func acceptedClientMutationProjection(method string) appwire.MutationProjectionState {
	switch method {
	case clientMutationMethodNotesHumanSet, clientMutationMethodStart,
		clientMutationMethodSteer,
		clientMutationMethodQueue,
		clientMutationMethodDrain,
		clientMutationMethodPromote:
		return appwire.MutationProjectionPending
	case clientMutationMethodInterrupt:
		return appwire.MutationProjectionReflected
	case clientMutationMethodCancel:
		return appwire.MutationProjectionRemoved
	default:
		// An unrecognized method has not proven its effect is visible.
		return appwire.MutationProjectionPending
	}
}

func mutationReceipt(
	threadID string,
	record clientMutationRecord,
	disposition appwire.MutationDisposition,
	projectionState appwire.MutationProjectionState,
) appwire.MutationReceipt {
	return appwire.MutationReceipt{
		ClientMutationID: record.ClientMutationID,
		Disposition:      disposition,
		ThreadID:         threadID,
		InstanceID:       threadID,
		TurnID:           record.StableTurnID,
		QueueEntryIDs:    append([]string(nil), record.StableQueueEntryIDs...),
		ProjectionState:  projectionState,
	}
}

func applyClientMutationRecord(
	record *clientMutationRecord,
	result json.RawMessage,
	projectionState appwire.MutationProjectionState,
) {
	record.OperationState = clientMutationOperationApplied
	record.ExecutionState = "accepted"
	record.ProjectionState = projectionState
	record.Result = append(json.RawMessage(nil), result...)
}

func addPendingSteering(snapshot *clientMutationSnapshot, record *clientMutationRecord, input []appwire.InputItem) {
	snapshot.PendingExecutions[record.ClientMutationID] = appwire.PendingMutation{
		ClientMutationID: record.ClientMutationID,
		Method:           record.Method,
		Input:            cloneClientMutationInput(input),
		ExecutionState:   "accepted",
		TurnID:           record.StableTurnID,
		ProjectionState:  acceptedClientMutationProjection(record.Method),
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

// clientMutationQueueClientMutationIDs is clientMutationQueueIDs' counterpart
// for the CLIENT's own identity on each entry (ClientMutationID), rather than
// the daemon-minted stable queue-entry id: it names entries by the identity a
// client's own optimistic record was enqueued under.
func clientMutationQueueClientMutationIDs(entries []clientMutationQueueEntry) []string {
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ClientMutationID
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
		if slices.Contains(record.StableQueueEntryIDs, entryID) {
			return true
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
	for _, entry := range client {
		// A steer a turn has popped and is appending is accepted in the store
		// until its incorporation lands; it is not back in the queue.
		if _, inFlight := s.steeringInFlight[entry.ClientMutationID]; !inFlight {
			daemon = append(daemon, entry)
		}
	}
	s.steeringQueue = daemon
	s.mu.Unlock()
}

// snapshotHasPendingUserSteering reports whether the durable store holds user
// steering that could still be delivered -- the steering a Stop has something
// to park. It reads the durable store rather than s.steeringQueue because a
// steer is recorded there first (clientMutationSteer commits, then reflects)
// and stays there across a restart, which is the delivery this answer governs.
// A steer is pending from its acceptance until its transcript append is
// recorded (finalizeIncorporatedSteering), a turn appending it included: if
// that append fails the steer is still deliverable and still needs parking,
// and if it lands the steer is gone and rule H releases a hold that named it.
func snapshotHasPendingUserSteering(snapshot *clientMutationSnapshot) bool {
	for _, id := range snapshot.SteeringOrder {
		if pending, ok := snapshot.PendingExecutions[id]; ok && pending.ExecutionState == "accepted" {
			return true
		}
	}
	return false
}

func clientSteeringFromSnapshot(snapshot clientMutationSnapshot) []steeringMessage {
	client := make([]steeringMessage, 0, len(snapshot.SteeringOrder))
	for _, id := range snapshot.SteeringOrder {
		pending, ok := snapshot.PendingExecutions[id]
		if !ok || pending.ExecutionState != "accepted" {
			continue
		}
		queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
		origin := steeringOriginFromJournal(snapshot.Journal, id)
		client = append(client, steeringMessage{
			// A journal written before the write-path strip can carry controls in
			// a note-origin pending text: the rebuilt entry reaches the model and
			// the transcript, so that text is stripped like every other load path,
			// while ordinary steering keeps the bytes the user typed. The record's
			// method decides a kindless record, so a normal steer whose text
			// imitates the note prefix is not mistaken for a note update.
			Text:       rebuiltSteeringText(origin, queued.Text),
			Images:     queued.Images,
			SkillNames: queued.SkillNames,
			Source:     events.SteeringSourceUser,
			// The kind survives the restart because it rode the durable
			// journal record (see SteeringKind on clientMutationRecord),
			// not the reflected in-memory entry. Plain user steering
			// carries no stamp and keeps its empty kind, while a record
			// whose method proves the note write rebuilds with the note
			// kind even though no kind was recorded.
			Kind:             origin.steeringKind(),
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
	// The turn this process inherited from the durable snapshot, and only when a
	// client turn/start owns it. See the recoveredTurnID field doc: a
	// queue-origin owner leaves it empty on purpose, so no follow-up start is
	// admitted behind a turn the claim path would never take.
	s.recoveredTurnID = s.recoveredStartTurnID()
	incorporated := make(map[string]string, len(s.restoredClientMutationTurns))
	maps.Copy(incorporated, s.restoredClientMutationTurns)
	// steeringOutcome is the terminal state the transcript's entry for a steer
	// stands for: a steering turn was incorporated, a failure turn is a skill
	// selection that could not be prepared. It is read from the FULL
	// transcript index (restoredClientMutationItems) and the resumed history
	// both: the history starts at the last compaction, and a steer recorded
	// before it is still recorded.
	steeringOutcome := map[string]string{}
	for id, items := range s.restoredClientMutationItems {
		switch {
		case items.Failure:
			steeringOutcome[id] = "failed"
		case items.Steering:
			steeringOutcome[id] = "incorporated"
		}
	}
	for _, turn := range s.history {
		if turn.ClientMutationID != "" {
			incorporated[turn.ClientMutationID] = turn.StableTurnID
			switch turn.Kind {
			case schema.TurnSteering:
				steeringOutcome[turn.ClientMutationID] = "incorporated"
			case schema.TurnFailure:
				steeringOutcome[turn.ClientMutationID] = "failed"
			}
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
			if pending.Method != clientMutationMethodQueue && pending.Method != clientMutationMethodStart {
				// Client steering: the table below, not this loop.
				continue
			}
			if stableTurnID, ok := incorporated[id]; ok && stableTurnID == pending.TurnID {
				record.ExecutionState = "incorporated"
				record.ProjectionState = appwire.MutationProjectionReflected
				pending.ExecutionState = "incorporated"
				pending.ProjectionState = appwire.MutationProjectionReflected
				snapshot.PendingExecutions[id] = pending
				snapshot.Journal[id] = record
				continue
			}
			if pending.ExecutionState != "claimed" {
				continue
			}
			if pending.Method == clientMutationMethodQueue {
				if err := returnClaimedQueuedMutation(snapshot, id, pending, &record); err != nil {
					return err
				}
			} else {
				pending.ExecutionState = "accepted"
				snapshot.PendingExecutions[id] = pending
				if snapshot.AcceptedTurns > 0 {
					snapshot.AcceptedTurns--
				}
				snapshot.BudgetReservations[id] = clientMutationBudgetReservation{
					TurnID: pending.TurnID,
					Slots:  1,
				}
			}
			record.ExecutionState = "accepted"
			snapshot.Journal[id] = record
		}
		// Compatibility, and the only such rule: a store written before steers
		// were accepted until recorded can hold a steer the old popSteeringHead
		// marked "claimed". It is accepted again here, so the table below
		// decides it like any other: recorded, it is finalized; unrecorded, it
		// never landed and is queued once.
		for _, id := range snapshot.SteeringOrder {
			if pending, ok := snapshot.PendingExecutions[id]; ok && pending.ExecutionState == "claimed" {
				pending.ExecutionState = "accepted"
				snapshot.PendingExecutions[id] = pending
				record := snapshot.Journal[id]
				record.ExecutionState = "accepted"
				snapshot.Journal[id] = record
			}
		}
		// Client steering: the transcript is the record of delivery. A steer
		// it holds (by id and reserved turn) was delivered by a turn that died
		// before the store's incorporation write and is finalized; one it does
		// not hold is still queued, accepted. A hold naming nothing (a snapshot
		// written before #710 could park steering unconditionally at Stop, and
		// the finalization can retire the last steer a Stop parked) is released
		// by rule H; release only, never arm, since restore is not a Stop
		// (TestRestoredSteeringWakesWhenTheDaemonAttaches).
		reconcileClientSteering(snapshot, func(id string) string {
			if stableTurnID, ok := incorporated[id]; !ok || stableTurnID != snapshot.PendingExecutions[id].TurnID {
				return ""
			}
			// A steer the transcript holds under its reserved turn is
			// recorded whatever the entry's kind; a match never reads as
			// unrecorded.
			if state := steeringOutcome[id]; state != "" {
				return state
			}
			return "incorporated"
		}, false)
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

// clientMutationTranscriptHolds reports whether this session's history already
// holds a turn for this client mutation and stable turn id. It is not the store's
// incorporation mark: the user-input turn is written to the transcript before
// that mark is stored, so a mark that failed leaves an execution the store still
// calls "claimed" whose turn is already on disk. Requeueing that one appends and
// processes the message twice.
func (s *Session) clientMutationTranscriptHolds(clientMutationID, stableTurnID string) bool {
	items := s.clientMutationTranscriptItems(clientMutationID, stableTurnID)
	return items.User || items.Failure
}

// claimedQueuedRecordedInTranscript samples which of this session's claimed queue
// executions the transcript already holds, keyed by client mutation id.
//
// Callers MUST take this before they enter the store serializer: the transcript
// scan takes Session.mu, and the serializer never waits on it. It is the same
// shape as recordedSteeringAwaitingMark, which exists for the same reason.
func (s *Session) claimedQueuedRecordedInTranscript() map[string]bool {
	if s == nil || s.clientMutations == nil {
		return nil
	}
	var recorded map[string]bool
	for id, pending := range s.clientMutations.snapshot().PendingExecutions {
		if pending.Method != clientMutationMethodQueue || pending.ExecutionState != "claimed" {
			continue
		}
		if s.clientMutationTranscriptHolds(id, pending.TurnID) {
			if recorded == nil {
				recorded = make(map[string]bool)
			}
			recorded[id] = true
		}
	}
	return recorded
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

func (s *Session) recoverClientMutationFailures(publishEnvironment bool) error {
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
		// At restore no execution runs: the failed start's entries form its
		// own execution, which ends failed. Inside a running turn they join it.
		// A failure whose entries are all recorded already has nothing to add.
		items := s.clientMutationTranscriptItems(id, pending.TurnID)
		own := s.attachedTranscript().RunningTurnID() == "" && (!items.User || !items.Failure)
		if own {
			s.beginExecution(pending.TurnID)
		}
		err := s.recordClientMutationFailure(id, pending, *record.Failure, publishEnvironment)
		if own {
			s.completeExecution(schema.TurnFailed)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) recordClientMutationFailure(
	clientMutationID string,
	pending appwire.PendingMutation,
	failure clientMutationFailure,
	publishEnvironment bool,
) error {
	items := s.clientMutationTranscriptItems(clientMutationID, pending.TurnID)
	queued := queuedInputFromClientMutation(clientMutationQueueEntry{Input: pending.Input})
	if !items.User {
		if err := s.clientMutationFailureFault("before_user"); err != nil {
			return err
		}
		if err := s.appendEnvironmentContext(publishEnvironment); err != nil {
			return fmt.Errorf("append environment context: %w", err)
		}
		// Persist the failed start's attachments before its turn is built so
		// the message can name their durable paths (agent/image_persist.go);
		// idempotent by content, so a first attempt that already wrote the
		// bytes rewrites the same path.
		queued.Images = s.persistInputImages(queued.Images)
		turn := schema.NewTurn(schema.TurnUserInput, buildSelectedUserInputMessage(queued.Text, queued.Images, queued.SkillNames))
		turn.ClientMutationID = clientMutationID
		turn.StableTurnID = pending.TurnID
		// The typed selection must ride the persisted failed input before the
		// journal payload cleanup below clears it: the record keeps both the
		// names and the original prose for correction and explicit retry.
		if len(queued.SkillNames) > 0 {
			turn.SkillState = &schema.SkillTurnState{Input: skillInputRecordFromQueued(queued)}
		}
		if err := s.appendTurnAfterTranscriptWrite(
			turn,
			func() error { return s.writeTranscriptSyncedLocked(turn) },
			func() {
				s.clientMutationAppendedTurn = true
				s.history = append(s.history, turn)
			},
		); err != nil {
			return fmt.Errorf("append failed client start input: %w", err)
		}
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
		if err := s.appendTurnAfterTranscriptWrite(
			turn,
			func() error { return s.writeTranscriptSyncedLocked(turn) },
			func() {
				s.clientMutationAppendedTurn = true
				s.history = append(s.history, turn)
			},
		); err != nil {
			return fmt.Errorf("append failed client start diagnostic: %w", err)
		}
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

// finalizeIncorporatedSteering records in the store that a client steer's
// transcript append landed: the steer is incorporated and leaves the pending
// executions and the steering order.
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
		finalizeSteeringInSnapshot(snapshot, clientMutationID, "incorporated")
		return nil
	})
}

// finalizeSteeringInSnapshot retires a steer the transcript holds to its
// terminal state -- "incorporated" for a steering turn, "failed" for a
// recorded selection failure. finalizeIncorporatedSteering's write, shared
// with the steering table, which marks from the transcript the steers whose
// own write the store refused or a dead process never made.
func finalizeSteeringInSnapshot(snapshot *clientMutationSnapshot, clientMutationID, terminalState string) {
	record := snapshot.Journal[clientMutationID]
	record.OperationState = clientMutationOperationTerminal
	record.ExecutionState = terminalState
	record.ProjectionState = appwire.MutationProjectionReflected
	record.Payload = nil
	snapshot.Journal[clientMutationID] = record
	delete(snapshot.PendingExecutions, clientMutationID)
	removeClientMutationSteeringOrder(snapshot, clientMutationID)
}

// completeClientMutationTurn is the lifecycle completion hook for Task 4.
// Queue transcript incorporation retains the payload and stable turn identity;
// only durable terminal completion may remove the runnable execution.
func (s *Session) completeClientMutationTurn(clientMutationID string) error {
	_, err := s.completeClientMutationTurnWithState(clientMutationID, "terminal")
	return err
}

func (s *Session) completeClientMutationInterruptedTurn(clientMutationID string) error {
	_, err := s.completeClientMutationTurnWithState(clientMutationID, "interrupted")
	return err
}

// completeClientMutationTurnWithState settles one client-mutation turn and
// reports whether doing so finalized an interrupt fence naming that turn --
// i.e. whether a Stop is what ended it. The drain loop needs that fact: a turn
// ended by a Stop must leave the queue head parked (wms7's ruling), while a
// turn ended by a bare host cancellation may still drain it, and by the time
// the drain loop asks, the fence this call just finalized is already gone.
func (s *Session) completeClientMutationTurnWithState(clientMutationID, executionState string) (stopFinalized bool, err error) {
	returned := false
	// For the fence this completion may finalize; sampled outside the mutate.
	recordedIDs, recorded := s.recordedSteeringAwaitingMark()
	// Whether the transcript already holds this execution's turn, also sampled
	// outside the mutate: the transcript scan takes Session.mu, and the store
	// serializer must never wait on it.
	claimedRecorded := s.claimedQueuedRecordedInTranscript()
	fenceFinalized := false
	err = s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		// A queued turn that was claimed and never recorded did not run, and
		// settling it here would lose it: the entry is already out of the input
		// queue and nothing else puts it back within this process. Return the
		// claim instead. The ordinary way to reach this is a cancelled context,
		// and a Stop is what cancels it (katas e519 + nss1).
		//
		// "claimed" is not enough: the user-input turn is written to the
		// transcript before its incorporation mark, so a mark that failed leaves
		// a claimed execution whose turn is already on disk. Requeueing that one
		// appends and processes the message a second time, so the transcript
		// decides, on the fact the caller sampled outside this serializer.
		//
		// A recorded claim is not left claimed either: that would keep the active
		// turn pinned on a message the session already ran, wedging every later
		// turn until a restart. The transcript holding the turn IS the thing
		// incorporation records, so the execution is promoted and settles through
		// the same path an incorporated one takes -- no second append, no second
		// run, and the active turn is cleared below.
		if pending.ExecutionState == "claimed" && pending.Method == clientMutationMethodQueue {
			if claimedRecorded[clientMutationID] {
				pending.ExecutionState = "incorporated"
				snapshot.PendingExecutions[clientMutationID] = pending
			} else {
				record := snapshot.Journal[clientMutationID]
				if err := returnClaimedQueuedMutation(snapshot, clientMutationID, pending, &record); err != nil {
					return err
				}
				record.ExecutionState = "accepted"
				snapshot.Journal[clientMutationID] = record
				snapshot.QueueRevision++
				returned = true
				// The turn-boundary half of wms7. A turn claimed out of the queue and
				// stopped during its PRE-TURN WORK ends here, not in the incorporated
				// branch below, so reporting the Stop only from there left the drain
				// loop hearing "a bare host cancellation" -- and running the very
				// message the user stopped, which it has just put back on the queue.
				//
				// Nothing is finalized here: the interrupt does that after this
				// completion returns, which is exactly why the fence is still legible
				// from this branch and why reporting it costs nothing.
				if snapshot.InterruptFence != nil && snapshot.InterruptFence.ExpectedTurnID == pending.TurnID {
					stopFinalized = true
				}
				return nil
			}
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
			stopFinalized = true
			fenceFinalized = true
			finalized, finalizeErr := finalizeClientMutationInterrupt(snapshot, s.ID(), recorded, claimedRecorded)
			returned = returned || finalized
			return finalizeErr
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if fenceFinalized {
		s.steeringMarked(recordedIDs)
		// The Stop that fenced this turn has finalized, so a start the fence was
		// holding back can now be claimed. Nothing else wakes it: a Stop parks
		// the input queue, and wakeForPendingSteering only serves steering.
		s.wakeRunnableClientMutationStartAfterFence()
	}
	if returned {
		// The durable queue grew a message back. QueueDepth, QueuePreview,
		// WireState and the queued-input wake all read the process-local copy,
		// so without this the message is durably present and invisible -- and
		// the wake that would run it gates on that same stale depth.
		s.reflectDurableInputQueue()
		s.wakeForPendingQueuedInput()
	}
	return stopFinalized, nil
}

// returnClaimedQueuedMutation puts a claimed queue entry back the way
// popQueueHead found it: at the head of the input queue, holding the turn
// budget the claim consumed, and no longer naming a running turn.
//
// It runs from two places and both are load-bearing. At startup it recovers a
// claim whose process died. At turn completion it recovers a claim whose turn
// returned before incorporating anything -- which is what a cancelled context
// does, and a Stop is what cancels it. Without the second, the message leaves
// the queue, never runs, and its turn id pins ActiveTurnID for the life of the
// process, so every later turn/start is refused with "turn is already active"
// (katas e519 + nss1).
//
// The record goes back to the not-yet-visible projection a fresh queue entry
// starts in, not the reflected state a genuine incorporation earns: nothing was
// written to the transcript, so a client told "reflected" would drop its
// optimistic copy with nothing authoritative to replace it.
func returnClaimedQueuedMutation(
	snapshot *clientMutationSnapshot,
	clientMutationID string,
	pending appwire.PendingMutation,
	record *clientMutationRecord,
) error {
	if len(pending.QueueEntryIDs) != 1 {
		return fmt.Errorf("claimed queue mutation %q has %d queue entry IDs", clientMutationID, len(pending.QueueEntryIDs))
	}
	snapshot.InputQueue = append([]clientMutationQueueEntry{{
		ID:               pending.QueueEntryIDs[0],
		ClientMutationID: clientMutationID,
		Input:            cloneClientMutationInput(pending.Input),
	}}, snapshot.InputQueue...)
	if snapshot.AcceptedTurns > 0 {
		snapshot.AcceptedTurns--
	}
	snapshot.BudgetReservations[clientMutationID] = clientMutationBudgetReservation{TurnID: pending.TurnID, Slots: 1}
	delete(snapshot.PendingExecutions, clientMutationID)
	if snapshot.ActiveTurnID == pending.TurnID {
		snapshot.ActiveTurnID = ""
	}
	// Callers own the queue revision and the runtime reflection: restore bumps
	// the revision once for the whole rebuild, and the completion path bumps it
	// per return. Both must reflect afterwards -- QueueDepth, QueuePreview,
	// WireState and the queued-input wake all read the process-local copy, so a
	// message restored only here would be durably present and invisible.
	record.ProjectionState = acceptedClientMutationProjection(record.Method)
	return nil
}

func removeClientMutationSteeringOrder(snapshot *clientMutationSnapshot, clientMutationID string) {
	for i, id := range snapshot.SteeringOrder {
		if id == clientMutationID {
			snapshot.SteeringOrder = append(snapshot.SteeringOrder[:i], snapshot.SteeringOrder[i+1:]...)
			return
		}
	}
}

// appendClientMutationTranscriptLocked writes a client-mutation turn's
// transcript entry; callers hold attentionMu (their append/write pair —
// appendTurnAfterTranscriptWrite). The test seam runs under that hold; seam
// closures only record turns or inject errors.
func (s *Session) appendClientMutationTranscriptLocked(turn schema.Turn) error {
	if s.clientMutationTranscriptAppend != nil {
		return s.clientMutationTranscriptAppend(turn)
	}
	// Durability owner: the client-mutation journal and the steering/user-input
	// claim consumers advance on this record, so it must be synced, not merely
	// recorded, before they do (H1).
	return s.writeTranscriptSyncedLocked(turn)
}
