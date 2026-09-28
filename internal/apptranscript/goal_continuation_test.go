package apptranscript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestGoalContinuationSavedProjection(t *testing.T) {
	const notice = "672edce7-238a-4cd9-92a7-36290a41d193"
	const input = "3eec9a93-2687-4d66-871c-9c71f3e61a5f"
	goal := transcript.Entry{Kind: "entry", Seq: 3, Turn: schema.Turn{
		Kind: schema.TurnSteering, Message: llm.User(input), StableTurnID: "turn_goal",
		GoalContinuation: &schema.GoalContinuationInfo{Text: notice},
	}}
	ordinary := transcript.Entry{Kind: "entry", Seq: 4, Turn: schema.NewTurn(schema.TurnSteering, llm.User(notice))}
	path := writeEntries(t, reservedUserEntry(1, input, "turn_user"), assistantTextEntry(2, input), goal, ordinary, assistantTextEntry(5, input))
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	if got := turnIDs(full); !reflect.DeepEqual(got, []string{"turn_user", "turn_goal"}) {
		t.Fatalf("turn IDs = %v, want distinct user and goal turns", got)
	}
	if len(full[1].Items) != 3 {
		t.Fatalf("goal turn has %d items, want notice, ordinary steering, and reply", len(full[1].Items))
	}
	item := full[1].Items[0]
	if item.Type != "systemMessage" || item.Text != notice || item.TurnID != "turn_goal" {
		t.Fatal("saved goal notice does not match the live item contract")
	}
	if full[1].Items[1].Type != "steering" || full[1].Items[1].Text != notice {
		t.Fatal("ordinary steering was misclassified from its text")
	}
}
