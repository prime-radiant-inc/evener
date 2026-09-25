package agent

import (
	"fmt"
	"slices"
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
	// A turn already recorded runs again only when recovery reclaims it: it
	// reopens, and stays open until its next completion.
	reopen := s.recordedExecutions[turnID]
	s.mu.Unlock()
	s.attachedTranscript().BeginExecution(turnID, reopen)
	if !reopen {
		return
	}
	err := func() error {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		_, err := s.recordTranscriptLocked(schema.Turn{Kind: schema.TurnReopen, Timestamp: s.sclock().Now().UTC()}, transcript.DoorBuffered, transcript.PlaceSession)
		return err
	}()
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	s.surfaceTranscriptWarnings()
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
	s.roundID = ""
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
	s.lastRecorded = recordedOrdinal{recorded: rec.Recorded, ordinal: rec.Ordinal}
	if !rec.Recorded {
		return
	}
	if rec.Turn.TurnKind == schema.TurnSpanExecution {
		if s.recordedExecutions == nil {
			s.recordedExecutions = map[string]bool{}
		}
		s.recordedExecutions[rec.Turn.TurnID] = true
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

// roundIDForModelCall is the open model round's id, opening a round with a
// fresh r_ id when none is open. A round covers the requests that can record
// one ASSISTANT entry (a salvage entry included): its attempts, retries and
// fallback groups, up to the first recorded one, which closes it. The end of
// the execution closes it too.
func (s *Session) roundIDForModelCall() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roundID == "" {
		s.roundID = identifier.MustNewRoundID()
	}
	return s.roundID
}

// closeCrashedExecutions records an interrupted completion for every
// execution a crash left open in entries, except the turns pending client
// work will run again: recovery reclaims those, and they reopen. It runs once
// at restore, after the writer is attached, and reports whether it recorded
// anything.
func (s *Session) closeCrashedExecutions(entries []transcript.Entry) bool {
	executions := transcript.ExecutionTurns(entries)
	s.mu.Lock()
	s.recordedExecutions = make(map[string]bool, len(executions))
	for turnID := range executions {
		s.recordedExecutions[turnID] = true
	}
	s.mu.Unlock()
	recorded := false
	for _, turnID := range closeCrashedExecutionTargets(executions, s.turnsPendingWork()) {
		now := s.sclock().Now().UTC()
		turn := schema.Turn{Kind: schema.TurnCompletion, Timestamp: now, Completion: &schema.TurnCompletionInfo{Status: schema.TurnInterrupted, CompletedAt: now}}
		rec, err := func() (transcript.Record, error) {
			s.attentionMu.Lock()
			defer s.attentionMu.Unlock()
			return s.recordTranscriptLocked(turn, transcript.DoorDurable, transcript.PlaceInTurn(turnID))
		}()
		if err != nil {
			s.pendingTranscriptWarnings = append(s.pendingTranscriptWarnings, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		}
		recorded = recorded || rec.Recorded
	}
	return recorded
}

// closeCrashedExecutionTargets is the open executions resume closes: every
// open one but those pending work names, in TurnID order.
func closeCrashedExecutionTargets(executions map[string]bool, pending map[string]bool) []string {
	var targets []string
	for turnID, open := range executions {
		if open && !pending[turnID] {
			targets = append(targets, turnID)
		}
	}
	slices.Sort(targets)
	return targets
}

// turnsPendingWork names the turns the client-mutation store still owes a
// run, which restart recovery reclaims under the same TurnID.
func (s *Session) turnsPendingWork() map[string]bool {
	pending := map[string]bool{}
	if s.clientMutations == nil {
		return pending
	}
	for _, execution := range s.clientMutations.snapshot().PendingExecutions {
		if execution.TurnID != "" {
			pending[execution.TurnID] = true
		}
	}
	return pending
}

// recordedOrdinal is whether a write was recorded, and at which entry ordinal.
type recordedOrdinal struct {
	recorded bool
	ordinal  uint64
}

// recordTranscriptOnly records a transcript-only entry in the running
// execution, or the gap between executions. It never enters history.
func (s *Session) recordTranscriptOnly(turn schema.Turn) (transcript.Record, error) {
	if turn.Timestamp.IsZero() {
		turn.Timestamp = s.sclock().Now().UTC()
	}
	rec, err := func() (transcript.Record, error) {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		return s.recordTranscriptLocked(turn, transcript.DoorBuffered, transcript.PlaceSession)
	}()
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	s.surfaceTranscriptWarnings()
	return rec, err
}

// recordNotice records a presentational notice, the history form of a live
// notice a reader would otherwise lose on reload.
func (s *Session) recordNotice(notice schema.NoticeInfo) {
	_, _ = s.recordTranscriptOnly(schema.Turn{Kind: schema.TurnNotice, Notice: &notice})
}

// deliverCommunicate delivers a communicate message: it is recorded as a
// COMMUNICATE entry first, and announced once the entry is recorded, so a
// delivered message is never missing from history.
func (s *Session) deliverCommunicate(data events.CommunicateData) {
	_, _ = s.recordTranscriptOnly(schema.Turn{Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: data.CallID, EndTurn: data.EndTurn, Message: data.Message}})
	s.emit(events.EventCommunicate, data)
}

// highestClientMutationTurnSequence is the highest turn_m<N> sequence any of
// turns names, as its TurnID, StableTurnID or OwningTurnID; 0 when none does.
// A transcript holding copied turns (a fork's prefix) names sequences its own
// client-mutation store never reserved, and its next reservation must exceed
// them.
func highestClientMutationTurnSequence(turns []schema.Turn) uint64 {
	var highest uint64
	for _, turn := range turns {
		for _, name := range []string{turn.TurnID, turn.StableTurnID, turn.OwningTurnID} {
			if sequence, ok := clientMutationStartSequence(name); ok {
				highest = max(highest, sequence)
			}
		}
	}
	return highest
}
