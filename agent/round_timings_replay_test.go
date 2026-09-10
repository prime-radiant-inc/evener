package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestExpandHistoryDropsPersistedRoundTimings(t *testing.T) {
	turn := schema.NewTurn(schema.TurnRoundTimings, llm.System("Round 0 total=1s"))
	turn.RoundTimings = &schema.RoundTimings{Round: 0, TotalRound: time.Second}
	history := expandHistory([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("question")),
		turn,
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")),
	}, replayScope{})
	if len(history) != 2 || history[0].Text() != "question" || history[1].Text() != "answer" {
		t.Fatalf("provider history = %#v, want user and assistant only", history)
	}
}
