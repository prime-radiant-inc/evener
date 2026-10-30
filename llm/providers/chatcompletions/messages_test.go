package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func assistantTurn(thinking, sig, text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: thinking, Signature: sig}},
		{Kind: llm.ContentText, Text: text},
	}}
}

func TestToChatMessages_RoleAndContentCaps(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: ""}, {Kind: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "f", Content: "ok"}}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "next"}}},
	}
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	caps.Fields[registry.FieldDeveloperRole] = true
	caps.ToolResultName = new(true)
	caps.StripEmptyContent = new(true)
	caps.AssistantAfterToolResult = new(true)
	out, err := toChatMessages(msgs, caps, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["role"] != "developer" {
		t.Fatalf("developer role: %v", out[0])
	}
	if out[1]["content"] != "hi" {
		t.Fatalf("empty text must be stripped: %v", out[1])
	}
	if out[3]["name"] != "f" {
		t.Fatalf("tool result name: %v", out[3])
	}
	if out[4]["role"] != "assistant" || out[4]["content"] != "" || out[5]["role"] != "user" {
		t.Fatalf("assistant turn must be inserted after the tool result: %v", out[4:])
	}
}

func TestToChatMessages_ReasoningReplay(t *testing.T) {
	base := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	turn := []llm.Message{assistantTurn("thought", "reasoning", "answer")}

	out, _ := toChatMessages(turn, base, false)
	if out[0]["reasoning"] != "thought" {
		t.Fatalf("Signature names the field the text arrived on: %v", out[0])
	}

	field := base
	field.ReasoningField = new("reasoning_content")
	out, _ = toChatMessages(turn, field, false)
	if out[0]["reasoning_content"] != "thought" || out[0]["reasoning"] != nil {
		t.Fatalf("ReasoningField wins over the signature: %v", out[0])
	}

	details := base
	details.ReasoningField = new("reasoning_details")
	out, _ = toChatMessages(turn, details, true)
	items, ok := out[0]["reasoning_details"].([]map[string]any)
	if !ok || len(items) != 1 || items[0]["text"] != "thought" || items[0]["type"] != "reasoning.text" {
		t.Fatalf("reasoning_details replay: %v", out[0])
	}

	signed := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought", Signature: "reasoning_details", EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig-1","format":"anthropic-claude-v1","index":0}]`}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}
	out, _ = toChatMessages(signed, base, false)
	items = out[0]["reasoning_details"].([]map[string]any)
	if len(items) != 1 || items[0]["text"] != "thought" || items[0]["signature"] != "sig-1" {
		t.Fatalf("signed item must absorb the text: %v", items)
	}

	asText := base
	asText.ThinkingAsText = new(true)
	out, _ = toChatMessages(turn, asText, false)
	if out[0]["content"] != "thought\n\nanswer" || out[0]["reasoning"] != nil {
		t.Fatalf("thinking as text: %v", out[0])
	}

	off := base
	off.Reasoning = new(false)
	out, _ = toChatMessages(turn, off, false)
	if out[0]["reasoning"] != nil || out[0]["reasoning_content"] != nil || out[0]["reasoning_details"] != nil {
		t.Fatalf("Reasoning=false drops replayed thinking: %v", out[0])
	}

	empty := base
	empty.EmptyReasoningContent = new(true)
	out, _ = toChatMessages([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "plain"}}}}, empty, false)
	if v, has := out[0]["reasoning_content"]; !has || v != "" {
		t.Fatalf("empty reasoning_content on assistant turns: %v", out[0])
	}
}

// chatImageMessage is a user message with a text prompt and one image part
// carrying the given detail.
func chatImageMessage(detail string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "look"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte{1}, MediaType: "image/png", Detail: detail}},
	}}
}

// imageCaps is caps carrying an image_detail, or no cap when the value is empty.
func imageCaps(detail string) registry.Caps {
	if detail == "" {
		return registry.Caps{}
	}
	return registry.Caps{ImageDetail: &detail}
}

// imageWireDetail is the detail the chat wire carries for the single image part
// of msg under caps, or nil when the field is absent.
func imageWireDetail(t *testing.T, caps registry.Caps, msg llm.Message) any {
	t.Helper()
	out, err := toChatMessages([]llm.Message{msg}, caps, false)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := out[0]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v", out[0]["content"])
	}
	return parts[1]["image_url"].(map[string]any)["detail"]
}

// TestToChatMessages_ImageDetailHonorsRowCap pins the wire detail an image part
// carries. The chat wire accepts only auto|low|high: the cap vocabulary's
// "original" (full resolution) reaches it as "high", "omit" drops the field,
// and a part that names no detail takes the row's cap. A part's own detail
// otherwise wins.
func TestToChatMessages_ImageDetailHonorsRowCap(t *testing.T) {
	if got := imageWireDetail(t, imageCaps("original"), chatImageMessage("")); got != "high" {
		t.Fatalf(`image_detail "original" wire detail = %v, want "high"`, got)
	}
	if got := imageWireDetail(t, imageCaps("omit"), chatImageMessage("")); got != nil {
		t.Fatalf(`image_detail "omit" must drop the detail field, got %v`, got)
	}
	if got := imageWireDetail(t, imageCaps("low"), chatImageMessage("")); got != "low" {
		t.Fatalf(`image_detail "low" wire detail = %v, want "low"`, got)
	}
	if got := imageWireDetail(t, registry.Caps{}, chatImageMessage("")); got != nil {
		t.Fatalf("no image_detail must drop the field, got %v", got)
	}
	if got := imageWireDetail(t, registry.Caps{}, chatImageMessage("original")); got != "high" {
		t.Fatalf(`part detail "original" must map to "high", got %v`, got)
	}
	if got := imageWireDetail(t, imageCaps("low"), chatImageMessage("high")); got != "high" {
		t.Fatalf("a part's own detail must win over the row cap, got %v", got)
	}
	if got := imageWireDetail(t, imageCaps("omit"), chatImageMessage("low")); got != nil {
		t.Fatalf(`image_detail "omit" must drop the field even when the part names a detail, got %v`, got)
	}
}

// TestToChatMessages_DocumentBecomesFilePart pins the wire form a document
// takes on the chat protocol across payload sources: a user message bearing
// a document part builds the multimodal content array with a file part
// carrying the bytes as a data URI, or the source URL when no bytes are set,
// and the bytes win when both are set, as in every other document-capable
// builder. The plain-text form silently drops the document, and the model
// answers the prompt with no document in hand (#5472).
func TestToChatMessages_DocumentBecomesFilePart(t *testing.T) {
	const pdfDataURI = "data:application/pdf;base64,JVBERi0xLjcgc3R1Yg=="
	cases := []struct {
		name     string
		document llm.DocumentData
		wantData string
	}{
		{"bytes", llm.DocumentData{Data: []byte("%PDF-1.7 stub"), MediaType: "application/pdf"}, pdfDataURI},
		{"bytes with a blank media type", llm.DocumentData{Data: []byte("%PDF-1.7 stub")}, pdfDataURI},
		{"url", llm.DocumentData{URL: "https://example.com/doc.pdf"}, "https://example.com/doc.pdf"},
		{"bytes win over a url", llm.DocumentData{Data: []byte("%PDF-1.7 stub"), MediaType: "application/pdf", URL: "https://example.com/doc.pdf"}, pdfDataURI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
				{Kind: llm.ContentDocument, Document: &tc.document},
				{Kind: llm.ContentText, Text: "describe this"},
			}}}
			caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
			out, err := toChatMessages(msgs, caps, false)
			if err != nil {
				t.Fatal(err)
			}
			parts, ok := out[0]["content"].([]map[string]any)
			if !ok {
				t.Fatalf("a document-bearing user message must build the multimodal array, got %T", out[0]["content"])
			}
			if len(parts) != 2 {
				t.Fatalf("content = %v, want the file part and the text part", parts)
			}
			if parts[0]["type"] != "file" {
				t.Fatalf("parts[0] = %v, want the file part first", parts[0])
			}
			file, ok := parts[0]["file"].(map[string]any)
			if !ok {
				t.Fatalf("file part carries a file object, got %v", parts[0])
			}
			if file["file_data"] != tc.wantData {
				t.Fatalf("file_data = %v, want %v", file["file_data"], tc.wantData)
			}
			if parts[1]["type"] != "text" || parts[1]["text"] != "describe this" {
				t.Fatalf("parts[1] = %v, want the text part riding along", parts[1])
			}
		})
	}
}

// TestToChatMessages_DocumentWithoutBytesStaysPlain pins the gate's negative:
// a document part with no bytes and no URL cannot become a file part, so the
// message keeps the plain-text form and the empty part is dropped rather than
// emitting a file part with nothing to carry.
func TestToChatMessages_DocumentWithoutBytesStaysPlain(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{MediaType: "application/pdf"}},
		{Kind: llm.ContentText, Text: "hi"},
	}}}
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	out, err := toChatMessages(msgs, caps, false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0]["content"] != "hi" {
		t.Fatalf("a document part with no payload must be dropped, got %v", out[0]["content"])
	}
}

// TestToChatMessages_NeverEmitsAnInvalidImageDetail pins the #4489 invariant:
// the chat wire accepts only auto|low|high, so no combination of row cap and
// part detail may put any other value on it.
func TestToChatMessages_NeverEmitsAnInvalidImageDetail(t *testing.T) {
	for _, capDetail := range []string{"", "original", "omit", "low", "high"} {
		for _, partDetail := range []string{"", "original", "low", "high", "auto", "medium", "ORIGINAL"} {
			d := imageWireDetail(t, imageCaps(capDetail), chatImageMessage(partDetail))
			if d == nil {
				continue
			}
			switch d {
			case "auto", "low", "high":
			default:
				t.Fatalf("cap %q part %q emitted the invalid detail %v", capDetail, partDetail, d)
			}
		}
	}
}
