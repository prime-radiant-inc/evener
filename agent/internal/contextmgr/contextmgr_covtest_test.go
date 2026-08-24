package contextmgr

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestCovWithCompactionTurnCallbackRoutesThroughManager(t *testing.T) {
	turn := schema.Turn{
		Kind:    schema.TurnCheckpoint,
		Message: llm.User("checkpoint payload"),
	}
	var operationTurns, fallbackTurns []schema.Turn
	cm := &Manager{OnCompactionTurn: func(got schema.Turn) {
		fallbackTurns = append(fallbackTurns, got)
	}}
	ctx := WithCompactionTurnCallback(context.Background(), func(got schema.Turn) {
		operationTurns = append(operationTurns, got)
	})

	cm.handleCompactionTurn(ctx, turn)
	if !reflect.DeepEqual(operationTurns, []schema.Turn{turn}) {
		t.Fatalf("operation callback turns = %#v, want %#v", operationTurns, []schema.Turn{turn})
	}
	if len(fallbackTurns) != 0 {
		t.Fatalf("fallback callback ran despite operation callback: %#v", fallbackTurns)
	}

	cm.handleCompactionTurn(context.Background(), turn)
	if !reflect.DeepEqual(fallbackTurns, []schema.Turn{turn}) {
		t.Fatalf("fallback turns without operation callback = %#v, want %#v", fallbackTurns, []schema.Turn{turn})
	}
}
