package apptranscript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// echoedCommunicateEntries is issue #7150's shape: an opener, an assistant
// message that announces communicate call com1 beside a command call, the
// command's result, and then the COMMUNICATE delivery that repeats the
// assistant text.
func echoedCommunicateEntries() []schema.Turn {
	opener := schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_e1", TurnKind: schema.TurnSpanExecution,
		Kind: schema.TurnUserInput, Message: llm.User("say it"),
	}
	assistant := schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_e1", Kind: schema.TurnAssistant, RoundID: "r_1",
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Done."},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "com1", Name: "communicate", Arguments: json.RawMessage(`{"message":"Done."}`)}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "cmd1", Name: "shell", Arguments: json.RawMessage(`{"command":"true"}`)}},
		}},
	}
	results := schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_e1", Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "cmd1", Name: "shell", Content: "ok"}},
		}},
	}
	delivery := schema.Turn{
		Format: schema.TurnFormatIdentity, TurnID: "turn_e1", Kind: schema.TurnCommunicate,
		Communicate: &schema.CommunicateInfo{CallID: "com1", EndTurn: true, Message: "Done."},
	}
	return []schema.Turn{opener, assistant, results, delivery}
}

// fullReadEntries projects the entries the way the hub's replay read does: one
// shared registry and the transcript read model's entry projector.
func fullReadEntries(t testing.TB, entries []schema.Turn) []appwire.Turn {
	t.Helper()
	reg := NewToolCallRegistry()
	decoded := make([]transcript.Entry, len(entries))
	for i := range entries {
		decoded[i] = transcript.Entry{Turn: entries[i]}
	}
	turns, err := itemTurnsFromEntries(transcript.Header{}, decoded, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := ProjectEntryParts(turnID, entryIndex, turn, reg, nil, nil)
		return items
	})
	if err != nil {
		t.Fatalf("itemTurnsFromEntries: %v", err)
	}
	return turns
}

// TestFullReadStampsEchoedCommunicateCallIDOnTheMessage covers #7150: the
// index binds an echoed delivery to the visible agentMessage it repeats
// (build.go's attachCommunicationEcho), so the full read must name the same
// call on that row.
func TestFullReadStampsEchoedCommunicateCallIDOnTheMessage(t *testing.T) {
	items := allTurnsItems(fullReadEntries(t, echoedCommunicateEntries()))
	var messages []appwire.ThreadItem
	for _, item := range items {
		if item.Type == "agentMessage" {
			messages = append(messages, item)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("agentMessages = %+v, want the delivery suppressed into the assistant row", messages)
	}
	if messages[0].Text != "Done." || messages[0].CallID != "com1" {
		t.Fatalf("agentMessage = %+v, want Done. stamped with com1", messages[0])
	}
}

// TestFullReadLeavesAGenuineDeliveryCallIDOnItsOwnRow is the other half: a
// delivery whose message is not the assistant text renders its own row, and
// the assistant row keeps no call.
func TestFullReadLeavesAGenuineDeliveryCallIDOnItsOwnRow(t *testing.T) {
	entries := echoedCommunicateEntries()
	entries[3].Communicate.Message = "Something else."
	items := allTurnsItems(fullReadEntries(t, entries))
	var got []string
	for _, item := range items {
		if item.Type == "agentMessage" {
			got = append(got, item.Text+"|"+item.CallID)
		}
	}
	want := []string{"Done.|", "Something else.|com1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("agentMessages = %q, want %q", got, want)
	}
}
