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
	// turnID is the TurnID the execution runs under.
	turnID string
	// announced records that EventExecutionStarted announced the execution,
	// so its end is announced too.
	announced bool
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
// session's transcript, before any of its entries is recorded. The
// execution-started func and EventExecutionStarted learn its TurnID first.
func (s *Session) beginExecution(name string) {
	turnID := executionTurnID(name)
	s.mu.Lock()
	announced := s.sessionStarted
	s.execution = executionState{running: true, turnID: turnID, announced: announced, startedAt: s.sclock().Now()}
	// A turn already recorded runs again only when recovery reclaims it: it
	// reopens, and stays open until its next completion.
	reopen := s.recordedExecutions[turnID]
	started := s.executionStarted
	s.mu.Unlock()
	if announced {
		if started != nil {
			started(turnID)
		}
		s.emit(events.EventExecutionStarted, events.ExecutionStartedData{TurnID: turnID})
	}
	s.attachedTranscript().BeginExecution(turnID, reopen)
	if reopen {
		_ = s.recordTranscriptOnlyAt(schema.Turn{Kind: schema.TurnReopen}, transcript.PlaceSession)
	}
}

// completeExecution records the running execution's completion entry. status
// is how the input loop saw the run end; a TURN_FAILURE recorded during the
// run makes it failed. With no execution running, or one that recorded
// nothing, there is nothing to complete and nothing is written. It announces
// the end of the execution's last round, then, after the completion entry,
// the end of the execution with the status recorded (status when nothing was).
func (s *Session) completeExecution(status schema.TurnCompletionStatus) {
	s.mu.Lock()
	execution := s.execution
	s.execution = executionState{}
	s.roundID = ""
	s.mu.Unlock()
	s.endLastRound()
	if !execution.running {
		return
	}
	ended := status
	if s.attachedTranscript() != nil {
		if execution.failed {
			status = schema.TurnFailed
		}
		now := s.sclock().Now().UTC()
		rec := s.recordTranscriptOnlyAt(schema.Turn{
			Kind:       schema.TurnCompletion,
			Timestamp:  now,
			Completion: &schema.TurnCompletionInfo{Status: status, CompletedAt: now, DurationMS: max(now.Sub(execution.startedAt).Milliseconds(), 0)},
		}, transcript.PlaceCompletion)
		if rec.Recorded {
			ended = status
		}
	}
	if execution.announced {
		s.emit(events.EventExecutionEnded, events.ExecutionEndedData{TurnID: execution.turnID, Status: string(ended)})
	}
}

// noteRecordedLocked keeps the session's bookkeeping of what it recorded: the
// write's ordinal (for the pair log), a client-mutation execution's TurnID
// (the only kind that can run again), a TURN_FAILURE that fails the running
// execution, and a USER_INPUT entry's index, which its USER_INPUT event
// reports. Callers hold s.mu.
func (s *Session) noteRecordedLocked(rec transcript.Record) {
	s.lastRecorded = recordedOrdinal{recorded: rec.Recorded, ordinal: rec.Ordinal, model: rec.Turn.Model}
	if !rec.Recorded || rec.Turn.OriginalOrdinal != nil {
		// A fold's copy of an entry recorded earlier says nothing about the
		// execution running when the copy is written.
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
//
// Opening a round announces it, after announcing the end of the round before
// it: a closed round has ended only once the next one opens, because its
// tools and hooks run between the two.
func (s *Session) roundIDForModelCall() string {
	s.mu.Lock()
	if s.roundID != "" {
		defer s.mu.Unlock()
		return s.roundID
	}
	previous := s.lastRoundID
	if s.lastRoundEnded {
		previous = ""
	}
	s.roundID = identifier.MustNewRoundID()
	s.lastRoundID, s.lastRoundEnded = s.roundID, false
	roundID := s.roundID
	s.mu.Unlock()
	if previous != "" {
		s.emit(events.EventRoundEnded, events.RoundEndedData{RoundID: previous})
	}
	s.emit(events.EventRoundStarted, events.RoundStartedData{RoundID: roundID})
	return roundID
}

// endLastRound announces the end of the latest round the session opened,
// unless it is already announced.
func (s *Session) endLastRound() {
	s.mu.Lock()
	roundID := s.lastRoundID
	ended := s.lastRoundEnded
	s.lastRoundEnded = true
	s.mu.Unlock()
	if roundID != "" && !ended {
		s.emit(events.EventRoundEnded, events.RoundEndedData{RoundID: roundID})
	}
}

// SetExecutionStartedFunc installs a callback run in beginExecution before the
// execution's first entry is recorded, with the execution's TurnID. serve wires
// it to Server.SetProcessingTurn. Install it before the session runs input.
// It is called exactly for the executions EventExecutionStarted announces:
// not for one restore runs before SESSION_START.
func (s *Session) SetExecutionStartedFunc(fn func(turnID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionStarted = fn
}

// SetTranscriptRecordedFunc installs fn as the transcript's recorded-entry hook
// (transcript.Writer.OnRecorded) now and on every writer the session attaches
// or reopens. fn runs under the append lock: see OnRecorded. Appenders hold
// the session's locks around it, so fn must not call back into the session
// (TranscriptRecordedLength included) or take any lock an appender or the
// session holds; it may take leaf locks only.
func (s *Session) SetTranscriptRecordedFunc(fn func(transcript.Record)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcriptRecorded = fn
	s.transcript.OnRecorded(fn)
}

// setTranscriptLocked makes w the session's writer, installing the session's
// recorded-entry hook on it. The hook belongs to the file's shared append
// tail, and today's reopens (attention recovery) open the new writer while
// the old one still holds that tail, so it already carries the hook; the
// install matters for the first attach and for any reopen made after every
// writer on the file closed, which starts a fresh tail. Callers hold s.mu.
func (s *Session) setTranscriptLocked(w *transcript.Writer) {
	s.transcript = w
	if s.transcriptRecorded != nil {
		w.OnRecorded(s.transcriptRecorded)
	}
}

// TranscriptRecordedLength is the recorded length of the session's transcript,
// 0 when it has none.
func (s *Session) TranscriptRecordedLength() int64 {
	return s.attachedTranscript().RecordedLength()
}

// SetDescendantRecordedFunc installs fn on every descendant session this
// session spawns, now and later (inherited like the descendant event func):
// fn(sessionID, record) under that child's append lock. The constraints of
// SetTranscriptRecordedFunc apply: fn must not call back into any session of
// the tree or take a lock an appender or a session holds.
func (s *Session) SetDescendantRecordedFunc(fn func(sessionID string, rec transcript.Record)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.spawn.descendantRecorded = fn
}

// descendantRecordedHook is a descendant's own recorded-entry hook: the
// inherited descendant func, told the descendant's session id. Nil when no
// func is inherited.
func descendantRecordedHook(fn func(sessionID string, rec transcript.Record), sessionID string) func(transcript.Record) {
	if fn == nil {
		return nil
	}
	return func(rec transcript.Record) { fn(sessionID, rec) }
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

// closeAbandonedExecutions closes, interrupted, every execution restore left
// open for pending work that recovery then retired without running it — a
// turn an accepted Stop finalized. It runs after restore's recovery passes and
// reports whether it recorded anything.
func (s *Session) closeAbandonedExecutions() bool {
	pending := s.turnsPendingWork()
	s.mu.Lock()
	var abandoned []string
	for turnID := range s.openPendingExecutions {
		if !pending[turnID] {
			abandoned = append(abandoned, turnID)
			delete(s.openPendingExecutions, turnID)
		}
	}
	s.mu.Unlock()
	slices.Sort(abandoned)
	recorded := false
	for _, turnID := range abandoned {
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

// recordedOrdinal is whether a write was recorded, at which entry ordinal,
// and with which model.
type recordedOrdinal struct {
	recorded bool
	ordinal  uint64
	model    string
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
	// A closed writer is a session shutting down, not a writer failure.
	if writer := s.attachedTranscript(); !rec.Recorded && writer != nil && !writer.Closed() {
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
