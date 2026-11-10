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
