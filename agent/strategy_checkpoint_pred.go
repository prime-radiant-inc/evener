package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/llm"
)

// checkpointPredStrategy replaces the deterministic checkpoint (Layer 3) with
// a forward-looking predictive checkpoint. Before compaction, a cheap model
// predicts what information the agent will need going forward and generates a
// targeted checkpoint preserving exactly that.
//
// Layers 1, 2, and 4 are identical to compactStrategy.
type checkpointPredStrategy struct {
	cm *contextManager
}

// newCheckpointPredStrategy returns a checkpointPredStrategy bound to the given
// contextManager.
func newCheckpointPredStrategy(cm *contextManager) *checkpointPredStrategy {
	return &checkpointPredStrategy{cm: cm}
}

// Name returns the strategy's identifier, "checkpoint-pred".
func (s *checkpointPredStrategy) Name() string { return "checkpoint-pred" }

// Tools returns nil; this strategy registers no tools.
func (s *checkpointPredStrategy) Tools() []registeredTool { return nil }

// AfterAction is a no-op for this strategy and always returns nil.
func (s *checkpointPredStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	return nil
}

// ManageContext applies the layered compaction pipeline to history in place,
// each layer triggered once the estimated context pressure reaches its
// threshold: observation masking, thinking clearing, a predictive checkpoint
// (falling back to the deterministic checkpoint on error), and LLM
// summarization. Each layer that compacts emits an EventContextCompaction
// event, except the summarization layer, which emits one only on success and
// an EventWarning on failure. It is a no-op when no contextManager or context
// window is configured.
func (s *checkpointPredStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	if s.cm == nil {
		return nil
	}
	cw := s.cm.currentProfile().ContextWindowSize()
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
		p = pressure()
	}

	// Layer 2: Thinking clearing (same as compact).
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
		p = pressure()
	}

	// Layer 3: Predictive checkpoint (replaces deterministic checkpoint).
	if p >= s.cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := estimateTokens(*history)
		result, err := s.predictiveCheckpoint(ctx, *history, s.cm.PreserveRecentTurns)
		if err != nil {
			// Fall back to deterministic checkpoint on error.
			*history = checkpoint(*history, s.cm.PreserveRecentTurns, &s.cm.Meta, s.cm.resultToolName())
			emitFn(events.EventWarning, events.WarningData{
				Message: "Predictive checkpoint failed, using deterministic: " + err.Error(),
			})
		} else {
			*history = result
		}
		after := estimateTokens(*history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "checkpoint_pred",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if s.cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == TurnCheckpoint {
			s.cm.OnCompactionTurn((*history)[0])
		}
		compacted = true
		p = pressure()
	}

	// Layer 4: LLM summarization fallback (same as compact).
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
			if s.cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == TurnSummary {
				s.cm.OnCompactionTurn((*history)[0])
			}
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
func (s *checkpointPredStrategy) predictiveCheckpoint(ctx context.Context, history []Turn, preserveRecent int) ([]Turn, error) {
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
1. The original prompt/goal
2. Key decisions already made
3. Specific values, paths, or identifiers the agent will need
4. Current state and what remains to be done
5. Any errors encountered and how they were resolved

Write ONLY the checkpoint content. Be specific and concise (under 500 words). Focus on information the agent CANNOT re-derive from the codebase.`, b.String())

	cp := s.cm.currentProfile()
	req := llm.Request{
		Model:    cp.CheapModel(),
		Provider: cp.ID(),
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := s.cm.client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}

	checkpointText := "[CONTEXT CHECKPOINT - PREDICTIVE]\n" + resp.Text() + "\n[END CHECKPOINT]"
	checkpointTurn := NewTurn(TurnCheckpoint, llm.User(checkpointText))

	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result, nil
}
