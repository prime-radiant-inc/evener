package llm

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestEstimateMessagesInputTokensIsAlwaysMarkedInexact(t *testing.T) {
	got := EstimateMessagesInputTokens([]Message{User("hello there, this is some text")})
	if got.Tokens <= 0 {
		t.Fatalf("Tokens = %d, want a positive estimate", got.Tokens)
	}
	// History-only accounting has no provider behind it, so mislabelling the
	// estimate as exact would let a caller trust a number no tokenizer produced.
	if got.Exact {
		t.Error("Exact = true, want false for a local estimate")
	}
	if got.Source != TokenCountSourceLocalEstimate {
		t.Errorf("Source = %q, want %q", got.Source, TokenCountSourceLocalEstimate)
	}

	// More history must never estimate fewer tokens, or callers trimming to fit
	// a window would trim in the wrong direction.
	more := EstimateMessagesInputTokens([]Message{
		User("hello there, this is some text"),
		Assistant("and here is a considerably longer reply that adds more content"),
	})
	if more.Tokens <= got.Tokens {
		t.Errorf("estimate for more history = %d, want more than %d", more.Tokens, got.Tokens)
	}

	if empty := EstimateMessagesInputTokens(nil); empty.Exact || empty.Source != TokenCountSourceLocalEstimate {
		t.Errorf("empty history = %+v, want an inexact local estimate", empty)
	}
}

// A thinking part whose raw text the adapter will not replay must not be billed
// to the context estimate. The OpenAI Responses adapter re-sends a reasoning
// item only when it carries an encrypted_content blob, so raw reasoning_text
// kept on the part (gateway-fronted GLM, for example) is display-only. A part
// that does carry replayable metadata is still counted.
func TestEstimateMessagesInputTokens_ExcludesNonReplayableThinking(t *testing.T) {
	rawText := strings.Repeat("r", 400)
	thinkingOnly := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: rawText}},
	}}}
	withReplay := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: rawText, EncryptedContent: "opaque-blob"}},
	}}}

	got := EstimateMessagesInputTokens(thinkingOnly).Tokens
	if got != 0 {
		t.Fatalf("non-replayable thinking estimate = %d, want 0 (raw reasoning_text is display-only)", got)
	}
	if replay := EstimateMessagesInputTokens(withReplay).Tokens; replay <= got {
		t.Fatalf("replayable thinking estimate = %d, want > %d", replay, got)
	}
}

// The replayed payload is the encrypted blob itself, not the part's display
// text: the Responses adapter sends encrypted_content (plus the summary and id)
// and ignores Text. A blob-only part must therefore still be billed, and the
// estimate must grow with the blob.
func TestEstimateMessagesInputTokens_BillsReplayedEncryptedBlob(t *testing.T) {
	big := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: strings.Repeat("b", 400)}},
	}}}
	small := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: "b"}},
	}}}
	got := EstimateMessagesInputTokens(big).Tokens
	if got == 0 {
		t.Fatalf("blob-only thinking estimate = 0, want the replayed blob billed")
	}
	if smallTokens := EstimateMessagesInputTokens(small).Tokens; smallTokens >= got {
		t.Fatalf("estimate did not grow with the blob: %d vs %d", smallTokens, got)
	}
}

// Redacted thinking replays only its text payload: anthropic/request.go emits
// "data": Text and never the signature, so a signature must not be billed.
// An OpenAI-compatible encrypted reasoning_details array is replayed together
// with the separately parsed text, so the text must be billed alongside the
// blob; ID and Summary never ride a compat blob.
func TestEstimateMessagesInputTokens_CompatEncryptedBlobBillsItsText(t *testing.T) {
	const blob = `[{"type":"reasoning.text","text":"","signature":"sig-1"}]`
	plain := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: blob}},
	}}}
	withText := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: blob, Text: strings.Repeat("t", 400)}},
	}}}
	a := EstimateMessagesInputTokens(plain).Tokens
	if b := EstimateMessagesInputTokens(withText).Tokens; b <= a {
		t.Fatalf("compat blob with replayed text = %d, want > %d", b, a)
	}
	extra := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: blob, ID: strings.Repeat("i", 400), Summary: []string{strings.Repeat("s", 400)}}},
	}}}
	if c := EstimateMessagesInputTokens(extra).Tokens; c != a {
		t.Fatalf("compat blob ID/Summary changed the estimate: %d vs %d", c, a)
	}
}

func TestEstimateMessagesInputTokens_RedactedThinkingBillsTextOnly(t *testing.T) {
	withSig := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentRedThinking, Thinking: &ThinkingData{Text: "redacted", Signature: strings.Repeat("s", 400)}},
	}}}
	textOnly := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentRedThinking, Thinking: &ThinkingData{Text: "redacted"}},
	}}}
	if a, b := EstimateMessagesInputTokens(withSig).Tokens, EstimateMessagesInputTokens(textOnly).Tokens; a != b {
		t.Fatalf("redacted signature changed the estimate: %d vs %d", a, b)
	}
}

// The Anthropic adapter replays a thinking part with no signature as an
// unsigned "thinking" block (anthropic/request.go emits {"type":"thinking",
// "thinking": text, "signature": ""}), so its text must be billed even though
// no replay metadata marks it. Before the provider-aware rule this was zero.
func TestEstimateInputTokens_AnthropicReplaysUnsignedThinkingText(t *testing.T) {
	rawText := strings.Repeat("t", 400)
	req := thinkingOnlyRequest("anthropic", "claude-test", rawText)

	if got, want := EstimateInputTokens(req).Tokens, len(rawText)/4; got != want {
		t.Fatalf("Tokens = %d, want %d: the Anthropic adapter replays unsigned thinking text", got, want)
	}

	// The same part on the provider-blind history path stays unbilled: that
	// entry point carries no adapter to speak for.
	if blind := EstimateMessagesInputTokens(req.Messages).Tokens; blind != 0 {
		t.Fatalf("EstimateMessagesInputTokens = %d, want 0 without a provider", blind)
	}
}

// The OpenAI-compatible chat adapter replays a thinking part's text on the
// reasoning field, and merges it into assistant content on a ThinkingAsText
// row (chatcompletions/messages.go), so a text-only part must be billed here
// too. Before the provider-aware rule this was zero.
func TestEstimateInputTokens_OpenAICompatChatReplaysUnsignedThinkingText(t *testing.T) {
	rawText := strings.Repeat("t", 400)
	req := Request{
		Provider: "openai-compatible",
		Model:    "glm-4.6",
		Messages: []Message{{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentThinking, Thinking: &ThinkingData{Text: rawText}},
		}}},
	}

	if got, want := EstimateInputTokens(req).Tokens, len(rawText)/4; got != want {
		t.Fatalf("Tokens = %d, want %d: the OpenAI-compatible chat adapter replays unsigned thinking text", got, want)
	}
}

// The fix must not over-count everywhere: the OpenAI Responses adapter keeps
// raw reasoning text for display only (responses/input.go replays an encrypted
// blob alone), Google drops thinking parts, and a provider alias the estimator
// cannot resolve to either replaying adapter is not billed.
func TestEstimateInputTokens_NonReplayingProvidersDoNotBillUnsignedThinking(t *testing.T) {
	rawText := strings.Repeat("t", 400)
	messages := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: rawText}},
	}}}
	for _, tc := range []struct{ name, provider, model string }{
		{name: "openai responses", provider: "openai", model: "gpt-5.2"},
		{name: "google", provider: "google", model: "gemini-2.5-pro"},
		{name: "unresolvable provider alias", provider: "work", model: "mystery-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Provider: tc.provider, Model: tc.model, Messages: messages}
			if got := EstimateInputTokens(req).Tokens; got != 0 {
				t.Fatalf("Tokens = %d, want 0: %s does not replay unsigned thinking text", got, tc.name)
			}
		})
	}
}

// Threading the provider must not disturb the arms that already had a count:
// redacted thinking, signed parts, and encrypted blobs keep the same
// characters whether the adapter replays unsigned text or not.
func TestEstimateInputTokens_ProviderAwareRuleLeavesOtherThinkingShapesUnchanged(t *testing.T) {
	shapes := map[string]ContentPart{
		"anthropic signature":              {Kind: ContentThinking, Thinking: &ThinkingData{Text: "reasoning", Signature: strings.Repeat("s", 64)}},
		"compat field name":                {Kind: ContentThinking, Thinking: &ThinkingData{Text: "reasoning", Signature: "reasoning_content"}},
		"opaque encrypted blob":            {Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: strings.Repeat("b", 64)}},
		"compat encrypted array with text": {Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig"}]`, Text: strings.Repeat("t", 64)}},
		"redacted with signature":          {Kind: ContentRedThinking, Thinking: &ThinkingData{Text: strings.Repeat("r", 64), Signature: strings.Repeat("s", 64)}},
	}
	for name, part := range shapes {
		t.Run(name, func(t *testing.T) {
			messages := []Message{{Role: RoleAssistant, Content: []ContentPart{part}}}
			replaying := EstimateInputTokens(Request{Provider: "anthropic", Model: "claude-test", Messages: messages}).Tokens
			nonReplaying := EstimateInputTokens(Request{Provider: "google", Model: "gemini-2.5-pro", Messages: messages}).Tokens
			if replaying != nonReplaying {
				t.Fatalf("provider-aware rule changed a metadata-bearing shape: anthropic=%d google=%d", replaying, nonReplaying)
			}
			if replaying == 0 {
				t.Fatalf("metadata-bearing shape estimated 0 tokens, want its replay payload billed")
			}
		})
	}
}

// The finding behind this rule is oversized-request accounting: an estimate
// that fits before the thinking text is billed must cross the window once the
// replayed text is counted.
func TestEstimateInputTokens_ReplayedThinkingCrossesAnOversizedRequestThreshold(t *testing.T) {
	const window = 1_000
	req := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{
			// 3,000 text characters → 750 estimated tokens.
			{Role: RoleUser, Content: []ContentPart{{Kind: ContentText, Text: strings.Repeat("f", 3_000)}}},
			// 1,200 replayed thinking characters → 300 more estimated tokens.
			{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentThinking, Thinking: &ThinkingData{Text: strings.Repeat("t", 1_200)}}}},
		},
	}

	if got := EstimateInputTokens(req).Tokens; got <= window {
		t.Fatalf("oversized-request estimate = %d, want > %d once replayed thinking text is billed", got, window)
	}

	// The same history on an adapter that does not replay unsigned text stays
	// under the window, so the crossing comes from the replayed text alone.
	nonReplaying := req
	nonReplaying.Provider, nonReplaying.Model = "google", "gemini-2.5-pro"
	if below := EstimateInputTokens(nonReplaying).Tokens; below > window {
		t.Fatalf("non-replaying estimate = %d, want <= %d", below, window)
	}
}

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

// testPNG1024 is the one image the image-accounting tests need. Encoding it per
// call allocates 4 MiB for bytes that never change, so it is built once.
var testPNG1024 = sync.OnceValue(func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
})

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

// thinkingOnlyRequest builds a one-message request whose only payload is a
// thinking part with text and no replay metadata, so the estimate isolates the
// unsigned-thinking rule.
func thinkingOnlyRequest(provider, model, text string) Request {
	return Request{
		Provider: provider,
		Model:    model,
		Messages: []Message{{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentThinking, Thinking: &ThinkingData{Text: text}},
		}}},
	}
}

// A curated OpenAI-compatible chat provider (ollama, groq, zai, ...) carries no
// marker in its name, so only the resolved row can say that its adapter replays
// unsigned thinking text. Admission must bill it: undercounting here is what
// lets an oversized request pass the local budget.
func TestApplyTokenBudget_ResolvedOpenAIChatProviderBillsUnsignedThinking(t *testing.T) {
	const text = 400 // chars → 100 tokens
	reasoning := true
	res := registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{Reasoning: &reasoning}}

	_, with, err := ApplyTokenBudget(thinkingOnlyRequest("ollama", "llama-thinking", strings.Repeat("t", text)), res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget(with thinking): %v", err)
	}
	_, without, err := ApplyTokenBudget(Request{Provider: "ollama", Model: "llama-thinking"}, res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget(without thinking): %v", err)
	}
	if got, want := with.InputTokens-without.InputTokens, text/4; got != want {
		t.Fatalf("thinking text added %d budget tokens, want %d: a resolved openai-chat row replays unsigned thinking text", got, want)
	}
}

// The other direction: a row declared non-reasoning that does not merge
// thinking into content drops the part, so billing it would fail the budget
// early and force needless compaction.
func TestApplyTokenBudget_NonReasoningOpenAIChatRowDoesNotBillUnsignedThinking(t *testing.T) {
	reasoning := false
	res := registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{Reasoning: &reasoning}}

	_, with, err := ApplyTokenBudget(thinkingOnlyRequest("openai-compatible", "plain-model", strings.Repeat("t", 400)), res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget(with thinking): %v", err)
	}
	_, without, err := ApplyTokenBudget(Request{Provider: "openai-compatible", Model: "plain-model"}, res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget(without thinking): %v", err)
	}
	if got := with.InputTokens - without.InputTokens; got != 0 {
		t.Fatalf("thinking text added %d budget tokens, want 0: the row drops the part", got)
	}
}

// The resolved row decides, not the name: the same openai-named provider bills
// the text only when its row says the adapter replays it.
func TestEstimateInputTokens_ResolvedRowBeatsTheNameFallback(t *testing.T) {
	const text = 400
	req := thinkingOnlyRequest("openai", "gpt-x", strings.Repeat("t", text))
	if got := EstimateInputTokens(req).Tokens; got != 0 {
		t.Fatalf("name-path estimate = %d, want 0: the curated openai rows speak Responses", got)
	}
	reasoning := true
	res := registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{Reasoning: &reasoning}}
	if got, want := EstimateInputTokensForResolved(res, req).Tokens, text/4; got != want {
		t.Fatalf("resolved estimate = %d, want %d: the row says openai-chat, which replays the text", got, want)
	}
}

// The provider name is consulted before the model name, so a Claude model under
// a Google row does not claim an Anthropic replay; a Claude model under an alias
// that selects nothing is still the best signal there is.
func TestEstimateInputTokens_ProviderNameDominatesTheClaudeModelFallback(t *testing.T) {
	const text = 400
	google := thinkingOnlyRequest("google", "claude-sonnet", strings.Repeat("t", text))
	if got := EstimateInputTokens(google).Tokens; got != 0 {
		t.Fatalf("estimate = %d, want 0: the google provider drops the part regardless of the model name", got)
	}
	alias := thinkingOnlyRequest("work", "claude-sonnet", strings.Repeat("t", text))
	if got, want := EstimateInputTokens(alias).Tokens, text/4; got != want {
		t.Fatalf("estimate = %d, want %d: an unresolved alias carrying a Claude model still bills", got, want)
	}
}

// The resolved row decides the media family too: a caller that supplied no
// names (the history entry point) must not fall back to the generic placeholder
// for a row whose protocol has its own image-token rules.
func TestEstimateMessagesInputTokensForResolved_UsesTheResolvedMediaFamily(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}

	anthropic := registry.Resolved{Instance: "anthropic", Protocol: registry.ProtocolAnthropic}
	if got, want := EstimateMessagesInputTokensForResolved(anthropic, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
		t.Fatalf("resolved anthropic image history = %d, want %d: the row's media family decides", got, want)
	}
	openai := registry.Resolved{Instance: "openai", Protocol: registry.ProtocolOpenAIResponses}
	if got, want := EstimateMessagesInputTokensForResolved(openai, messages).Tokens, estimateOpenAIImageTokens(1024, 1024, ""); got != want {
		t.Fatalf("resolved openai image history = %d, want %d", got, want)
	}
	// Without a row there is no identity to key on, so the generic fallback stands.
	if got, want := EstimateMessagesInputTokens(messages).Tokens, fallbackMediaTokens+len("image/png")/4; got != want {
		t.Fatalf("targetless image history = %d, want %d", got, want)
	}
}

// A model family is a property of the model, not of the wire protocol:
// OpenRouter serves anthropic/claude-* over openai-chat, and a Claude or Gemini
// image must keep its own tokenizer family. The surface decides before the
// protocol; without a surface the protocol still does.
func TestEstimateMessagesInputTokensForResolved_SurfaceBeatsTheWireProtocol(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}

	openrouterClaude := registry.Resolved{
		Instance: "openrouter", ModelID: "anthropic/claude-opus-5",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceAnthropic,
	}
	if got, want := EstimateMessagesInputTokensForResolved(openrouterClaude, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
		t.Fatalf("openrouter claude image history = %d, want %d: the surface keeps the model's own family", got, want)
	}
	openrouterGemini := registry.Resolved{
		Instance: "openrouter", ModelID: "google/gemini-3-pro",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGoogle,
	}
	if got, want := EstimateMessagesInputTokensForResolved(openrouterGemini, messages).Tokens, estimateGoogleImageTokens(1024, 1024); got != want {
		t.Fatalf("openrouter gemini image history = %d, want %d", got, want)
	}
	// No surface: the protocol is the next-best axis.
	openaiChatRow := registry.Resolved{Instance: "custom", Protocol: registry.ProtocolOpenAIChat}
	if got, want := EstimateMessagesInputTokensForResolved(openaiChatRow, messages).Tokens, estimateOpenAIImageTokens(1024, 1024, ""); got != want {
		t.Fatalf("openai-chat image history = %d, want %d", got, want)
	}
}

// The row's recorded model family is the last word on the image-token family:
// a gateway can serve a vendor model behind a protocol that is not that
// vendor's own, and with a generic surface the protocol alone would pick the
// wrong tokenizer. An unrecognized family still falls through to the protocol.
func TestEstimateMessagesInputTokensForResolved_ModelFamilyBeatsTheWireProtocol(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}

	// A generic surface over openai-chat: only the recorded family knows the
	// row is a Claude.
	gatewayClaude := registry.Resolved{
		Instance: "gateway", ModelID: "anthropic/claude-opus-5",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
		Model: registry.Model{Family: "claude-opus"},
	}
	if got, want := EstimateMessagesInputTokensForResolved(gatewayClaude, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
		t.Fatalf("generic-surface claude image history = %d, want %d: the recorded family decides", got, want)
	}
	// The family outranks the protocol when they disagree.
	anthropicProtocolGPT := registry.Resolved{
		Instance: "gateway", ModelID: "gpt-5.2", Protocol: registry.ProtocolAnthropic,
		Model: registry.Model{Family: "gpt"},
	}
	if got, want := EstimateMessagesInputTokensForResolved(anthropicProtocolGPT, messages).Tokens, estimateOpenAIImageTokens(1024, 1024, ""); got != want {
		t.Fatalf("anthropic-protocol gpt image history = %d, want %d: the family outranks the protocol", got, want)
	}
	// A family with no image-token rules of its own leaves the protocol in charge.
	unknownFamily := registry.Resolved{
		Instance: "gateway", ModelID: "llama-4", Protocol: registry.ProtocolAnthropic,
		Model: registry.Model{Family: "llama"},
	}
	if got, want := EstimateMessagesInputTokensForResolved(unknownFamily, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
		t.Fatalf("unknown-family image history = %d, want %d: the protocol remains the fallback", got, want)
	}
}

// The history entry point the context manager uses decides by the row too, so
// compaction pressure sees replayable thinking text and does not see text the
// adapter drops.
func TestEstimateMessagesInputTokensForResolved_BillsAndSparesUnsignedThinking(t *testing.T) {
	const text = 400
	messages := []Message{{Role: RoleAssistant, Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: strings.Repeat("t", text)}},
	}}}

	anthropic := registry.Resolved{Protocol: registry.ProtocolAnthropic}
	if got, want := EstimateMessagesInputTokensForResolved(anthropic, messages).Tokens, text/4; got != want {
		t.Fatalf("anthropic history = %d, want %d", got, want)
	}
	responses := registry.Resolved{Protocol: registry.ProtocolOpenAIResponses}
	if got := EstimateMessagesInputTokensForResolved(responses, messages).Tokens; got != 0 {
		t.Fatalf("responses history = %d, want 0: the adapter keeps raw reasoning text for display only", got)
	}
	reasoning := false
	chatNo := registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{Reasoning: &reasoning}}
	if got := EstimateMessagesInputTokensForResolved(chatNo, messages).Tokens; got != 0 {
		t.Fatalf("non-reasoning chat history = %d, want 0", got)
	}
	if got := EstimateMessagesInputTokens(messages).Tokens; got != 0 {
		t.Fatalf("targetless history = %d, want 0: the name fallback has no row to decide from", got)
	}
}

// A partially resolved row must not lose the vendor its request names. With no
// resolved protocol the name rule decides, and it has to read the request's
// provider and model rather than the row's empty fields, or a resolved-but-partial
// row undercounts exactly the requests a name-based caller bills correctly.
func TestEstimateInputTokensForResolvedPartialRowKeepsTheRequestNames(t *testing.T) {
	req := Request{
		Provider: "anthropic",
		Model:    "claude-opus-5",
		Messages: []Message{{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentText, Text: "the visible answer"},
			{Kind: ContentThinking, Thinking: &ThinkingData{Text: strings.Repeat("unsigned reasoning text ", 40)}},
		}}},
	}
	partial := registry.Resolved{}

	resolvedTokens := EstimateInputTokensForResolved(partial, req).Tokens
	nameTokens := EstimateInputTokens(req).Tokens
	if resolvedTokens != nameTokens {
		t.Fatalf("partial-row estimate = %d, want the name-based estimate %d: the request names the vendor", resolvedTokens, nameTokens)
	}
}

// The o-series matcher is an OpenAI rule, not "any id that starts with o": a
// Mistral, OLMo, OpenChat or OPT model -- or a gateway path like openrouter/* or
// ollama/* -- served over another protocol keeps that protocol's image rules.
func TestEstimateMessagesInputTokensForResolved_OPrefixIsNotAnOpenAIClaim(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}
	for _, modelID := range []string{"open-mistral-7b", "olmo-2-7b", "openchat-3.5", "openrouter/auto", "ollama/llama3"} {
		row := registry.Resolved{Instance: "gateway", ModelID: modelID, Protocol: registry.ProtocolAnthropic}
		if got, want := EstimateMessagesInputTokensForResolved(row, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
			t.Fatalf("row %q image history = %d, want %d: a leading o is not an OpenAI claim", modelID, got, want)
		}
	}
	for _, modelID := range []string{"o", "o3", "o4-mini", "o-mini", "o-pro", "openai/o3", "openai.o4"} {
		row := registry.Resolved{Instance: "gateway", ModelID: modelID, Protocol: registry.ProtocolAnthropic}
		if got, want := EstimateMessagesInputTokensForResolved(row, messages).Tokens, estimateOpenAIImageTokens(1024, 1024, ""); got != want {
			t.Fatalf("o-series row %q image history = %d, want %d: the o-series is OpenAI's", modelID, got, want)
		}
	}
}

// The model's own family is the more particular fact: a row that records claude
// while carrying an OpenAI provider surface still bills Anthropic's image rules,
// because the surface describes where the row was reached, not what the model is.
func TestEstimateMessagesInputTokensForResolved_ModelFamilyBeatsProviderSurface(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}
	row := registry.Resolved{
		Instance: "gateway", ModelID: "gateway-zz",
		Surface: registry.SurfaceOpenAI, Model: registry.Model{Family: "claude"},
	}
	if got, want := EstimateMessagesInputTokensForResolved(row, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
		t.Fatalf("claude-family row over an openai surface = %d, want %d: the family is the model's own", got, want)
	}
}

// A provider name the caller supplied is evidence the caller chose to give, unlike
// a row's instance alias: the resolved and name-based estimators must agree on a
// row that carries no other facts.
func TestEstimateInputTokensForResolved_KeepsTheCallersProviderName(t *testing.T) {
	data := testPNG1024()
	req := Request{
		Provider: "google", Model: "gateway-zz",
		Messages: []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
		}}},
	}
	row := registry.Resolved{Instance: "google"}
	if got, want := EstimateInputTokensForResolved(row, req).Tokens, EstimateInputTokens(req).Tokens; got != want {
		t.Fatalf("resolved estimate with a caller-supplied provider = %d, want the name-based estimate %d", got, want)
	}
}

// A provider name that merely repeats the row's own instance alias is still an
// alias: with the row's protocol in hand it must not claim a vendor the row does
// not carry, or the request estimate and the history estimate disagree about one
// row.
func TestEstimateInputTokensForResolved_InstanceAliasDoesNotOutrankTheRowProtocol(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}
	row := registry.Resolved{Instance: "anthropic", ModelID: "gateway-zz", Protocol: registry.ProtocolOpenAIResponses}
	req := Request{Provider: "anthropic", Model: "gateway-zz", Messages: messages}

	got := EstimateInputTokensForResolved(row, req).Tokens
	want := EstimateMessagesInputTokensForResolved(row, messages).Tokens
	if got != want {
		t.Fatalf("request estimate = %d, want the history estimate %d for the same row: an alias is not vendor identity", got, want)
	}
}

// The estimator may read a caller-supplied provider name, never the instance the
// request resolved to: an aliased instance (here "google" serving an OpenAI
// protocol) must not claim a vendor's image rules for a row that does not carry
// them.
func TestLocalInputEstimateKeepsTheCallersProviderNotTheInstance(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentText, Text: "hello"},
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}
	row := registry.Resolved{Instance: "google", ModelID: "gateway-zz", Protocol: registry.ProtocolOpenAIResponses}
	target := dispatchTarget{name: "google", res: row, resolved: true}

	got := localInputEstimate(Request{Provider: "google", Model: "gateway-zz", Messages: messages}, "", target).Tokens
	want := EstimateInputTokensForResolved(row, Request{Model: "gateway-zz", Messages: messages}).Tokens
	if got != want {
		t.Fatalf("estimate with no caller provider = %d, want the row-and-protocol estimate %d", got, want)
	}
}

// A family the registry calls generic stays generic: gpt-oss rows declare
// text-only input, so no image family is claimed for them -- not from the
// protocol they are served over and not from anything else.
func TestEstimateMessagesInputTokensForResolved_GenericFamilyClaimsNoImageFamily(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}
	want := fallbackMediaTokens + len("image/png")/4
	for _, protocol := range []string{registry.ProtocolOpenAIResponses, registry.ProtocolOpenAIChat, registry.ProtocolAnthropic, registry.ProtocolGoogle} {
		row := registry.Resolved{
			Instance: "gateway", ModelID: "gpt-oss-120b", Protocol: protocol,
			Model: registry.Model{Family: "gpt-oss"},
		}
		if got := EstimateMessagesInputTokensForResolved(row, messages).Tokens; got != want {
			t.Fatalf("gpt-oss row over %q image history = %d, want the generic fallback %d", protocol, got, want)
		}
	}
}

// A row's configured image detail is what the adapter sends for images that
// carry none of their own, so the estimate bills the low-detail formula the
// request will actually use.
func TestEstimateMessagesInputTokensForResolved_AppliesTheRowsImageDetail(t *testing.T) {
	low := "low"
	row := registry.Resolved{
		Instance: "gateway", ModelID: "gpt-4o", Protocol: registry.ProtocolOpenAIResponses,
		Caps: registry.Caps{ImageDetail: &low},
	}
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: testPNG1024(), MediaType: "image/png"}},
	}}}
	if got, want := EstimateMessagesInputTokensForResolved(row, messages).Tokens, 85; got != want {
		t.Fatalf("low-detail row image history = %d, want %d: the row's image_detail applies", got, want)
	}
}

// Only the Responses builder injects the row's configured image detail (and a
// "high" default when the row sets none): the chat builder sends the image's own
// detail or nothing at all (chatcompletions/messages.go), so a chat row's
// image_detail never reaches the wire and the estimate must not bill for it.
func TestEstimateMessagesInputTokensForResolved_ChatRowDoesNotApplyTheRowsImageDetail(t *testing.T) {
	low := "low"
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: testPNG1024(), MediaType: "image/png"}},
	}}}
	withCap := registry.Resolved{
		Instance: "gateway", ModelID: "gpt-4o", Protocol: registry.ProtocolOpenAIChat,
		Caps: registry.Caps{ImageDetail: &low},
	}
	withoutCap := registry.Resolved{Instance: "gateway", ModelID: "gpt-4o", Protocol: registry.ProtocolOpenAIChat}
	got := EstimateMessagesInputTokensForResolved(withCap, messages).Tokens
	if want := EstimateMessagesInputTokensForResolved(withoutCap, messages).Tokens; got != want {
		t.Fatalf("chat row image history = %d, want %d: the chat builder never injects the row's detail", got, want)
	}
}

// The chat builder strips a tool-result image unless the row declares
// MultimodalToolResults (chatcompletions/messages.go), so nothing about the image
// reaches the wire and the estimate must not bill one: overcounting fails the
// budget early and compacts a request the provider would have taken. The
// Anthropic and Responses builders emit the image regardless of the cap, so only
// a row whose adapter actually drops it changes the count.
func TestEstimateMessagesInputTokensForResolved_BillsToolResultImagesOnlyWhenTheyRide(t *testing.T) {
	toolResult := func(withImage bool) []Message {
		result := &ToolResultData{ToolCallID: "call1", Name: "screenshot", Content: "ok"}
		if withImage {
			result.ImageData, result.ImageMediaType = testPNG1024(), "image/png"
		}
		return []Message{{Role: RoleTool, Content: []ContentPart{{Kind: ContentToolResult, ToolResult: result}}}}
	}
	// The image's own cost, priced by the row's image rule on an ordinary content
	// part, is what a riding tool-result image must add.
	imageCost := func(row registry.Resolved) int {
		plain := []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{Data: testPNG1024(), MediaType: "image/png"}},
		}}}
		return EstimateMessagesInputTokensForResolved(row, plain).Tokens
	}
	textOnly := func(row registry.Resolved) int {
		return EstimateMessagesInputTokensForResolved(row, toolResult(false)).Tokens
	}

	declared, undeclared := true, false
	chat := registry.Resolved{Instance: "gateway", ModelID: "gpt-4o", Protocol: registry.ProtocolOpenAIChat}
	withCap, withoutCap := chat, chat
	withCap.Caps = registry.Caps{MultimodalToolResults: &declared}
	withoutCap.Caps = registry.Caps{MultimodalToolResults: &undeclared}

	if got, want := EstimateMessagesInputTokensForResolved(withoutCap, toolResult(true)).Tokens, textOnly(withoutCap); got != want {
		t.Fatalf("undeclared-cap chat tool-result image = %d, want the text-only estimate %d: the builder strips the image", got, want)
	}
	if got, want := EstimateMessagesInputTokensForResolved(withCap, toolResult(true)).Tokens, textOnly(withCap)+imageCost(withCap); got != want {
		t.Fatalf("declared-cap chat tool-result image = %d, want %d: the declared cap keeps the image on the wire", got, want)
	}
	for _, protocol := range []string{registry.ProtocolAnthropic, registry.ProtocolOpenAIResponses} {
		row := registry.Resolved{Instance: "gateway", ModelID: "gpt-4o", Protocol: protocol}
		if got, want := EstimateMessagesInputTokensForResolved(row, toolResult(true)).Tokens, textOnly(row)+imageCost(row); got != want {
			t.Fatalf("%s tool-result image = %d, want %d: the adapter emits it regardless of the cap", protocol, got, want)
		}
	}
	if got := EstimateMessagesInputTokens(toolResult(true)).Tokens; got <= EstimateMessagesInputTokens(toolResult(false)).Tokens {
		t.Fatalf("name-based tool-result image = %d, want more than the text-only estimate: names carry no cap to read", got)
	}
}

// A low-detail image costs a fixed 85 tokens: OpenAI never reads its dimensions,
// so an image whose dimensions cannot be decoded locally (a remote URL) is still
// estimable when the detail that rides the wire is low. The undecoded fallback
// here would fail the budget on a request the provider bills at 85 tokens.
func TestEstimateMessagesInputTokensForResolved_AppliesTheRowsImageDetailWithoutDimensions(t *testing.T) {
	low := "low"
	const url = "https://images.test/cat.png"
	remote := func(detail string) []Message {
		return []Message{{Role: RoleUser, Content: []ContentPart{
			{Kind: ContentImage, Image: &ImageData{URL: url, MediaType: "image/png", Detail: detail}},
		}}}
	}
	row := func(protocol string, detail *string) registry.Resolved {
		return registry.Resolved{Instance: "gateway", ModelID: "gpt-4o", Protocol: protocol, Caps: registry.Caps{ImageDetail: detail}}
	}
	fallback := fallbackMediaTokens + len(url)/4 + len("image/png")/4

	if got := EstimateMessagesInputTokensForResolved(row(registry.ProtocolOpenAIResponses, &low), remote("")).Tokens; got != 85 {
		t.Fatalf("low-detail row remote image = %d, want the fixed low-detail cost 85", got)
	}
	if got := EstimateMessagesInputTokensForResolved(row(registry.ProtocolOpenAIResponses, nil), remote("")).Tokens; got != fallback {
		t.Fatalf("row without a configured detail = %d, want the undecoded fallback %d", got, fallback)
	}
	if got := EstimateMessagesInputTokensForResolved(row(registry.ProtocolOpenAIResponses, nil), remote("low")).Tokens; got != 85 {
		t.Fatalf("image's own low detail = %d, want 85: both builders send the image's own detail", got)
	}
	if got := EstimateMessagesInputTokensForResolved(row(registry.ProtocolOpenAIChat, &low), remote("")).Tokens; got != fallback {
		t.Fatalf("chat row remote image = %d, want the undecoded fallback %d: the chat builder never injects the row's detail", got, fallback)
	}
}

// A resolved row knows which adapter will build the request, so every thinking
// shape is billed by what that adapter puts on the wire:
//
//   - anthropic/request.go replays a thinking block (text, plus the signature
//     unless the value is really an OpenAI-compatible field name) and a
//     redacted_thinking block (its data); an encrypted blob never rides, and a
//     thinking part with no text is skipped entirely;
//   - responses/input.go replays one reasoning item carrying a non-compat
//     encrypted blob with its id and non-blank summaries, and drops every other
//     shape, the part's display text included;
//   - chatcompletions/messages.go replays the part's text (unless the row is
//     declared non-reasoning without replaying thinking as text) and the compat
//     reasoning_details array (unless the row is declared non-reasoning at all),
//     and drops a redacted part and an opaque Responses blob;
//   - the Google adapter emits no thinking shape at all.
func TestEstimateMessagesInputTokensForResolved_BillsThinkingShapesByAdapter(t *testing.T) {
	const (
		textChars    = 400 // 100 tokens
		blobChars    = 800 // 200 tokens
		idChars      = 400 // 100 tokens
		summaryChars = 400 // 100 tokens
	)
	text := strings.Repeat("t", textChars)
	sig := strings.Repeat("s", blobChars)
	blob := strings.Repeat("b", blobChars)
	id := strings.Repeat("i", idChars)
	summary := strings.Repeat("m", summaryChars)
	compat := `[{"type":"reasoning.text","text":"","signature":"` + strings.Repeat("g", textChars) + `"}]`

	part := func(kind ContentKind, thinking *ThinkingData) []Message {
		return []Message{{Role: RoleAssistant, Content: []ContentPart{{Kind: kind, Thinking: thinking}}}}
	}
	thinking := func(thinking *ThinkingData) []Message { return part(ContentThinking, thinking) }
	row := func(protocol string, caps registry.Caps) registry.Resolved {
		return registry.Resolved{Instance: "gateway", ModelID: "m", Protocol: protocol, Caps: caps}
	}
	declare := func(reasoning, thinkingAsText bool) registry.Caps {
		return registry.Caps{Reasoning: &reasoning, ThinkingAsText: &thinkingAsText}
	}

	anthropic, responses := registry.ProtocolAnthropic, registry.ProtocolOpenAIResponses
	chat, google := registry.ProtocolOpenAIChat, registry.ProtocolGoogle

	cases := []struct {
		name string
		row  registry.Resolved
		msgs []Message
		want int
	}{
		{"anthropic bills redacted data", row(anthropic, registry.Caps{}), part(ContentRedThinking, &ThinkingData{Text: text, Signature: sig}), textChars / 4},
		{"responses drops a redacted part", row(responses, registry.Caps{}), part(ContentRedThinking, &ThinkingData{Text: text}), 0},
		{"chat drops a redacted part", row(chat, registry.Caps{}), part(ContentRedThinking, &ThinkingData{Text: text}), 0},
		{"google drops a redacted part", row(google, registry.Caps{}), part(ContentRedThinking, &ThinkingData{Text: text}), 0},

		{"responses bills the blob, id and summaries", row(responses, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob, ID: id, Summary: []string{summary}}), (blobChars + idChars + summaryChars) / 4},
		{"responses drops the display text beside the blob", row(responses, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob, Text: text}), blobChars / 4},
		{"responses drops an id and summaries that do not ride", row(responses, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob, ID: "  ", Summary: []string{" "}}), blobChars / 4},
		{"chat drops the opaque blob and replays its text", row(chat, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob, Text: text}), textChars / 4},
		{"chat drops an opaque blob with no text", row(chat, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob}), 0},
		{"anthropic drops the opaque blob and replays the text", row(anthropic, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob, Text: text}), textChars / 4},
		{"anthropic drops an encrypted-only part", row(anthropic, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob}), 0},
		{"google drops an opaque blob", row(google, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: blob, Text: text}), 0},

		{"chat bills the compat array and its text", row(chat, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: compat, Text: text}), (len(compat) + textChars) / 4},
		{"anthropic drops the compat array and replays the text", row(anthropic, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: compat, Text: text}), textChars / 4},
		{"responses drops the compat array", row(responses, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: compat, Text: text}), 0},
		{"google drops the compat array", row(google, registry.Caps{}), thinking(&ThinkingData{EncryptedContent: compat}), 0},

		{"anthropic bills the text and the signature", row(anthropic, registry.Caps{}), thinking(&ThinkingData{Text: text, Signature: sig}), (textChars + blobChars) / 4},
		{"anthropic blanks a compat field name", row(anthropic, registry.Caps{}), thinking(&ThinkingData{Text: text, Signature: "reasoning_content"}), textChars / 4},
		{"chat bills the text without the signature", row(chat, registry.Caps{}), thinking(&ThinkingData{Text: text, Signature: sig}), textChars / 4},
		{"responses drops a signature part", row(responses, registry.Caps{}), thinking(&ThinkingData{Text: text, Signature: sig}), 0},
		{"google drops a signature part", row(google, registry.Caps{}), thinking(&ThinkingData{Text: text, Signature: sig}), 0},

		{"chat drops the compat array when reasoning is off", row(chat, declare(false, false)), thinking(&ThinkingData{EncryptedContent: compat, Text: text}), 0},
		{"chat drops thinking text when reasoning is off", row(chat, declare(false, false)), thinking(&ThinkingData{Text: text}), 0},
		{"chat replays thinking as text when reasoning is off", row(chat, declare(false, true)), thinking(&ThinkingData{Text: text}), textChars / 4},
		{"chat keeps the text off the compat array when reasoning is off", row(chat, declare(false, true)), thinking(&ThinkingData{EncryptedContent: compat, Text: text}), textChars / 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateMessagesInputTokensForResolved(tc.row, tc.msgs).Tokens; got != tc.want {
				t.Fatalf("estimate = %d, want %d", got, tc.want)
			}
		})
	}
}

// The row's instance alias is not vendor identity for the thinking rule either:
// with no caller-provided provider and no row facts, a row merely named
// "anthropic" must not bill thinking text.
func TestEstimateInputTokensForResolved_InstanceAliasDoesNotBillThinking(t *testing.T) {
	const text = "unsigned reasoning that only an Anthropic adapter would replay"
	row := registry.Resolved{Instance: "anthropic", ModelID: "gateway-zz"}
	req := thinkingOnlyRequest("", "gateway-zz", text)
	if got := EstimateInputTokensForResolved(row, req).Tokens; got != 0 {
		t.Fatalf("alias-only row bills %d thinking tokens, want 0: an instance name is not vendor identity", got)
	}
}

// The equivalence covers what the estimator reads, model id included: the name
// rules consult it whenever the row's own facts do not decide the family.
func TestEstimatorTargetsEquivalentCoversTheModelID(t *testing.T) {
	base := registry.Resolved{Instance: "gateway", ModelID: "gateway-zz", Protocol: registry.ProtocolOpenAIChat}
	same := base
	if !EstimatorTargetsEquivalent(base, same) {
		t.Fatal("identical rows are one estimator target")
	}
	otherModel := base
	otherModel.ModelID = "gateway-aa"
	if EstimatorTargetsEquivalent(base, otherModel) {
		t.Fatal("rows with different model ids are different estimator targets: the name rule reads the id")
	}
	otherFamily := base
	otherFamily.Model.Family = "claude"
	if EstimatorTargetsEquivalent(base, otherFamily) {
		t.Fatal("rows with different families are different estimator targets")
	}
}

// The MultimodalToolResults cap decides whether a tool-result image is billed, so
// two rows that differ only in it are not one estimator target: a caller that
// keeps a measurement keyed to the target would otherwise hold a count that bills
// an image the shaped request no longer carries.
func TestEstimatorTargetsEquivalentCoversMultimodalToolResults(t *testing.T) {
	declared, undeclared := true, false
	base := registry.Resolved{
		Instance: "gateway", ModelID: "gateway-zz", Protocol: registry.ProtocolOpenAIChat,
		Caps: registry.Caps{MultimodalToolResults: &declared},
	}
	same := base
	same.Caps.MultimodalToolResults = &declared
	if !EstimatorTargetsEquivalent(base, same) {
		t.Fatal("rows that agree on MultimodalToolResults are one estimator target")
	}
	other := base
	other.Caps.MultimodalToolResults = &undeclared
	if EstimatorTargetsEquivalent(base, other) {
		t.Fatal("rows that disagree on MultimodalToolResults are different estimator targets: the estimate reads it")
	}
}

// A namespaced OpenAI id is the same model as its bare spelling, and a gemma id
// is Google's: the name rule has to read both, with no row facts to decide from.
func TestProviderTokenFamilyReadsNamespacedAndGemmaNames(t *testing.T) {
	for _, model := range []string{"openai/gpt-4o", "openai.gpt-4o", "gpt-4o"} {
		if got := providerTokenFamily("gateway", model); got != "openai" {
			t.Fatalf("providerTokenFamily(gateway, %q) = %q, want openai", model, got)
		}
	}
	for _, model := range []string{"gemma-3-27b", "openai/gemma-3-27b"} {
		if got := providerTokenFamily("gateway", model); got != "google" {
			t.Fatalf("providerTokenFamily(gateway, %q) = %q, want google", model, got)
		}
	}
}

// An instance name is an alias, not vendor identity: a generic row that happens
// to be named "anthropic" must not bill Anthropic's image rules for a model the
// registry never classified, so a resolved target's family comes from the row's
// own facts and the wire protocol, not from what its instance is called.
func TestEstimateMessagesInputTokensForResolved_InstanceNameIsNotVendorIdentity(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}
	aliased := registry.Resolved{
		Instance: "anthropic", ModelID: "gateway-zz",
		Protocol: registry.ProtocolOpenAIResponses,
	}
	if got, want := EstimateMessagesInputTokensForResolved(aliased, messages).Tokens, estimateOpenAIImageTokens(1024, 1024, ""); got != want {
		t.Fatalf("aliased row image history = %d, want %d: the protocol decides, not the instance alias", got, want)
	}
}

// A resolved row the registry did not classify still honors a model name that
// identifies the vendor: the name describes the model, where the wire protocol
// describes only the endpoint, so a gateway row served over openai-chat must not
// lose its Claude family to the protocol. The family map mirrors the registry's
// own family → surface rule (llm/registry §6.1): claude, gemini/gemma, gpt and
// the o family are recognized, and gpt-oss is deliberately generic.
func TestEstimateMessagesInputTokensForResolved_NameIdentifiesTheFamilyWhenTheRowDoesNot(t *testing.T) {
	data := testPNG1024()
	messages := []Message{{Role: RoleUser, Content: []ContentPart{
		{Kind: ContentImage, Image: &ImageData{Data: data, MediaType: "image/png"}},
	}}}

	// The gateway shape the review named: generic surface, no recorded family,
	// and a model name that identifies the vendor.
	unnamedGatewayClaude := registry.Resolved{
		Instance: "openrouter", ModelID: "anthropic/claude-opus-5",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
	}
	if got, want := EstimateMessagesInputTokensForResolved(unnamedGatewayClaude, messages).Tokens, estimateAnthropicImageTokens(1024, 1024); got != want {
		t.Fatalf("name-only claude image history = %d, want %d: the model name identifies the family", got, want)
	}
	// The recognized families the registry maps for itself.
	gemma := registry.Resolved{
		Instance: "gateway", ModelID: "gemma-3-27b",
		Protocol: registry.ProtocolOpenAIChat, Model: registry.Model{Family: "gemma"},
	}
	if got, want := EstimateMessagesInputTokensForResolved(gemma, messages).Tokens, estimateGoogleImageTokens(1024, 1024); got != want {
		t.Fatalf("gemma image history = %d, want %d: gemma is a Google family", got, want)
	}
	oSeries := registry.Resolved{
		Instance: "gateway", ModelID: "o4-mini",
		Protocol: registry.ProtocolAnthropic, Model: registry.Model{Family: "o"},
	}
	if got, want := EstimateMessagesInputTokensForResolved(oSeries, messages).Tokens, estimateOpenAIImageTokens(1024, 1024, ""); got != want {
		t.Fatalf("o-series image history = %d, want %d: the o family is OpenAI's", got, want)
	}
	// gpt-oss is generic per §6.1, not an OpenAI claim, and the carve-out has to
	// hold for the names real rows carry: "gpt-" and a leading "o" (a gateway's
	// openai/gpt-oss name) both look like OpenAI to the name rule, so the name
	// stage must not re-claim the tokenizer the family map refused. With no
	// vendor decision left, the protocol decides.
	// The generic classification is a decision, not an absence: a gpt-oss row
	// claims no image family at all, so it takes the generic fallback rather than
	// the protocol's rules (roborev's eleventh round; the third round's
	// expectation that the protocol answers for it is superseded).
	for _, modelID := range []string{"gpt-oss-120b", "openai/gpt-oss-120b", "openai.gpt-oss-120b"} {
		gptOSS := registry.Resolved{
			Instance: "cerebras", ModelID: modelID, Protocol: registry.ProtocolAnthropic,
			Model: registry.Model{Family: "gpt-oss"},
		}
		want := fallbackMediaTokens + len("image/png")/4
		if got := EstimateMessagesInputTokensForResolved(gptOSS, messages).Tokens; got != want {
			t.Fatalf("gpt-oss row %q image history = %d, want the generic fallback %d: the family must not claim any vendor", modelID, got, want)
		}
	}
	// The rule at its source: a gpt-oss name is no vendor decision at all, so the
	// name-only entry points cannot claim OpenAI's image rules for it either.
	if got := providerTokenFamily("cerebras", "gpt-oss-120b"); got != "" {
		t.Fatalf("providerTokenFamily(cerebras, gpt-oss-120b) = %q, want no family: gpt-oss is deliberately generic", got)
	}
}
