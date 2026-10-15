package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A tool result's document bytes are billed only where the adapter puts them on
// the wire. The Anthropic and Responses builders write image media only (#4209;
// a document media type written into an image block is rejected), so a PDF tool
// result prices the same as its text alone. Google's function-response
// inlineData carries the claimed media type, so the bytes ride there and are
// billed.
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
	for _, protocol := range []string{registry.ProtocolAnthropic, registry.ProtocolOpenAIResponses} {
		row := registry.Resolved{Instance: "gateway", ModelID: "doc-model", Protocol: protocol}
		if got, want := EstimateMessagesInputTokensForResolved(row, pdfResult()).Tokens, textOnly(row); got != want {
			t.Errorf("%s tool-result document = %d, want the text-only estimate %d: the builder never serializes the bytes", protocol, got, want)
		}
	}
	declared := true
	google := registry.Resolved{
		Instance: "google", ModelID: "gemini-3-pro", Protocol: registry.ProtocolGoogle,
		Caps: registry.Caps{MultimodalToolResults: &declared},
	}
	if got, want := EstimateMessagesInputTokensForResolved(google, pdfResult()).Tokens, textOnly(google); got <= want {
		t.Errorf("declared-cap google tool-result document = %d, want more than the text-only estimate %d: inlineData carries the bytes", got, want)
	}
}
