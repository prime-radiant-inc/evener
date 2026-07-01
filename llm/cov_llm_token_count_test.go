package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// richMessage exercises every ContentKind arm of estimateMessageInputParts.
func richMessage(t *testing.T) Message {
	t.Helper()
	return Message{
		Role:       RoleAssistant,
		Name:       "assistant-name",
		ToolCallID: "call-123",
		Content: []ContentPart{
			{Kind: ContentText, Text: "some prose here"},
			{Kind: ContentImage, Image: &ImageData{MediaType: "image/png", Data: pngImage(t, 40, 40)}},
			{Kind: ContentAudio, Audio: &AudioData{MediaType: "audio/wav", Data: []byte("aud")}},
			{Kind: ContentDocument, Document: &DocumentData{URL: "https://example.com/a.pdf", MediaType: "application/pdf", FileName: "a.pdf"}},
			{Kind: ContentToolCall, ToolCall: &ToolCallData{ID: "tc1", Name: "do_thing", Arguments: json.RawMessage(`{"x":1}`)}},
			{Kind: ContentToolResult, ToolResult: &ToolResultData{ToolCallID: "tc1", Name: "do_thing", Content: "string result"}},
			{Kind: ContentToolResult, ToolResult: &ToolResultData{ToolCallID: "tc2", Content: []byte("byte result")}},
			{Kind: ContentToolResult, ToolResult: &ToolResultData{ToolCallID: "tc3", Content: map[string]any{"k": "v"}}},
			{Kind: ContentToolResult, ToolResult: &ToolResultData{ToolCallID: "tc4", ImageData: pngImage(t, 30, 30), ImageMediaType: "image/png"}},
			{Kind: ContentThinking, Thinking: &ThinkingData{Text: "reasoning", Signature: "sig"}},
			{Kind: ContentRedThinking, Thinking: &ThinkingData{Text: "redacted", Signature: "rsig"}},
			{Kind: ContentWebSearch, WebSearch: &WebSearchData{Query: "golang coverage", Raw: json.RawMessage(`{"results":[]}`)}},
			{Kind: ContentKind("unhandled_future_kind"), Text: "goes through the default marshal arm"},
		},
	}
}

func TestEstimateInputTokens_AllContentKinds(t *testing.T) {
	req := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{richMessage(t)},
	}

	got := EstimateInputTokens(req)
	if got.Tokens <= 0 {
		t.Fatalf("Tokens = %d, want > 0", got.Tokens)
	}
	// Deterministic: identical input yields identical output.
	if again := EstimateInputTokens(req); again.Tokens != got.Tokens {
		t.Fatalf("estimate not deterministic: %d vs %d", got.Tokens, again.Tokens)
	}
	// The rich message must estimate to strictly more than a bare text message.
	bare := EstimateInputTokens(Request{Provider: "anthropic", Model: "claude-test", Messages: []Message{User("hi")}})
	if got.Tokens <= bare.Tokens {
		t.Fatalf("rich message tokens %d not greater than bare %d", got.Tokens, bare.Tokens)
	}
}

func TestEstimateOpenAIImageTokens(t *testing.T) {
	openaiReq := func(img *ImageData) Request {
		return Request{
			Provider: "openai",
			Model:    "gpt-4o",
			Messages: []Message{{Role: RoleUser, Content: []ContentPart{{Kind: ContentImage, Image: img}}}},
		}
	}

	t.Run("low detail is flat 85", func(t *testing.T) {
		got := EstimateInputTokens(openaiReq(&ImageData{MediaType: "image/png", Detail: "low", Data: pngImage(t, 100, 100)}))
		if got.Tokens != 85 {
			t.Fatalf("low-detail Tokens = %d, want 85", got.Tokens)
		}
	})

	t.Run("small high image tiles once", func(t *testing.T) {
		// 100x100: no scaling, short=100, one 512 tile each side → 85 + 170.
		got := EstimateInputTokens(openaiReq(&ImageData{MediaType: "image/png", Data: pngImage(t, 100, 100)}))
		if got.Tokens != 85+170 {
			t.Fatalf("Tokens = %d, want %d", got.Tokens, 85+170)
		}
	})

	t.Run("mid image hits short-side downscale", func(t *testing.T) {
		// 1000x1000: scaleWithin leaves it (<=2048), short=1000>768 downscales to
		// 768x768 → 85 + 170*ceilDiv(768,512)^2 = 85 + 170*4.
		got := EstimateInputTokens(openaiReq(&ImageData{MediaType: "image/png", Data: pngImage(t, 1000, 1000)}))
		if got.Tokens != 85+170*4 {
			t.Fatalf("Tokens = %d, want %d", got.Tokens, 85+170*4)
		}
	})

	t.Run("oversized image hits scaleWithin downscale", func(t *testing.T) {
		// 3000x1000: scaleWithin scales long side to 2048 → 2048x682, short<=768,
		// → 85 + 170*ceilDiv(2048,512)*ceilDiv(683,512) = 85 + 170*4*2.
		got := EstimateInputTokens(openaiReq(&ImageData{MediaType: "image/png", Data: pngImage(t, 3000, 1000)}))
		if got.Tokens != 85+170*4*2 {
			t.Fatalf("Tokens = %d, want %d", got.Tokens, 85+170*4*2)
		}
	})
}

func TestImageDimensions_LocalFileURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, pngImage(t, 57, 57), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}

	// Anthropic 57x57 → ceilDiv(57,28)^2 = 3*3 = 9, read from the local file URL.
	req := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{URL: path, MediaType: "image/png"}},
		}}},
	}
	if got := EstimateInputTokens(req); got.Tokens != 9 {
		t.Fatalf("local-file image Tokens = %d, want 9", got.Tokens)
	}
}

func TestEstimateImageTokens_FallbackWhenDimensionsUnknown(t *testing.T) {
	// Remote URL with no inline bytes: dimensions cannot be read, so the estimate
	// falls back to a media constant plus quarter-length metadata terms.
	url := "https://example.com/remote.png"
	img := &ImageData{URL: url, MediaType: "image/png", Detail: "high"}
	req := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{{Kind: ContentImage, Image: img}}}},
	}
	want := fallbackMediaTokens + len(url)/4 + len("image/png")/4 + len("high")/4
	if got := EstimateInputTokens(req); got.Tokens != want {
		t.Fatalf("fallback Tokens = %d, want %d", got.Tokens, want)
	}
}

func TestEstimateImageTokens_DefaultProviderFamily(t *testing.T) {
	// Unknown provider/model with decodable dimensions takes the default arm:
	// fallbackMediaTokens + quarter-length of media type and detail.
	img := &ImageData{MediaType: "image/png", Detail: "auto", Data: pngImage(t, 40, 40)}
	req := Request{
		Provider: "some-unknown-provider",
		Model:    "mystery-model",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{{Kind: ContentImage, Image: img}}}},
	}
	want := fallbackMediaTokens + len("image/png")/4 + len("auto")/4
	if got := EstimateInputTokens(req); got.Tokens != want {
		t.Fatalf("default-family Tokens = %d, want %d", got.Tokens, want)
	}
}
