package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A tool result's document bytes are billed only where the adapter puts them on
// the wire. Anthropic's tool_result content carries a document block (#4465),
// so it prices the bytes; the Responses and Google tool-result shapes carry no
// document, so a PDF prices the same as its text alone there.
func TestEstimateMessagesInputTokensForResolved_BillsToolResultDocumentsOnlyWhereTheyRide(t *testing.T) {
	t.Parallel()
	pdfResult := func() []Message {
		return []Message{{Role: RoleTool, Content: []ContentPart{{Kind: ContentToolResult, ToolResult: &ToolResultData{
			ToolCallID: "call1", Name: "read_file", Content: "[document: a.pdf]",
			ImageData: testPNG1024(), ImageMediaType: "application/pdf",
		}}}}}
	}
	textOnly := func(row registry.Resolved) int {
		stripped := pdfResult()
		stripped[0].Content[0].ToolResult.ImageData = nil
		stripped[0].Content[0].ToolResult.ImageMediaType = ""
		return EstimateMessagesInputTokensForResolved(row, stripped).Tokens
	}
	declared := true
	cases := []struct {
		row    registry.Resolved
		billed bool
	}{
		{registry.Resolved{Instance: "gateway", ModelID: "doc-model", Protocol: registry.ProtocolAnthropic,
			Caps: registry.Caps{InputModalities: []string{"text", "pdf"}}}, true},
		{registry.Resolved{Instance: "gateway", ModelID: "doc-model", Protocol: registry.ProtocolAnthropic}, false},
		{registry.Resolved{Instance: "gateway", ModelID: "doc-model", Protocol: registry.ProtocolOpenAIResponses}, false},
		{registry.Resolved{
			Instance: "google", ModelID: "gemini-3-pro", Protocol: registry.ProtocolGoogle,
			Caps: registry.Caps{MultimodalToolResults: &declared},
		}, false},
	}
	for _, tc := range cases {
		got, want := EstimateMessagesInputTokensForResolved(tc.row, pdfResult()).Tokens, textOnly(tc.row)
		switch {
		case tc.billed && got <= want:
			t.Errorf("%s tool-result document = %d, want more than the text-only estimate %d: the bytes ride inline", tc.row.Protocol, got, want)
		case !tc.billed && got != want:
			t.Errorf("%s tool-result document = %d, want the text-only estimate %d: the builder never serializes the bytes", tc.row.Protocol, got, want)
		}
	}
}
