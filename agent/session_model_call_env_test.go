package agent

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestExpandHistoryEmitsEnvironmentTurnAsUserMessage(t *testing.T) {
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnEnvironment, llm.User("<environment_context>\ncwd: \"/w\"\n</environment_context>")),
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
	}
	got := expandHistory(turns, replayScope{})
	if len(got) != 2 {
		t.Fatalf("expanded %d messages, want 2: %+v", len(got), got)
	}
	if got[0].Role != llm.RoleUser || got[0].Text() == "" {
		t.Fatalf("environment message: %+v", got[0])
	}
	if got[1].Text() != "hello" {
		t.Fatalf("user input must follow environment context: %+v", got[1])
	}
}
