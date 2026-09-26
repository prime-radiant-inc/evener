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

// trailingZeroItemGroupCommunicateFixture persists a leading standalone turn
// (SUMMARY, which produces a systemMessage item so its group has GroupItems >
// 0) followed by a text-less assistant turn whose only content is a deferred
// communicate call with no paired result turn. The SUMMARY closes its group,
// so the assistant turn opens its own group. That group has GroupItems == 0
// (communicate deferred, no text). The full read projects every record before
// dropping empty groups, so CommRawArgs is seeded and the tail flush renders
// the message; the bounded turn-paged reader skips zero-item groups without
// projecting, so CommRawArgs is never seeded and the flush sees nothing.
func trailingZeroItemGroupCommunicateFixture() []transcript.Entry {
	return []transcript.Entry{
		// Leading standalone turn: SUMMARY with text produces a systemMessage
		// item, so its group has GroupItems > 0 and gets a slot.
		{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnSummary, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "compaction summary"},
		}}}},
		// Trailing text-less assistant turn with only a communicate call.
		// The SUMMARY closed its group, so this ASSISTANT starts its own
		// group. GroupItems == 0 (communicate deferred, no text). No
		// result turn follows — the communicate is unpaired.
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_trailing_zero",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"delivered at the tail"}`),
			}},
		}}}},
	}
}

// TestPagedReadFlushesTrailingZeroItemGroupCommunicate verifies that a trailing
// zero-item group (a text-less assistant turn with only a deferred communicate
// call, unpaired) seeds CommRawArgs on BOTH the full read and the bounded
// turn-paged read, so the tail flush renders the delivered message identically.
// Before the fix, the bounded reader skipped zero-item groups without
// projecting, so CommRawArgs was never seeded and the message vanished on
// paged reads while showing on the full read.
func TestPagedReadFlushesTrailingZeroItemGroupCommunicate(t *testing.T) {
	path := writeEntries(t, trailingZeroItemGroupCommunicateFixture()...)

	// Full read with flush: project with a shared registry (the way the
	// server's appTurnProjectionFromTranscriptFile does), then flush.
	reg := NewToolCallRegistry()
	fullProjector := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(turn, turnID, turnIndex, reg)
	}
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, fullProjector)
	FlushUnpairedCommunicates(&full, reg)
	if len(full) == 0 {
		t.Fatalf("full read produced 0 turns")
	}
	fullFlushed, ok := findAnyFlushedCommunicateItem(allTurnsItems(full))
	if !ok {
		t.Fatalf("full read with flush must render the trailing unpaired communicate; items: %+v", allTurnsItems(full))
	}
	if fullFlushed.Text != "delivered at the tail" {
		t.Fatalf("full read flushed communicate Text = %q, want %q", fullFlushed.Text, "delivered at the tail")
	}

	// Paged read (projectIndexedGroup path — flushes internally at the tail).
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 50, boundedTestProjector)
	if len(page.Turns) == 0 {
		t.Fatalf("paged read produced 0 turns")
	}
	pageFlushed, ok := findAnyFlushedCommunicateItem(allTurnsItems(page.Turns))
	if !ok {
		t.Fatalf("paged read must render the trailing unpaired communicate (parity with full read); items: %+v", allTurnsItems(page.Turns))
	}
	if pageFlushed.Text != fullFlushed.Text {
		t.Errorf("paged flushed Text = %q, want %q (full read parity)", pageFlushed.Text, fullFlushed.Text)
	}

	// The flushed item IDs must match (same call ID → same flushed ID).
	if pageFlushed.ID != fullFlushed.ID {
		t.Errorf("paged flushed ID = %q, want %q (full read parity)", pageFlushed.ID, fullFlushed.ID)
	}
	if !strings.HasPrefix(fullFlushed.ID, "item_assistant_flushed_call_trailing_zero") {
		t.Errorf("full read flushed ID = %q, want prefix item_assistant_flushed_call_trailing_zero", fullFlushed.ID)
	}
}
