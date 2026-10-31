package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func (s *Server) handleAppTurnStart(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	actor, manager, err := s.inputActor(ctx, params.ManagerProof)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	params.ManagerProof = nil
	unlock, err := s.admitRetrySafeMutation(params.Ref, params.ThreadID, &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	defer unlock()
	callbacks := s.retrySafeTurnFunctions()
	fn := callbacks.Start
	if manager {
		as := callbacks.StartAs
		if as == nil {
			return appwire.TurnStartResponse{}, appwire.Unavailable("Manager turn start not available")
		}
		fn = func(p appwire.TurnStartParams) (appwire.TurnStartResponse, error) { return as(p, actor) }
	}
	// Skill selections are consumed at the input's actual claim: this endpoint
	// prepares them against the durable input identity and delivers the
	// complete bodies at dispatch. The support verdict is this endpoint's own
	// wiring — the very seam the dispatch below requires — so a wired endpoint
	// keeps consuming selections at the claim while an unwired one refuses
	// them here with the typed unsupported-input error instead of asserting
	// support it cannot deliver. Exact catalog identity and invocation policy
	// remain consumption-time checks in the session.
	if params.Input, err = gateTurnInput(params.Input, fn != nil, true); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	return callRetrySafe(s, fn, params, params.ClientMutationID, "turn start not available")
}

func (s *Server) handleAppTurnSteer(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	actor, manager, err := s.inputActor(ctx, params.ManagerProof)
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	params.ManagerProof = nil
	unlock, err := s.admitRetrySafeMutation(params.Ref, params.ThreadID, &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	defer unlock()
	callbacks := s.retrySafeTurnFunctions()
	fn := callbacks.Steer
	if manager {
		as := callbacks.SteerAs
		if as == nil {
			return appwire.TurnSteerResponse{}, appwire.Unavailable("Manager steer not available")
		}
		fn = func(p appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) { return as(p, actor) }
	}
	// Skill selections are consumed at the steering's actual delivery: the
	// support verdict is this endpoint's own wiring. See the turn/start gate.
	if params.Input, err = gateTurnInput(params.Input, fn != nil, true); err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	return callRetrySafe(s, fn, params, params.ClientMutationID, "steer not available")
}

func (s *Server) handleAppSandboxEscalationResolve(ctx context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
	actor, manager, err := s.inputActor(ctx, params.ManagerProof)
	if err != nil {
		return appwire.EmptyResponse{}, err
	}
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.EmptyResponse{}, err
	}
	escalationID := strings.TrimSpace(params.EscalationID)
	if escalationID == "" {
		return appwire.EmptyResponse{}, appwire.InvalidParams("escalationId is required")
	}
	s.mu.RLock()
	fn := s.sandboxEscalationResolveFunc
	as := s.sandboxEscalationResolveAs
	s.mu.RUnlock()
	if manager && as == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("Manager approval not available")
	}
	if !manager && fn == nil {
		return appwire.EmptyResponse{}, appwire.Unavailable("sandbox escalation resolve not available")
	}
	if manager {
		err = as(escalationID, params.Approve, actor)
	} else {
		err = fn(escalationID, params.Approve)
	}
	if err != nil {
		// The escalation is unknown or already resolved (a stale card, a double
		// click, or a race with turn-interrupt/close). Surface it as a conflict so
		// the client can drop the card rather than retry.
		return appwire.EmptyResponse{}, appwire.Conflict(err.Error())
	}
	return appwire.EmptyResponse{}, nil
}

// handleAppDelegateStop ends one subagent's run at the user's request (S6).
// It targets the root that owns the tree, the way every turn mutation does.
// Stopping is idempotent, so it needs no client mutation id: a retry finds the
// run already ending and answers notRunning.
func (s *Server) handleAppDelegateStop(_ context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, params.ThreadID); err != nil {
		return appwire.DelegateStopResponse{}, err
	}
	delegateID := strings.TrimSpace(params.DelegateID)
	if delegateID == "" {
		return appwire.DelegateStopResponse{}, appwire.InvalidParams("delegateId is required")
	}
	s.mu.RLock()
	fn := s.delegateStopFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.DelegateStopResponse{}, appwire.Unavailable("delegate stop not available")
	}
	outcome, err := fn(delegateID)
	if errors.Is(err, agent.ErrUnknownDelegate) {
		return appwire.DelegateStopResponse{}, appwire.ResourceNotFound(err.Error())
	}
	if err != nil {
		return appwire.DelegateStopResponse{}, err
	}
	return appwire.DelegateStopResponse{Outcome: outcome}, nil
}

func (s *Server) handleAppJobsStop(ctx context.Context, request appwire.SessionStopRequest) (appwire.SessionStopResponse, error) {
	if err := request.Validate(); err != nil {
		return appwire.SessionStopResponse{}, err
	}
	ref := ""
	if request.Job != nil {
		ref = request.Job.Ref
	} else if request.OwnedDelegate != nil {
		ref = request.OwnedDelegate.Ref
	} else {
		return appwire.SessionStopResponse{}, appwire.InvalidParams("job or owned_delegate mode required")
	}
	if err := s.requireRootMutationTarget(ref, ""); err != nil {
		return appwire.SessionStopResponse{}, err
	}
	s.mu.RLock()
	fn := s.jobStopFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.SessionStopResponse{}, appwire.Unavailable("job stop not available")
	}
	return fn(ctx, request)
}

func (s *Server) handleAppTurnInterrupt(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	unlock, err := s.admitRetrySafeMutation(params.Ref, params.ThreadID, &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	defer unlock()
	var fn func(appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
	if interrupt := s.retrySafeTurnFunctions().Interrupt; interrupt != nil {
		fn = func(p appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) { return interrupt(ctx, p) }
	}
	return callRetrySafe(s, fn, params, params.ClientMutationID, "interrupt not available")
}

func (s *Server) handleAppTurnQueue(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	actor, manager, err := s.inputActor(ctx, params.ManagerProof)
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	params.ManagerProof = nil
	unlock, err := s.admitRetrySafeMutation(params.Ref, "", &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	defer unlock()
	callbacks := s.retrySafeTurnFunctions()
	fn := callbacks.Queue
	if manager {
		as := callbacks.QueueAs
		if as == nil {
			return appwire.TurnQueueResponse{}, appwire.Unavailable("Manager queue not available")
		}
		fn = func(p appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) { return as(p, actor) }
	}
	// Skill selections ride the queued entry and are consumed at its actual
	// claim; the support verdict is this endpoint's own wiring. See the
	// turn/start gate.
	if params.Input, err = gateTurnInput(params.Input, fn != nil, true); err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	return callRetrySafe(s, fn, params, params.ClientMutationID, "queue not available")
}

func (s *Server) handleAppTurnDrainAsSteer(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	actor, manager, err := s.inputActor(ctx, params.ManagerProof)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	params.ManagerProof = nil
	unlock, err := s.admitRetrySafeMutation(params.Ref, "", &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	defer unlock()
	callbacks := s.retrySafeTurnFunctions()
	fn := callbacks.Drain
	if manager {
		as := callbacks.DrainAs
		if as == nil {
			return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("Manager drain not available")
		}
		fn = func(p appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) { return as(p, actor) }
	}
	// Drained entries keep their selections, consumed with the combined
	// steering at its actual delivery; the support verdict is this endpoint's
	// own wiring. See the turn/start gate. The input is optional: drain's job
	// is to pop queued entries, with the composer payload an atomic bonus.
	if params.Input, err = gateTurnInput(params.Input, fn != nil, false); err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	return callRetrySafe(s, fn, params, params.ClientMutationID, "drain-as-steer not available")
}

// handleAppTurnPromoteQueuedAsSteer validates static request shape and leaves
// queue compare-and-commit decisions to the Session callback. No turn in
// flight is required.
func (s *Server) handleAppTurnPromoteQueuedAsSteer(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	actor, manager, err := s.inputActor(ctx, params.ManagerProof)
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	params.ManagerProof = nil
	unlock, err := s.admitRetrySafeMutation(params.Ref, "", &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	defer unlock()
	if err := requireQueueEntryShape(params.Index, params.ExpectedEntryID); err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	callbacks := s.retrySafeTurnFunctions()
	fn := callbacks.Promote
	if manager {
		as := callbacks.PromoteAs
		if as == nil {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Unavailable("Manager promote not available")
		}
		fn = func(p appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return as(p, actor)
		}
	}
	return callRetrySafe(s, fn, params, params.ClientMutationID, "promote-queued-as-steer not available")
}

// handleAppTurnCancelQueued validates static request shape and leaves queue
// compare-and-commit decisions to the Session callback.
func (s *Server) handleAppTurnCancelQueued(_ context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	unlock, err := s.admitRetrySafeMutation(params.Ref, "", &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	defer unlock()
	if err := requireQueueEntryShape(params.Index, params.ExpectedEntryID); err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	return callRetrySafe(s, s.retrySafeTurnFunctions().Cancel, params, params.ClientMutationID, "cancel-queued not available")
}

// handleAppGoalSet handles goal/set. An empty objective clears the goal; both
// set and clear route through the single goalFunc callback (the callback maps an
// empty objective to ClearGoal). Started reports whether the goal loop began
// immediately (idle session) versus after the current turn (a turn is running,
// whose gate is the backstop).
func (s *Server) handleAppGoalSet(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.GoalSetResponse{}, err
	}
	s.mu.RLock()
	fn := s.goalFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.GoalSetResponse{}, appwire.Unavailable("goal not available")
	}
	started, err := fn(params.Objective)
	if err != nil {
		return appwire.GoalSetResponse{}, err
	}
	// The callback emits EventGoalUpdated after its successful store mutation.
	// That typed carrier is committed into the cached snapshot with its
	// notification; pulling here would race it with a second, stale authority.
	return appwire.GoalSetResponse{Started: started}, nil
}

// handleAppNotesHumanSet handles notes/human/set. The callback stores the
// human's session whiteboard and returns the stored (post-clamp) value every
// downstream consumer converges on. Like handleAppGoalSet it never emits
// pushes directly: the callback emits EventNotesUpdated after its successful
// store mutation, and the projector owns that event's push emission.
func (s *Server) handleAppNotesHumanSet(_ context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	// The lock re-checks the root target via requireRootMutationIdentity
	// (which calls requireRootMutationTarget), so the explicit call below is
	// kept for the pre-lock shape errors it reports, not removed as a double
	// check: both reject identically, and the post-lock recheck is what
	// fences a clear that wins the race.
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.NotesHumanSetResponse{}, err
	}
	unlock, err := s.admitRetrySafeMutation(params.Ref, "", &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.NotesHumanSetResponse{}, err
	}
	defer unlock()
	s.mu.RLock()
	fn := s.notesHumanSetFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.NotesHumanSetResponse{}, appwire.Unavailable("notes not available")
	}
	stored, err := fn(params.ClientMutationID, params.Note)
	if err != nil {
		return appwire.NotesHumanSetResponse{}, agent.NormalizeClientMutationError(params.ClientMutationID, err)
	}
	return stored, nil
}

// handleAppUrlsRemove handles urls/remove. The callback removes one URL list
// entry by id; an unknown id is an InvalidParams error, not a silent no-op,
// so a client acting on a stale list learns its entry is gone. Like
// handleAppGoalSet it never emits pushes directly: the callback emits
// EventUrlsUpdated after a successful removal, and the projector owns that
// event's push emission.
func (s *Server) handleAppUrlsRemove(_ context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	// Same double-check contract as handleAppNotesHumanSet: the explicit
	// requireRootMutationTarget stays for pre-lock shape errors while the
	// lock's own requireRootMutationIdentity recheck fences a racing clear.
	if err := s.requireRootMutationTarget(params.Ref, ""); err != nil {
		return appwire.UrlsRemoveResponse{}, err
	}
	unlock, err := s.admitRetrySafeMutation(params.Ref, "", &params.ExpectedInstanceID, &params.ClientMutationID)
	if err != nil {
		return appwire.UrlsRemoveResponse{}, err
	}
	defer unlock()
	// Trim once into a local: padded IDs must match on removal exactly as
	// they validate and report, not diverge across the three uses.
	id := strings.TrimSpace(params.ID)
	if id == "" {
		return appwire.UrlsRemoveResponse{}, appwire.InvalidParams("id is required")
	}
	s.mu.RLock()
	fn := s.urlsRemoveFunc
	s.mu.RUnlock()
	if fn == nil {
		return appwire.UrlsRemoveResponse{}, appwire.Unavailable("urls not available")
	}
	removed, err := fn(params.ClientMutationID, id)
	if err != nil {
		return appwire.UrlsRemoveResponse{}, agent.NormalizeClientMutationError(params.ClientMutationID, err)
	}
	if !removed {
		// Caller-supplied and printed by terminals; %q escapes a control
		// sequence instead of letting the terminal execute it.
		return appwire.UrlsRemoveResponse{}, appwire.InvalidParams(fmt.Sprintf("no URL entry with id %q", id))
	}
	return appwire.UrlsRemoveResponse{}, nil
}

// admitRetrySafeMutation trims the mutation identity in place, then admits one
// retry-safe turn callback only while no thread/clear owns the identity gate.
// It validates before and after taking the read lock so a clear that wins the
// race cannot leave an old-generation callback admitted behind its
// replacement.
func (s *Server) admitRetrySafeMutation(rawRef, threadID string, expectedInstanceID, clientMutationID *string) (func(), error) {
	*clientMutationID = strings.TrimSpace(*clientMutationID)
	*expectedInstanceID = strings.TrimSpace(*expectedInstanceID)
	if err := s.requireRootMutationIdentity(rawRef, threadID, *expectedInstanceID, *clientMutationID); err != nil {
		return nil, err
	}
	if !s.appMutationGate.TryRLock() {
		return nil, appwire.MutationNotAccepted(*clientMutationID, "thread clear is already in progress")
	}
	if err := s.requireRootMutationIdentity(rawRef, threadID, *expectedInstanceID, *clientMutationID); err != nil {
		s.appMutationGate.RUnlock()
		return nil, err
	}
	return s.appMutationGate.RUnlock, nil
}

func (s *Server) retrySafeTurnFunctions() RetrySafeTurnFunctions {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retrySafeTurns
}

// gateTurnInput normalizes a mutation's input and refuses selections the
// endpoint cannot deliver. wired is the endpoint's own wiring: it is the
// support verdict for skill and command selections.
func gateTurnInput(items []appwire.InputItem, wired, contentRequired bool) ([]appwire.InputItem, error) {
	input, err := appwire.NormalizeMutationInput(items)
	if err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if err := appwire.ValidateSkillInputSupport(input.Items, wired); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if err := appwire.ValidateCommandInputSupport(input.Items, wired); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if contentRequired && !input.HasContent() {
		return nil, appwire.InvalidParams("input is required")
	}
	return input.Items, nil
}

func requireQueueEntryShape(index int, expectedEntryID string) error {
	if index < 0 {
		return appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(expectedEntryID) == "" {
		return appwire.InvalidParams("expectedEntryId is required")
	}
	return nil
}

// callRetrySafe dispatches an admitted retry-safe mutation to its callback.
//
// A mutation accepts a durable intent and may emit NOTHING: turn/start, for
// one, does not reflect the durable queue, so no QUEUE_CHANGED announces the
// pending execution it just recorded. The serve loop's later claim does emit
// one, but a client reading between accept and claim would not see its own
// in-flight mutation -- which is the single thing the retry-safe projection
// exists to show a reconnecting client.
//
// Every mutation refreshes, not just the ones that emit. They all commit to the
// same store, and a uniform refresh means nobody has to re-derive which of
// them happens to emit. The sample is cheap: the store publishes its committed
// generation under a narrow mutex it never holds across a write.
func callRetrySafe[P, R any](s *Server, fn func(P) (R, error), params P, clientMutationID, unavailable string) (R, error) {
	if fn == nil {
		var zero R
		return zero, appwire.Unavailable(unavailable)
	}
	response, err := fn(params)
	s.refreshFacets(facetQueue)
	return response, agent.NormalizeClientMutationError(clientMutationID, err)
}
