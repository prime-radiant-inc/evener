package server

import (
	"context"
	"maps"
	"strings"

	"primeradiant.com/evener/appwire"
)

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

	s.mu.Lock()
	if s.clearJournalErr != nil {
		err := appwire.MutationUnknown(params.ClientMutationID, "thread clear journal is unavailable")
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, err
	}
	if s.appRef != "" && params.Ref != s.appRef {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread identity is stale")
	}
	if record, ok := s.clearRecords[params.ClientMutationID]; ok {
		if record.RequestHash != requestHash || record.Ref != params.Ref || record.ExpectedInstanceID != params.ExpectedInstanceID {
			s.mu.Unlock()
			return appwire.ThreadClearResponse{}, appwire.InvalidRequest("client mutation ID reused with a different clear request")
		}
		if record.State == threadClearApplied {
			if record.Response == nil {
				s.mu.Unlock()
				return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt is incomplete")
			}
			response := *record.Response
			response.Receipt.Disposition = appwire.MutationDispositionReplayed
			s.mu.Unlock()
			return response, nil
		}
		// A reserved record with a different current instance means the process
		// reached the replacement before it crashed or lost its response. The
		// current stable ref is authoritative, so persist the reconstructed
		// receipt and replay it instead of running a second replacement.
		currentRef := s.appRef
		currentInstanceID := s.appThreadID
		if currentRef == params.Ref && currentInstanceID != params.ExpectedInstanceID {
			s.mu.Unlock()
			response := s.threadClearResponse(params.ClientMutationID, appwire.MutationDispositionApplied)
			s.mu.Lock()
			currentRecord, stillReserved := s.clearRecords[params.ClientMutationID]
			if !stillReserved || currentRecord.State != threadClearReserved || s.appRef != params.Ref || s.appThreadID == params.ExpectedInstanceID {
				s.mu.Unlock()
				return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread clear state changed while recovering")
			}
			record = currentRecord
			record.State = threadClearApplied
			record.Response = &response
			s.clearRecords[params.ClientMutationID] = record
			if err := persistThreadClearJournal(s.clearJournalPath, s.clearRecords); err != nil {
				s.mu.Unlock()
				return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt could not be persisted")
			}
			response.Receipt.Disposition = appwire.MutationDispositionReplayed
			s.mu.Unlock()
			return response, nil
		}
	}

	if params.ExpectedInstanceID != s.appThreadID {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, "thread instance is stale")
	}
	if reason := s.clearBlockedReasonLocked(); reason != "" {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, reason)
	}
	fn := s.clearFunc
	if fn == nil {
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.Unavailable("thread/clear is unavailable")
	}
	record := threadClearRecord{
		ClientMutationID:   params.ClientMutationID,
		RequestHash:        requestHash,
		Ref:                params.Ref,
		ExpectedInstanceID: params.ExpectedInstanceID,
		State:              threadClearReserved,
	}
	// A new reservation supersedes every older record for the same stable ref:
	// the instance those records expected (or installed) is being replaced, so
	// none of them can be a live client's retry anymore. This bounds the
	// journal at one record per ref instead of one per clear, forever. The two
	// snapshots make each rollback below a wholesale restore; only this
	// handler mutates clearRecords, and appMutationGate serializes it.
	beforeReservation := maps.Clone(s.clearRecords)
	for id, existing := range s.clearRecords {
		if existing.Ref == params.Ref {
			delete(s.clearRecords, id)
		}
	}
	s.clearRecords[params.ClientMutationID] = record
	if err := persistThreadClearJournal(s.clearJournalPath, s.clearRecords); err != nil {
		s.clearRecords = beforeReservation
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear reservation could not be persisted")
	}
	reserved := maps.Clone(s.clearRecords)
	s.mu.Unlock()

	if err := fn(ctx, params); err != nil {
		s.mu.Lock()
		// The failed clear installed nothing, so the records its reservation
		// superseded describe the still-current instance: restoring the
		// pre-reservation journal keeps an older receipt's replay alive after
		// a failed newer clear.
		s.clearRecords = beforeReservation
		persistErr := persistThreadClearJournal(s.clearJournalPath, s.clearRecords)
		if persistErr != nil {
			// Roll memory back to the journal the reservation persisted. Disk
			// may already hold the rollback if only the post-rename directory
			// sync failed, but either state is safe: every persist rewrites
			// the whole file from memory, so the next successful one resyncs
			// disk, and a retry that re-invokes the callback re-runs work that
			// failed without installing anything.
			s.clearRecords = reserved
		}
		s.mu.Unlock()
		if persistErr != nil {
			return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear outcome could not be persisted")
		}
		return appwire.ThreadClearResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, err.Error())
	}

	response := s.threadClearResponse(params.ClientMutationID, appwire.MutationDispositionApplied)
	s.mu.Lock()
	record = s.clearRecords[params.ClientMutationID]
	record.State = threadClearApplied
	record.Response = &response
	s.clearRecords[params.ClientMutationID] = record
	if err := persistThreadClearJournal(s.clearJournalPath, s.clearRecords); err != nil {
		// Keep the reserved record and the new identity. A retry will take the
		// crash-recovery branch above and make the receipt durable before it
		// reports success.
		s.mu.Unlock()
		return appwire.ThreadClearResponse{}, appwire.MutationUnknown(params.ClientMutationID, "thread clear receipt could not be persisted")
	}
	s.mu.Unlock()
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
