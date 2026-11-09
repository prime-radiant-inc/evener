package server

import (
	"context"
	"strings"

	"primeradiant.com/evener/appwire"
)

// threadClearAction is what decideThreadClear concludes about a request.
type threadClearAction int

const (
	// threadClearReject refuses the request with the decision's error.
	threadClearReject threadClearAction = iota
	// threadClearReplay answers with a receipt the journal already holds.
	threadClearReplay
	// threadClearRecover finishes a clear whose replacement is installed but
	// whose receipt was never recorded.
	threadClearRecover
	// threadClearProceed runs the clear; its reservation is already journaled.
	threadClearProceed
)

type threadClearDecision struct {
	action      threadClearAction
	err         error
	response    appwire.ThreadClearResponse
	fn          func(context.Context, appwire.ThreadClearParams) error
	reservation threadClearReservation
}

func rejectThreadClear(err error) threadClearDecision {
	return threadClearDecision{action: threadClearReject, err: err}
}

// handleAppThreadClear owns the retry-safe identity transition for clear. The
// callback is deliberately outside s.mu: it provisions and closes sessions and
// may run hooks, while appMutationGate prevents another clear or retry-safe
// turn mutation from competing with it.
func (s *Server) handleAppThreadClear(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	params.Ref = strings.TrimSpace(params.Ref)
	params.ClientMutationID = strings.TrimSpace(params.ClientMutationID)
	params.ExpectedInstanceID = strings.TrimSpace(params.ExpectedInstanceID)
	if params.Ref == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("ref is required")
	}
	if params.ClientMutationID == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	if params.ExpectedInstanceID == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("expectedInstanceId is required")
	}
	parsedRef, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams(err.Error())
	}
	if parsedRef.SourceID != "local" {
		return appwire.ThreadClearResponse{}, appwire.SessionUnavailable("thread is unavailable")
	}
	requestHash, err := threadClearRequestHash(params)
	if err != nil {
		return appwire.ThreadClearResponse{}, appwire.InternalError(err.Error())
	}
	if !s.appMutationGate.TryLock() {
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "a retry-safe mutation is already in progress")
	}
	defer s.appMutationGate.Unlock()

	decision := s.decideThreadClear(params, requestHash)
	switch decision.action {
	case threadClearReplay:
		return decision.response, nil
	case threadClearRecover:
		return s.recoverThreadClear(params)
	case threadClearProceed:
		return s.runThreadClear(ctx, params, decision)
	default:
		return appwire.ThreadClearResponse{}, decision.err
	}
}

// decideThreadClear checks the request against the journal and the live
// identity and, when a clear should run, journals its reservation.
func (s *Server) decideThreadClear(params appwire.ThreadClearParams, requestHash string) threadClearDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clearJournal.loadErr != nil {
		return rejectThreadClear(appwire.MutationUnknown(params.ClientMutationID, "thread clear journal is unavailable"))
	}
	if s.appRef != "" && params.Ref != s.appRef {
		return rejectThreadClear(appwire.MutationNotAccepted(params.ClientMutationID, "thread identity is stale"))
	}
	if record, ok := s.clearJournal.records[params.ClientMutationID]; ok {
		if record.RequestHash != requestHash || record.Ref != params.Ref || record.ExpectedInstanceID != params.ExpectedInstanceID {
			return rejectThreadClear(appwire.InvalidRequest("client mutation ID reused with a different clear request"))
		}
		if record.State == threadClearApplied {
			if record.Response == nil {
				return rejectThreadClear(appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt is incomplete"))
			}
			response := *record.Response
			response.Receipt.Disposition = appwire.MutationDispositionReplayed
			return threadClearDecision{action: threadClearReplay, response: response}
		}
		// A reserved record with a different current instance means the process
		// reached the replacement before it crashed or lost its response. The
		// current stable ref is authoritative, so the reconstructed receipt is
		// recorded and replayed instead of running a second replacement.
		if s.appRef == params.Ref && s.appThreadID != params.ExpectedInstanceID {
			return threadClearDecision{action: threadClearRecover}
		}
	}
	if params.ExpectedInstanceID != s.appThreadID {
		return rejectThreadClear(appwire.MutationNotAccepted(params.ClientMutationID, "thread instance is stale"))
	}
	if reason := s.clearBlockedReasonLocked(); reason != "" {
		return rejectThreadClear(appwire.MutationNotAccepted(params.ClientMutationID, reason))
	}
	if s.clearFunc == nil {
		return rejectThreadClear(appwire.Unavailable("thread/clear is unavailable"))
	}
	reservation, err := s.clearJournal.reserve(threadClearRecord{
		ClientMutationID:   params.ClientMutationID,
		RequestHash:        requestHash,
		Ref:                params.Ref,
		ExpectedInstanceID: params.ExpectedInstanceID,
		State:              threadClearReserved,
	})
	if err != nil {
		return rejectThreadClear(appwire.MutationUnknown(params.ClientMutationID, "thread clear reservation could not be persisted"))
	}
	return threadClearDecision{action: threadClearProceed, fn: s.clearFunc, reservation: reservation}
}

// recoverThreadClear records the receipt of a replacement that is already
// installed and replays it.
func (s *Server) recoverThreadClear(params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	response := s.threadClearResponse(params.ClientMutationID, appwire.MutationDispositionApplied)
	s.mu.Lock()
	defer s.mu.Unlock()
	record, stillReserved := s.clearJournal.records[params.ClientMutationID]
	if !stillReserved || record.State != threadClearReserved || s.appRef != params.Ref || s.appThreadID == params.ExpectedInstanceID {
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread clear state changed while recovering")
	}
	if err := s.clearJournal.markApplied(params.ClientMutationID, &response); err != nil {
		return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt could not be persisted")
	}
	response.Receipt.Disposition = appwire.MutationDispositionReplayed
	return response, nil
}

// runThreadClear invokes the clear callback for a journaled reservation and
// records the receipt of the replacement it installs.
func (s *Server) runThreadClear(ctx context.Context, params appwire.ThreadClearParams, decision threadClearDecision) (appwire.ThreadClearResponse, error) {
	if err := decision.fn(ctx, params); err != nil {
		s.mu.Lock()
		persistErr := s.clearJournal.release(decision.reservation)
		s.mu.Unlock()
		if persistErr != nil {
			return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear outcome could not be persisted")
		}
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, err.Error())
	}

	response := s.threadClearResponse(params.ClientMutationID, appwire.MutationDispositionApplied)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A receipt that cannot be persisted leaves the record reserved with the new
	// identity installed; a retry takes the recovery path and makes it durable
	// before it reports success.
	if err := s.clearJournal.markApplied(params.ClientMutationID, &response); err != nil {
		return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt could not be persisted")
	}
	return response, nil
}

func (s *Server) clearBlockedReasonLocked() string {
	if s.processing || strings.TrimSpace(s.appActiveTurnID) != "" {
		return "thread has unresolved turn work"
	}
	if s.appEnvelope.Queue.Depth > 0 || len(s.appEnvelope.PendingMutations) > 0 {
		return "thread has unresolved queued work"
	}
	if s.appEnvelope.PendingQuestion != nil || len(s.appEnvelope.PendingEscalations) > 0 {
		return "thread has unresolved approval work"
	}
	return ""
}

func (s *Server) threadClearResponse(clientMutationID string, disposition appwire.MutationDisposition) appwire.ThreadClearResponse {
	thread := s.appThread()
	thread.Evener.MutationStateAuthoritative = true
	return appwire.ThreadClearResponse{
		Thread: thread,
		Ref:    thread.Evener.Ref,
		Receipt: appwire.MutationReceipt{
			ClientMutationID: clientMutationID,
			Disposition:      disposition,
			ThreadID:         thread.ID,
			InstanceID:       thread.Evener.InstanceID,
			ProjectionState:  appwire.MutationProjectionReflected,
		},
	}
}
