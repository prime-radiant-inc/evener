package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
)

// RecursiveDistillStrategy implements logarithmic memory hierarchy through
// continuous micro-compactions. Every 10 turns, recent actions are distilled
// into 1-2 sentences (micro-summary). Every 50 turns (5 micro-summaries),
// the micro-summaries are folded into a macro-summary. This creates a
// log-depth hierarchy where information is gradually compressed, never lost
// in a single step.
//
// Uses compact as the base compaction mechanism, with distilled summaries
// injected as a steering message that survives compaction.
type RecursiveDistillStrategy struct {
	cm             *ContextManager
	microSummaries []string
	macroSummaries []string
	lastMicroAt    int // turn count at last micro-summary
	lastMacroAt    int // turn count at last macro-summary
}

func NewRecursiveDistillStrategy(cm *ContextManager) *RecursiveDistillStrategy {
	return &RecursiveDistillStrategy{
		cm:             cm,
		microSummaries: []string{},
		macroSummaries: []string{},
	}
}

func (s *RecursiveDistillStrategy) Name() string            { return "recursive-distill" }
func (s *RecursiveDistillStrategy) Tools() []RegisteredTool { return nil }

func (s *RecursiveDistillStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error {
	// Run standard compact compaction.
	s.cm.MaybeCompact(ctx, history, sysPromptChars, emitFn)

	// After compaction, inject distilled summary hierarchy if we have any.
	if len(s.macroSummaries) > 0 || len(s.microSummaries) > 0 {
		s.injectDistilledContext(history)
	}

	return nil
}

// injectDistilledContext places the distilled memory hierarchy as a steering
// message at the end of history. Removes any previous distilled turn first.
func (s *RecursiveDistillStrategy) injectDistilledContext(history *[]Turn) {
	var b strings.Builder
	b.WriteString("[DISTILLED MEMORY]\n")

	if len(s.macroSummaries) > 0 {
		b.WriteString("Session overview:\n")
		for _, ms := range s.macroSummaries {
			b.WriteString("  " + ms + "\n")
		}
		b.WriteString("\n")
	}

	if len(s.microSummaries) > 0 {
		b.WriteString("Recent actions:\n")
		for _, ms := range s.microSummaries {
			b.WriteString("  " + ms + "\n")
		}
	}

	b.WriteString("[END DISTILLED MEMORY]")

	// Remove any existing distilled memory turn.
	filtered := (*history)[:0]
	for _, t := range *history {
		if t.Kind == TurnSteering && strings.Contains(t.Message.Text(), "[DISTILLED MEMORY]") {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	// Append at the end.
	distilledTurn := NewTurn(TurnSteering, llm.User(b.String()))
	*history = append(*history, distilledTurn)
}

// AfterAction checks if enough turns have accumulated for a micro or macro
// distillation step.
func (s *RecursiveDistillStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	if client == nil || s.cm == nil {
		return nil
	}

	turnCount := len(history)

	// Micro-summary every 10 turns.
	if turnCount-s.lastMicroAt >= 10 {
		micro, err := s.microSummarize(ctx, client, history)
		if err == nil {
			s.microSummaries = append(s.microSummaries, micro)
			s.lastMicroAt = turnCount
		}

		// Macro-summary every 50 turns (when we've accumulated 5 micro-summaries).
		if len(s.microSummaries) >= 5 && turnCount-s.lastMacroAt >= 50 {
			macro, err := s.macroSummarize(ctx, client, s.microSummaries)
			if err == nil {
				s.macroSummaries = append(s.macroSummaries, macro)
				s.microSummaries = nil // Reset after folding into macro.
				s.lastMacroAt = turnCount
			}
		}
	}

	return nil
}

// microSummarize distills the last 10 turns into 1-2 sentences.
func (s *RecursiveDistillStrategy) microSummarize(ctx context.Context, client *llm.Client, history []Turn) (string, error) {
	recent := history
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}

	var b strings.Builder
	for _, t := range recent {
		switch t.Kind {
		case TurnAssistant:
			b.WriteString("Assistant: " + truncate(t.Message.Text(), 200) + "\n")
		case TurnTool, TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := fmt.Sprint(p.ToolResult.Content)
					b.WriteString(fmt.Sprintf("Tool(%s): %s\n", p.ToolResult.Name, truncate(content, 100)))
				}
			}
		}
	}

	prompt := fmt.Sprintf(`Summarize these coding agent actions as a structured status update (2-4 sentences). Include:
- What was accomplished (specific files modified, specific changes applied)
- What remains to be done (concrete next steps)
- Any errors or blockers encountered

%s
Status update:`, b.String())

	cp := s.cm.currentProfile()
	req := llm.Request{
		Model:    cp.CheapModel(),
		Provider: cp.ID(),
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := client.Complete(ctx, req)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(resp.Text()), nil
}

// macroSummarize folds multiple micro-summaries into a single overview sentence.
func (s *RecursiveDistillStrategy) macroSummarize(ctx context.Context, client *llm.Client, micros []string) (string, error) {
	var b strings.Builder
	for i, m := range micros {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, m))
	}

	prompt := fmt.Sprintf(`Consolidate these action summaries into a structured progress report (3-5 sentences). Preserve:
- All specific files that were modified and what changes were made
- What concrete work remains (do NOT generalize — list specific files/tasks)
- Any errors or blockers

%s
Progress report:`, b.String())

	cp := s.cm.currentProfile()
	req := llm.Request{
		Model:    cp.CheapModel(),
		Provider: cp.ID(),
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := client.Complete(ctx, req)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(resp.Text()), nil
}
