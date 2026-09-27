package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestTurnGrouperPlacesEntriesLikeTheAccumulator(t *testing.T) {
	user := schema.NewTurn(schema.TurnUserInput, llm.User("u"))
	assistant := schema.NewTurn(schema.TurnAssistant, llm.Assistant("a"))
	hook := schema.NewTurn(schema.TurnHookCompleted, llm.User("h"))
	owned := schema.NewTurn(schema.TurnSteering, llm.User("s"))
	owned.OwningTurnID = "turn_1"
	ownedByOpen := schema.NewTurn(schema.TurnSteering, llm.User("s"))
	ownedByOpen.OwningTurnID = "turn_m7"
	stable := schema.NewTurn(schema.TurnUserInput, llm.User("m"))
	stable.StableTurnID = "turn_m7"

	var g TurnGrouper
	steps := []struct {
		entry   schema.Turn
		wantID  string
		wantNew bool
	}{
		{assistant, "turn_1", true}, // a stray continuation opens its own group
		{user, "turn_2", true},
		{assistant, "turn_2", false},
		{owned, "turn_1", true}, // owner is not the open turn: a new group under the owner id
		{hook, "turn_5", true},  // standalone
		{assistant, "turn_6", true},
		{stable, "turn_m7", true},
		{ownedByOpen, "turn_m7", false},
	}
	for i, step := range steps {
		id, isNew := g.Place(&step.entry, i+1)
		if id != step.wantID || isNew != step.wantNew {
			t.Fatalf("entry %d: Place = (%q, %v), want (%q, %v)", i+1, id, isNew, step.wantID, step.wantNew)
		}
	}
}
