package apptranscript

import (
	"fmt"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A header emitted on its own is a fragment of the prelude turn when the file
// later records a prelude run: the prompt and that run are one turn, so each
// says it has the other. A run whose items all settled elsewhere is not a
// boundary, so only visible runs move the flags.
func TestPreludeHeaderAndRunAreFragmentsOfOneTurn(t *testing.T) {
	header := transcript.Header{SessionID: "prelude_fragment", SystemPrompt: "You are Evener."}
	entries := []transcript.Entry{
		identityEntry(1, schema.Turn{Kind: schema.TurnUserInput, TurnID: "turn_e1", TurnKind: schema.TurnSpanExecution, Message: llm.User("run")}),
		identityEntry(2, schema.Turn{Kind: schema.TurnUserInput, TurnID: appwire.SystemPreludeTurnID, TurnKind: schema.TurnSpanPrelude, Message: llm.User("first prompt")}),
	}
	reg := NewToolCallRegistry()
	turns, err := itemTurnsFromEntries(header, entries, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := ProjectEntryParts(turnID, entryIndex, turn, reg, nil, nil)
		return items
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, turn := range turns {
		got = append(got, fmt.Sprintf("%s|%s|earlier=%v|later=%v", turn.ID, turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems))
	}
	want := []string{
		appwire.SystemPreludeTurnID + "|fragment|earlier=false|later=true",
		"turn_e1|full|earlier=false|later=false",
		appwire.SystemPreludeTurnID + "|fragment|earlier=true|later=false",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("turns = %v, want the header, the execution and the prelude run as %v", got, want)
	}
}
