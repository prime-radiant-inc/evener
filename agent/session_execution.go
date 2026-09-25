package agent

import (
	"fmt"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
)

// An execution is one real run of the model for one input: from its
// admission in processOneInput to its completion entry, which the input loop
// records once the run's outcome — completed, failed or interrupted — is
// settled. The transcript's append registry holds the running execution's
// TurnID, so every entry the session records meanwhile, and any attention
// delivered from another goroutine, joins it; the completion entry is the
// last entry of the span and ends it.

// executionState is the running execution's bookkeeping on the session,
// guarded by s.mu.
type executionState struct {
	// startedAt is when the execution was admitted, for its DurationMS.
	startedAt time.Time
	// failed records a TURN_FAILURE recorded during the execution: its
	// completion then records failed whatever the input loop saw.
	failed bool
	// userInputEntry is the 1-based entry index of the latest USER_INPUT
	// entry the session recorded, 0 before one is.
	userInputEntry int
}

// executionTurnID is the TurnID an execution runs under: the client-mutation
// name it was admitted with (turn_m<N>), or a fresh t_ id — for a turn with
// no name, and for a legacy-spelled recovered name (turn_<n>), which runs
// again under a fresh id while the turn it names stays as it was recorded.
func executionTurnID(name string) string {
	if _, ok := clientMutationStartSequence(name); ok {
		return name
	}
	return identifier.MustNewTurnID()
}

// beginExecution admits an execution named name (see executionTurnID) on the
// session's transcript, before any of its entries is recorded.
func (s *Session) beginExecution(name string) {
	turnID := executionTurnID(name)
	s.mu.Lock()
	s.execution = executionState{startedAt: s.sclock().Now(), userInputEntry: s.execution.userInputEntry}
	s.mu.Unlock()
	s.attachedTranscript().BeginExecution(turnID, false)
}

// completeExecution records the running execution's completion entry. status
// is how the input loop saw the run end; a TURN_FAILURE recorded during the
// run makes it failed. With no execution running, or one that recorded
// nothing, there is nothing to complete and nothing is written.
func (s *Session) completeExecution(status schema.TurnCompletionStatus) {
	s.mu.Lock()
	if s.execution.failed {
		status = schema.TurnFailed
	}
	startedAt := s.execution.startedAt
	s.execution = executionState{userInputEntry: s.execution.userInputEntry}
	s.mu.Unlock()
	now := s.sclock().Now().UTC()
	turn := schema.Turn{
		Kind:       schema.TurnCompletion,
		Timestamp:  now,
		Completion: &schema.TurnCompletionInfo{Status: status, CompletedAt: now, DurationMS: max(now.Sub(startedAt).Milliseconds(), 0)},
	}
	err := func() error {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		if s.attachedTranscript() == nil {
			return nil // no transcript, or none attached yet: no execution to end
		}
		_, err := s.recordTranscriptLocked(turn, transcript.DoorBuffered, transcript.PlaceCompletion)
		return err
	}()
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	s.surfaceTranscriptWarnings()
}

// noteRecordedLocked keeps the execution's bookkeeping of what the session
// recorded: a TURN_FAILURE marks the running execution failed, and a
// USER_INPUT entry's index is what its USER_INPUT event reports. Callers
// hold s.mu.
func (s *Session) noteRecordedLocked(rec transcript.Record) {
	if !rec.Recorded {
		return
	}
	switch rec.Turn.Kind {
	case schema.TurnFailure:
		s.execution.failed = true
	case schema.TurnUserInput:
		s.execution.userInputEntry = int(rec.Ordinal) + 1
	}
}

// recordedUserInputTurn is the 1-based transcript entry index of the latest
// USER_INPUT entry the session recorded, or fallback — the entry's history
// position — for a session with no transcript.
func (s *Session) recordedUserInputTurn(fallback int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execution.userInputEntry > 0 {
		return s.execution.userInputEntry
	}
	return fallback
}
