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
	s.clientMutations = store
	return nil
}

func (s *Session) clientMutationQueue(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
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
// own to run: it is drained into a turn built to carry it, entering as
// EntrySteeringCarrier -- a kind that passes the same pending-question gate as
// EntryUserInput (steering is the user speaking too) but is not routed through
// acceptUserInput, so it costs no MaxTurns slot and appends no empty user turn.
//
// onRunnable, when non-nil, is called with the turn id at the moment a claim
// becomes real, so the daemon can publish the running turn and wire
// cancellation to it before the turn starts producing. For steering that id is
// claimSteeringCarrierTurn's -- one of the pending steer mutations' own
// reserved ids, not a freshly minted one, so the id the client was told in its
// Applied receipt is the id that actually runs.
func (s *Session) ProcessPendingUserInput(ctx context.Context, onRunnable func(string)) (string, bool, error) {
	if err := s.ensureClientMutationStore(); err != nil {
		return "", false, err
	}
	queued := s.popQueueHead()
	if strings.TrimSpace(queued.Text) != "" || len(queued.Images) > 0 {
		if onRunnable != nil && queued.StableTurnID != "" {
			onRunnable(queued.StableTurnID)
		}
		ctx = withQueuedClientMutation(ctx, queued)
		result, err := s.ProcessInputKind(ctx, queued.Text, queued.Images, EntryUserInput)
		return result, true, err
	}
	if !s.hasPendingUserSteering() {
		return "", false, nil
	}
	turnID, ok := s.claimSteeringCarrierTurn()
	if !ok {
		// Another mutation already owns the active-turn slot, or an interrupt
		// fence is ending one: this wake cannot name itself. Stand down rather
		// than run unaddressable -- the steering stays queued for whichever
		// turn runs next, the same stand-down contract mintRunningTurnID's
		// callers rely on.
		return "", false, nil
	}
	// Hand the claim back on EVERY exit, not just the ones that reach
	// processOneInput's own deferred release. That defer is registered several
	// early returns into the call: the entry gate refuses a closed session
	// before it, and so does the cancellation check processOneInput makes
	// before taking any name. A claim stranded on either is not recoverable --
	// the pending steer still owns the id, so forgetRunningTurnNoOneOwns leaves
	// it alone at load -- and every later turn/start is then refused with "turn
	// is already active" for the life of the session, across restarts.
	// releaseRunningTurnID compare-and-clears, so on the ordinary path (where
	// the turn's own release already ran, or a later mutation took the slot)
	// this is a no-op.
	defer s.releaseRunningTurnID(turnID)
	if onRunnable != nil {
		onRunnable(turnID)
	}
	ctx = withSteeringCarrierTurn(ctx, turnID)
	result, err := s.ProcessInputKind(ctx, "", nil, EntrySteeringCarrier)
	return result, true, err
}

// steeringCarrierContextKey carries the turn id claimSteeringCarrierTurn
// reserved from ProcessPendingUserInput down into processOneInput, the same
// way queuedClientMutationContextKey carries a claimed queue entry's identity.
type steeringCarrierContextKey struct{}

func withSteeringCarrierTurn(ctx context.Context, turnID string) context.Context {
	return context.WithValue(ctx, steeringCarrierContextKey{}, turnID)
}

func steeringCarrierTurnIDFromContext(ctx context.Context) string {
	turnID, _ := ctx.Value(steeringCarrierContextKey{}).(string)
	return turnID
}

// claimSteeringCarrierTurn reserves the durable turn identity for a wake whose
// only job is to carry already-accepted user steering. It reuses the id the
// head of the pending steering already reserved at its own acceptance --
// reserveClientMutationTurnID, called from clientMutationSteer/Drain/Promote
// -- rather than minting a fresh one, so the id returned in that mutation's
// Applied receipt is the id that actually runs.
//
// Returns ok=false when nothing is claimable: an interrupt fence is ending a
// turn, another mutation already holds the active-turn slot, or (a benign
// race with whatever cleared hasPendingUserSteering's answer between the
// caller's check and this call) no user steering is left pending. The caller
// treats every case the same way -- stand down -- because the steering that
// prompted the wake, if still queued, stays queued for whichever turn runs
// next; nothing is lost by waiting.
func (s *Session) claimSteeringCarrierTurn() (turnID string, ok bool) {
	if err := s.ensureClientMutationStore(); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("open client mutation store: %v", err)})
		return "", false
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.InterruptFence != nil || snapshot.ActiveTurnID != "" {
			return nil
		}
		// A Stop parks pending user steering until the user asks for something to
		// run, the same way QueueHeld parks the input queue. Claiming the steering
		// carrier here would hand the steer to a turn the Stop just ended, so it
		// is refused -- mirroring popQueueHead's QueueHeld gate (issue #174). The
		// steer stays in PendingExecutions/SteeringOrder; nothing moves, so its
		// causal provenance is never at risk (issue #146, Option C).
		if snapshot.SteeringHeld {
			return nil
		}
		for _, id := range snapshot.SteeringOrder {
			pending, exists := snapshot.PendingExecutions[id]
			if !exists || pending.ExecutionState != "accepted" || pending.TurnID == "" {
				continue
			}
			snapshot.ActiveTurnID = pending.TurnID
			turnID = pending.TurnID
			return nil
		}
		return nil
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("claim steering carrier turn failed: %v", err)})
		return "", false
	}
	return turnID, turnID != ""
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
	if s.clientMutations != nil && s.clientMutations.steeringHeld() {
		return
	}
	// Only the user's own steering provokes a turn. Daemon-authored steering --
	// the current-task reminder, hook context, a transcript pointer -- is
	// context for whatever turn runs next, and the round loop drains it into
	// that turn. Waking for it would start a turn before the user has said
	// anything, which is both a turn nobody asked for and a turn that can hold
	// the session's turn identity when the opening turn/start arrives.
	if !s.hasPendingUserSteering() {
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
		steeringInput := combineClientMutationInputs(entries, params.Input)
		snapshot.InputQueue = remaining
		snapshot.QueueRevision++
		response = appwire.TurnDrainAsSteerResponse{Receipt: mutationReceipt(s.ID(), *record, appwire.MutationDispositionApplied, acceptedClientMutationProjection(record.Method))}
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
	s.reflectDurableInputQueue()
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
	case clientMutationMethodStart,
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
	daemon = append(daemon, client...)
	s.steeringQueue = daemon
	s.mu.Unlock()
}

// snapshotHasPendingUserSteering reports whether the durable store holds user
// steering that could still be delivered -- the steering a Stop has something
// to park. It reads the durable store rather than s.steeringQueue because a
// steer is recorded there first (clientMutationSteer commits, then reflects)
// and stays there across a restart, which is the delivery this answer governs.
//
// cancelledTurnID names the turn a Stop is ending, or is empty when nobody is
// stopping anything (the restore normalization).
//
// "Accepted" is the resting state clientSteeringFromSnapshot materializes into
// the in-memory queue. "Claimed" is the window popSteeringHead opens: the
// claim commits before consumeSteeringMessage appends the transcript entry
// that finalizes it, and restoreDurableClientMutationQueues returns a claim
// that never landed to accepted -- so a claimed steer is still deliverable
// across a restart and still needs parking. The one exception is the steer
// whose own reserved id IS the turn being cancelled: that steer is the
// steering-carrier turn the Stop is ending rather than a passenger it has to
// hold back, and its record disappears as soon as its append finalizes, so
// parking for it would leave a hold naming nothing.
func snapshotHasPendingUserSteering(snapshot *clientMutationSnapshot, cancelledTurnID string) bool {
	for _, id := range snapshot.SteeringOrder {
		pending, ok := snapshot.PendingExecutions[id]
		if !ok {
			continue
		}
		switch pending.ExecutionState {
		case "accepted":
			return true
		case "claimed":
			if cancelledTurnID == "" || pending.TurnID != cancelledTurnID {
				return true
			}
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
	maps.Copy(incorporated, s.restoredClientMutationTurns)
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
				if err := returnClaimedQueuedMutation(snapshot, id, pending, &record); err != nil {
					return err
				}
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
		// Release a steering hold the loop above left naming nothing. A
		// snapshot written before #710 could park steering unconditionally at
		// Stop, so a restored hold can name no pending steer at all -- and a
		// hold naming nothing swallows every steer the resumed session accepts
		// afterwards. This runs after the claimed-steering returns above, so a
		// claim that never landed still counts as parked.
		//
		// Release only, never arm: restore is not a Stop, and a session that
		// was never stopped must keep waking for the steering it is holding
		// (TestRestoredSteeringWakesWhenTheDaemonAttaches).
		if snapshot.SteeringHeld && !snapshotHasPendingUserSteering(snapshot, "") {
			snapshot.SteeringHeld = false
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
		s.maybeAppendEnvironmentContext()
		turn := schema.NewTurn(schema.TurnUserInput, buildUserInputMessage(queued.Text, queued.Images))
		turn.ClientMutationID = clientMutationID
		turn.StableTurnID = pending.TurnID
		if err := s.writeTranscriptDurable(turn); err != nil {
			return fmt.Errorf("append failed client start input: %w", err)
		}
		s.mu.Lock()
		s.clientMutationAppendedTurn = true
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
		s.clientMutationAppendedTurn = true
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
	err = s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending, ok := snapshot.PendingExecutions[clientMutationID]
		if !ok {
			return nil
		}
		// A queued turn that was claimed but never incorporated did not run, and
		// settling it here would lose it: the entry is already out of the input
		// queue and nothing else puts it back within this process. Return the
		// claim instead. The ordinary way to reach this is a cancelled context,
		// and a Stop is what cancels it (katas e519 + nss1).
		if pending.ExecutionState == "claimed" && pending.Method == clientMutationMethodQueue {
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
			return finalizeClientMutationInterrupt(snapshot, s.ID())
		}
		return nil
	})
	if err != nil {
		return false, err
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

func (s *Session) appendClientMutationTranscript(turn schema.Turn) error {
	if s.clientMutationTranscriptAppend != nil {
		return s.clientMutationTranscriptAppend(turn)
	}
	return s.writeTranscriptDurable(turn)
}
