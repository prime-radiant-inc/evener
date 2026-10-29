package chatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestToChatMessages_ThinkingAsTextReasoningOnly pins the ThinkingAsText arm
// for an assistant turn that thought but never answered: the reasoning becomes
// the message content and no reasoning field is set.
func TestToChatMessages_ThinkingAsTextReasoningOnly(t *testing.T) {
	turn := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "just thinking"}},
	}}}
	out, err := toChatMessages(turn, registry.Caps{ThinkingAsText: new(true), Fields: registry.Baseline(registry.ProtocolOpenAIChat)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["content"] != "just thinking" {
		t.Fatalf("content = %v, want the reasoning alone", out[0]["content"])
	}
	for _, field := range []string{"reasoning", "reasoning_content", "reasoning_details"} {
		if v, has := out[0][field]; has && v != nil {
			t.Fatalf("%s = %v, want none when thinking rides as text", field, v)
		}
	}
}

// TestToChatMessages_AssistantTextWithToolCall pins that an assistant turn
// carrying both text and tool calls keeps its text on the wire beside the
// calls, instead of dropping it.
func TestToChatMessages_AssistantTextWithToolCall(t *testing.T) {
	turn := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "let me check"},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}},
	}}}
	out, err := toChatMessages(turn, registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["content"] != "let me check" {
		t.Fatalf("content = %v, want the text kept beside tool_calls", out[0]["content"])
	}
	if calls, _ := out[0]["tool_calls"].([]map[string]any); len(calls) != 1 {
		t.Fatalf("tool_calls = %v", out[0]["tool_calls"])
	}
}

// TestToChatMessages_ToolResultNonStringContent pins the non-string arm of
// the tool-result content switch: any non-string value reaches the wire as
// its JSON encoding.
func TestToChatMessages_ToolResultNonStringContent(t *testing.T) {
	turn := []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Content: map[string]any{"rows": 2}}},
	}}}
	out, err := toChatMessages(turn, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["content"] != `{"rows":2}` || out[0]["tool_call_id"] != "c1" {
		t.Fatalf("tool message = %v", out[0])
	}
}

// TestToChatMessages_TextPartsJoinWithNewline pins the text-join separator:
// two text parts in one message become one newline-separated string, and two
// thinking parts join the same way on the reasoning field.
func TestToChatMessages_TextPartsJoinWithNewline(t *testing.T) {
	twoText := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "line one"},
		{Kind: llm.ContentText, Text: "line two"},
	}}}
	out, err := toChatMessages(twoText, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["content"] != "line one\nline two" {
		t.Fatalf("content = %q, want the parts joined by a newline", out[0]["content"])
	}

	twoThinking := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "step one"}},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "step two"}},
	}}}
	out, err = toChatMessages(twoThinking, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["reasoning_content"] != "step one\nstep two" {
		t.Fatalf("reasoning_content = %q, want the parts joined by a newline", out[0]["reasoning_content"])
	}
}

// TestToChatMessages_ImagePartShapes pins the multimodal image walk: a nil
// image part is skipped, inline bytes with no claimed media type default to
// png, and an image part with neither URL nor data is dropped.
func TestToChatMessages_ImagePartShapes(t *testing.T) {
	msg := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "look"},
		{Kind: llm.ContentImage, Image: nil},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte{1, 2, 3}, MediaType: ""}},
		{Kind: llm.ContentImage, Image: &llm.ImageData{}},
	}}}
	out, err := toChatMessages(msg, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := out[0]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v, want text plus one image part only", out[0]["content"])
	}
	if parts[0]["type"] != "text" {
		t.Fatalf("parts[0] = %v, want the text part first", parts[0])
	}
	imageURL, _ := parts[1]["image_url"].(map[string]any)
	if parts[1]["type"] != "image_url" || imageURL["url"] != "data:image/png;base64,AQID" {
		t.Fatalf("image part = %v, want a png data URI from the claimed media type default", parts[1])
	}
}

// TestRequestWithoutToolResultImagesPrivateCopy pins the strip the builder
// applies on rows without multimodal tool results: the image fields are
// removed from a private copy — the caller's request keeps its bytes, the
// non-matching parts pass through untouched, and later messages can carry the
// image-bearing result without the earlier scan short-circuiting.
func TestRequestWithoutToolResultImagesPrivateCopy(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "run"}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "note"},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "c1", Content: "shot",
				ImageData:      []byte("fake-bytes"),
				ImageMediaType: "image/png",
			}},
		}},
	}}
	stripped := requestWithoutToolResultImages(req)

	if got := req.Messages[1].Content[1].ToolResult; len(got.ImageData) == 0 || got.ImageMediaType != "image/png" {
		t.Fatalf("the caller's request was mutated: %+v", got)
	}
	toolContent := stripped.Messages[1].Content
	if toolContent[0].Text != "note" {
		t.Fatalf("the non-matching part must pass through: %+v", toolContent[0])
	}
	strippedResult := toolContent[1].ToolResult
	if len(strippedResult.ImageData) != 0 || strippedResult.ImageMediaType != "" || strippedResult.Content != "shot" {
		t.Fatalf("stripped result = %+v, want the image fields removed and the content kept", strippedResult)
	}
	if stripped.Messages[0].Content[0].Text != "run" {
		t.Fatalf("earlier messages must pass through: %+v", stripped.Messages[0])
	}

	// The copy is deep enough that mutating the stripped content cannot be
	// observed in the caller's request.
	stripped.Messages[1].Content[0].Text = "mutated"
	if req.Messages[1].Content[0].Text != "note" {
		t.Fatal("the stripped request shares the caller's content slice")
	}
}

// TestBuildBody_CacheControlMarksMultimodalContent pins the parts-array arm
// of the cache-control marker: a multimodal user message gets its marker on
// the last text part of the content array, not by promoting a string.
func TestBuildBody_CacheControlMarksMultimodalContent(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "look"},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.invalid/i.png"}},
		}},
	}}
	body := build(t, req, resolved(func(c *registry.Caps) { c.CacheControl = new("anthropic") }))
	msgs := body["messages"].([]map[string]any)
	content, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content = %#v, want the multimodal parts array", msgs[0]["content"])
	}
	marker, _ := content[0]["cache_control"].(map[string]any)
	if content[0]["type"] != "text" || marker == nil || marker["type"] != "ephemeral" {
		t.Fatalf("text part = %v, want the ephemeral marker on it", content[0])
	}
	if _, has := content[1]["cache_control"]; has {
		t.Fatalf("image part must not carry the marker: %v", content[1])
	}
	if !strings.HasPrefix(content[1]["image_url"].(map[string]any)["url"].(string), "https://example.invalid/i.png") {
		t.Fatalf("image part = %v", content[1])
	}
}
