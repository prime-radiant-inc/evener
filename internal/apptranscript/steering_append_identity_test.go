package apptranscript

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestSteeringOwnerIncrementalIndexIdentity keeps one indexed cache alive while
// a session grows through the same append sequence used by the daemon. The
// steering entry may stay in the already-open m10 logical turn or open the
// delayed m11 owner; either way, every warm indexed read must agree with the
// full projection and with one-item/one-turn paging.
func TestSteeringOwnerIncrementalIndexIdentity(t *testing.T) {
	t.Run("inline_m10_then_delayed_m11", func(t *testing.T) {
		path := t.TempDir() + "/session.transcript.jsonl"
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "steering-append"})
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer func() {
			if err := writer.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()

		appendTurn := func(turn schema.Turn) {
			t.Helper()
			if err := writer.Append(turn); err != nil {
				t.Fatalf("Append %s: %v", turn.Kind, err)
			}
		}

		appendTurn(schema.Turn{
			Kind:         schema.TurnUserInput,
			StableTurnID: "turn_m10",
			Message:      llm.User("prefix"),
		})
		appendTurn(steeringAppendAssistantToolCall("prefix-call", "prefix_tool"))

		cache := NewTurnCache()
		assertSteeringAppendReads(t, cache, path, "turn_m10")

		appendTurn(schema.Turn{
			Kind:         schema.TurnSteering,
			StableTurnID: "mutation_m11",
			OwningTurnID: "turn_m10",
			Message:      llm.User("steer"),
		})
		assertSteeringAppendReads(t, cache, path, "turn_m10")

		appendTurn(steeringAppendAssistantToolCall("followup-call", "followup_tool"))
		assertSteeringAppendReads(t, cache, path, "turn_m10")

		appendTurn(schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Content: []llm.ContentPart{{
				Kind:       llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: "followup-call", Name: "followup_tool", Content: "result"},
			}}},
		})
		assertSteeringAppendReads(t, cache, path, "turn_m10")

		appendTurn(schema.Turn{Kind: schema.TurnSteering, StableTurnID: "mutation_m12", OwningTurnID: "turn_m11", Message: llm.User("delayed steer")})
		assertSteeringAppendReads(t, cache, path, "turn_m11")
		appendTurn(steeringAppendAssistantToolCall("delayed-call", "delayed_tool"))
		assertSteeringAppendReads(t, cache, path, "turn_m11")
		appendTurn(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "delayed-call", Name: "delayed_tool", Content: "delayed result"}}}}})
		assertSteeringAppendReads(t, cache, path, "turn_m11")
	})
}

func steeringAppendAssistantToolCall(id, name string) schema.Turn {
	return schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: []byte(`{"input":"value"}`)},
		}}},
	}
}

func assertSteeringAppendReads(t *testing.T, cache *TurnCache, path, owner string) {
	t.Helper()
	index, _, err := cache.loadTurnIndexContext(context.Background(), path, testMaxLineBytes, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := openGroupState(index); got != owner {
		t.Fatalf("append owner = %q, want persisted logical owner %q", got, owner)
	}
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:steering-append-" + owner,
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("%s LatestItemWindowFromFile: %v", owner, err)
	}
	if len(window.Candidates) != 1 {
		t.Fatalf("%s latest candidate count=%d, want 1", owner, len(window.Candidates))
	}
	gotItems := append([]appwire.ThreadItem(nil), window.Candidates[0].Item)
	for window.OlderCursor != "" {
		window, _, err = cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
			ThreadRef: "local:steering-append-" + owner,
			Cursor:    window.OlderCursor,
			Limit:     1,
		}, boundedTestProjector)
		if err != nil {
			t.Fatalf("%s PreviousItemWindowFromFile: %v", owner, err)
		}
		gotItems = append([]appwire.ThreadItem{window.Candidates[0].Item}, gotItems...)
	}
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	wantIDs := []string{"turn_m10"}
	if owner == "turn_m11" {
		wantIDs = append(wantIDs, owner)
	}
	if got := turnIDs(full); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("%s full turn IDs=%v, want %v", owner, got, wantIDs)
	}

	wantItems := flattenSteeringAppendItems(full)
	if len(gotItems) != len(wantItems) {
		t.Fatalf("%s item count=%d, want %d (full turns=%d)", owner, len(gotItems), len(wantItems), len(full))
	}
	for i := range wantItems {
		got, want := gotItems[i], wantItems[i]
		if got.TranscriptKey != want.TranscriptKey || got.Position == nil || want.Position == nil || *got.Position != *want.Position {
			t.Fatalf("%s item %d identity=(key %q, position %+v), want (key %q, position %+v)", owner, i, got.TranscriptKey, got.Position, want.TranscriptKey, want.Position)
		}
	}

	var paged []appwire.Turn
	cursor := ""
	for {
		page := requirePageFromFile(t, cache, path, testMaxLineBytes, cursor, 1, boundedTestProjector)
		paged = append(page.Turns, paged...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(paged, full) {
		t.Fatalf("%s full/page projection differ: full=%#v paged=%#v", owner, full, paged)
	}
}

func flattenSteeringAppendItems(turns []appwire.Turn) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0)
	for _, turn := range turns {
		items = append(items, turn.Items...)
	}
	return items
}
