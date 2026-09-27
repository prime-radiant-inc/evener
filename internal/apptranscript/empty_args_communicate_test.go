package apptranscript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestHookShapeCommunicateIndexScanParity proves the index scan's
// GroupItems/logicalTurnCount agrees with the full read for the hook shape.
// The fixture omits the closing assistant text so the result group contains
// only the TOOL_RESULTS record: with the broken index scan (openReg reset on
// StartsGroup), the result record's GroupItems is 0 and the group is dropped
// from logicalTurnCount, diverging from the full read. The malformed variant
// isolates the index-scan issue (no per-group flush phantom).
func TestHookShapeCommunicateIndexScanParity(t *testing.T) {
	const rawArgs = `{junk: 1` // unrepairable: no flush phantom, isolates index-scan issue
	fixture := []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:           "call_comm_hook_idx",
				Name:         "communicate",
				Arguments:    json.RawMessage(`{}`),
				RawArguments: rawArgs,
			},
			}}}}},
		hookCompletedEntry(3),
		{Kind: "entry", Seq: 4, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_hook_idx",
				Name:       "communicate",
				IsError:    true,
				PrevalOnly: true,
			}},
		}}}},
	}
	path := writeEntries(t, fixture...)

	// Full read: the reference. Threads one registry, renders the error item.
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	fullItem, ok := findCommunicateErrorItem(allTurnsItems(full))
	if !ok {
		t.Fatalf("full read must render the rejected communicate error item; items: %+v", allTurnsItems(full))
	}

	// Index-scan parity: the bounded read's logical turn count must agree.
	cache := NewTurnCache()
	boundedCount := requireTurnCountFromFile(t, cache, path, testMaxLineBytes, boundedTestProjector)
	if boundedCount != len(full) {
		t.Fatalf("bounded logicalTurnCount = %d, want %d (full read turn count)", boundedCount, len(full))
	}

	// The bounded read must also render the error item (parity with full read).
	latest, _ := requireLatestFromFile(t, cache, path, testMaxLineBytes, 50, boundedTestProjector)
	latestItem, ok := findCommunicateErrorItem(allTurnsItems(latest))
	if !ok {
		t.Fatalf("bounded read must render the rejected communicate error item (index-scan parity); items: %+v", allTurnsItems(latest))
	}
	assertCommunicateErrorItemMatches(t, latestItem, fullItem, "bounded")
}

// TestEmptyArgsRejectedCommunicateRenders proves a communicate call whose
// original Arguments were zero-length renders a rejected error item on reload,
// matching the live path's error card. SentArguments() returns "" for empty
// Arguments, so CommRawArgs[id] is "". The reload branch gates on
// rawArgs != "" (finding 2), which suppresses the item; the live path has no
// such guard. The fixture uses a simple shape (no hook) to isolate finding 2
// from finding 1's group-boundary issue.
func TestEmptyArgsRejectedCommunicateRenders(t *testing.T) {
	fixture := []transcript.Entry{
		userEntry(1, "do the thing"),
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "thinking about it"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_comm_empty",
				Name:      "communicate",
				Arguments: json.RawMessage(``), // empty — SentArguments returns ""
			},
			}}}}},
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_empty",
				Name:       "communicate",
				IsError:    true,
				PrevalOnly: true,
			}},
		}}}},
		assistantTextEntry(4, "all done"),
	}
	path := writeEntries(t, fixture...)

	// Full read: must render the empty-args rejected communicate error item.
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	fullItem, ok := findCommunicateErrorItem(allTurnsItems(full))
	if !ok {
		t.Fatalf("full read must render the empty-args rejected communicate error item; items: %+v", allTurnsItems(full))
	}
	if fullItem.ArgumentsJSON != "" {
		t.Errorf("full read empty-args error item ArgumentsJSON = %q, want \"\"", fullItem.ArgumentsJSON)
	}
	if fullItem.Status != appwire.TurnStatusFailed {
		t.Errorf("full read empty-args error item Status = %q, want %q", fullItem.Status, appwire.TurnStatusFailed)
	}
	if !fullItem.PrevalOnly {
		t.Error("full read empty-args error item PrevalOnly = false, want true")
	}

	// Bounded read: must also render the error item (parity with full read).
	latest, _ := requireLatestFromFile(t, NewTurnCache(), path, testMaxLineBytes, 50, boundedTestProjector)
	latestItem, ok := findCommunicateErrorItem(allTurnsItems(latest))
	if !ok {
		t.Fatalf("bounded read must render the empty-args rejected communicate error item (parity with full read); items: %+v", allTurnsItems(latest))
	}
	assertCommunicateErrorItemMatches(t, latestItem, fullItem, "bounded")
}
