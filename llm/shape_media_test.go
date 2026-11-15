package llm

// Regression tests for the shared-shaping half of #4217: a direct Client
// caller must not send tool-result media the resolved row cannot see, and a
// target-metadata refresh that changes that visibility must invalidate a
// recorded estimate.

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestShapeRequestDropsToolResultMediaTheRowCannotSee(t *testing.T) {
	t.Parallel()
	req := Request{Messages: []Message{{Role: RoleTool, Content: []ContentPart{{
		Kind: ContentToolResult,
		ToolResult: &ToolResultData{
			ToolCallID: "c1", Content: "[image: chart.png]",
			ImageData: []byte("png-bytes"), ImageMediaType: "image/png",
		},
	}}}}}
	blind := registry.Resolved{Protocol: registry.ProtocolOpenAIChat}
	out := ShapeRequest(req, blind)
	if len(out.Messages[0].Content[0].ToolResult.ImageData) != 0 {
		t.Fatal("a blind row's shaped request kept tool-result image bytes")
	}
	if len(req.Messages[0].Content[0].ToolResult.ImageData) == 0 {
		t.Fatal("shaping mutated the caller's request")
	}
	if got := out.Messages[0].Content[0].ToolResult.Content; got != "[image: chart.png]" {
		t.Fatalf("shaping lost the tool result's text: %v", got)
	}
	seeing := registry.Resolved{
		Protocol: registry.ProtocolAnthropic,
		Caps:     registry.Caps{InputModalities: []string{"text", "image"}},
	}
	kept := ShapeRequest(req, seeing)
	if len(kept.Messages[0].Content[0].ToolResult.ImageData) == 0 {
		t.Fatal("an image-capable row lost its tool-result bytes")
	}
}

func TestEstimatorTargetsEquivalentComparesToolResultImageVisibility(t *testing.T) {
	t.Parallel()
	base := registry.Resolved{Instance: "gw", ModelID: "m", Protocol: registry.ProtocolAnthropic}
	blind, seeing := base, base
	blind.Caps.InputModalities = []string{"text"}
	seeing.Caps.InputModalities = []string{"text", "image"}
	if EstimatorTargetsEquivalent(blind, seeing) {
		t.Fatal("targets that differ in tool-result image visibility compared equivalent")
	}
}

// TestEstimatorTargetsEquivalentComparesToolResultDocumentVisibility pins #6076:
// a metadata refresh that changes whether an Anthropic row sees a PDF tool
// result changes what the estimator prices, so the cached measurement must be
// invalidated in both directions. The two rows carry the same protocol and image
// facts; only the effective PDF-delivery predicate differs.
func TestEstimatorTargetsEquivalentComparesToolResultDocumentVisibility(t *testing.T) {
	t.Parallel()
	base := registry.Resolved{Instance: "gw", ModelID: "m", Protocol: registry.ProtocolAnthropic}
	blind, capable := base, base
	blind.Caps.InputModalities = []string{"text", "image"}
	capable.Caps.InputModalities = []string{"text", "image", "pdf"}
	if RowSeesToolResultMedia(blind, "application/pdf") {
		t.Fatal("document-blind row still sees a PDF tool result; the fixture does not exercise the predicate")
	}
	if !RowSeesToolResultMedia(capable, "application/pdf") {
		t.Fatal("document-capable row does not see a PDF tool result; the fixture does not exercise the predicate")
	}
	if EstimatorTargetsEquivalent(capable, blind) {
		t.Fatal("a refresh from document-capable to document-blind compared equivalent")
	}
	if EstimatorTargetsEquivalent(blind, capable) {
		t.Fatal("a refresh from document-blind to document-capable compared equivalent")
	}
	if !EstimatorTargetsEquivalent(capable, capable) {
		t.Fatal("identical document-capable rows compared different")
	}
}
