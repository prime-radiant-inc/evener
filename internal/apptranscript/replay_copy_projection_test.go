package apptranscript

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// replayCopyTranscript writes the shape a fold leaves behind: the round it
// copied, the copies themselves just ahead of the marker that claims them, the
// marker, and one more turn after it.
func replayCopyTranscript(t testing.TB) string {
	t.Helper()
	round := func(seq int, stable string) transcript.Entry {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User("ask "+stable))
		turn.StableTurnID = stable
		return transcript.Entry{Kind: "entry", Seq: seq, Turn: turn}
	}
	answer := func(seq int) transcript.Entry {
		return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer"))}
	}
	copyOf := func(seq int, entry transcript.Entry) transcript.Entry {
		turn := entry.Turn
		turn.ContextReplay = true
		turn.CompactionFoldID = "fold_projection"
		return transcript.Entry{Kind: "entry", Seq: seq, Turn: turn}
	}
	marker := schema.NewTurn(schema.TurnSummary, llm.System("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]"))
	marker.CompactionFoldID = "fold_projection"
	return writeEntries(t,
		round(1, "turn_m1"),
		answer(2),
		copyOf(3, round(1, "turn_m1")),
		copyOf(4, answer(2)),
		transcript.Entry{Kind: "entry", Seq: 5, Turn: marker},
		round(6, "turn_m2"),
	)
}

// A replay copy is a second physical record of a turn the file already holds.
// It carries no item of its own: the original owns the UI item, and the copy
// exists so the model's history survives an anchor. A reader that projects one
// shows the same turn twice, and the two readers of the same file then
// disagree about how many items a group has — which is a cache error, not a
// cosmetic duplicate.
func TestReplayCopiesProjectNoItems(t *testing.T) {
	path := replayCopyTranscript(t)

	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	texts := map[string]int{}
	for _, turn := range full {
		for _, item := range turn.Items {
			texts[strings.TrimSpace(item.Text)]++
		}
	}
	if got := texts["ask turn_m1"]; got != 1 {
		t.Fatalf("the copied user turn appears %d times in the full projection, want once: the copy is the same turn written down again", got)
	}
	if got := texts["answer"]; got != 1 {
		t.Fatalf("the copied assistant turn appears %d times in the full projection, want once", got)
	}

	// The bounded reader walks the same records one page at a time. Its group
	// item counts come from the index, which must count what the projection
	// emits — a copy that projects an item in one and not the other is the
	// mismatch the cache reports.
	cache := NewTurnCache()
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:replay", Limit: 1}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	var paged []appwire.ThreadItem
	for {
		for _, candidate := range window.Candidates {
			paged = append([]appwire.ThreadItem{candidate.Item}, paged...)
		}
		if window.OlderCursor == "" {
			break
		}
		window, _, err = cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{ThreadRef: "local:replay", Limit: 1, Cursor: window.OlderCursor}, boundedTestProjector)
		if err != nil {
			t.Fatalf("PreviousItemWindowFromFile: %v", err)
		}
	}
	pagedTexts := map[string]int{}
	for _, item := range paged {
		pagedTexts[strings.TrimSpace(item.Text)]++
	}
	if got := pagedTexts["ask turn_m1"]; got != 1 {
		t.Fatalf("the copied user turn appears %d times across the pages, want once", got)
	}
}

// A copy carries the same usage its original does, so a group that counts both
// reports the round twice — and a copy that consumed an item ordinal moves
// every later key. Both readers have to see the same entries for either to
// hold.
func TestReplayCopiesAddNoUsageAndNoOrdinals(t *testing.T) {
	spend := llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	opener := schema.NewTurn(schema.TurnUserInput, llm.User("ask"))
	opener.StableTurnID = "turn_m1"
	answer := schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer"))
	answer.Usage = spend
	copyOf := func(turn schema.Turn) schema.Turn {
		turn.ContextReplay = true
		turn.CompactionFoldID = "fold_usage"
		return turn
	}
	marker := schema.NewTurn(schema.TurnSummary, llm.System("[CONTEXT SUMMARY]"))
	marker.CompactionFoldID = "fold_usage"
	path := writeEntries(t,
		transcript.Entry{Kind: "entry", Seq: 1, Turn: opener},
		transcript.Entry{Kind: "entry", Seq: 2, Turn: answer},
		transcript.Entry{Kind: "entry", Seq: 3, Turn: copyOf(opener)},
		transcript.Entry{Kind: "entry", Seq: 4, Turn: copyOf(answer)},
		transcript.Entry{Kind: "entry", Seq: 5, Turn: marker},
	)

	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	for _, turn := range full {
		if turn.ID != "turn_m1" || turn.Usage == nil {
			continue
		}
		if turn.Usage.TotalTokens != 110 {
			t.Fatalf("the opener's group reports %d total tokens, want the round's 110 counted once", turn.Usage.TotalTokens)
		}
	}
	fullKeys := keysForTurns(full)

	indexed, _ := requireLatestFromFile(t, NewTurnCache(), path, testMaxLineBytes, 100, boundedTestProjector)
	if got := keysForTurns(indexed); !reflect.DeepEqual(got, fullKeys) {
		t.Fatalf("bounded keys=%v, full keys=%v: a copy moved one reader's ordinals and not the other's", got, fullKeys)
	}
}
