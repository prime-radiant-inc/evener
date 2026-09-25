package agent

import (
	"errors"
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
	// running is set from the execution's admission to its completion.
	running bool
	// startedAt is when the execution was admitted, for its DurationMS.
	startedAt time.Time
	// failed records a TURN_FAILURE recorded during the execution: its
	// completion then records failed whatever the input loop saw.
	failed bool
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
	s.execution = executionState{running: true, startedAt: s.sclock().Now()}
	// A turn already recorded runs again only when recovery reclaims it: it
	// reopens, and stays open until its next completion.
	reopen := s.recordedExecutions[turnID]
	s.mu.Unlock()
	s.attachedTranscript().BeginExecution(turnID, reopen)
	if reopen {
		_ = s.recordTranscriptOnlyAt(schema.Turn{Kind: schema.TurnReopen}, transcript.PlaceSession)
	}
}

// completeExecution records the running execution's completion entry. status
// is how the input loop saw the run end; a TURN_FAILURE recorded during the
// run makes it failed. With no execution running, or one that recorded
// nothing, there is nothing to complete and nothing is written.
func (s *Session) completeExecution(status schema.TurnCompletionStatus) {
	s.mu.Lock()
	execution := s.execution
	s.execution = executionState{}
	s.roundID = ""
	s.mu.Unlock()
	if !execution.running || s.attachedTranscript() == nil {
		return
	}
	if execution.failed {
		status = schema.TurnFailed
	}
	now := s.sclock().Now().UTC()
	_ = s.recordTranscriptOnlyAt(schema.Turn{
		Kind:       schema.TurnCompletion,
		Timestamp:  now,
		Completion: &schema.TurnCompletionInfo{Status: status, CompletedAt: now, DurationMS: max(now.Sub(execution.startedAt).Milliseconds(), 0)},
	}, transcript.PlaceCompletion)
}

// noteRecordedLocked keeps the session's bookkeeping of what it recorded: the
// write's ordinal (for the pair log), a client-mutation execution's TurnID
// (the only kind that can run again), a TURN_FAILURE that fails the running
// execution, and a USER_INPUT entry's index, which its USER_INPUT event
// reports. Callers hold s.mu.
func (s *Session) noteRecordedLocked(rec transcript.Record) {
	s.lastRecorded = recordedOrdinal{recorded: rec.Recorded, ordinal: rec.Ordinal}
	if !rec.Recorded {
		return
	}
	if rec.Turn.TurnKind == schema.TurnSpanExecution {
		s.noteRecordedExecutionLocked(rec.Turn.TurnID)
	}
	switch rec.Turn.Kind {
	case schema.TurnFailure:
		s.execution.failed = true
	case schema.TurnUserInput:
		s.userInputEntry = int(rec.Ordinal) + 1
	}
}

// noteRecordedExecutionLocked remembers that an execution turn is recorded,
// so that it reopens if it runs again. Only a client-mutation name can run
// again (every other execution runs under a fresh id), so the set stays
// bounded by client turns. Callers hold s.mu.
func (s *Session) noteRecordedExecutionLocked(turnID string) {
	if _, ok := clientMutationStartSequence(turnID); !ok {
		return
	}
	if s.recordedExecutions == nil {
		s.recordedExecutions = map[string]bool{}
	}
	s.recordedExecutions[turnID] = true
}

// recordedUserInputTurn is the 1-based transcript entry index of the latest
// USER_INPUT entry the session recorded, or fallback — the entry's history
// position — for a session with no transcript.
func (s *Session) recordedUserInputTurn(fallback int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userInputEntry > 0 {
		return s.userInputEntry
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
	pending := s.turnsPendingWork()
	s.mu.Lock()
	for turnID, open := range executions {
		s.noteRecordedExecutionLocked(turnID)
		if open && pending[turnID] {
			if s.openPendingExecutions == nil {
				s.openPendingExecutions = map[string]bool{}
			}
			s.openPendingExecutions[turnID] = true
		}
	}
	s.mu.Unlock()
	recorded := false
	for _, turnID := range closeCrashedExecutionTargets(executions, pending) {
		rec, err := s.completeTurn(turnID, schema.TurnInterrupted)
		if err != nil {
			s.pendingTranscriptWarnings = append(s.pendingTranscriptWarnings, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		}
		recorded = recorded || rec.Recorded
	}
	return recorded
}

// completeTurn records a completion of status for turnID, an execution no
// running process owns any more.
func (s *Session) completeTurn(turnID string, status schema.TurnCompletionStatus) (transcript.Record, error) {
	now := s.sclock().Now().UTC()
	turn := schema.Turn{Kind: schema.TurnCompletion, Timestamp: now, Completion: &schema.TurnCompletionInfo{Status: status, CompletedAt: now}}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.recordTranscriptLocked(turn, transcript.DoorDurable, transcript.PlaceInTurn(turnID))
}

// takeOpenPendingExecution reports whether turnID was an open execution that
// restore left open for pending work to finish, and forgets it: only one
// finisher completes it.
func (s *Session) takeOpenPendingExecution(turnID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	open := s.openPendingExecutions[turnID]
	delete(s.openPendingExecutions, turnID)
	return open
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

// recordTranscriptOnlyAt records a transcript-only entry with the given
// placement. It never enters history. A write that fails is reported as a
// warning.
func (s *Session) recordTranscriptOnlyAt(turn schema.Turn, place transcript.Placement) transcript.Record {
	if turn.Timestamp.IsZero() {
		turn.Timestamp = s.sclock().Now().UTC()
	}
	rec, err := func() (transcript.Record, error) {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		return s.recordTranscriptLocked(turn, transcript.DoorBuffered, place)
	}()
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
	s.surfaceTranscriptWarnings()
	return rec
}

// recordNotice records a presentational notice, the history form of a live
// notice a reader would otherwise lose on reload.
func (s *Session) recordNotice(notice schema.NoticeInfo) {
	_ = s.recordTranscriptOnlyAt(schema.Turn{Kind: schema.TurnNotice, Notice: &notice}, transcript.PlaceSession)
}

// deliverCommunicate delivers a communicate message: it is recorded as a
// COMMUNICATE entry first, and announced only once the entry is recorded, so a
// delivered message is never missing from history. A served session whose
// transcript does not record it fails closed and delivers nothing, returning
// the refusal; a session with no transcript, or one nobody serves, announces
// it as it always has.
func (s *Session) deliverCommunicate(data events.CommunicateData) error {
	rec := s.recordTranscriptOnlyAt(schema.Turn{Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: data.CallID, EndTurn: data.EndTurn, Message: data.Message}}, transcript.PlaceSession)
	if !rec.Recorded && s.attachedTranscript() != nil {
		if refusal := s.failClosed(errors.New("a communicate message was not recorded")); refusal != nil {
			s.announceFailClosed()
			return refusal
		}
	}
	s.emit(events.EventCommunicate, data)
	return nil
}

// highestClientMutationTurnSequence is the highest turn_m<N> sequence any of
// entries names, as its TurnID, StableTurnID or OwningTurnID; 0 when none does.
// A transcript holding copied turns (a fork's prefix) names sequences its own
// client-mutation store never reserved, and its next reservation must exceed
// them.
func highestClientMutationTurnSequence(entries []transcript.Entry) uint64 {
	var highest uint64
	for _, entry := range entries {
		turn := entry.Turn
		for _, name := range []string{turn.TurnID, turn.StableTurnID, turn.OwningTurnID} {
			if sequence, ok := clientMutationStartSequence(name); ok {
				highest = max(highest, sequence)
			}
		}
	}
	return highest
}
