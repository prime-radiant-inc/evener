package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The pre-compact hook appends steering (here the pinned note's handoff) after
// the checkpoint folds and before the summarizer runs, so the summarizer folds
// turns the checkpoint left in its preserved tail. A hold among those turns is
// the human partner's message too: the summary carries it verbatim even when
// the model's reply drops it (#4173).
func TestCompact_SummaryCarriesPartnerMessagesTheHookPushedIntoTheFold(t *testing.T) {
	t.Parallel()
	const hold = "don't publish until I say so"
	s := newScriptedSummaryCompactSession(t, "hook-fold-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("## Progress\nDone")}
	})
	seedNumberedSessionHistory(t, s, 12)
	s.mu.Lock()
	s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(hold)))
	for i := range 5 {
		s.history = append(s.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("work %d", i))))
	}
	s.mu.Unlock()
	s.setPinnedNote("REMEMBER: the release checklist")

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	history := currentHistory(t, s)
	if len(history) == 0 || history[0].Kind != schema.TurnSummary {
		t.Fatalf("history does not start with a summary: %d turns", len(history))
	}
	for _, turn := range history[1:] {
		if turn.Message.Text() == hold {
			t.Fatalf("the hold stayed a raw preserved turn, so the summarizer did not fold it")
		}
	}
	if !strings.Contains(history[0].Message.Text(), hold) {
		t.Fatalf("the hold is gone from the model's context after compaction:\n%s", history[0].Message.Text())
	}
}
