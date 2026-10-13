package anthropic

import (
	"encoding/base64"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
)

// A user-message document part must reach the wire as Anthropic's document
// content block: the vision side-channel sends a PDF this way, and the API
// accepts base64 application/pdf documents in user messages (#4209).
func TestToAnthropicMessages_UserDocumentBlock(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "describe this"},
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.4 body"), MediaType: "application/pdf", FileName: "document.pdf"}},
	}}})
	if err != nil {
		t.Fatalf("user document: %v", err)
	}
	blocks := docBlocks(t, msgs, 0)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want text + document", len(blocks))
	}
	doc := blocks[1]
	if doc["type"] != "document" {
		t.Fatalf("block type = %v, want document", doc["type"])
	}
	src, _ := doc["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "application/pdf" {
		t.Fatalf("source = %#v, want base64 application/pdf", src)
	}
	if src["data"] != base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 body")) {
		t.Fatalf("source data = %v, want base64 of the document bytes", src["data"])
	}
}

// An inline document with no claimed media type reads as the PDF default the
// tool layer and the Responses builder apply.
func TestToAnthropicMessages_UserDocumentDefaultsToPDF(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.4")}},
	}}})
	if err != nil {
		t.Fatalf("user document: %v", err)
	}
	src, _ := docBlocks(t, msgs, 0)[0]["source"].(map[string]any)
	if src["media_type"] != "application/pdf" {
		t.Fatalf("source = %#v, want the application/pdf default", src)
	}
}

// A document with no bytes but a URL uses Anthropic's url source, mirroring the
// Responses builder's file_data fallback.
func TestToAnthropicMessages_UserDocumentURLBlock(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf", MediaType: "application/pdf"}},
	}}})
	if err != nil {
		t.Fatalf("user document url: %v", err)
	}
	doc := docBlocks(t, msgs, 0)[0]
	src, _ := doc["source"].(map[string]any)
	if doc["type"] != "document" || src["type"] != "url" || src["url"] != "https://example.com/a.pdf" {
		t.Fatalf("block = %#v, want a document url source", doc)
	}
}

// A document carrying both inline bytes and a URL sends the bytes: the inline
// data wins, as it does in the image helper and the Responses builder, so a
// caller that supplies both never silently switches sources.
func TestToAnthropicMessages_UserDocumentPrefersInlineData(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{
			Data: []byte("%PDF-1.4"), MediaType: "application/pdf", URL: "https://example.com/a.pdf",
		}},
	}}})
	if err != nil {
		t.Fatalf("user document: %v", err)
	}
	src, _ := docBlocks(t, msgs, 0)[0]["source"].(map[string]any)
	if src["type"] != "base64" || src["data"] != base64.StdEncoding.EncodeToString([]byte("%PDF-1.4")) {
		t.Fatalf("source = %#v, want the inline base64 source to win over the URL", src)
	}
}

// Anthropic assistant content has no document type (parity with the Responses
// builder, which rejects the kind in its assistant branch), so an assistant
// document part still fails loudly rather than being silently dropped.
func TestToAnthropicMessages_AssistantDocumentRejected(t *testing.T) {
	t.Parallel()
	_, _, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.4"), MediaType: "application/pdf"}},
	}}})
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("assistant document error = %v, want ConfigurationError", err)
	}
}

// A tool result's document bytes stay off the wire: an image block carrying a
// pdf media type is rejected by the API (#4209 comment), and the design keeps
// documents on the side-channel (llm.DeliversToolResultMedia is image-only), so
// the block carries the text alone.
func TestToAnthropicMessages_ToolResultDocumentBytesStayOffTheWire(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "c1", Name: "read_file", Content: "[document: a.pdf]",
			ImageData: []byte("%PDF-1.4"), ImageMediaType: "application/pdf",
		}},
	}}})
	if err != nil {
		t.Fatalf("tool-result document: %v", err)
	}
	block := docBlocks(t, msgs, 0)[0]
	if block["type"] != "tool_result" {
		t.Fatalf("block = %#v, want tool_result", block)
	}
	if got, ok := block["content"].(string); !ok || got != "[document: a.pdf]" {
		t.Fatalf("tool_result content = %#v, want the text alone", block["content"])
	}
}

// Image tool-result bytes still ride inline, unchanged.
func TestToAnthropicMessages_ToolResultImageBlock(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "c1", Name: "read_file", Content: "[image: a.png]",
			ImageData: []byte("png-bytes"), ImageMediaType: "image/png",
		}},
	}}})
	if err != nil {
		t.Fatalf("tool-result image: %v", err)
	}
	block := docBlocks(t, msgs, 0)[0]
	content, ok := block["content"].([]map[string]any)
	if !ok || len(content) != 2 || content[1]["type"] != "image" {
		t.Fatalf("tool_result content = %#v, want text + image", block["content"])
	}
}

// An empty media type reads as the tool layer's image default, so the image
// block still rides inline.
func TestToAnthropicMessages_ToolResultEmptyMediaIsAnImage(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "c1", Name: "read_file", Content: "[image]",
			ImageData: []byte("bytes"),
		}},
	}}})
	if err != nil {
		t.Fatalf("tool-result empty media: %v", err)
	}
	content, ok := docBlocks(t, msgs, 0)[0]["content"].([]map[string]any)
	if !ok || len(content) != 2 || content[1]["type"] != "image" {
		t.Fatalf("tool_result content = %#v, want text + image", content)
	}
}

func docBlocks(t *testing.T, msgs []map[string]any, i int) []map[string]any {
	t.Helper()
	if i >= len(msgs) {
		t.Fatalf("message %d missing; got %d messages", i, len(msgs))
	}
	blocks, ok := msgs[i]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("message %d content = %#v, want []map[string]any", i, msgs[i]["content"])
	}
	return blocks
}
