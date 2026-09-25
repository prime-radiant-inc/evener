package apptranscript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// textlessAssistantEchoesAssistantTextFixture is the four-entry shape from
// issue #2320 finding 2: a text-bearing assistant turn followed by a
// text-less assistant turn whose healed communicate message echoes the
// first turn's text. All four entries are continuations of the same logical
// turn (no USER_INPUT opener; the first ASSISTANT starts a group that stays
// open for continuations).
//
//  1. ASSISTANT(text "the answer", read_file)
//  2. TOOL_RESULTS(read_file result, IsError=false)
//  3. ASSISTANT(no text, communicate, Arguments={}, RawArguments=`{message: "the answer"}`)
//  4. TOOL_RESULTS(communicate result, IsError=false — healed)
//
// The live projector records lastAssistantText only when text is non-empty
// and scopes the echo check to the logical turn (matchesLastAssistantMessage).
// Since all four entries share one turn, the healed communicate echoes the
// first turn's text within the same turn → live suppresses the echo and
// renders ONE agentMessage. Before the fix, ProjectTurn unconditionally set
// reg.LastAssistantText at the end of every ASSISTANT record, zeroing it on
// the text-less record 3; the healed communicate's echo check then failed
// (LastAssistantText was "") and reload rendered TWO agentMessages.
func textlessAssistantEchoesAssistantTextFixture() []transcript.Entry {
	const commRawArgs = `{message: "the answer"}` // malformed JSON — bare key, healed
	return []transcript.Entry{
		// 1. ASSISTANT(text "the answer", read_file)
		{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "the answer"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:        "call_read_echo",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"README.md"}`),
			}},
		}}}},
		// 2. TOOL_RESULTS(read_file result, IsError=false)
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_read_echo",
				Name:       "read_file",
				Content:    "file content",
				IsError:    false,
			}},
		}}}},
		// 3. ASSISTANT(no text, communicate with malformed raw args echoing "the answer")
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID:           "call_comm_echo_turn",
				Name:         "communicate",
				Arguments:    json.RawMessage(`{}`),
				RawArguments: commRawArgs,
			}},
		}}}},
		// 4. TOOL_RESULTS(communicate result, IsError=false — healed)
		{Kind: "entry", Seq: 4, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call_comm_echo_turn",
				Name:       "communicate",
				Content:    `{"accepted":true}`,
				IsError:    false,
			}},
		}}}},
	}
}

// countAgentMessagesWithText counts agentMessage items whose Text matches.
func countAgentMessagesWithText(items []appwire.ThreadItem, text string) int {
	count := 0
	for _, item := range items {
		if item.Type == "agentMessage" && item.Text == text {
			count++
		}
	}
	return count
}

// TestTextlessAssistantDoesNotZeroReloadEchoState verifies the four-entry shape
// from issue #2320 finding 2: a text-bearing assistant turn followed by a
// text-less assistant turn whose healed communicate echoes the first turn's
// text. All four entries share one logical turn, so the live projector scopes
// the echo check to that turn and suppresses the duplicate — live renders ONE
// agentMessage. Before the fix, ProjectTurn zeroed reg.LastAssistantText on
// the text-less record, so the healed communicate's echo check failed and
// reload rendered TWO. The fix stores LastAssistantTurnID on the registry and
// applies the echo check only when the result turn's turnID matches, mirroring
// matchesLastAssistantMessage.
func TestTextlessAssistantDoesNotZeroReloadEchoState(t *testing.T) {
	entries := textlessAssistantEchoesAssistantTextFixture()

	// Reload side: project all four entries through one shared registry with
	// the same turnID (all are continuations of the same logical turn, so the
	// full read threads one registry and one turnID across them).
	reg := NewToolCallRegistry()
	const turnID = "turn_1"
	var allItems []appwire.ThreadItem
	for i, entry := range entries {
		items := ProjectTurn(turnID, i+1, entry.Turn, reg, nil, nil)
		allItems = append(allItems, items...)
	}

	// The reload must render exactly ONE agentMessage with text "the answer"
	// (the assistant text from record 1). The healed communicate's echo must
	// be suppressed — it echoes the same text within the same logical turn.
	got := countAgentMessagesWithText(allItems, "the answer")
	if got != 1 {
		t.Fatalf("reload agentMessage count for %q = %d, want 1 (text only, echo suppressed); items: %+v", "the answer", got, allItems)
	}
}

// TestTextlessAssistantEchoDoesNotSuppressCrossTurnMessage verifies the fix
// does NOT suppress a genuine cross-turn message: when the healed communicate
// is in a DIFFERENT logical turn than the assistant text, the echo check must
// fail (turnIDs differ) and the communicate message must render. This is the
// "do not merely skip the empty overwrite" guard from the brief.
func TestTextlessAssistantEchoDoesNotSuppressCrossTurnMessage(t *testing.T) {
	entries := textlessAssistantEchoesAssistantTextFixture()

	// Project with DIFFERENT turnIDs: the assistant text is in turn_1, the
	// communicate result is in turn_2 (simulating two logical turns).
	reg := NewToolCallRegistry()
	turnIDs := []string{"turn_1", "turn_1", "turn_2", "turn_2"}
	var allItems []appwire.ThreadItem
	for i, entry := range entries {
		items := ProjectTurn(turnIDs[i], i+1, entry.Turn, reg, nil, nil)
		allItems = append(allItems, items...)
	}

	// The cross-turn healed communicate must render — the echo check must
	// fail because the result turn's turnID (turn_2) differs from the
	// assistant text's turnID (turn_1).
	got := countAgentMessagesWithText(allItems, "the answer")
	if got != 2 {
		t.Fatalf("cross-turn agentMessage count for %q = %d, want 2 (text + genuine cross-turn communicate); items: %+v", "the answer", got, allItems)
	}
}
