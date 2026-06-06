package contextmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
	logPath := filepath.Join(host.StateDir(), "sessions", host.ID()+".log.jsonl")
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
	cw := s.cm.currentProfile().ContextWindowSize()
	if cw <= 0 {
		return nil
	}

	estimatePressure := func() float64 {
		return s.cm.EstimatePressure(*history, sysPromptChars)
	}

	p := estimatePressure()
	compacted := false

	// Invalidate API token measurement before running any layer so that
	// between-layer pressure() calls use char/4 (reflects in-place mutations).
	if p >= s.cm.ObservationMaskThreshold {
		s.cm.mu.Lock()
		s.cm.lastInputTokens = 0
		s.cm.historyLenAtMeasure = 0
		s.cm.mu.Unlock()
	}

	// Layer 1: Observation masking.
	if p >= s.cm.ObservationMaskThreshold {
		before := estimateTokens(*history)
		maskObservations(*history, s.cm.PreserveRecentTurns, s.cm.resultToolName())
		after := estimateTokens(*history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "observation_mask",
			TurnsBefore:     len(*history),
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = estimatePressure()
	}

	// Layer 2: Thinking clearing.
	if p >= s.cm.ThinkingClearThreshold {
		before := estimateTokens(*history)
		clearThinking(*history, s.cm.PreserveRecentTurns)
		after := estimateTokens(*history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "thinking_clear",
			TurnsBefore:     len(*history),
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = estimatePressure()
	}

	// Layer 3 (replaced): Session-log checkpoint.
	if p >= s.cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := estimateTokens(*history)
		*history = s.sessionLogCheckpoint(*history, s.cm.PreserveRecentTurns)
		after := estimateTokens(*history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "session_log_checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if s.cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == schema.TurnCheckpoint {
			s.cm.OnCompactionTurn((*history)[0])
		}
		compacted = true
		p = estimatePressure()
	}

	// Layer 4: LLM summarization fallback.
	if p >= s.cm.SummarizeThreshold && s.cm.client != nil {
		turnsBefore := len(*history)
		before := estimateTokens(*history)
		result, err := s.cm.summarizeWithLLM(ctx, *history, s.cm.PreserveRecentTurns)
		if err != nil {
			emitFn(events.EventWarning, events.WarningData{
				Message: "LLM summarization failed: " + err.Error(),
			})
		} else {
			*history = result
			after := estimateTokens(*history)
			emitFn(events.EventContextCompaction, events.ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
			if s.cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == schema.TurnSummary {
				s.cm.OnCompactionTurn((*history)[0])
			}
			compacted = true
		}
	}

	// Reset token measurement after compaction since history content changed.
	if compacted {
		s.cm.mu.Lock()
		s.cm.lastInputTokens = 0
		s.cm.historyLenAtMeasure = 0
		s.cm.mu.Unlock()
	}

	return nil
}

// sessionLogCheckpoint replaces old history with a checkpoint built from the
// session log. Returns a new history slice: [checkpoint_turn, ...preserved_recent].
func (s *SessionLogStrategy) sessionLogCheckpoint(history []schema.Turn, preserveRecent int) []schema.Turn {
	if len(history) <= preserveRecent {
		return history
	}

	cutoff := safeCutoff(history, len(history)-preserveRecent)
	if cutoff < 0 {
		return history
	}

	// Extract original prompt from old history (same logic as checkpoint()).
	originalPrompt := extractOriginalPrompt(history[:cutoff])
	if len(originalPrompt) > 500 {
		originalPrompt = originalPrompt[:500] + "..."
	}

	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT - SESSION LOG]\n")
	b.WriteString(fmt.Sprintf("Original prompt: %s\n", originalPrompt))
	if s.session != nil {
		b.WriteString(fmt.Sprintf("This session's id is %s. Use read_session_transcript to recover earlier detail, or find_session_transcripts to search it.\n", s.session.ID()))
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
	return result
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
		if strings.HasPrefix(text, "[CONTEXT SUMMARY]") {
			continue
		}
		return text
	}
	return ""
}

func extractOriginalPromptLine(text, prefix string) string {
	idx := strings.Index(text, prefix)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(prefix):]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		return rest[:nl]
	}
	return rest
}

// AfterAction forks a summarization of the recent turns and appends the
// result to the session log. Errors from the LLM are non-fatal.
func (s *SessionLogStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	if s.session == nil || s.session.Profile() == nil {
		return nil
	}
	// Pass only the last ~10 turns so the cheap model summarizes
	// what just happened rather than processing the entire session.
	recent := history
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}
	entry, err := forkSummarize(ctx, client, s.session.Profile(), recent, len(history))
	if err != nil {
		return nil //nolint:nilerr // fork summarization is best-effort; failure must not fail the session
	}
	return s.session.WithResponseSideEffects(ctx, func() {
		s.session.Emit(events.EventForkSummary, events.ForkSummaryData{Turn: entry.Turn})
		if err := s.log.Append(entry); err != nil {
			s.session.Emit(events.EventWarning, events.WarningData{Message: "session log append failed: " + err.Error()})
		}
	})

}
