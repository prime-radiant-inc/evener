package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
)

// CheckpointPredStrategy replaces the deterministic checkpoint (Layer 3) with
// a forward-looking predictive checkpoint. Before compaction, a cheap model
// predicts what information the agent will need going forward and generates a
// targeted checkpoint preserving exactly that.
//
// Layers 1, 2, and 4 are identical to CompactStrategy.
type CheckpointPredStrategy struct {
	cm *ContextManager
}

func NewCheckpointPredStrategy(cm *ContextManager) *CheckpointPredStrategy {
	return &CheckpointPredStrategy{cm: cm}
}

func (s *CheckpointPredStrategy) Name() string            { return "checkpoint-pred" }
func (s *CheckpointPredStrategy) Tools() []RegisteredTool { return nil }

func (s *CheckpointPredStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	return nil
}

func (s *CheckpointPredStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error {
	if s.cm == nil {
		return nil
	}
	cw := s.cm.profile.ContextWindowSize()
	if cw <= 0 {
		return nil
	}

	pressure := func() float64 {
		return s.cm.EstimatePressure(*history, sysPromptChars)
	}

	p := pressure()
	compacted := false

	if p >= s.cm.ObservationMaskThreshold {
		s.cm.mu.Lock()
		s.cm.lastInputTokens = 0
		s.cm.historyLenAtMeasure = 0
		s.cm.mu.Unlock()
	}

	// Layer 1: Observation masking (same as compact).
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
		p = pressure()
	}

	// Layer 2: Thinking clearing (same as compact).
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
		p = pressure()
	}

	// Layer 3: Predictive checkpoint (replaces deterministic checkpoint).
	if p >= s.cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		result, err := s.predictiveCheckpoint(ctx, *history, s.cm.PreserveRecentTurns)
		if err != nil {
			// Fall back to deterministic checkpoint on error.
			*history = checkpoint(*history, s.cm.PreserveRecentTurns)
			emitFn(EventWarning, WarningData{
				Message: "Predictive checkpoint failed, using deterministic: " + err.Error(),
			})
		} else {
			*history = result
		}
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "checkpoint_pred",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = pressure()
	}

	// Layer 4: LLM summarization fallback (same as compact).
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

	if compacted {
		s.cm.mu.Lock()
		s.cm.lastInputTokens = 0
		s.cm.historyLenAtMeasure = 0
		s.cm.mu.Unlock()
	}

	return nil
}

// predictiveCheckpoint asks a cheap model to predict what information the agent
// will need going forward, then creates a targeted checkpoint preserving that.
func (s *CheckpointPredStrategy) predictiveCheckpoint(ctx context.Context, history []Turn, preserveRecent int) ([]Turn, error) {
	if len(history) <= preserveRecent {
		return history, nil
	}
	cutoff := safeCutoff(history, len(history)-preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	// Build a condensed view of old history for the prediction model.
	const maxHistoryChars = 30_000
	var b strings.Builder
	for i := 0; i < cutoff; i++ {
		t := history[i]
		switch t.Kind {
		case TurnUserInput:
			b.WriteString("User: " + truncate(t.Message.Text(), 300) + "\n")
		case TurnAssistant:
			b.WriteString("Assistant: " + truncate(t.Message.Text(), 300) + "\n")
		case TurnTool, TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := fmt.Sprint(p.ToolResult.Content)
					b.WriteString(fmt.Sprintf("Tool(%s): %s\n", p.ToolResult.Name, truncate(content, 200)))
				}
			}
		}
		if b.Len() > maxHistoryChars {
			b.WriteString("\n[... truncated ...]\n")
			break
		}
	}

	prompt := fmt.Sprintf(`You are about to lose access to this conversation history due to context limits. Predict what information the agent will need to continue its work successfully.

The agent's conversation so far:
%s

Generate a checkpoint preserving:
1. The original task/goal
2. Key decisions already made
3. Specific values, paths, or identifiers the agent will need
4. Current state and what remains to be done
5. Any errors encountered and how they were resolved

Write ONLY the checkpoint content. Be specific and concise (under 500 words). Focus on information the agent CANNOT re-derive from the codebase.`, b.String())

	req := llm.Request{
		Model:    s.cm.profile.CheapModel(),
		Provider: s.cm.profile.ID(),
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := s.cm.client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}

	checkpointText := "[CONTEXT CHECKPOINT - PREDICTIVE]\n" + resp.Text() + "\n[END CHECKPOINT]"
	checkpointTurn := NewTurn(TurnUserInput, llm.User(checkpointText))

	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result, nil
}
