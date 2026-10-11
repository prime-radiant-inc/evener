package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/llm"
)

func TestAttentionFoldIgnoresTranscriptOnlyEntries(t *testing.T) {
	attention := schema.NewTurn(schema.TurnSteering, llm.User("delegate reported"))
	attention.AttentionID = "delegate:d1"
	plain := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("go")),
		attention,
		delegateAttentionResolutionTurn("delegate:d1", delegateAttentionConsumed),
	}
	want, err := foldDelegateAttention(entriesOf(plain))
	if err != nil {
		t.Fatal(err)
	}
	if len(want.order) == 0 {
		t.Fatal("the fixture folds no attention")
	}
	got, err := foldDelegateAttention(entriesOf(schematest.InterleaveTranscriptOnly(plain)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fold = %+v, want %+v", got, want)
	}
}
