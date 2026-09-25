package apptranscript

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Round 8: a standalone HOOK_COMPLETED turn between an assistant communicate
// call and its paired tool results closes the logical group, so the results
// land in a new group with a fresh ToolCallRegistry. The bounded reads
// instantiate a fresh registry per group, so CommRawArgs seeded by the
// assistant turn never reaches the result turn — the error item vanishes and
// the assistant group's per-group flush renders a spurious agentMessage. These
// tests hold the bounded reads to the full read's contract across that hook
// shape.

// hookCompletedEntry builds a HOOK_COMPLETED transcript entry — the standalone
// turn kind that closes the open logical group. A PreToolUse hook between an
// assistant's communicate call and its paired tool results is the shape that
// splits the communicate pairing across group boundaries (finding 1).
func hookCompletedEntry(seq int) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
		Kind:    schema.TurnHookCompleted,
		Message: llm.System("PreToolUse hook exit 0"),
		Hook:    &schema.HookInfo{Event: "PreToolUse", ExitCode: 0},
	}}
}

// hookCommunicateFixture persists the hook shape that splits a communicate
// pairing across group boundaries: the assistant seeds CommRawArgs in group 0,
// HOOK_COMPLETED closes group 0 and opens group 1, and the tool results land in
// group 2 with a fresh registry. The communicate carries valid JSON so the
// bounded read's per-group flush extracts a spurious agentMessage from
// CommRawArgs (consequence a).
func hookCommunicateFixture() []transcript.Entry {
	return []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_comm_hook",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"hello","end_turn":true}`),
			},
			}}}}},
		hookCompletedEntry(3),
		{Kind: "entry", Seq: 4, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_hook",
				Name:       "communicate",
				IsError:    true,
				PrevalOnly: true,
			}},
		}}}},
		assistantTextEntry(5, "all done"),
	}
}

// hookCommunicateMalformedFixture is the unrepairable-malformed variant: the
// raw bytes have no "message" field, so flushUnpairedCommunicateItems extracts
// no message and renders nothing — the error item vanishes in both the
// assistant group (flush skips) and the result group (fresh registry) on the
// bounded read (consequence b).
func hookCommunicateMalformedFixture() []transcript.Entry {
	const rawArgs = `{junk: 1` // malformed JSON; RepairJSON fixes quotes but no message field
	return []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:           "call_comm_hook_mal",
				Name:         "communicate",
				Arguments:    json.RawMessage(`{}`),
				RawArguments: rawArgs,
			},
			}}}}},
		hookCompletedEntry(3),
		{Kind: "entry", Seq: 4, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_hook_mal",
				Name:       "communicate",
				IsError:    true,
				PrevalOnly: true,
			}},
		}}}},
		assistantTextEntry(5, "all done"),
	}
}

// allTurnsItems flattens all items across all turns into a single slice.
func allTurnsItems(turns []appwire.Turn) []appwire.ThreadItem {
	var items []appwire.ThreadItem
	for _, turn := range turns {
		items = append(items, turn.Items...)
	}
	return items
}

// findAnyFlushedCommunicateItem returns the first flushed communicate
// agentMessage (ID prefix "item_assistant_flushed_") from a slice of items.
func findAnyFlushedCommunicateItem(items []appwire.ThreadItem) (appwire.ThreadItem, bool) {
	for _, item := range items {
		if item.Type == "agentMessage" && strings.HasPrefix(item.ID, "item_assistant_flushed_") {
			return item, true
		}
	}
	return appwire.ThreadItem{}, false
}

// assertNoFlushedCommunicate checks that no flushed communicate phantom appears
// in any turn.
func assertNoFlushedCommunicate(t *testing.T, turns []appwire.Turn, label string) {
	t.Helper()
	for _, turn := range turns {
		if item, ok := findAnyFlushedCommunicateItem(turn.Items); ok {
			t.Errorf("%s rendered a spurious flushed communicate agentMessage (phantom): %+v", label, item)
		}
	}
}

// TestHookShapeCommunicateParityWithFullRead is the round-8 parity oracle for
// the hook shape: a PrevalOnly-rejected communicate split across group
// boundaries by a HOOK_COMPLETED turn must render IDENTICALLY on the full read
// and the bounded (turn-paged and item-window) reads — error item present on
// BOTH, no spurious delivered agentMessage on the bounded side. Includes the
// unrepairable-malformed variant where the error item vanishes entirely on the
// bounded read.
func TestHookShapeCommunicateParityWithFullRead(t *testing.T) {
	t.Run("valid-JSON", func(t *testing.T) {
		hookCommunicateParity(t, hookCommunicateFixture(), `{"message":"hello","end_turn":true}`)
	})
	t.Run("unrepairable-malformed", func(t *testing.T) {
		hookCommunicateParity(t, hookCommunicateMalformedFixture(), `{junk: 1`)
	})
}

func hookCommunicateParity(t *testing.T, fixture []transcript.Entry, wantArgs string) {
	path := writeEntries(t, fixture...)

	// Full read: the reference. Threads one registry, pairs the communicate.
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	fullItem, ok := findCommunicateErrorItem(allTurnsItems(full))
	if !ok {
		t.Fatalf("full read must render the rejected communicate error item; items: %+v", allTurnsItems(full))
	}
	if fullItem.ArgumentsJSON != wantArgs {
		t.Fatalf("full read error item ArgumentsJSON = %q, want %q", fullItem.ArgumentsJSON, wantArgs)
	}
	assertNoFlushedCommunicate(t, full, "full read")

	// Turn-paging bounded read (projectIndexedGroup path).
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 50, boundedTestProjector)
	if len(page.Turns) != len(full) {
		t.Fatalf("paged read produced %d turns, want %d (full read)", len(page.Turns), len(full))
	}
	pageItem, ok := findCommunicateErrorItem(allTurnsItems(page.Turns))
	if !ok {
		t.Fatalf("paged read must render the rejected communicate error item (parity with full read); items: %+v", allTurnsItems(page.Turns))
	}
	assertCommunicateErrorItemMatches(t, pageItem, fullItem, "paged")
	assertNoFlushedCommunicate(t, page.Turns, "paged read")

	// Item-window bounded read (projectIndexedItemRanges path).
	window, _, err := NewTurnCache().LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_hook_parity",
		Limit:     40,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	var windowItem appwire.ThreadItem
	for _, c := range window.Candidates {
		if c.Item.Type == "commandExecution" && c.Item.ToolName == "communicate" && c.Item.PrevalOnly {
			windowItem = c.Item
		}
	}
	if windowItem.Type == "" {
		t.Fatalf("item-window read must render the rejected communicate error item (parity with full read); candidates: %+v", window.Candidates)
	}
	assertCommunicateErrorItemMatches(t, windowItem, fullItem, "item-window")
	for _, c := range window.Candidates {
		if c.Item.Type == "agentMessage" && strings.HasPrefix(c.Item.ID, "item_assistant_flushed_") {
			t.Errorf("item-window read rendered a spurious flushed communicate agentMessage (phantom): %+v", c.Item)
		}
	}
}

// TestHookShapeCommunicateAcrossPageBoundary proves the deferred communicate
// state survives a page/window boundary that splits the assistant turn (which
// seeds CommRawArgs) from its paired result turn across the hook-shaped group
// boundary. The item window is walked one item at a time so the rejected
// communicate's error item — produced by the result turn — lands on a page of
// its own. The error item must still carry the deferred raw bytes, and no page
// may render a spurious flushed agentMessage.
func TestHookShapeCommunicateAcrossPageBoundary(t *testing.T) {
	path := writeEntries(t, hookCommunicateFixture()...)
	const wantArgs = `{"message":"hello","end_turn":true}`

	cache := NewTurnCache()
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_hook_boundary",
		Limit:     1,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("LatestItemWindowFromFile: %v", err)
	}
	// Walk oldest-to-newest, one item per page.
	var pages [][]appwire.ThreadItem
	for {
		if len(window.Candidates) > 0 {
			pages = append([][]appwire.ThreadItem{{window.Candidates[0].Item}}, pages...)
		}
		if window.OlderCursor == "" {
			break
		}
		window, _, err = cache.PreviousItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
			ThreadRef: "local:th_hook_boundary",
			Cursor:    window.OlderCursor,
			Limit:     1,
		}, boundedTestProjector)
		if err != nil {
			t.Fatalf("PreviousItemWindowFromFile: %v", err)
		}
	}
	var errorItem appwire.ThreadItem
	found := false
	for _, p := range pages {
		if item, ok := findCommunicateErrorItem(p); ok {
			errorItem = item
			found = true
		}
		if item, ok := findAnyFlushedCommunicateItem(p); ok {
			t.Errorf("page-boundary walk rendered a spurious flushed communicate: %+v", item)
		}
	}
	if !found {
		t.Fatalf("no one-item page rendered the rejected communicate error item; pages: %+v", pages)
	}
	if errorItem.ArgumentsJSON != wantArgs {
		t.Errorf("page-boundary error item ArgumentsJSON = %q, want deferred raw bytes %q", errorItem.ArgumentsJSON, wantArgs)
	}
	if errorItem.Status != appwire.TurnStatusFailed {
		t.Errorf("page-boundary error item Status = %q, want %q", errorItem.Status, appwire.TurnStatusFailed)
	}
	if !errorItem.PrevalOnly {
		t.Error("page-boundary error item PrevalOnly = false, want true")
	}
}
