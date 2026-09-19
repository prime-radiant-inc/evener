package contextmgr

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// ObsMaskStrategy implements aggressive observation masking as the primary
// context management mechanism. Based on JetBrains (NeurIPS 2025) finding that
// dropping tool outputs equals or beats LLM summarization for code agents.
//
// Approach: Drop ALL tool output content older than N turns (replace with minimal
// markers). Keep ALL assistant reasoning verbatim. Fall back to deterministic
// checkpoint only if still over pressure. No thinking clearing, no LLM
// summarization — the hypothesis is that tool outputs are re-readable, so
// preserving reasoning is more valuable than preserving observations.
type ObsMaskStrategy struct {
	cm *Manager
}

// NewObsMaskStrategy returns an ObsMaskStrategy backed by the given Manager.
func NewObsMaskStrategy(cm *Manager) *ObsMaskStrategy {
	return &ObsMaskStrategy{cm: cm}
}

// Name returns the strategy identifier "obs-mask".
func (s *ObsMaskStrategy) Name() string { return "obs-mask" }

// Tools returns the tools registered by this strategy; it registers none.
func (s *ObsMaskStrategy) Tools() []tool.RegisteredTool { return nil }

// AfterAction is a no-op; this strategy performs no work after each action.
func (s *ObsMaskStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	return nil
}

// ManageContext reduces context pressure in two layers. When pressure reaches
// the Manager's ObservationMaskThreshold it aggressively masks tool
// outputs older than PreserveRecentTurns, replacing them with minimal markers.
// If pressure still reaches CheckpointThreshold it then folds history into a
// deterministic checkpoint. Each applied layer emits an EventContextCompaction
// event via emitFn, and any compaction resets the manager's cached token
// measurements. It is a no-op when no Manager is set or the context
// window size is non-positive.
func (s *ObsMaskStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	if s.cm == nil {
		return nil
	}
	// One profile snapshot covers the window check and every diagnostic below:
	// a model switch landing mid-compaction would otherwise bill one layer's
	// numbers by another model's thinking rule and image family.
	prof, _, _ := s.cm.profileSnapshot()
	if cw := contextWindowOf(prof); cw <= 0 {
		return nil
	}

	// Each phase reads pressure and its before/after diagnostics from ONE
	// snapshot (see pressureFromSnapshot), so a concurrent SetProfile cannot
	// decide a layer by one model and describe it by another.
	pressure := func() (float64, *provider.Profile) { return s.cm.pressureWithProfile(history, sysPromptChars) }

	p, prof := pressure()
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
		before := s.cm.estimateTokensFor(prof, *history)
		aggressiveMaskObservations(*history, s.cm.PreserveRecentTurns)
		after := s.cm.estimateTokensFor(prof, *history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "aggressive_obs_mask",
			TurnsBefore:     len(*history),
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p, prof = pressure()
	}

	// Layer 2: Deterministic checkpoint as fallback if masking wasn't enough.
	if p >= s.cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := s.cm.estimateTokensFor(prof, *history)
		result, err := checkpointWithInput(ctx, *history, s.cm.PreserveRecentTurns, s.cm.metaFor(ctx), s.cm.resultToolName())
		if err != nil {
			return err
		}
		*history = result
		after := s.cm.estimateTokensFor(prof, *history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if len(*history) > 0 && (*history)[0].Kind == schema.TurnCheckpoint {
			s.cm.handleCompactionTurn(ctx, (*history)[0])
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
func aggressiveMaskObservations(history []schema.Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := attentionTransparentRecentCutoff(history, preserveRecent)
	if cutoff <= 0 {
		return
	}

	for i := range cutoff {
		t := &history[i]
		if t.Kind != schema.TurnTool && t.Kind != schema.TurnToolResults {
			continue
		}
		// Copy-on-write over the shared *ToolResultData payloads, for the
		// same losing-fold leak maskObservations documents.
		var rebuilt []llm.ContentPart
		for j := range t.Message.Content {
			p := t.Message.Content[j]
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

			if rebuilt == nil {
				rebuilt = append([]llm.ContentPart(nil), t.Message.Content...)
			}
			// Minimal mask: just tool name and status.
			masked := *tr
			masked.Content = fmt.Sprintf("[%s: OK]", tr.Name)
			rebuilt[j].ToolResult = &masked
		}
		if rebuilt != nil {
			t.Message.Content = rebuilt
		}
	}
}
