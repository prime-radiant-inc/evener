package apptranscript

import (
	"reflect"
	"testing"
	"time"

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
		if kind == schema.TurnRoundTimings {
			// Without its measurements the record projects no item and its
			// group renders as nothing, which would hide the boundary this
			// table is about.
			turn.RoundTimings = &schema.RoundTimings{Round: 1, TotalRound: time.Second}
		}
		return turn
	}
	replay := func(turn schema.Turn) schema.Turn {
		turn.ContextReplay = true
		return turn
	}
	// A fold now writes its replay copies just AHEAD of its markers, tagging
	// every record of the run with its own id. This projection ignores the
	// tag, but a fixture that mirrors what the writer produces is what makes
	// the case about the on-disk order rather than about a hand-built shape.
	fold := func(turn schema.Turn) schema.Turn {
		turn.CompactionFoldID = "fold_grouping"
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
		// The same records as the case above, in the order the fold writes
		// them today: copies first, then the marker they belong to. Replay
		// records are skipped for items but keep their physical record and
		// inherit the open group from what precedes them (turn_index.go), so
		// the run has to group identically either way.
		{"copies ahead of their marker preserve the open group", []schema.Turn{
			fold(replay(record(schema.TurnUserInput, "turn_active", ""))),
			fold(replay(record(schema.TurnAssistant, "assistant", ""))),
			fold(replay(record(schema.TurnRoundTimings, "timing", "turn_active"))),
			fold(record(schema.TurnSummary, "summary", "turn_active")),
			fold(record(schema.TurnSteering, "steering", "turn_active")),
			record(schema.TurnUserInput, "turn_next", ""),
		}, []string{"turn_active", "turn_next"}},
		// The same run with no copy of an OPENER among its copies: this fold
		// ran mid-turn, so its first copy is an assistant record rather than
		// the user input the case above copies. It has to join the group
		// already open instead of starting one of its own, in the indexed
		// build as in the full one.
		//
		// A copy that is the first record of a transcript — a non-opener with
		// no group to join at all — is not reachable: a fold copies the turns
		// recorded while it ran (session_compaction.go:195, :257), and each of
		// those was written to this same transcript by the pair that logged it
		// (session.go:1727, :1776), so an original always precedes its copy.
		{"copies with no opener among them preserve the open group", []schema.Turn{
			fold(replay(record(schema.TurnAssistant, "assistant", ""))),
			fold(replay(record(schema.TurnRoundTimings, "timing", "turn_active"))),
			fold(record(schema.TurnSummary, "summary", "turn_active")),
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
		// The reviewer's sequence: the fragment above, and then the running
		// turn's next record. The fragment is one record of a turn that is
		// over; the assistant belongs to the turn that is still running, so it
		// must not join the fragment.
		// The reviewer's combination: a fragment for an EARLIER turn, then
		// metadata owned by the turn that is still running, then its next
		// record. The timing record's group is the running turn's own, so the
		// assistant belongs in it — and both projections have to say so.
		{"metadata for the running turn resumes it", []schema.Turn{
			record(schema.TurnAssistant, "assistant", ""),
			record(schema.TurnContextCompaction, "layer", "turn_other"),
			record(schema.TurnRoundTimings, "timing", "turn_active"),
			record(schema.TurnAssistant, "next", ""),
		}, []string{"turn_active", "turn_other", "turn_active"}},
		// A round's timing record is metadata about a round that is over,
		// exactly like the compaction record below it: arriving late it names
		// its own turn and takes nothing with it.
		{"a late owned timing record is a fragment too", []schema.Turn{
			record(schema.TurnUserInput, "turn_next", ""),
			record(schema.TurnRoundTimings, "timing", "turn_active"),
			record(schema.TurnAssistant, "assistant", ""),
		}, []string{"turn_active", "turn_next", "turn_active", "turn_next"}},
		{"a continuation after a late fragment belongs to the running turn", []schema.Turn{
			record(schema.TurnUserInput, "turn_next", ""),
			record(schema.TurnContextCompaction, "layer", "turn_active"),
			record(schema.TurnAssistant, "assistant", ""),
		}, []string{"turn_active", "turn_next", "turn_active", "turn_next"}},
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
