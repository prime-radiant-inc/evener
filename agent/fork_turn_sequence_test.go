package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func startClientTurn(t *testing.T, s *Session, mutationID string) string {
	t.Helper()
	s.SetClientMutationStartWakeFunc(func() {})
	accepted, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: mutationID, ExpectedInstanceID: s.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: mutationID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return accepted.Turn.ID
}

func TestHighestClientMutationTurnSequence(t *testing.T) {
	turns := []schema.Turn{
		{TurnID: "turn_m2"}, {StableTurnID: "turn_m5"}, {OwningTurnID: "turn_m3"},
		{TurnID: "t_abc"}, {StableTurnID: "turn_11"}, {StableTurnID: "q_1"},
	}
	if got := highestClientMutationTurnSequence(turns); got != 5 {
		t.Fatalf("highest = %d, want 5", got)
	}
}

// A fork child copies turns named turn_m1..turn_m<N>; its own client turns
// must be named above them, or a new turn would reuse a copied turn's ID.
func TestAForkChildNamesItsTurnsAboveTheOnesItCopies(t *testing.T) {
	parent, _ := newExecutionSession(t)
	var names []string
	for _, id := range []string{"first", "second"} {
		names = append(names, startClientTurn(t, parent, id))
	}
	if names[1] != "turn_m2" {
		t.Fatalf("parent turns = %v", names)
	}
	if _, err := parent.ProcessInput(context.Background(), "after", nil); err != nil {
		t.Fatal(err)
	}
	divergence := 0
	for i, turn := range transcriptTurnsOf(t, parent) {
		if turn.Kind == schema.TurnUserInput && turn.Message.Text() == "after" {
			divergence = i + 1
		}
	}
	stateDir := parent.stateDir
	childID, err := ForkSession(stateDir, parent.ID(), divergence, "edited", "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Close()
	child := restoreExecutionSession(t, stateDir, childID)
	if got := startClientTurn(t, child, "in the child"); got != "turn_m3" {
		t.Fatalf("the child's first client turn is %q, want turn_m3", got)
	}
}
