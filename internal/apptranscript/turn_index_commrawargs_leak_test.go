package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// staleCommRawArgsLeakFixture persists a transcript where a before-window
// zero-item group's deferred communicate call is paired (delivered) by a
// before-window result group, then a later item-bearing group occupies the
// newest page. Layout:
//
//	prelude (SystemPrompt)              → slot 0
//	ASSISTANT text-less communicate      → zero-item group G1 (no slot)
//	USER_INPUT "thanks"                  → opens G2 (closes G1)
//	TOOL_RESULTS call_dup_leak delivered → continues G2, renders agentMessage +
//	                                        deletes CommRawArgs[call_dup_leak]
//	                                      G2 is item-bearing → slot 1
//	USER_INPUT "one more"                → opens G3 (closes G2)
//	                                      G3 is item-bearing → slot 2
//
// logicalTurnCount = 3 (prelude + G2 + G3); G1 is zero-item and consumes no
// slot. The communicate is PAIRED (consumed by the delivered TOOL_RESULT), so
// the full read shows exactly one regular agentMessage and the tail flush
// renders nothing. On the latest page (limit=1, cursor=""), only slot 2 (G3)
// is in window. Before the fix, the non-clearing seedRegistryFromRecord left
// G1's stale call_dup_leak in reg (G2's consume was skipped as before-window,
// and G3's empty-snapshot seed did not clear it), so the tail flush rendered a
// DUPLICATE flushed agentMessage attributed to G3.
func staleCommRawArgsLeakFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stale_commrawargs_leak.transcript.jsonl")
	header := transcript.Header{
		Kind:          "header",
		FormatVersion: transcript.FormatVersion,
		SessionID:     "stale_leak",
		SystemPrompt:  "You are Evener.",
	}
	entries := []transcript.Entry{
		// G1: text-less assistant turn whose only content is a deferred
		// communicate call. GroupItems == 0 (communicate deferred, no text).
		{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_dup_leak",
				Name:      "communicate",
				Arguments: json.RawMessage(`{"message":"paired delivered message","end_turn":true}`),
			}},
		}}}},
		// USER_INPUT opens G2, closing G1.
		userEntry(2, "thanks"),
		// TOOL_RESULTS delivers the communicate (IsError=false): renders the
		// agentMessage item and deletes CommRawArgs[call_dup_leak]. G2 is
		// item-bearing (the user item + the agentMessage).
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_dup_leak",
				Name:       "communicate",
				IsError:    false,
			}},
		}}}},
		// USER_INPUT opens G3 (item-bearing), closing G2.
		userEntry(4, "one more"),
	}
	data, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// countAgentMessages returns the number of agentMessage items across all turns.
func countAgentMessages(turns []appwire.Turn) int {
	count := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "agentMessage" {
				count++
			}
		}
	}
	return count
}

// TestPagedReadNoStaleCommRawArgsLeak verifies that a before-window zero-item
// group's paired communicate does not leak as a duplicate flushed agentMessage
// on the newest turn-paged page. The communicate is paired (delivered) by a
// before-window result group, so the full read shows exactly one regular
// agentMessage and no flushed phantom. Before the fix, the non-clearing
// seedRegistryFromRecord left the stale call in reg across pages, and the tail
// flush rendered a spurious duplicate on the latest page.
//
// RED at 51ab28db9a: the latest page (limit=1) renders a flushed
// agentMessage (the stale call_dup_leak) attributed to G3.
// GREEN at head: the authoritative seed clears the stale entry.
func TestPagedReadNoStaleCommRawArgsLeak(t *testing.T) {
	path := staleCommRawArgsLeakFixture(t)

	// Full read with a shared registry (the way the server projects), then
	// flush. The communicate is paired, so exactly one regular agentMessage
	// appears and the flush renders nothing.
	reg := NewToolCallRegistry()
	fullProjector := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(turn, turnID, turnIndex, reg)
	}
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, fullProjector)
	FlushUnpairedCommunicates(&full, reg)
	if n := countAgentMessages(full); n != 1 {
		t.Fatalf("full read agentMessage count = %d, want 1 (the paired deliver); items: %+v", n, allTurnsItems(full))
	}
	assertNoFlushedCommunicate(t, full, "full read")

	// Latest page (limit=1, cursor=""): only slot 2 (G3) is in window, tail=true.
	// Before the fix the stale CommRawArgs from G1 survived the non-clearing
	// seed at G3 and the tail flush rendered a duplicate flushed agentMessage.
	page := requirePageFromFile(t, NewTurnCache(), path, testMaxLineBytes, "", 1, boundedTestProjector)
	if len(page.Turns) == 0 {
		t.Fatalf("latest page produced 0 turns")
	}
	assertNoFlushedCommunicate(t, page.Turns, "latest page limit=1")
}
