package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
)

// obsMaskStrategy implements aggressive observation masking as the primary
// context management mechanism. Based on JetBrains (NeurIPS 2025) finding that
// dropping tool outputs equals or beats LLM summarization for code agents.
//
// Approach: Drop ALL tool output content older than N turns (replace with minimal
// markers). Keep ALL assistant reasoning verbatim. Fall back to deterministic
// checkpoint only if still over pressure. No thinking clearing, no LLM
// summarization — the hypothesis is that tool outputs are re-readable, so
// preserving reasoning is more valuable than preserving observations.
type obsMaskStrategy struct {
	cm *contextManager
}

// newObsMaskStrategy returns an obsMaskStrategy backed by the given contextManager.
func newObsMaskStrategy(cm *contextManager) *obsMaskStrategy {
	return &obsMaskStrategy{cm: cm}
}

// Name returns the strategy identifier "obs-mask".
func (s *obsMaskStrategy) Name() string { return "obs-mask" }

// Tools returns the tools registered by this strategy; it registers none.
func (s *obsMaskStrategy) Tools() []registeredTool { return nil }

// AfterAction is a no-op; this strategy performs no work after each action.
func (s *obsMaskStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	return nil
}

// ManageContext reduces context pressure in two layers. When pressure reaches
// the contextManager's ObservationMaskThreshold it aggressively masks tool
// outputs older than PreserveRecentTurns, replacing them with minimal markers.
// If pressure still reaches CheckpointThreshold it then folds history into a
// deterministic checkpoint. Each applied layer emits an EventContextCompaction
// event via emitFn, and any compaction resets the manager's cached token
// measurements. It is a no-op when no contextManager is set or the context
// window size is non-positive.
func (s *obsMaskStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, EventData)) error {
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

	// Layer 1: Aggressive observation masking — replace ALL tool output with
	// minimal "[tool: OK]" markers. Much more aggressive than compact's Layer 1
	// which generates readable summaries.
	if p >= s.cm.ObservationMaskThreshold {
		before := estimateTokens(*history)
		aggressiveMaskObservations(*history, s.cm.PreserveRecentTurns)
		after := estimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "aggressive_obs_mask",
			TurnsBefore:     len(*history),
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = pressure()
	}

	// Layer 2: Deterministic checkpoint as fallback if masking wasn't enough.
	if p >= s.cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := estimateTokens(*history)
		*history = checkpoint(*history, s.cm.PreserveRecentTurns, &s.cm.Meta, s.cm.resultToolName())
		after := estimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if s.cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == TurnCheckpoint {
			s.cm.OnCompactionTurn((*history)[0])
		}
		compacted = true
	}

	if compacted {
		s.cm.mu.Lock()
		s.cm.lastInputTokens = 0
		s.cm.historyLenAtMeasure = 0
		s.cm.mu.Unlock()
	}

	return nil
}

// aggressiveMaskObservations replaces ALL tool result content with minimal
// "[tool: OK]" markers. Unlike maskObservations which generates readable
// one-line summaries, this drops content entirely. Error results are preserved.
func aggressiveMaskObservations(history []Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := len(history) - preserveRecent
	if cutoff <= 0 {
		return
	}

	for i := 0; i < cutoff; i++ {
		t := &history[i]
		if t.Kind != TurnTool && t.Kind != TurnToolResults {
			continue
		}
		for j := range t.Message.Content {
			p := &t.Message.Content[j]
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			tr := p.ToolResult

			// Always preserve error results.
			if tr.IsError {
				continue
			}

			content, ok := tr.Content.(string)
			if !ok {
				continue
			}

			// Skip already-masked results (short bracket markers).
			if strings.HasPrefix(content, "[") && len(content) < 100 {
				continue
			}

			// Minimal mask: just tool name and status.
			tr.Content = fmt.Sprintf("[%s: OK]", tr.Name)
		}
	}
}
