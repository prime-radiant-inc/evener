package contextmgr

import (
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// On a provider failure, the session persists a salvage pair: a TurnAssistant
// carrying the recovered partial output, immediately followed by a
// TurnSteering explaining what happened. The two turns are meaningless apart
// — a compaction cutoff landing between them would leave either a salvaged
// draft with no explanation or a steering note about content that's gone.
// These tests pin that safeCutoff never splits the pair.

func TestSalvagePair_CutoffWalksBackToKeepPairTogether(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("start the task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("first attempt")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("[salvaged partial output from failed turn]")},
		{Kind: schema.TurnSteering, Message: llm.User("Provider request failed after streaming the output above; retrying.")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("continuing after recovery")},
	}
	// preserveRecent=2 -> naive cutoff = len(history)-2 = 3, which lands
	// exactly on the steering turn: the salvaged assistant turn (index 2)
	// would be summarized away while its steering explanation (index 3)
	// survives. safeCutoff must walk back to 2 so the pair stays together.
	const preserveRecent = 2
	got := safeCutoff(history, len(history)-preserveRecent)
	if got != 2 {
		t.Fatalf("safeCutoff = %d, want 2 (salvage pair must stay together)", got)
	}
	if history[got].Kind != schema.TurnAssistant {
		t.Fatalf("preserved tail starts with %s, want TurnAssistant (the salvaged turn)", history[got].Kind)
	}
	if history[got+1].Kind != schema.TurnSteering {
		t.Fatalf("turn after the salvaged assistant is %s, want TurnSteering", history[got+1].Kind)
	}
}

func TestSalvagePair_WithinPreserveTail_Untouched(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("start the task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("first attempt")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("[salvaged partial output from failed turn]")},
		{Kind: schema.TurnSteering, Message: llm.User("Provider request failed after streaming the output above; retrying.")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("continuing after recovery")},
	}
	// preserveRecent=4 -> naive cutoff = len(history)-4 = 1, which already
	// lands before the salvage pair (indices 2-3) and on a TurnAssistant, so
	// no walk-back is needed. The pair sits entirely inside the preserved
	// tail on its own and safeCutoff must leave the cutoff unchanged.
	const preserveRecent = 4
	naiveCutoff := len(history) - preserveRecent
	got := safeCutoff(history, naiveCutoff)
	if got != naiveCutoff {
		t.Fatalf("safeCutoff = %d, want %d (pair already inside preserved tail; cutoff must be untouched)", got, naiveCutoff)
	}
}
