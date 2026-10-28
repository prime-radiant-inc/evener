package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// SessionLogStrategy combines compact layers 1+2 (observation masking and
// thinking clearing) with a session-log-based checkpoint (replacing the
// deterministic layer 3), LLM summarization fallback (layer 4), and forked
// summarization in AfterAction.
type SessionLogStrategy struct {
	cm      *Manager
	session Host
	log     *sessionlog.SessionLog
}

// NewSessionLogStrategy creates a SessionLogStrategy backed by the given
// Manager and host. The session log is persisted alongside the
// session snapshot.
func NewSessionLogStrategy(cm *Manager, host Host) (*SessionLogStrategy, error) {
	logPath := schema.SessionLogPath(host.StateDir(), host.ID())
	log, err := sessionlog.NewSessionLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("session log strategy: %w", err)
	}
	return &SessionLogStrategy{
		cm:      cm,
		session: host,
		log:     log,
	}, nil
}

// Name returns the strategy's identifier, "session-log".
func (s *SessionLogStrategy) Name() string { return "session-log" }

// Tools returns no additional tool definitions for this strategy.
func (s *SessionLogStrategy) Tools() []tool.RegisteredTool { return nil }

// ManageContext applies compaction layers selectively:
//   - Layer 1: observation masking
//   - Layer 2: thinking clearing
//   - Layer 3 (replaced): session-log checkpoint instead of deterministic checkpoint
//   - Layer 4: LLM summarization fallback
func (s *SessionLogStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	if s.cm == nil {
		return nil
	}
	// One profile snapshot covers the window check and every diagnostic below:
	// a model switch landing mid-compaction would otherwise bill one layer's
	// numbers by another model's thinking rule and image family.
	prof, _, _ := s.cm.profileSnapshot()
	cw, _ := effectiveWindowOf(prof)
	if cw <= 0 {
		return nil
	}
	discloseAssumedWindow(s.cm, prof, emitFn)

	// Each phase reads pressure and its before/after diagnostics from ONE
	// snapshot (see pressureFromSnapshot), so a concurrent SetProfile cannot
	// decide a layer by one model and describe it by another.
	estimatePressure := func() (float64, *provider.Profile) { return s.cm.pressureWithProfile(history, sysPromptChars) }

	p, prof := estimatePressure()
	// Captured before any layer invalidates the measurement: the tripwire
	// (issue #4564) forces the checkpoint layer below whatever the pressure
	// estimate says. It carries the profile it was captured under and governs
	// only while that profile is still the gate's assumption
	// (assumptionStillGoverns).
	tripwire := s.cm.unknownWindowTripwireFires(prof)
	tripwireProf := prof
	compacted := false

	// Invalidate API token measurement before running any layer so that
	// between-layer pressure() calls use char/4 (reflects in-place mutations).
	if p >= s.cm.ObservationMaskThreshold {
		s.cm.resetMeasurement()
	}

	// Layer 1: Observation masking.
	if p >= s.cm.ObservationMaskThreshold {
		s.cm.applyLayer(ctx, emitFn, history, prof, "observation_mask", "", func(h []schema.Turn) ([]schema.Turn, error) {
			maskObservations(h, s.cm.PreserveRecentTurns, s.cm.resultToolName())
			return h, nil
		})
		compacted = true
		p, prof = estimatePressure()
	}

	// Layer 2: Thinking clearing.
	if p >= s.cm.ThinkingClearThreshold {
		s.cm.applyLayer(ctx, emitFn, history, prof, "thinking_clear", "", func(h []schema.Turn) ([]schema.Turn, error) {
			clearThinking(h, s.cm.PreserveRecentTurns)
			return h, nil
		})
		compacted = true
		p, prof = estimatePressure()
	}

	// Layer 3 (replaced): Session-log checkpoint. checkpointed is what this
	// pass's checkpoint folded, which a summary completing the same fold reads
	// the partner's messages from.
	var checkpointed []schema.Turn
	if p >= s.cm.CheckpointThreshold || (tripwire && assumptionStillGoverns(tripwireProf, prof)) {
		s.cm.applyLayer(ctx, emitFn, history, prof, "session_log_checkpoint", schema.TurnCheckpoint, func(h []schema.Turn) ([]schema.Turn, error) {
			var folded []schema.Turn
			h, folded = s.sessionLogCheckpointWithMeta(h, s.cm.PreserveRecentTurns, s.cm.metaFor(ctx))
			checkpointed = folded
			return h, nil
		})
		compacted = true
		p, prof = estimatePressure()
	}

	// Layer 4: LLM summarization fallback.
	if p >= s.cm.SummarizeThreshold && s.cm.client != nil {
		if s.cm.applyLayer(ctx, emitFn, history, prof, "summarize", schema.TurnSummary, func(h []schema.Turn) ([]schema.Turn, error) {
			return s.cm.summarizeFold(ctx, h, s.cm.PreserveRecentTurns, "", checkpointed)
		}) {
			compacted = true
		}
	}

	// Reset token measurement after compaction since history content changed.
	if compacted {
		s.cm.resetMeasurement()
	}

	return nil
}

// sessionLogCheckpoint replaces old history with a checkpoint built from the
// session log. Returns a new history slice: [checkpoint_turn, ...preserved_recent].
// It uses the Manager's fallback metadata; fold publishers go through
// sessionLogCheckpointWithMeta with their per-call metadata instead.
func (s *SessionLogStrategy) sessionLogCheckpoint(history []schema.Turn, preserveRecent int) []schema.Turn {
	var meta *CompactionMeta
	if s != nil && s.cm != nil {
		meta = &s.cm.Meta
	}
	result, _ := s.sessionLogCheckpointWithMeta(history, preserveRecent, meta)
	return result
}

// sessionLogCheckpointWithMeta also returns the turns the checkpoint folded
// (nil when it folded nothing), for a summary completing the same fold.
func (s *SessionLogStrategy) sessionLogCheckpointWithMeta(history []schema.Turn, preserveRecent int, meta *CompactionMeta) ([]schema.Turn, []schema.Turn) {
	if attentionTransparentTurnCount(history) <= preserveRecent {
		return history, nil
	}

	cutoff := foldCutoff(history, preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	// Extract original prompt from old history (same logic as checkpoint()).
	originalPrompt := extractOriginalPrompt(history[:cutoff])
	if len(originalPrompt) > 500 {
		originalPrompt = originalPrompt[:500] + "..."
	}

	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT - SESSION LOG]\n")
	fmt.Fprintf(&b, "Original prompt: %s\n", originalPrompt)
	if s.session != nil {
		fmt.Fprintf(&b, "This session's id is %s.%s\n", s.session.ID(), s.transcriptRecoverySentence(meta))
	}
	b.WriteString("\n")

	b.WriteString("Session log:\n")
	logStr := s.log.String()
	if logStr != "" {
		b.WriteString(logStr)
	} else {
		b.WriteString("(no entries)")
	}
	b.WriteString("\n[END CHECKPOINT]\n")

	checkpointTurn := schema.NewTurn(schema.TurnCheckpoint, llm.User(b.String()))
	result := make([]schema.Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result, history[:cutoff:cutoff]
}

// extractOriginalPrompt finds the original user prompt from history, handling
// previous checkpoints and summaries.
func extractOriginalPrompt(history []schema.Turn) string {
	for _, t := range history {
		if t.Kind != schema.TurnUserInput {
			continue
		}
		text := t.Message.Text()
		if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, "[CONTEXT CHECKPOINT - SESSION LOG]") {
			if prompt := extractOriginalPromptLine(text, "Original prompt: "); prompt != "" {
				return prompt
			}
			if prompt := extractOriginalPromptLine(text, "Original task: "); prompt != "" {
				return prompt
			}
			continue
		}
		if strings.HasPrefix(text, summaryMarkerStart) {
			continue
		}
		return text
	}
	return ""
}

func extractOriginalPromptLine(text, prefix string) string {
	_, rest, found := strings.Cut(text, prefix)
	if !found {
		return ""
	}
	rest, _, _ = strings.Cut(rest, "\n")
	return rest
}

// AfterAction forks a summarization of the recent turns and appends the
// result to the session log. A summarization failure is returned so the
// caller can surface it as a warning without failing the turn. The fork runs
// on the manager's shared cheap caller, not on the client the host passes, so
// that a cheap model the provider refuses is learned once per session.
func (s *SessionLogStrategy) AfterAction(ctx context.Context, history []schema.Turn, _ *llm.Client) error {
	if s.session == nil || s.session.Profile() == nil {
		return nil
	}
	history = attentionTransparentHistory(history)
	// Pass only the last ~10 turns so the cheap model summarizes
	// what just happened rather than processing the entire session.
	recent := history
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}
	entry, err := forkSummarize(ctx, s.cm.cheap, s.session.Profile(), recent, len(history))
	if err != nil {
		// Surface it; the caller warns without failing the turn.
		return fmt.Errorf("session-log strategy: %w", err)
	}
	return s.session.WithResponseSideEffects(ctx, func() {
		s.session.Emit(events.EventForkSummary, events.ForkSummaryData{Turn: entry.Turn})
		if err := s.log.Append(entry); err != nil {
			s.session.Emit(events.EventWarning, events.WarningData{Message: "session log append failed: " + err.Error()})
		}
	})

}

// transcriptRecoverySentence is the strategy's view of how the agent may
// recover folded-away detail: worded from the transcript tools the session
// actually serves, and silent when the manager never reported any.
func (s *SessionLogStrategy) transcriptRecoverySentence(meta *CompactionMeta) string {
	if s == nil || s.cm == nil || meta == nil {
		return ""
	}
	return meta.TranscriptRecoverySentence()
}
