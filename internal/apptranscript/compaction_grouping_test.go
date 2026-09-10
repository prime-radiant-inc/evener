package apptranscript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestCompactionOwnershipAcrossIncrementalItemReaders(t *testing.T) {
	record := func(kind schema.TurnKind, stable, owner string) schema.Turn {
		turn := schema.NewTurn(kind, llm.System("fixture"))
		turn.StableTurnID, turn.OwningTurnID = stable, owner
		if kind == schema.TurnContextCompaction {
			turn.ContextCompaction = &schema.ContextCompaction{Layer: "fixture", TurnsBefore: 8, TurnsAfter: 4}
		}
		return turn
	}
	replay := func(turn schema.Turn) schema.Turn {
		turn.ContextReplay = true
		return turn
	}
	cases := []struct {
		name string
		tail []schema.Turn
		ids  []string
	}{
		{"context recovery copies preserve the open group", []schema.Turn{
			record(schema.TurnSummary, "summary", "turn_active"),
			replay(record(schema.TurnUserInput, "turn_active", "")),
			replay(record(schema.TurnAssistant, "assistant", "")),
			replay(record(schema.TurnRoundTimings, "timing", "turn_active")),
			record(schema.TurnSteering, "steering", "turn_active"),
			record(schema.TurnUserInput, "turn_next", ""),
		}, []string{"turn_active", "turn_next"}},
		{"context recovery copies preserve a closed boundary", []schema.Turn{
			record(schema.TurnSummary, "summary", ""),
			replay(record(schema.TurnUserInput, "turn_active", "")),
			replay(record(schema.TurnAssistant, "assistant", "")),
			record(schema.TurnAssistant, "continuation", ""),
		}, []string{"turn_active", "summary", "continuation"}},
		{"owned sequence", []schema.Turn{
			record(schema.TurnContextCompaction, "layer", "turn_active"),
			record(schema.TurnCheckpoint, "checkpoint", "turn_active"),
			record(schema.TurnSummary, "summary", "turn_active"),
			record(schema.TurnSteering, "steering", "turn_active"),
		}, []string{"turn_active"}},
		{"assistant after owned checkpoint", []schema.Turn{
			record(schema.TurnCheckpoint, "checkpoint", "turn_active"),
			record(schema.TurnAssistant, "assistant", ""),
		}, []string{"turn_active"}},
		{"unowned steering after owned summary", []schema.Turn{
			record(schema.TurnSummary, "summary", "turn_active"),
			record(schema.TurnSteering, "steering", ""),
		}, []string{"turn_active"}},
		{"unowned checkpoint closes group", []schema.Turn{
			record(schema.TurnCheckpoint, "boundary", ""),
			record(schema.TurnAssistant, "assistant", ""),
		}, []string{"turn_active", "boundary", "assistant"}},
		{"matching identity does not reopen unowned gap", []schema.Turn{
			record(schema.TurnCheckpoint, "boundary", ""),
			record(schema.TurnContextCompaction, "layer", "boundary"),
		}, []string{"turn_active", "boundary", "boundary"}},
		{"different owner opens group", []schema.Turn{
			record(schema.TurnCheckpoint, "checkpoint", "turn_other"),
			record(schema.TurnSummary, "summary", "turn_other"),
		}, []string{"turn_active", "turn_other"}},
		{"old owner does not cross gap", []schema.Turn{
			record(schema.TurnSummary, "summary", ""),
			record(schema.TurnContextCompaction, "layer", "turn_active"),
		}, []string{"turn_active", "summary", "turn_active"}},
		{"late owner after next input is a paging fragment", []schema.Turn{
			record(schema.TurnUserInput, "turn_next", ""),
			record(schema.TurnContextCompaction, "layer", "turn_active"),
		}, []string{"turn_active", "turn_next", "turn_active"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEntries(t, reservedUserEntry(1, "input", "turn_active"))
			cache := NewTurnCache()
			requireLatestFromFile(t, cache, path, testMaxLineBytes, 1, boundedTestProjector)
			for i, turn := range tc.tail {
				appendFile(t, path, marshalEntryLine(t, transcript.Entry{Kind: "entry", Seq: i + 2, Turn: turn}))
				full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
				for _, reader := range []*TurnCache{cache, NewTurnCache()} {
					indexed, _ := requireLatestFromFile(t, reader, path, testMaxLineBytes, 100, boundedTestProjector)
					if !reflect.DeepEqual(indexed, full) {
						t.Fatalf("after append %d indexed keys=%v IDs=%v, full keys=%v IDs=%v", i, keysForTurns(indexed), turnIDs(indexed), keysForTurns(full), turnIDs(full))
					}
					window, _, err := reader.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:compaction", Limit: 1}, boundedTestProjector)
					if err != nil {
						t.Fatal(err)
					}
					var paged []string
					for {
						var keys []string
						for _, candidate := range window.Candidates {
							keys = append(keys, candidate.Item.TranscriptKey)
						}
						paged = append(keys, paged...)
						if window.OlderCursor == "" {
							break
						}
						window, _, err = reader.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:compaction", Limit: 1, Cursor: window.OlderCursor}, boundedTestProjector)
						if err != nil {
							t.Fatal(err)
						}
					}
					if want := keysForTurns(full); !reflect.DeepEqual(paged, want) {
						t.Fatalf("item page keys=%v, want full keys=%v", paged, want)
					}
				}
			}
			full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
			if got := turnIDs(full); !reflect.DeepEqual(got, tc.ids) {
				t.Fatalf("logical turn IDs=%v, want %v", got, tc.ids)
			}
		})
	}
}

func TestContextCompactionWithoutNumbersHasNoRawPayload(t *testing.T) {
	items := ProjectTurn("turn_active", 1, schema.Turn{Kind: schema.TurnContextCompaction, ContextCompaction: &schema.ContextCompaction{}}, nil, nil, nil)
	if len(items) != 1 || items[0].EventKind != appwire.ThreadItemEventKindContextCompaction || items[0].Raw != nil {
		t.Fatalf("empty compaction payload must match the live item contract: %+v", items)
	}
}
