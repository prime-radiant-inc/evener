package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func TestManagerObservationAnswerIsNotCommunicate(t *testing.T) {
	t.Parallel()
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnSteering} {
		items, _ := apptranscript.ProjectEntryParts("answer", 1, schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "answer", Kind: kind, Message: llm.User("answer"), AnswerToTurnIDs: []string{"question"}}, nil, nil, nil)
		if len(items) != 1 || items[0].Observation == nil {
			t.Fatal("missing actual input observation")
		}
		updates := recordedObservationUpdates(items, appwire.SessionWatchRequest{Completion: true, Events: []string{"communicate"}}, "local:worker", "instance", "1@worker")
		if len(updates) != 0 {
			t.Fatalf("accepted input became communicate: %+v", updates)
		}
	}
}
