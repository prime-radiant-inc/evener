package agent

import (
	"slices"
	"sync/atomic"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// ObserveExecutionFrontierWake is the observation seam for the recorded
// execution announcement (#6816): it runs on the recording goroutine at the
// frontier wake, after the announced entry became readable and before the
// emitter can emit it, with the newest announced turn. Tests store it to prove
// a subscriber's history read already returns that turn; nil in production. An
// atomic pointer keeps the read race-free against a test storing or clearing
// it, the same convention as observeDelegateStopWait.
var ObserveExecutionFrontierWake atomic.Pointer[func(sessionID, turnID string)]

// LatestExecutionTurnID is the newest recorded execution, including an entry
// whose durability is still owed. Completion never clears the recorded frontier.
func (s *Session) LatestExecutionTurnID() string {
	return s.LatestExecutionFrontier().TurnID
}

// LatestExecutionFrontier samples the turn and its sequence under the same leaf.
// Only descendant installation consumes the sequence. Like envelope sampling,
// this takes Session.mu then the leaf, never an append or attention lock.
func (s *Session) LatestExecutionFrontier() events.ExecutionFrontier {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionFrontierMu.Lock()
	defer s.executionFrontierMu.Unlock()
	return s.executionFrontier
}

// seedExecutionFrontier consumes the constructor's validated view. A committed
// stamp always wins, including a queued stamp whose event has not been emitted.
// Reopens only reinstall the dispatcher and never seed again.
func (s *Session) seedExecutionFrontier(entries []transcript.Entry) {
	s.executionFrontierMu.Lock()
	defer s.executionFrontierMu.Unlock()
	if s.executionFrontier.TurnID != "" || len(s.frontierPending) != 0 {
		return
	}
	for _, entry := range slices.Backward(entries) {
		turn := entry.Turn
		if turn.OriginalOrdinal == nil && turn.TurnKind == schema.TurnSpanExecution {
			s.executionFrontier = events.ExecutionFrontier{TurnID: turn.TurnID, Sequence: s.executionFrontier.Sequence + 1}
			return
		}
	}
}

// installTranscriptRecordedLocked preserves the internal listener across every
// external callback replacement and writer attach. Callers hold s.mu.
func (s *Session) installTranscriptRecordedLocked() {
	if s.transcript == nil {
		return
	}
	s.executionFrontierMu.Lock()
	if s.frontierWake == nil && !s.closing && (s.events != nil || s.descendantEvent != nil) {
		s.frontierWake = make(chan struct{}, 1)
		s.frontierEmitterWG.Add(1)
		go s.emitRecordedExecutions()
	}
	wake := s.frontierWake
	s.executionFrontierMu.Unlock()
	external := s.transcriptRecorded
	s.transcript.OnRecorded(func(rec transcript.Record) {
		execution := rec.Recorded && rec.Turn.OriginalOrdinal == nil && rec.Turn.TurnKind == schema.TurnSpanExecution
		announce := false
		if execution {
			// Once the history has paired, a record that carries conversation
			// provenance or a frontier delta is fenced behind the producer's
			// publication handoff, which advances the readable boundary only
			// when the complete snapshot arrives. Such a record is announced
			// from that handoff (publishTranscriptLocked), never from here.
			deferred := rec.Turn.ConversationFrontier != nil || rec.Turn.ConversationProvenance != nil
			s.executionFrontierMu.Lock()
			s.executionFrontier = events.ExecutionFrontier{TurnID: rec.Turn.TurnID, Sequence: s.executionFrontier.Sequence + 1}
			if !s.frontierStopped && wake != nil {
				s.frontierPending = append(s.frontierPending, rec.Turn.TurnID)
				announce = !deferred
			}
			s.executionFrontierMu.Unlock()
		}
		if external != nil {
			external(rec)
		}
		// external staged the record; a record the history can already read may
		// be announced now. Deferred records wait for their publication.
		if announce {
			s.announceFrontier(wake, rec.Turn.TurnID)
		}
	})
}

// announceFrontier reports the wake to the test seam and signals the
// recorded-execution emitter. The caller passes the turn the wake covers and
// the wake channel it sampled while appending to frontierPending.
func (s *Session) announceFrontier(wake chan struct{}, turnID string) {
	if observe := ObserveExecutionFrontierWake.Load(); observe != nil {
		(*observe)(s.id, turnID)
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// wakeFrontierPending wakes the recorded-execution emitter once the publication
// handoff has made the newest pending execution readable. For a deferred record
// this is the only announcement; for one the hook already announced it is a
// redundant re-wake that the emitter's pending drain absorbs (#6816).
func (s *Session) wakeFrontierPending() {
	s.executionFrontierMu.Lock()
	pending, stopped, wake := s.frontierPending, s.frontierStopped, s.frontierWake
	s.executionFrontierMu.Unlock()
	if stopped || len(pending) == 0 || wake == nil {
		return
	}
	s.announceFrontier(wake, pending[len(pending)-1])
}

// emitRecordedExecutions takes no append lock and releases the leaf before
// emit, which acquires Session.mu. Close joins the lifetime worker at the
// event-stream boundary under the ordinary shared close budget.
func (s *Session) emitRecordedExecutions() {
	defer s.frontierEmitterWG.Done()
	done := s.sessionContext().Done()
	for {
		select {
		case <-s.frontierWake:
		case <-done:
			s.stopFrontierEmitter()
		}
		s.executionFrontierMu.Lock()
		if s.frontierStartupPending {
			stopped := s.frontierStopped
			s.executionFrontierMu.Unlock()
			if stopped {
				return
			}
			continue
		}
		pending := s.frontierPending
		s.frontierPending = nil
		stopped := s.frontierStopped
		s.executionFrontierMu.Unlock()
		for _, id := range pending {
			s.emit(events.ExecutionFirstRecordedData{TurnID: id})
		}
		if stopped {
			return
		}
	}
}

// publishFrontierStartup releases construction-time commits only after the
// session introduction entered the stream. Re-waking preserves commits whose
// earlier wake the worker already consumed while publication was held.
func (s *Session) publishFrontierStartup(published bool) {
	s.executionFrontierMu.Lock()
	defer s.executionFrontierMu.Unlock()
	if !s.frontierStartupPending || s.frontierStopped {
		return
	}
	if !published || s.sessionContext().Err() != nil {
		s.frontierStopped = true
		s.frontierPending = nil
	} else {
		s.frontierStartupPending = false
	}
	if s.frontierWake != nil {
		select {
		case s.frontierWake <- struct{}{}:
		default:
		}
	}
}

// Failed constructors own the pre-start worker: none of its held signals may
// escape, and its lifetime ends before the constructor releases its binding.
func (s *Session) abortFrontierStartup() {
	s.stopFrontierEmitter()
	s.cancelFunc()
	s.frontierEmitterWG.Wait()
}

func (s *Session) stopFrontierEmitter() {
	s.executionFrontierMu.Lock()
	defer s.executionFrontierMu.Unlock()
	s.frontierStopped = true
	if s.frontierStartupPending {
		s.frontierPending = nil
	}
	if s.frontierWake != nil {
		select {
		case s.frontierWake <- struct{}{}:
		default:
		}
	}
}
