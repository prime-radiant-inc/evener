package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/llm"
)

// SessionLogStrategy combines compact layers 1+2 (observation masking and
// thinking clearing) with a session-log-based checkpoint (replacing the
// deterministic layer 3), LLM summarization fallback (layer 4), a recall
// tool, and forked summarization in AfterAction.
type SessionLogStrategy struct {
	cm      *ContextManager
	session *Session
	log     *SessionLog
}

// NewSessionLogStrategy creates a SessionLogStrategy backed by the given
// ContextManager and Session. The session log is persisted alongside the
// session snapshot.
func NewSessionLogStrategy(cm *ContextManager, session *Session) *SessionLogStrategy {
	logPath := filepath.Join(session.stateDir, "sessions", session.id+".log.jsonl")
	return &SessionLogStrategy{
		cm:      cm,
		session: session,
		log:     NewSessionLog(logPath),
	}
}

func (s *SessionLogStrategy) Name() string { return "session-log" }

func (s *SessionLogStrategy) Tools() []RegisteredTool {
	return []RegisteredTool{sessionLogRecallToolDef(s)}
}

// sessionLogRecallToolDef builds the recall RegisteredTool for this strategy.
// Uses the same pattern as RecallStrategy's recallToolDef.
func sessionLogRecallToolDef(strategy *SessionLogStrategy) RegisteredTool {
	return RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        "recall",
				Description: "Search through earlier parts of this session's history that may have been compacted away. Use this when you need to remember details from earlier in the session.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "What you want to recall from earlier in the session",
						},
					},
					"required": []string{"question"},
				},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			question, ok := args["question"].(string)
			if !ok || question == "" {
				return nil, fmt.Errorf("recall requires a non-empty 'question' string")
			}

			sess := strategy.session
			if sess == nil {
				return nil, fmt.Errorf("recall: no session reference available")
			}

			sess.maybeAutoSave()

			path := transcriptPath(sess.stateDir, sess.id)
			return recallExecute(ctx, sess.client, sess.profile.ID(), sess.profile.Model(), path, question)
		},
	}
}

// ManageContext applies compaction layers selectively:
//   - Layer 1: observation masking
//   - Layer 2: thinking clearing
//   - Layer 3 (replaced): session-log checkpoint instead of deterministic checkpoint
//   - Layer 4: LLM summarization fallback
func (s *SessionLogStrategy) ManageContext(ctx context.Context, history *[]Turn, pressure float64, sysPromptChars int, emitFn func(EventKind, any)) error {
	if s.cm == nil {
		return nil
	}
	cw := s.cm.profile.ContextWindowSize()
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
		before := EstimateTokens(*history)
		maskObservations(*history, s.cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
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
		before := EstimateTokens(*history)
		clearThinking(*history, s.cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
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
		before := EstimateTokens(*history)
		*history = s.sessionLogCheckpoint(*history, s.cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "session_log_checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = estimatePressure()
	}

	// Layer 4: LLM summarization fallback.
	if p >= s.cm.SummarizeThreshold && s.cm.client != nil {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		result, err := s.cm.summarizeWithLLM(ctx, *history, s.cm.PreserveRecentTurns)
		if err != nil {
			emitFn(EventWarning, WarningData{
				Message: "LLM summarization failed: " + err.Error(),
			})
		} else {
			*history = result
			after := EstimateTokens(*history)
			emitFn(EventContextCompaction, ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
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
func (s *SessionLogStrategy) sessionLogCheckpoint(history []Turn, preserveRecent int) []Turn {
	if len(history) <= preserveRecent {
		return history
	}

	cutoff := safeCutoff(history, len(history)-preserveRecent)
	if cutoff < 0 {
		return history
	}

	// Extract original task from old history (same logic as checkpoint()).
	originalTask := extractOriginalTask(history[:cutoff])
	if len(originalTask) > 500 {
		originalTask = originalTask[:500] + "..."
	}

	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT - SESSION LOG]\n")
	b.WriteString(fmt.Sprintf("Original task: %s\n\n", originalTask))

	b.WriteString("Session log:\n")
	logStr := s.log.String()
	if logStr != "" {
		b.WriteString(logStr)
	} else {
		b.WriteString("(no entries)")
	}
	b.WriteString("\n[END CHECKPOINT]\n")

	checkpointTurn := NewTurn(TurnUserInput, llm.User(b.String()))
	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result
}

// extractOriginalTask finds the original user task from history, handling
// previous checkpoints and summaries.
func extractOriginalTask(history []Turn) string {
	for _, t := range history {
		if t.Kind != TurnUserInput {
			continue
		}
		text := t.Message.Text()
		if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, "[CONTEXT CHECKPOINT - SESSION LOG]") {
			// Extract "Original task: ..." from a previous checkpoint.
			if idx := strings.Index(text, "Original task: "); idx >= 0 {
				rest := text[idx+len("Original task: "):]
				if nl := strings.Index(rest, "\n"); nl >= 0 {
					return rest[:nl]
				}
				return rest
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

// AfterAction forks a summarization of the recent turns and appends the
// result to the session log. Errors from the LLM are non-fatal.
func (s *SessionLogStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	if s.session == nil || s.session.profile == nil {
		return nil
	}
	entry, err := ForkSummarize(ctx, client, s.session.profile, history, len(history))
	if err != nil {
		// Non-fatal: log the error but don't fail the session.
		return nil
	}
	return s.log.Append(entry)
}
