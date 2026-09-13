package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestConversationSignalsIgnoreRoundTimingMetadata(t *testing.T) {
	payload := schema.RoundTimings{Round: 1, TotalRound: time.Second}
	timing := schema.NewTurn(schema.TurnRoundTimings, llm.System(payload.Announcement()))
	timing.RoundTimings = &payload
	sess := &Session{history: []schema.Turn{timing}}
	if turns, responses := sess.conversationSignals(); turns != 0 || responses != 0 {
		t.Fatalf("metadata-only signals = (%d, %d), want no prior conversation", turns, responses)
	}
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")),
	)
	sess.modelResponses = 1
	if turns, responses := sess.conversationSignals(); turns != 2 || responses != 1 {
		t.Fatalf("conversation signals = (%d, %d), want two turns and one response", turns, responses)
	}
}
