package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A tool result's document bytes are billed only where the adapter puts them on
// the wire. The Anthropic and Responses builders write image media only (#4209;
// a document media type written into an image block is rejected), so a PDF tool
// result prices the same as its text alone. Google likewise omits non-image
// bytes from function responses to avoid duplicating side-channel documents.
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
	for _, row := range []registry.Resolved{
		{Instance: "gateway", ModelID: "doc-model", Protocol: registry.ProtocolAnthropic},
		{Instance: "gateway", ModelID: "doc-model", Protocol: registry.ProtocolOpenAIResponses},
		{
			Instance: "google", ModelID: "gemini-3-pro", Protocol: registry.ProtocolGoogle,
			Caps: registry.Caps{MultimodalToolResults: &declared},
		},
	} {
		if got, want := EstimateMessagesInputTokensForResolved(row, pdfResult()).Tokens, textOnly(row); got != want {
			t.Errorf("%s tool-result document = %d, want the text-only estimate %d: the builder never serializes the bytes", row.Protocol, got, want)
		}
	}
}
