package apptranscript

import (
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Round 9: an unpaired communicate flushed at end-of-transcript lands at Item
// == the sidecar group item count. cursorBoundaryRank's strict >= rejection
// makes a cursor naming that position permanently stale, so a follow-up
// PreviousItemWindowFromFile errors instead of returning older items. These
// tests pin the fix: accept the flushed position for the tail group and
// suppress re-emission of flushed items on backward pages.

// isTranscriptItemCursorStale reports whether err is the stale-cursor WireError
// that cursorBoundaryRank returns for an out-of-bounds item position.
func isTranscriptItemCursorStale(err error) bool {
	if wireErr, ok := errors.AsType[appwire.WireError](err); ok {
		if data, ok := wireErr.Data.(appwire.ErrorData); ok {
			return data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
		}
	}
	return false
}

// singleFlushedFixture persists one logical turn whose assistant entry issues a
// communicate call with no paired result: 2 indexed items (userMessage +
// assistant text) and 1 flushed agentMessage at position 2. With itemLimit=1
// the latest window picks the flushed item and emits an OlderCursor naming
// {Item: 2} — exactly the sidecar count, which cursorBoundaryRank rejects.
func singleFlushedFixture() []transcript.Entry {
	return []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_flushed_single",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"hello there"}`),
			},
			}}}}},
	}
}

// doubleFlushedFixture persists one logical turn with 3 indexed items (user +
// 2 assistant texts) and 2 flushed agentMessages at positions 3 and 4. This
// pins the >= vs > boundary: itemLimit == flushed count (2) and itemLimit <
// count (1) must both page.
func doubleFlushedFixture() []transcript.Entry {
	return []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "first response"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_flushed_a",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"message alpha"}`),
			},
			}}}}},
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "second response"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_flushed_b",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"message beta"}`),
			},
			}}}}},
	}
}

// TestFlushedCursorPreviousWindowReturnsOlderItems is the exact repro: a
// transcript ending with one unpaired communicate. A latest-window read with
// itemLimit=1 picks the flushed agentMessage (SelectCandidates picks the
// newest). The follow-up PreviousItemWindowFromFile with that window's
// OlderCursor must RETURN OLDER ITEMS, not TranscriptItemCursorStale.
func TestFlushedCursorPreviousWindowReturnsOlderItems(t *testing.T) {
	path := writeEntries(t, singleFlushedFixture()...)
	cache := NewTurnCache()

	// Latest window with itemLimit=1: the flushed communicate is the newest item.
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_flushed_single",
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	if len(window.Candidates) != 1 {
		t.Fatalf("latest window has %d candidates, want 1: %+v", len(window.Candidates), window.Candidates)
	}
	if _, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{window.Candidates[0].Item}); !ok {
		t.Fatalf("latest window candidate is not a flushed communicate: %+v", window.Candidates[0].Item)
	}
	if window.OlderCursor == "" {
		t.Fatalf("latest window has no OlderCursor; need one to page above the flushed item")
	}

	// Follow-up page: PreviousItemWindowFromFile with the flushed item's cursor.
	prev, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_flushed_single",
		Cursor:    window.OlderCursor,
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		if isTranscriptItemCursorStale(err) {
			t.Fatalf("PreviousItemWindowFromFile returned TranscriptItemCursorStale for a cursor naming a flushed item; expected older indexed items: %v", err)
		}
		t.Fatalf("PreviousItemWindowFromFile: %v", err)
	}
	if len(prev.Candidates) == 0 {
		t.Fatalf("previous window returned no items; expected at least one older indexed item")
	}
	// The returned item must be an ordinary indexed item, not a flushed communicate.
	for _, c := range prev.Candidates {
		if _, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			t.Errorf("previous window returned a flushed communicate instead of an older indexed item: %+v", c.Item)
		}
	}
}

// TestFlushedCursorBoundaryMultipleTrailingCommunicates pins the >= vs >
// boundary: with 2 trailing unpaired communicates, itemLimit == their count
// (2) and itemLimit < count (1) must both page to older indexed items without
// TranscriptItemCursorStale.
func TestFlushedCursorBoundaryMultipleTrailingCommunicates(t *testing.T) {
	path := writeEntries(t, doubleFlushedFixture()...)
	cache := NewTurnCache()

	// itemLimit == flushed count (2): latest window picks the 2 flushed items.
	window2, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_flushed_double",
		Limit:     2,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile (limit=2): %v", err)
	}
	flushedCount := 0
	for _, c := range window2.Candidates {
		if _, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			flushedCount++
		}
	}
	if flushedCount != 2 {
		t.Fatalf("limit=2 latest window has %d flushed candidates, want 2: %+v", flushedCount, window2.Candidates)
	}
	if window2.OlderCursor == "" {
		t.Fatalf("limit=2 latest window has no OlderCursor")
	}

	// Follow-up with itemLimit=2: must page to older indexed items.
	prev2, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_flushed_double",
		Cursor:    window2.OlderCursor,
		Limit:     2,
	}, boundedTestProjector)
	if err != nil {
		if isTranscriptItemCursorStale(err) {
			t.Fatalf("PreviousItemWindowFromFile (limit==count) returned TranscriptItemCursorStale: %v", err)
		}
		t.Fatalf("PreviousItemWindowFromFile (limit=2): %v", err)
	}
	if len(prev2.Candidates) == 0 {
		t.Fatalf("previous window (limit=2) returned no items; expected older indexed items")
	}
	for _, c := range prev2.Candidates {
		if _, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			t.Errorf("previous window (limit=2) returned a flushed communicate: %+v", c.Item)
		}
	}

	// itemLimit < flushed count (1): latest window picks 1 flushed item (the newest).
	window1, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_flushed_double_lt",
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile (limit=1): %v", err)
	}
	if len(window1.Candidates) != 1 {
		t.Fatalf("limit=1 latest window has %d candidates, want 1", len(window1.Candidates))
	}
	if _, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{window1.Candidates[0].Item}); !ok {
		t.Fatalf("limit=1 latest window candidate is not a flushed communicate: %+v", window1.Candidates[0].Item)
	}
	if window1.OlderCursor == "" {
		t.Fatalf("limit=1 latest window has no OlderCursor")
	}

	// Follow-up with itemLimit=1: must page to older indexed items.
	prev1, _, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_flushed_double_lt",
		Cursor:    window1.OlderCursor,
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		if isTranscriptItemCursorStale(err) {
			t.Fatalf("PreviousItemWindowFromFile (limit<count) returned TranscriptItemCursorStale: %v", err)
		}
		t.Fatalf("PreviousItemWindowFromFile (limit=1): %v", err)
	}
	if len(prev1.Candidates) == 0 {
		t.Fatalf("previous window (limit=1) returned no items; expected older indexed items")
	}
	for _, c := range prev1.Candidates {
		if _, ok := findAnyFlushedCommunicateItem([]appwire.ThreadItem{c.Item}); ok {
			t.Errorf("previous window (limit=1) returned a flushed communicate: %+v", c.Item)
		}
	}
}

// TestFlushedCursorInvariantOrdinaryItemsPaging proves cursors naming ordinary
// indexed items still validate exactly as before the fix: an ordinary-landing
// transcript (no unpaired communicates) pages the same way pre- and post-fix.
// This is a regression guard — it must pass at head and after the fix.
func TestFlushedCursorInvariantOrdinaryItemsPaging(t *testing.T) {
	fixture := []transcript.Entry{
		userEntry(1, "do the thing"),
		assistantTextEntry(2, "first response"),
		assistantTextEntry(3, "second response"),
		assistantTextEntry(4, "third response"),
	}
	path := writeEntries(t, fixture...)
	cache := NewTurnCache()

	// Walk oldest-to-newest with itemLimit=1, one item per page.
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_ordinary_invariant",
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}

	var pages [][]appwire.ThreadItem
	for {
		if len(window.Candidates) > 0 {
			pages = append([][]appwire.ThreadItem{{window.Candidates[0].Item}}, pages...)
		}
		if window.OlderCursor == "" {
			break
		}
		window, _, err = cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
			ThreadRef: "local:th_ordinary_invariant",
			Cursor:    window.OlderCursor,
			Limit:     1,
		}, boundedTestProjector)
		if err != nil {
			t.Fatalf("PreviousItemWindowFromFile: %v", err)
		}
	}

	// 4 indexed items (user + 3 assistant texts): 4 one-item pages.
	if len(pages) != 4 {
		t.Fatalf("got %d one-item pages, want 4", len(pages))
	}
	// No page may contain a flushed communicate.
	for _, p := range pages {
		if item, ok := findAnyFlushedCommunicateItem(p); ok {
			t.Errorf("ordinary-landing transcript rendered a flushed communicate: %+v", item)
		}
	}
	// Verify the pages have distinct keys (chronological walk, no repeats).
	seenKeys := map[string]bool{}
	for i, p := range pages {
		key := p[0].TranscriptKey
		if key == "" {
			t.Errorf("page %d has empty transcript key", i)
		}
		if seenKeys[key] {
			t.Errorf("page %d repeats transcript key %q", i, key)
		}
		seenKeys[key] = true
	}
}
