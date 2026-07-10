//go:build serffuzz

package events

import (
	"testing"

	"primeradiant.com/serf/llm"
)

func FuzzSessionEventToStreamEvent(f *testing.F) {
	f.Add(uint8(0), "delta", "call-1", "read_file")
	f.Add(uint8(4), "", "", "")
	f.Add(uint8(7), "mismatch", "call-2", "write_file")
	f.Add(uint8(8), "agent only", "call-3", "shell")

	f.Fuzz(func(t *testing.T, variant uint8, text, callID, toolName string) {
		switch variant % 9 {
		case 0:
			assertFuzzStreamMapping(t, SessionStartData{Model: text}, EventSessionStart, llm.StreamEventStreamStart, "", "", "", false)
		case 1:
			assertFuzzStreamMapping(t, SessionEndData{Reason: text}, EventSessionEnd, llm.StreamEventFinish, "", "", "", false)
		case 2:
			assertFuzzStreamMapping(t, AssistantTextStartData{Model: text}, EventAssistantTextStart, llm.StreamEventTextStart, "", "", "", false)
		case 3:
			assertFuzzStreamMapping(t, AssistantTextDeltaData{Delta: text}, EventAssistantTextDelta, llm.StreamEventTextDelta, text, "", "", false)
		case 4:
			assertFuzzStreamMapping(t, AssistantTextEndData{Text: text}, EventAssistantTextEnd, llm.StreamEventTextEnd, "", "", "", false)
		case 5:
			assertFuzzStreamMapping(t, ToolCallStartData{CallID: callID, ToolName: toolName}, EventToolCallStart, llm.StreamEventToolCallStart, "", callID, toolName, true)
		case 6:
			assertFuzzStreamMapping(t, ToolCallEndData{CallID: callID, ToolName: toolName}, EventToolCallEnd, llm.StreamEventToolCallEnd, "", callID, toolName, true)
		case 7:
			ev := SessionEvent{Kind: EventAssistantTextDelta, Data: ToolCallStartData{CallID: callID, ToolName: toolName}}
			if got := ev.ToStreamEvent(); got != nil {
				t.Fatalf("mismatched payload produced stream event: %+v", got)
			}
		case 8:
			ev := SessionEvent{Kind: EventWarning, Data: WarningData{Message: text}}
			if got := ev.ToStreamEvent(); got != nil {
				t.Fatalf("agent-only event produced stream event: %+v", got)
			}
		}
	})
}

func assertFuzzStreamMapping(t *testing.T, data EventData, wantKind EventKind, wantType llm.StreamEventType, wantDelta, wantCallID, wantToolName string, wantTool bool) {
	t.Helper()
	ev := SessionEvent{Kind: data.eventKind(), Data: data}
	if ev.Kind != wantKind {
		t.Fatalf("typed payload kind = %q, want %q", ev.Kind, wantKind)
	}
	got := ev.ToStreamEvent()
	if got == nil {
		t.Fatalf("ToStreamEvent(%q) = nil", wantKind)
	}
	if got.Type != wantType || got.Delta != wantDelta {
		t.Fatalf("stream envelope = %+v, want type=%q delta=%q", got, wantType, wantDelta)
	}
	if !wantTool {
		if got.ToolCall != nil {
			t.Fatalf("stream envelope unexpectedly carried tool call: %+v", got.ToolCall)
		}
		return
	}
	if got.ToolCall == nil || got.ToolCall.ID != wantCallID || got.ToolCall.Name != wantToolName {
		t.Fatalf("stream tool call = %+v, want id=%q name=%q", got.ToolCall, wantCallID, wantToolName)
	}
}
