package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// Compact forces context compaction regardless of current pressure.
// Runs all compaction layers (observation masking, thinking clearing,
// checkpoint, and LLM summarization). Safe to call while idle.
func (s *Session) Compact(ctx context.Context) error {
	// Refused while a question is pending (spec §5.3): summarizing away the
	// transcript tail the pending question lives in would compact out from
	// under the user's reply. Returning before any history read/mutation
	// leaves the history and the pending question untouched — the reply or
	// Clear are the only ways forward (protecting the pending-ask tail through
	// compaction instead of refusing outright is the fast-follow). Keyed on
	// the pending-ask set (askPendingCount), not the awaiting rest state: under
	// attention-status-model v5, SessionAwaiting also covers a plain
	// output-producing rest with nothing pending, where Compact must proceed
	// normally.
	if s.askPendingCount() > 0 {
		return errors.New("a question is pending; reply or clear first")
	}

	if s.contextMgr == nil {
		return errors.New("context manager not initialized")
	}

	s.contextMgr.Meta = s.buildCompactionMeta()

	s.mu.Lock()
	histCopy := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &histCopy)
	s.contextMgr.ForceCompact(ctx, &histCopy, "", emitFn)
	flushCompactionHooks()

	s.mu.Lock()
	s.history = histCopy
	s.mu.Unlock()

	s.maybeAutoSave()
	return nil
}

// noteHandoffPrefix frames the agent's note as a message from its pre-compaction
// self when it is injected into the fresh post-compaction context.
const noteHandoffPrefix = "Here's your note to yourself from before compaction:"

func renderNoteHandoff(note string) string {
	return noteHandoffPrefix + "\n" + note
}

type steeringTurnRecord struct {
	turn schema.Turn
	text string
}

func (s *Session) compactionEmitFunc(ctx context.Context, history *[]schema.Turn) (func(events.EventKind, events.EventData), func()) {
	preCompactRan := false
	var pendingSteering []steeringTurnRecord
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction && !preCompactRan {
			preCompactRan = true
			pendingSteering = append(pendingSteering, s.runPreCompactHook(ctx, history)...)
			s.mu.Lock()
			s.nudgedSinceCompact = false // reset nudge latch on ANY compaction
			s.mu.Unlock()
		}
		s.emit(kind, data)
	}
	flush := func() {
		s.flushSteeringTurnRecords(pendingSteering)
	}
	return emitFn, flush
}

// runPreCompactHook gathers the steering messages re-injected once per
// compaction and appends them to history as TurnSteering turns. The order is
// plugin PreCompact output first, then the active goal objective last: appending
// the objective at the strongest recency position (the trailing steering turn
// that safeCutoff protects) is what lets it survive the same compaction. The
// goal path runs even with no plugins loaded; only the plugin part is guarded by
// a non-nil hookRunner.
func (s *Session) runPreCompactHook(ctx context.Context, history *[]schema.Turn) []steeringTurnRecord {
	if history == nil {
		return nil
	}
	var messages []string
	if s.hookRunner != nil {
		compactResult := s.hookRunner.RunPreCompact(ctx, s.hookInput(plugin.HookPreCompact))
		for _, m := range compactResult.ModelContext {
			messages = append(messages, wrapHookContext(m))
		}
		for _, m := range compactResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
	}
	if note := s.PinnedNote(); note != "" {
		messages = append(messages, renderNoteHandoff(note))
		s.clearPinnedNote() // one-shot handoff: consumed by this compaction, not re-stamped
	}
	messages = append(messages, s.goalCompactionSteering()...)
	return appendSteeringMessagesToHistory(history, messages)
}

func appendSteeringMessagesToHistory(history *[]schema.Turn, messages []string) []steeringTurnRecord {
	var records []steeringTurnRecord
	for _, msg := range messages {
		if strings.TrimSpace(msg) == "" {
			continue
		}
		turn := schema.NewTurn(schema.TurnSteering, llm.User(msg))
		*history = append(*history, turn)
		records = append(records, steeringTurnRecord{turn: turn, text: msg})
	}
	return records
}

func (s *Session) flushSteeringTurnRecords(records []steeringTurnRecord) {
	for _, record := range records {
		if s.transcript != nil {
			if err := s.transcript.Append(record.turn); err != nil {
				s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
			}
		}
		s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: record.text})
	}
}

// buildCompactionMeta gathers session-level metadata for enriching compaction summaries.
func (s *Session) buildCompactionMeta() contextmgr.CompactionMeta {
	meta := contextmgr.CompactionMeta{}

	// Session id — only populated for persistent sessions (stateDir set), where transcript tools are available.
	if s.stateDir != "" {
		meta.SessionID = s.id
	}

	return meta
}
