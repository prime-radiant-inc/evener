package appprojector

import (
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// agentMessageItems collects every agentMessage item carried by an item/started
// or item/completed notification, so a test can assert on what the envelope
// actually gained from a round.
func agentMessageItems(notifications []AppNotification) []appwire.ThreadItem {
	var items []appwire.ThreadItem
	for _, n := range notifications {
		if n.Method != appwire.NotifyItemStarted && n.Method != appwire.NotifyItemCompleted {
			continue
		}
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if !ok || params.Item.Type != "agentMessage" {
			continue
		}
		items = append(items, params.Item)
	}
	return items
}

// TestProjectorToolOnlyRoundMintsNoAgentMessageItem covers the shape of every
// round that answers with tool calls alone: the agent still emits the text
// lifecycle (ASSISTANT_TEXT_END is the round's usage and finish-reason carrier)
// with empty text, and the envelope must gain no agentMessage item for it. The
// turn's usage still has to land, since that is the only reason the empty
// lifecycle is emitted at all.
func TestProjectorToolOnlyRoundMintsNoAgentMessageItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "read the file"}})

	start := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})
	if items := agentMessageItems(start); len(items) != 0 {
		t.Fatalf("assistant text start opened an agentMessage item: %+v", items)
	}
	end := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{
		Text:         "",
		Usage:        llm.Usage{InputTokens: 100, OutputTokens: 50},
		FinishReason: "tool_calls",
		Model:        "claude-opus-4-5",
	}})
	if items := agentMessageItems(end); len(items) != 0 {
		t.Fatalf("tool-only round completed an agentMessage item: %+v", items)
	}

	p.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "read_file",
		CallID:        "call_1",
		ArgumentsJSON: `{"path":"README.md"}`,
	}})
	sessionEnd := p.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	turn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if turn.Usage == nil {
		t.Fatalf("turn.Usage=nil, want the tool-only round's usage")
	}
	if turn.Usage.InputTokens != 100 || turn.Usage.OutputTokens != 50 {
		t.Fatalf("turn.Usage=%+v, want in=100 out=50", turn.Usage)
	}
}

// TestProjectorToolOnlyRoundWithoutTextStartMintsNoAgentMessageItem is the same
// round without a preceding ASSISTANT_TEXT_START: a stream that produced no text
// never emits one, so the End alone must not conjure an item either.
func TestProjectorToolOnlyRoundWithoutTextStartMintsNoAgentMessageItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "read the file"}})

	end := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{
		Usage: llm.Usage{InputTokens: 7, OutputTokens: 3},
		Model: "claude-opus-4-5",
	}})
	if items := agentMessageItems(end); len(items) != 0 {
		t.Fatalf("tool-only round completed an agentMessage item: %+v", items)
	}

	sessionEnd := p.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	turn := notificationTurn(t, sessionEnd, appwire.NotifyTurnCompleted)
	if turn.Usage == nil || turn.Usage.InputTokens != 7 || turn.Usage.OutputTokens != 3 {
		t.Fatalf("turn.Usage=%+v, want in=7 out=3", turn.Usage)
	}
}

// TestProjectorStreamedTextOpensItemBeforeFirstDelta pins the streaming shape a
// text-bearing round must keep: the item is opened with (and before) the first
// delta, because consumers key a delta by an item id they have already seen and
// drop it otherwise.
func TestProjectorStreamedTextOpensItemBeforeFirstDelta(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})

	first := p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "hi "}})
	if len(first) != 2 {
		t.Fatalf("first delta notifications=%+v, want item/started then the delta", first)
	}
	if first[0].Method != appwire.NotifyItemStarted || first[1].Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("first delta methods=%q,%q", first[0].Method, first[1].Method)
	}
	startedItem := notificationThreadItem(t, first, appwire.NotifyItemStarted)
	if startedItem.Type != "agentMessage" || startedItem.Status != appwire.TurnStatusInProgress {
		t.Fatalf("started item=%+v", startedItem)
	}
	deltaParams, ok := first[1].Params.(appwire.AgentMessageDeltaParams)
	if !ok {
		t.Fatalf("delta params=%T", first[1].Params)
	}
	if deltaParams.ItemID != startedItem.ID {
		t.Fatalf("delta item_id=%q, want the started item %q", deltaParams.ItemID, startedItem.ID)
	}

	second := p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "there"}})
	if len(second) != 1 || second[0].Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("second delta reopened the item: %+v", second)
	}

	end := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{
		Text:  "hi there",
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 4},
		Model: "claude-opus-4-5",
	}})
	completed := notificationThreadItem(t, end, appwire.NotifyItemCompleted)
	if completed.ID != startedItem.ID || completed.Text != "hi there" || completed.Status != "completed" {
		t.Fatalf("completed item=%+v, want %q completed with the streamed text", completed, startedItem.ID)
	}
}

// TestProjectorStreamedTextWithEmptyFinalKeepsStreamedText covers the round that
// streamed text and then reported empty final text: the accumulated deltas are
// the item's text, and the item still completes rather than dangling in
// progress.
func TestProjectorStreamedTextWithEmptyFinalKeepsStreamedText(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "partial"}})

	end := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: ""}})
	completed := notificationThreadItem(t, end, appwire.NotifyItemCompleted)
	if completed.Text != "partial" || completed.Status != "completed" {
		t.Fatalf("completed item=%+v, want the streamed text completed", completed)
	}
}

// TestProjectorUnstreamedTextMaterializesItemAtEnd covers the non-streaming
// round whose text only arrives on ASSISTANT_TEXT_END: the item is minted there
// and carried by item/completed alone, the shape userMessage and communicate
// items already use.
func TestProjectorUnstreamedTextMaterializesItemAtEnd(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})

	end := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "the answer"}})
	if hasAppNotification(end, appwire.NotifyItemStarted) {
		t.Fatalf("end opened a separate item: %+v", end)
	}
	completed := notificationThreadItem(t, end, appwire.NotifyItemCompleted)
	if completed.Type != "agentMessage" || completed.ID == "" || completed.Text != "the answer" || completed.Status != "completed" {
		t.Fatalf("completed item=%+v", completed)
	}
}

// TestProjectorTextThenToolCallsKeepsBothItems covers the mixed round: text
// followed by tool calls keeps the agent message and the tool item, both in the
// same turn.
func TestProjectorTextThenToolCallsKeepsBothItems(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	started := p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "claude-opus-4-5"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "I'll check."}})
	end := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "I'll check."}})
	message := notificationThreadItem(t, end, appwire.NotifyItemCompleted)
	if message.Text != "I'll check." || message.TurnID != turnID {
		t.Fatalf("agent message item=%+v, want %q in turn %q", message, "I'll check.", turnID)
	}

	toolStart := p.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})
	tool := notificationThreadItem(t, toolStart, appwire.NotifyItemStarted)
	if tool.Type != "commandExecution" || tool.ID == message.ID || tool.TurnID != turnID {
		t.Fatalf("tool item=%+v, want a distinct commandExecution in turn %q", tool, turnID)
	}
}
