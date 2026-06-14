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
	s.nudgedSinceCompact = false // reset nudge latch on any compaction
	s.mu.Unlock()

	s.maybeAutoSave()
	return nil
}

const (
	pinnedNoteOpen  = "[NOTE TO SELF]"
	pinnedNoteClose = "[END NOTE TO SELF]"
)

func renderPinnedNote(note string) string {
	return pinnedNoteOpen + "\n" + note + "\n" + pinnedNoteClose
}

// stripPinnedNoteTurns removes any existing pinned-note steering turn so a fresh
// copy can be re-stamped without accumulation.
func stripPinnedNoteTurns(history *[]schema.Turn) {
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), pinnedNoteOpen) {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered
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
		stripPinnedNoteTurns(history)
		messages = append(messages, renderPinnedNote(note))
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
