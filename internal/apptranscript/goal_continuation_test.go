package apptranscript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
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
	for _, cache := range []*TurnCache{NewTurnCache(), NewTurnCache()} {
		latest, cursor := requireLatestFromFile(t, cache, path, testMaxLineBytes, 1, boundedTestProjector)
		want, wantCursor := latestGroupedTurns(full, 1)
		if !reflect.DeepEqual(latest, want) || cursor != wantCursor {
			t.Fatal("bounded goal read differs from full projection")
		}
		page := requirePageFromFile(t, cache, path, testMaxLineBytes, cursor, 1, boundedTestProjector)
		if !reflect.DeepEqual(page.Turns, full[:1]) || page.NextCursor != "" {
			t.Fatal("paging across a goal opener changed the prior turn")
		}
		count, err := cache.TurnCountFromFile(path, testMaxLineBytes, boundedTestProjector)
		if err != nil || count != 2 {
			t.Fatalf("indexed count = %d, err = %v; want 2", count, err)
		}
		window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:th_1", Limit: 10}, boundedTestProjector)
		if err != nil {
			t.Fatal(err)
		}
		var items []appwire.ThreadItem
		for _, candidate := range window.Candidates {
			items = append(items, candidate.Item)
		}
		wantItems := append(append([]appwire.ThreadItem{}, full[0].Items...), full[1].Items...)
		if !reflect.DeepEqual(items, wantItems) {
			t.Fatal("item paging changed goal item identity or display")
		}
	}
}

func TestGoalContinuationAppendedToIndexedTranscript(t *testing.T) {
	const input = "a0f00f5a-0685-42d8-932d-2f606c4bb3f8"
	path := writeEntries(t, reservedUserEntry(1, input, "turn_user"), assistantTextEntry(2, input))
	cache := NewTurnCache()
	requireLatestFromFile(t, cache, path, testMaxLineBytes, 1, boundedTestProjector)
	goal := transcript.Entry{Kind: "entry", Seq: 3, Turn: schema.Turn{
		Kind: schema.TurnSteering, Message: llm.User(input), StableTurnID: "turn_goal",
		GoalContinuation: &schema.GoalContinuationInfo{Text: "29294482-adcf-430d-af21-9c8716a8c911"},
	}}
	appendFile(t, path, marshalEntryLine(t, goal))
	latest, _ := requireLatestFromFile(t, cache, path, testMaxLineBytes, 1, boundedTestProjector)
	if len(latest) != 1 || latest[0].ID != "turn_goal" || len(latest[0].Items) != 1 {
		t.Fatal("appended goal notice did not open its own indexed turn")
	}
	appendFile(t, path, marshalEntryLine(t, assistantTextEntry(4, input)))
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	for _, reader := range []*TurnCache{cache, NewTurnCache()} {
		latest, cursor := requireLatestFromFile(t, reader, path, testMaxLineBytes, 1, boundedTestProjector)
		want, wantCursor := latestGroupedTurns(full, 1)
		if !reflect.DeepEqual(latest, want) || cursor != wantCursor || len(latest[0].Items) != 2 {
			t.Fatal("appended reply lost its goal opener across incremental or saved-index reads")
		}
	}
}
