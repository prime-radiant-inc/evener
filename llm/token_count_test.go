package llm

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
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
	req := Request{
		Provider: "anthropic",
		Model:    "claude-test",
		Messages: []Message{{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentThinking, Thinking: &ThinkingData{Text: rawText}},
		}}},
	}

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
