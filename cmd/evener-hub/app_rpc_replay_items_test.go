package hub

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func TestAppItemsFromReplayTurnConvertsCommunicateToAgentMessage(t *testing.T) {
	toolNames := apptranscript.NewToolCallRegistry()
	// Finding 1a (round 6): ALL communicates are deferred to the paired tool
	// result, so the assistant turn alone renders no items — the message
	// is recovered from CommRawArgs at the result turn.
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "ASSISTANT",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "tool_call",
			ToolCall: &llm.ToolCallData{
				ID:        "call_1",
				Name:      "communicate",
				Arguments: []byte(`{"message":"done","end_turn":true}`),
			},
		}}},
	}, toolNames)

	if len(items) != 0 {
		t.Fatalf("assistant turn should defer communicate to result, got items=%+v", items)
	}

	// The result turn recovers the agentMessage from the seeded CommRawArgs.
	results := appItemsFromReplayTurn("turn_2", 2, schema.Turn{
		Kind: "TOOL_RESULTS",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind:       "tool_result",
			ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "communicate", Content: `{"accepted":true}`},
		}}},
	}, toolNames)
	if len(results) != 1 || results[0].Type != "agentMessage" || results[0].Text != "done" {
		t.Fatalf("communicate result should render agentMessage, got %+v", results)
	}
}

func TestAppItemsFromReplayTurnCarriesToolStateRaw(t *testing.T) {
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "TOOL_RESULTS",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "tool_result",
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_delegate_send",
				Name:       "delegate_send",
				Content:    "started delegate turn",
				ToolState:  []byte(`{"job_id":"job_1","status":"running"}`),
			},
		}}},
	}, apptranscript.NewToolCallRegistry())

	if len(items) != 1 || items[0].ToolName != "delegate_send" || items[0].Output != "started delegate turn" {
		t.Fatalf("tool result items=%+v", items)
	}
	if string(items[0].Raw) != `{"job_id":"job_1","status":"running"}` {
		t.Fatalf("tool result Raw = %s, want replay tool_state", items[0].Raw)
	}
}

func TestAppItemsFromReplayTurnProjectsThinking(t *testing.T) {
	raw := []byte(`{"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[` +
		`{"kind":"thinking","thinking":{"text":"Let me plan this out."}},` +
		`{"kind":"text","text":"The answer is 42."}` +
		`]}}}`)
	entry := unmarshalReplayEntry(t, raw)
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, apptranscript.NewToolCallRegistry())

	if len(items) != 2 {
		t.Fatalf("expected reasoning + agentMessage, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me plan this out." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
	if items[1].Type != "agentMessage" || items[1].Text != "The answer is 42." {
		t.Fatalf("agent message item=%+v", items[1])
	}
}

func TestAppItemsFromReplayTurnProjectsRedactedThinking(t *testing.T) {
	raw := []byte(`{"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[` +
		`{"kind":"redacted_thinking","thinking":{"redacted":true,"encrypted_content":"xyz"}},` +
		`{"kind":"text","text":"ok"}` +
		`]}}}`)
	entry := unmarshalReplayEntry(t, raw)
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, apptranscript.NewToolCallRegistry())

	if len(items) != 2 {
		t.Fatalf("expected reasoning + agentMessage, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text == "" || strings.Contains(items[0].Text, "xyz") {
		t.Fatalf("redacted reasoning item=%+v, want a placeholder that keeps the encrypted content out", items[0])
	}
}

func TestAppItemsFromReplayTurnProjectsWebSearch(t *testing.T) {
	raw := []byte(`{"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[` +
		`{"kind":"web_search","web_search":{"query":"go context","raw":{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://go.dev/ctx","title":"Context"}]}}}` +
		`]}}}`)
	entry := unmarshalReplayEntry(t, raw)
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, apptranscript.NewToolCallRegistry())
	if len(items) != 1 || items[0].Type != "commandExecution" || items[0].ToolName != "web_search" {
		t.Fatalf("web_search items=%+v", items)
	}
	if !strings.Contains(items[0].ArgumentsJSON, "go context") {
		t.Fatalf("args missing query: %s", items[0].ArgumentsJSON)
	}
	if !strings.Contains(items[0].Output, "Context") || !strings.Contains(items[0].Output, "https://go.dev/ctx") {
		t.Fatalf("output missing results: %q", items[0].Output)
	}
}

// A transcript file can hold any content kind the schema can decode. Replay
// still hands the client only what it can render: audio and document parts
// reach Images no more than they reach the live EventUserInput payload, which
// has no field for them at all.
func TestAppItemsFromReplayTurnKeepsNonImagePartsOutOfImages(t *testing.T) {
	raw := []byte(`{"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[` +
		`{"kind":"text","text":"summarize"},` +
		`{"kind":"document","document":{"file_name":"report.pdf","media_type":"application/pdf"}},` +
		`{"kind":"audio","audio":{"media_type":"audio/wav"}},` +
		`{"kind":"image","image":{"media_type":"image/png","data":"cG5n"}}` +
		`]}}}`)
	entry := unmarshalReplayEntry(t, raw)
	items := appItemsFromReplayTurn("turn_1", 1, entry.Turn, apptranscript.NewToolCallRegistry())
	if len(items) != 1 || items[0].Type != "userMessage" {
		t.Fatalf("expected userMessage, got %+v", items)
	}
	images := items[0].Images
	if len(images) != 1 {
		t.Fatalf("expected the picture alone, got %+v", images)
	}
	if images[0].Type != "input_image" || images[0].MediaType != "image/png" {
		t.Fatalf("image=%+v", images[0])
	}
}

func TestAppItemsFromReplayTurnDoesNotAcceptLegacyToolCallKind(t *testing.T) {
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "ASSISTANT",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "commandExecution",
			ToolCall: &llm.ToolCallData{
				ID:        "call_legacy",
				Name:      "read_file",
				Arguments: []byte(`{"file_path":"/tmp/example.txt"}`),
			},
		}}},
	}, apptranscript.NewToolCallRegistry())

	if len(items) != 0 {
		t.Fatalf("legacy commandExecution transcript part should be ignored, got %+v", items)
	}
}

func TestAppItemsFromReplayTurnAcceptsCurrentToolCallKind(t *testing.T) {
	toolNames := apptranscript.NewToolCallRegistry()
	items := appItemsFromReplayTurn("turn_1", 1, schema.Turn{
		Kind: "ASSISTANT",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "tool_call",
			ToolCall: &llm.ToolCallData{
				ID:        "call_read",
				Name:      "read_file",
				Arguments: []byte(`{"file_path":"/tmp/example.txt","intent":"Inspect example output."}`),
			},
		}}},
	}, toolNames)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want one tool item", items)
	}
	if got := items[0]; got.Type != "commandExecution" || got.CallID != "call_read" || !strings.Contains(got.ArgumentsJSON, "/tmp/example.txt") {
		t.Fatalf("tool item=%+v", got)
	}
	if items[0].Description != "Inspect example output." {
		t.Fatalf("tool description=%q", items[0].Description)
	}
}

func TestAppItemsFromReplayTurnSteeringCarriesImageMetadata(t *testing.T) {
	img := []byte("png")
	items := appItemsFromReplayTurn("turn_3", 3, schema.Turn{
		Kind: "STEERING",
		Message: llm.Message{Content: []llm.ContentPart{{
			Kind: "image",
			Image: &llm.ImageData{
				Data:      img,
				MediaType: "image/png",
			},
		}}},
	}, apptranscript.NewToolCallRegistry())

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	got := items[0]
	if got.Type != "steering" || got.Text == "" || len(got.Images) != 1 {
		t.Fatalf("steering item=%+v, want image placeholder and image metadata", got)
	}
	if got.Images[0].Metadata["sha"] != imageSha(img) || got.Images[0].Metadata["size"] != strconv.Itoa(len(img)) {
		t.Fatalf("image metadata=%+v, want sha/size", got.Images[0].Metadata)
	}
}

// A steer the human typed is a person speaking, and reload must say so.
// Decoded from the real on-disk JSON shape (agent/schema.Turn's
// steering_source tag) so the wire tag is under test, not just the field:
// the web UI's SteeringItem branches on source === "user" to render the steer
// exactly like a prompt (issue #24), and an empty Source is stripped by
// omitempty, leaving the client no source at all rather than a wrong one.
func TestAppItemsFromReplayTurnSteeringCarriesUserSource(t *testing.T) {
	raw := []byte(`{"turn":{"kind":"STEERING","steering_source":"user","message":{"role":"user","content":[` +
		`{"kind":"text","text":"new worktree"}` +
		`]}}}`)
	entry := unmarshalReplayEntry(t, raw)
	items := appItemsFromReplayTurn("turn_3", 3, entry.Turn, apptranscript.NewToolCallRegistry())

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	if items[0].Type != "steering" || items[0].Source != events.SteeringSourceUser {
		t.Fatalf("steering item=%+v, want type steering with source %q", items[0], events.SteeringSourceUser)
	}
}

// Daemon nudges carry no steering_source on disk, and must stay anonymous:
// they render as the quiet collapsible divider, never as user speech.
func TestAppItemsFromReplayTurnSteeringWithoutSourceStaysAnonymous(t *testing.T) {
	raw := []byte(`{"turn":{"kind":"STEERING","message":{"role":"user","content":[` +
		`{"kind":"text","text":"<SYSTEM-REMINDER>nudge</SYSTEM-REMINDER>"}` +
		`]}}}`)
	entry := unmarshalReplayEntry(t, raw)
	items := appItemsFromReplayTurn("turn_4", 4, entry.Turn, apptranscript.NewToolCallRegistry())

	if len(items) != 1 {
		t.Fatalf("items=%+v, want one steering item", items)
	}
	if items[0].Source != "" {
		t.Fatalf("steering item source=%q, want empty for a daemon nudge", items[0].Source)
	}
}

func TestAppItemsFromReplayTurnIncludesCompactionTurns(t *testing.T) {
	checkpoint := appItemsFromReplayTurn("turn_4", 4, schema.Turn{
		Kind:    "CHECKPOINT",
		Message: llm.Message{Content: []llm.ContentPart{{Kind: "text", Text: "[CONTEXT CHECKPOINT]\nfirst compacted state"}}},
	}, apptranscript.NewToolCallRegistry())
	if len(checkpoint) != 1 {
		t.Fatalf("checkpoint items=%+v", checkpoint)
	}
	if got := checkpoint[0]; got.Type != "systemMessage" || got.Description == "" || !strings.Contains(got.Text, "first compacted state") {
		t.Fatalf("checkpoint item=%+v", got)
	}

	summary := appItemsFromReplayTurn("turn_5", 5, schema.Turn{
		Kind:    "SUMMARY",
		Message: llm.Message{Content: []llm.ContentPart{{Kind: "text", Text: "[CONTEXT SUMMARY]\nsecond compacted state"}}},
	}, apptranscript.NewToolCallRegistry())
	if len(summary) != 1 {
		t.Fatalf("summary items=%+v", summary)
	}
	if got := summary[0]; got.Type != "systemMessage" || got.Description == "" || got.Description == checkpoint[0].Description || !strings.Contains(got.Text, "second compacted state") {
		t.Fatalf("summary item=%+v, want a description distinct from the checkpoint's %q", got, checkpoint[0].Description)
	}
}

// unmarshalReplayEntry decodes a replay transcript entry from its raw JSON,
// failing the test on malformed input.
func unmarshalReplayEntry(t *testing.T, raw []byte) transcript.Entry {
	t.Helper()
	var entry transcript.Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal replay entry: %v", err)
	}
	return entry
}
