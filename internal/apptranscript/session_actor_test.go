package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSessionActorInputAndSteeringProjection(t *testing.T) {
	manager := schema.SessionActor{Kind: "manager", ManagerRef: "local:manager"}
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnSteering} {
		turn := schema.NewTurn(kind, llm.User("same text"))
		turn.Actor = manager
		turn.SteeringSource = "user"
		projected := ProjectTurn("turn_1", 1, turn, nil, nil, nil)
		if len(projected) != 1 || projected[0].Actor == nil || projected[0].Actor.Kind != "manager" || projected[0].Actor.ManagerRef != manager.ManagerRef {
			t.Fatalf("%s projection lost actor: %+v", kind, projected)
		}
		turn.Actor = schema.SessionActor{Kind: "human"}
		human := ProjectTurn("turn_1", 1, turn, nil, nil, nil)
		if len(human) != 1 || human[0].Actor == nil || human[0].Actor.Kind != "human" || human[0].Actor.ManagerRef != "" {
			t.Fatalf("%s projection changed human actor: %+v", kind, human)
		}
	}
}
