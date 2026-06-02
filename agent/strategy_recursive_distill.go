package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// recursiveDistillStrategy implements logarithmic memory hierarchy through
// continuous micro-compactions. Every 10 turns, recent actions are distilled
// into 1-2 sentences (micro-summary). Every 50 turns (5 micro-summaries),
// the micro-summaries are folded into a macro-summary. This creates a
// log-depth hierarchy where information is gradually compressed, never lost
// in a single step.
//
// Uses compact as the base compaction mechanism, with distilled summaries
// injected as a steering message that survives compaction.
type recursiveDistillStrategy struct {
	cm             *contextManager
	microSummaries []string
	macroSummaries []string
	lastMicroAt    int // turn count at last micro-summary
	lastMacroAt    int // turn count at last macro-summary
}

// newRecursiveDistillStrategy returns a recursiveDistillStrategy bound to the
// given contextManager with empty micro- and macro-summary hierarchies.
func newRecursiveDistillStrategy(cm *contextManager) *recursiveDistillStrategy {
	return &recursiveDistillStrategy{
		cm:             cm,
		microSummaries: []string{},
		macroSummaries: []string{},
	}
}

// Name returns the strategy identifier "recursive-distill".
func (s *recursiveDistillStrategy) Name() string { return "recursive-distill" }

// Tools returns nil, as this strategy registers no tools.
func (s *recursiveDistillStrategy) Tools() []tool.RegisteredTool { return nil }

// ManageContext runs the standard compact compaction and then, if any
// distilled summaries exist, injects the distilled memory hierarchy as a
// steering message at the end of history.
func (s *recursiveDistillStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
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
func (s *recursiveDistillStrategy) injectDistilledContext(history *[]schema.Turn) {
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
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), "[DISTILLED MEMORY]") {
			continue
		}
		filtered = append(filtered, t)
	}
	*history = filtered

	// Append at the end.
	distilledTurn := schema.NewTurn(schema.TurnSteering, llm.User(b.String()))
	*history = append(*history, distilledTurn)
}

// AfterAction checks if enough turns have accumulated for a micro or macro
// distillation step.
func (s *recursiveDistillStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
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
func (s *recursiveDistillStrategy) microSummarize(ctx context.Context, client *llm.Client, history []schema.Turn) (string, error) {
	recent := history
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}

	var b strings.Builder
	for _, t := range recent {
		switch t.Kind {
		case schema.TurnAssistant:
			b.WriteString("Assistant: " + truncate(t.Message.Text(), 200) + "\n")
		case schema.TurnTool, schema.TurnToolResults:
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
func (s *recursiveDistillStrategy) macroSummarize(ctx context.Context, client *llm.Client, micros []string) (string, error) {
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
