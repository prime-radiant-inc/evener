package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	// Attribute the summarizer's LLM side calls to this session in the
	// per-session API log (the per-attempt context only covers turn model calls).
	ctx = llm.WithAPILogContext(ctx, s.id)
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

	compactionCtx, emitFn, flushCompactionHooks := s.compactionEmitFunc(ctx, &histCopy)
	s.contextMgr.ForceCompact(compactionCtx, &histCopy, "", emitFn)
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
	kind string
}

// preCompactMessage pairs one pre-compact steering message with the
// events.SteeringKind* naming which of runPreCompactHook's three sources
// (plugin PreCompact output, the pinned-note handoff, the goal objective)
// produced it. Without this, every message merged into one batch reads as
// whatever kind the batch's caller hardcodes — a goal objective labeled as a
// plugin hook, for instance — regardless of which source actually built it.
type preCompactMessage struct {
	text string
	kind string
}

func (s *Session) steerCompactionTranscriptReminder() {
	if s.stateDir == "" || s.id == "" {
		return
	}
	ref := encodeRef("", s.id)
	s.SteerKind("<SYSTEM-REMINDER>If you need the exact transcript of this session before compaction, use the transcript tool instead of reading raw transcript files directly. Default read: read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"markdown\"}). For long sessions, first get a turn map with read_transcript({\"transcript_ref\": \""+ref+"\", \"format\": \"outline\"}), then read a focused range with read_transcript({\"transcript_ref\": \""+ref+"\", \"range\": \"A-B\"}).</SYSTEM-REMINDER>", events.SteeringKindTranscriptPointer)
}

func (s *Session) compactionEmitFunc(ctx context.Context, history *[]schema.Turn) (context.Context, func(events.EventKind, events.EventData), func()) {
	preCompactRan := false
	artifactProduced := false
	var existingArtifacts []schema.Turn
	if history != nil {
		for _, turn := range *history {
			if isSessionNameCompactionTurn(turn) {
				existingArtifacts = append(existingArtifacts, turn)
			}
		}
	}
	var pendingSteering []steeringTurnRecord
	ctx = contextmgr.WithCompactionTurnCallback(ctx, func(turn schema.Turn) {
		s.handleCompactionTurn(turn)
		if isSessionNameCompactionTurn(turn) && !consumeMatchingCompactionArtifact(&existingArtifacts, turn) {
			artifactProduced = true
		}
	})
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if !preCompactRan {
				preCompactRan = true
				pendingSteering = append(pendingSteering, s.runPreCompactHook(ctx, history)...)
				s.mu.Lock()
				s.nudgedSinceCompact = false // reset nudge latch on ANY compaction
				s.mu.Unlock()
			}
		}
		s.emit(kind, data)
	}
	flush := func() {
		s.flushSteeringTurnRecords(pendingSteering)
		if artifactProduced {
			s.steerCompactionTranscriptReminder()
		}
	}
	return ctx, emitFn, flush
}

func consumeMatchingCompactionArtifact(existing *[]schema.Turn, turn schema.Turn) bool {
	for i, candidate := range *existing {
		if reflect.DeepEqual(candidate, turn) {
			*existing = append((*existing)[:i], (*existing)[i+1:]...)
			return true
		}
	}
	return false
}

// runPreCompactHook gathers the steering messages re-injected once per
// compaction and appends them to history as TurnSteering turns. The order is
// plugin PreCompact output first, then the active goal objective last: appending
// the objective at the strongest recency position (the trailing steering turn
// that safeCutoff protects) is what lets it survive the same compaction. The
// goal path runs even with no plugins loaded; only the plugin part is guarded by
// a non-nil hookRunner. The three sources are genuinely different things, so
// each keeps its own events.SteeringKind* (precompact-hook / note-handoff /
// goal-objective) rather than being merged under one label.
func (s *Session) runPreCompactHook(ctx context.Context, history *[]schema.Turn) []steeringTurnRecord {
	if history == nil {
		return nil
	}
	var messages []preCompactMessage
	if s.hookRunner != nil {
		compactResult := s.hookRunner.RunPreCompact(s.apiLogContext(ctx), s.hookInput(plugin.HookPreCompact))
		for _, m := range compactResult.ModelContext {
			messages = append(messages, preCompactMessage{text: wrapHookContext(m), kind: events.SteeringKindPrecompactHook})
		}
		for _, m := range compactResult.UserMessages {
			s.deliverHookUserMessage(m)
		}
	}
	if note := s.PinnedNote(); note != "" {
		messages = append(messages, preCompactMessage{text: renderNoteHandoff(note), kind: events.SteeringKindNoteHandoff})
		s.clearPinnedNote() // one-shot handoff: consumed by this compaction, not re-stamped
	}
	for _, m := range s.goalCompactionSteering() {
		messages = append(messages, preCompactMessage{text: m, kind: events.SteeringKindGoalObjective})
	}
	return appendSteeringMessagesToHistory(history, messages)
}

func appendSteeringMessagesToHistory(history *[]schema.Turn, messages []preCompactMessage) []steeringTurnRecord {
	var records []steeringTurnRecord
	for _, msg := range messages {
		if strings.TrimSpace(msg.text) == "" {
			continue
		}
		turn := schema.NewTurn(schema.TurnSteering, llm.User(msg.text))
		turn.SteeringKind = msg.kind
		*history = append(*history, turn)
		records = append(records, steeringTurnRecord{turn: turn, text: msg.text, kind: msg.kind})
	}
	return records
}

func (s *Session) flushSteeringTurnRecords(records []steeringTurnRecord) {
	for _, record := range records {
		appendTurn := s.cfg.testOnly.appendCompactionTurn
		if appendTurn == nil {
			appendTurn = s.writeTranscript
		}
		if err := appendTurn(record.turn); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		}
		s.emit(events.EventSteeringInjected, events.SteeringInjectedData{Text: record.text, Kind: record.kind})
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
