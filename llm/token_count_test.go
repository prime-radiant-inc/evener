package llm

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
)

type countAdapter struct {
	name string
	got  Request
	out  InputTokenCount
	err  error
}

func (a *countAdapter) Name() string { return a.name }
func (a *countAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *countAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, ErrStreamUnsupported
}
func (a *countAdapter) CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error) {
	_ = ctx
	a.got = req
	return a.out, a.err
}

func TestEstimateInputTokens_ImageDataDoesNotScaleWithByteLength(t *testing.T) {
	reqWithImage := func(size int) Request {
		return Request{
			Provider: "openai-compatible",
			Model:    "m",
			Messages: []Message{{Role: RoleUser, Content: []ContentPart{
				{Kind: ContentText, Text: "describe this image"},
				{Kind: ContentImage, Image: &ImageData{MediaType: "image/png", Data: bytes.Repeat([]byte{0x89}, size)}},
			}}},
		}
	}

	small := EstimateInputTokens(reqWithImage(1))
	large := EstimateInputTokens(reqWithImage(1_500_000))

	if small.Tokens != large.Tokens {
		t.Fatalf("estimate scaled with raw image bytes: small=%d large=%d", small.Tokens, large.Tokens)
	}
	if large.Tokens > 1_000 {
		t.Fatalf("estimate counted raw image payload: got %d", large.Tokens)
	}
	if large.Exact {
		t.Fatalf("local estimate should not be exact")
	}
}

func TestEstimateInputTokens_GoogleImageTiles(t *testing.T) {
	req := Request{
		Provider: "google",
		Model:    "gemini-2.5-pro",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{MediaType: "image/png", Data: pngImage(t, 800, 800)}},
		}}},
	}

	got := EstimateInputTokens(req)
	if got.Tokens != 4*258 {
		t.Fatalf("Tokens = %d, want %d", got.Tokens, 4*258)
	}
	if got.Source != TokenCountSourceLocalEstimate {
		t.Fatalf("Source = %q, want %q", got.Source, TokenCountSourceLocalEstimate)
	}

	// Non-square image: ceilDiv(800,768)=2, ceilDiv(1600,768)=3 → 258*2*3=1548.
	// Addition instead of multiplication would give 258*(2+3)=1290, catching that bug.
	req2 := Request{
		Provider: "google",
		Model:    "gemini-2.5-pro",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{MediaType: "image/png", Data: pngImage(t, 800, 1600)}},
		}}},
	}
	got2 := EstimateInputTokens(req2)
	if got2.Tokens != 258*2*3 {
		t.Fatalf("non-square Tokens = %d, want %d", got2.Tokens, 258*2*3)
	}
}

func TestEstimateInputTokens_AnthropicImagePatches(t *testing.T) {
	// 57×57: ceilDiv(57,28)=3, so 3*3=9 tokens.
	// Floor division (57/28=2) would give 2*2=4, making the ceiling behaviour observable.
	req := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{MediaType: "image/png", Data: pngImage(t, 57, 57)}},
		}}},
	}

	got := EstimateInputTokens(req)
	if got.Tokens != 9 {
		t.Fatalf("Tokens = %d, want 9", got.Tokens)
	}
}

func TestClient_CountInputTokens_UsesAdapterCounter(t *testing.T) {
	c := NewClient()
	// Adapter returns only Tokens; Exact/Source/Provider/Model are deliberately blank
	// so the enrichment block in CountInputTokens must fill every field.
	a := &countAdapter{
		name: "counted",
		out:  InputTokenCount{Tokens: 123},
	}
	c.Register(a)

	got, err := c.CountInputTokens(context.Background(), Request{
		Provider: "counted",
		Model:    "m",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Tokens != 123 {
		t.Fatalf("Tokens = %d, want 123", got.Tokens)
	}
	if !got.Exact {
		t.Fatalf("Exact = false, want true")
	}
	if got.Source != TokenCountSourceProvider {
		t.Fatalf("Source = %q, want %q", got.Source, TokenCountSourceProvider)
	}
	if got.Provider != "counted" {
		t.Fatalf("Provider = %q, want counted", got.Provider)
	}
	if got.Model != "m" {
		t.Fatalf("Model = %q, want m", got.Model)
	}
	if a.got.Provider != "counted" {
		t.Fatalf("adapter request provider = %q, want counted", a.got.Provider)
	}
}

func TestClient_CountInputTokens_FallsBackWhenCounterUnsupported(t *testing.T) {
	c := NewClient()
	a := &countAdapter{
		name: "unsupported",
		err:  ErrInputTokenCountUnsupported,
	}
	c.Register(a)

	got, err := c.CountInputTokens(context.Background(), Request{
		Provider: "unsupported",
		Model:    "m",
		Messages: []Message{User("hello world")},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Exact {
		t.Fatalf("fallback estimate should not be exact: %+v", got)
	}
	if got.Source != TokenCountSourceLocalEstimate {
		t.Fatalf("Source = %q, want %q", got.Source, TokenCountSourceLocalEstimate)
	}
	if got.Provider != "unsupported" {
		t.Fatalf("Provider = %q, want unsupported", got.Provider)
	}
	if got.Tokens != len("hello world")/4 {
		t.Fatalf("Tokens = %d, want %d", got.Tokens, len("hello world")/4)
	}
}

func TestClient_CountInputTokens_FallsBackToLocalEstimate(t *testing.T) {
	c := NewClient()
	c.Register(&fakeAdapter{name: "plain"})

	got, err := c.CountInputTokens(context.Background(), Request{
		Provider: "plain",
		Model:    "m",
		Messages: []Message{User("hello world")},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Exact {
		t.Fatalf("fallback estimate should not be exact: %+v", got)
	}
	if got.Source != TokenCountSourceLocalEstimate {
		t.Fatalf("Source = %q, want %q", got.Source, TokenCountSourceLocalEstimate)
	}
	if got.Tokens != len("hello world")/4 {
		t.Fatalf("Tokens = %d, want %d", got.Tokens, len("hello world")/4)
	}
}

func pngImage(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}
