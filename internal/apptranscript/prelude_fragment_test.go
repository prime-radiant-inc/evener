package apptranscript

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A header emitted on its own is a fragment of the prelude turn when the file
// later records a prelude run: the prompt and that run are one turn, so each
// says it has the other. The projection must not count a run whose items all
// settled elsewhere as a fragment, so only visible runs move the flags.
func TestPreludeHeaderAndRunAreFragmentsOfOneTurn(t *testing.T) {
	stamped := func(turnID string, kind schema.TurnSpanKind, turn schema.Turn) transcript.Entry {
		turn.Format, turn.TurnID, turn.TurnKind = schema.TurnFormatIdentity, turnID, kind
		return transcript.Entry{Turn: turn}
	}
	header := transcript.Header{SessionID: "prelude_fragment", SystemPrompt: "You are Evener."}
	entries := []transcript.Entry{
		stamped("turn_e1", schema.TurnSpanExecution, schema.NewTurn(schema.TurnUserInput, llm.User("run"))),
		stamped(appwire.SystemPreludeTurnID, schema.TurnSpanPrelude, schema.NewTurn(schema.TurnUserInput, llm.User("first prompt"))),
	}
	reg := NewToolCallRegistry()
	turns, err := itemTurnsFromEntries(header, entries, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := ProjectEntryParts(turnID, entryIndex, turn, reg, nil, nil)
		return items
	})
	if err != nil {
		t.Fatal(err)
	}
	var described []string
	for _, turn := range turns {
		described = append(described, fmt.Sprintf("%s|%s|earlier=%v|later=%v", turn.ID, turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems))
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %v, want the header, the execution and the prelude run", described)
	}
	if got := described[0]; got != appwire.SystemPreludeTurnID+"|fragment|earlier=false|later=true" {
		t.Fatalf("header turn = %s, want a leading fragment with later items", got)
	}
	if got := described[1]; got != "turn_e1|full|earlier=false|later=false" {
		t.Fatalf("execution turn = %s, want a whole turn of its own", got)
	}
	if got := described[2]; got != appwire.SystemPreludeTurnID+"|fragment|earlier=true|later=false" {
		t.Fatalf("prelude run = %s, want a trailing fragment with earlier items", got)
	}
}
