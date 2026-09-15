package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"math"
	"os"
	"strings"

	"primeradiant.com/evener/llm/registry"

	// Registered for their image.DecodeConfig side effects: media token
	// estimation decodes inline image bytes to read width/height.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const fallbackMediaTokens = 256

// TokenCountSource identifies how an InputTokenCount was produced.
type TokenCountSource string

const (
	// TokenCountSourceLocalEstimate marks counts computed by the local,
	// deterministic estimator rather than the provider.
	TokenCountSourceLocalEstimate TokenCountSource = "local_estimate"
	// TokenCountSourceProvider marks counts returned by the provider's exact
	// token-counting endpoint.
	TokenCountSourceProvider TokenCountSource = "provider"
)

// ErrInputTokenCountUnsupported is returned by an InputTokenCounter when the
// provider has no exact token-counting support, signaling callers to fall back
// to the local estimate.
var ErrInputTokenCountUnsupported = errors.New("input token count unsupported")

// InputTokenCount reports input tokens for a request. Exact=false means the
// count is a local estimate; exact provider responses and normal post-call
// Usage remain authoritative.
type InputTokenCount struct {
	Tokens   int              `json:"tokens"`
	Exact    bool             `json:"exact"`
	Source   TokenCountSource `json:"source"`
	Provider string           `json:"provider,omitempty"`
	Model    string           `json:"model,omitempty"`
	Raw      map[string]any   `json:"raw,omitempty"`
}

// InputTokenCounter is implemented by adapters that support provider-side
// preflight token counting for full llm.Request inputs.
type InputTokenCounter interface {
	CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error)
}

// EstimateInputTokens returns a deterministic local estimate for a request.
// It never counts inline media bytes as text.
//
// The unsigned-thinking rule is decided from the provider and model names,
// because a bare request carries no resolved row. Callers that have resolved
// the request's target should use EstimateInputTokensForResolved instead: the
// name rule cannot recognize a curated OpenAI-compatible chat provider whose
// name carries no marker, nor an instance alias, and both replay unsigned
// thinking text.
func EstimateInputTokens(req Request) InputTokenCount {
	return estimateInputTokens(targetFromNames(req.Provider, req.Model), req)
}

// EstimateInputTokensForResolved returns the local estimate with the
// unsigned-thinking rule decided by the target's resolved row — its protocol
// and reasoning capabilities — rather than by name. Callers that have resolved
// the request's target should prefer this.
func EstimateInputTokensForResolved(res registry.Resolved, req Request) InputTokenCount {
	return estimateInputTokens(targetFromResolved(res, req.Provider, req.Model), req)
}

func estimateInputTokens(t targetInfo, req Request) InputTokenCount {
	tokens := estimateMessagesInputTokens(t, req.Messages)
	if len(req.Tools) > 0 {
		b, _ := json.Marshal(req.Tools)
		tokens += len(b) / 4
	}
	if req.ResponseFormat != nil {
		b, _ := json.Marshal(req.ResponseFormat)
		tokens += len(b) / 4
	}
	return InputTokenCount{
		Tokens:   tokens,
		Exact:    false,
		Source:   TokenCountSourceLocalEstimate,
		Provider: req.Provider,
		Model:    req.Model,
	}
}

// EstimateMessagesInputTokens estimates only a message list. It is useful for
// history-only accounting that does not yet have a full Request. Its
// unsigned-thinking rule is the name fallback, since a bare message list
// carries no target; callers that hold a resolved row should use
// EstimateMessagesInputTokensForResolved.
func EstimateMessagesInputTokens(messages []Message) InputTokenCount {
	return estimateMessageList(targetFromNames("", ""), messages)
}

// EstimateMessagesInputTokensForResolved estimates a message list for a
// resolved target, deciding the unsigned-thinking rule from the row's protocol
// and reasoning capabilities. History accounting (the context manager's
// compaction pressure) uses it so the thinking text those adapters replay is
// counted.
func EstimateMessagesInputTokensForResolved(res registry.Resolved, messages []Message) InputTokenCount {
	return estimateMessageList(targetFromResolved(res, "", ""), messages)
}

func estimateMessageList(t targetInfo, messages []Message) InputTokenCount {
	return InputTokenCount{
		Tokens: estimateMessagesInputTokens(t, messages),
		Exact:  false,
		Source: TokenCountSourceLocalEstimate,
	}
}

// CountInputTokens resolves the request's target and uses its exact counter
// when available. Targets without exact support fall back to the local
// estimate. The count is taken over the shaped request, so it describes the
// input body Complete would send. It intentionally does not apply completion
// admission: callers need the exact count to decide how an oversized request
// should be reduced before dispatch.
func (c *Client) CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error) {
	if err := req.Validate(); err != nil {
		return InputTokenCount{}, err
	}
	if req.AdapterTimeout == nil {
		req.AdapterTimeout = new(DefaultAdapterTimeout())
	}
	t, err := c.dispatchTarget(req)
	if err != nil {
		return InputTokenCount{}, err
	}
	// The instance a request resolves to is not evidence about the model, so the
	// estimator keeps the caller's own provider name -- empty when the caller gave
	// none -- rather than the name the dispatch happened to use.
	callerProvider := req.Provider
	req.Provider = t.name
	if t.resolved {
		req = ShapeRequest(req, t.res)
	}
	localEstimate := func() InputTokenCount {
		return localInputEstimate(req, callerProvider, t)
	}

	// countExactly is the exact-count call of whichever half of the target
	// serves the request; nil when an override offers no exact counter.
	var countExactly func(context.Context) (InputTokenCount, error)
	if t.override == nil {
		countExactly = func(ctx context.Context) (InputTokenCount, error) {
			tokens, err := t.protocol.CountTokens(ctx, req, t.res)
			return InputTokenCount{Tokens: tokens, Exact: true, Source: TokenCountSourceProvider, Provider: t.name, Model: req.Model}, err
		}
	} else if counter, ok := t.override.(InputTokenCounter); ok {
		countExactly = func(ctx context.Context) (InputTokenCount, error) { return counter.CountInputTokens(ctx, req) }
	}
	if countExactly == nil {
		out := localEstimate()
		out.Provider = t.name
		return out, nil
	}

	opCtx, operation := c.beginProviderOperation(ctx)
	out, err := countExactly(opCtx)
	operation.settle(opCtx, err)
	if err != nil {
		if errors.Is(err, ErrInputTokenCountUnsupported) {
			estimate := localEstimate()
			estimate.Provider = t.name
			return estimate, nil
		}
		return InputTokenCount{}, RewriteErrorProvider(err, t.name)
	}
	if out.Source == "" {
		out.Source = TokenCountSourceProvider
	}
	if out.Source == TokenCountSourceProvider {
		out.Exact = true
	}
	if out.Provider == "" {
		out.Provider = t.name
	}
	if out.Model == "" {
		out.Model = req.Model
	}
	return out, nil
}

func estimateMessagesInputTokens(t targetInfo, messages []Message) int {
	chars := 0
	tokens := 0
	for _, m := range messages {
		c, counted := estimateMessageInputParts(t, m)
		chars += c
		tokens += counted
	}
	return tokens + chars/4
}

func estimateMessageInputParts(t targetInfo, m Message) (int, int) {
	chars := len(m.Name) + len(m.ToolCallID)
	tokens := 0
	for _, p := range m.Content {
		switch p.Kind {
		case ContentText:
			chars += len(p.Text)
		case ContentImage:
			if p.Image != nil {
				tokens += estimateImageTokens(t, p.Image)
			}
		case ContentAudio:
			tokens += fallbackMediaTokens
		case ContentDocument:
			if p.Document != nil {
				tokens += fallbackMediaTokens + len(p.Document.URL)/4 + len(p.Document.MediaType)/4 + len(p.Document.FileName)/4
			}
		case ContentToolCall:
			if p.ToolCall != nil {
				chars += len(p.ToolCall.ID)
				chars += len(p.ToolCall.Name)
				chars += len(p.ToolCall.Arguments)
			}
		case ContentToolResult:
			if p.ToolResult != nil {
				chars += len(p.ToolResult.ToolCallID)
				chars += len(p.ToolResult.Name)
				if len(p.ToolResult.ImageData) > 0 || p.ToolResult.ImageMediaType != "" {
					tokens += estimateImageTokens(t, &ImageData{Data: p.ToolResult.ImageData, MediaType: p.ToolResult.ImageMediaType})
				}
				switch x := p.ToolResult.Content.(type) {
				case string:
					chars += len(x)
				case []byte:
					chars += len(x)
				default:
					b, _ := json.Marshal(x)
					chars += len(b)
				}
			}
		case ContentThinking, ContentRedThinking:
			chars += thinkingReplayChars(t, p)
		case ContentWebSearch:
			if p.WebSearch != nil {
				chars += len(p.WebSearch.Query)
				chars += len(p.WebSearch.Raw)
			}
		default:
			b, _ := json.Marshal(p)
			chars += len(b)
		}
	}
	return chars, tokens
}

// thinkingReplayChars counts the characters a thinking part contributes to the
// outgoing request. The estimator bills the payload each adapter may replay.
//
//   - redacted thinking replays its text payload only (anthropic/request.go
//     emits "data": Text and never a signature);
//   - an encrypted blob is replayed verbatim together with its summary and id
//     by the OpenAI Responses adapter (responses/input.go), and by the
//     OpenAI-compatible chat adapter for the reasoning_details shape;
//   - a cryptographic signature (Anthropic) replays its text and signature; an
//     OpenAI-compatible wire field name in Signature means the text is replayed
//     in that field, with the name itself not payload.
//
// Text that carries no replay metadata is billed only for the adapters that
// replay it unsigned, decided by the target the caller supplied: a resolved row
// decides it exactly (unsignedThinkingReplayed), while a caller that passed
// only names gets the documented fallback (unsignedThinkingReplayedByName).
func thinkingReplayChars(target targetInfo, p ContentPart) int {
	if p.Thinking == nil {
		return 0
	}
	t := p.Thinking
	if p.Kind == ContentRedThinking {
		return len(t.Text)
	}
	if t.EncryptedContent != "" {
		// An OpenAI-compatible encrypted reasoning_details array is replayed by
		// the chat adapter together with the separately parsed text
		// (chatcompletions/messages.go), and the Anthropic adapter replays the
		// text while ignoring the blob. The opaque OpenAI Responses blob is the
		// other shape: it replays with its summary and id and puts no text on
		// the wire, so only that shape bills them.
		if IsOpenAICompatEncryptedReasoning(t.EncryptedContent) {
			return len(t.EncryptedContent) + len(t.Text)
		}
		chars := len(t.EncryptedContent) + len(t.ID)
		for _, s := range t.Summary {
			chars += len(s)
		}
		return chars
	}
	if t.Signature != "" {
		if IsOpenAICompatReasoningField(t.Signature) {
			return len(t.Text)
		}
		return len(t.Text) + len(t.Signature)
	}
	if target.unsignedThinking {
		return len(t.Text)
	}
	return 0
}

// targetInfo is what the local estimator knows about the request's target: the
// names the media estimates key on, and whether the target's adapter replays a
// thinking part that carries text and no replay metadata.
type targetInfo struct {
	provider, model string
	protocol        string
	surface         string
	family          string
	// resolved marks a target built from a registry row. The row's instance is
	// an alias it was reached through, not vendor identity, so the media-family
	// name rule reads a resolved target's model name alone -- unless the caller
	// supplied the provider itself, which is evidence the caller chose to give.
	resolved bool
	// providerFromCaller marks a provider name that came from the caller rather
	// than from the row's instance.
	providerFromCaller bool
	unsignedThinking   bool
}

// vendorNameBasis is the provider name the media-family name rule may read. A
// resolved row's instance name says where the row was reached, not who made the
// model -- any gateway can be called anything -- so a resolved target offers only
// its model name; a caller that passed names alone has nothing else to go on.
func (t targetInfo) vendorNameBasis() string {
	if t.resolved && !t.providerFromCaller {
		return ""
	}
	return t.provider
}

// mediaFamily is the image-token family for the target, resolved from the most
// particular fact available to the least: the row's recorded model family, then
// its surface, then the model's own name, and only then the wire protocol. A
// tokenizer family is a property of the model, not of the endpoint that serves
// it: OpenRouter serves anthropic/claude-* and google/gemini-* rows over
// openai-chat, a row the registry never classified still carries a name that says
// which model it is, and a resolver can fill the surface from the provider when
// the row omits it -- so the family, which is the model's own, answers before the
// surface. The protocol answers last because it describes only the endpoint; the
// name rule is the last resort for callers that passed names only.
func (t targetInfo) mediaFamily() string {
	if f := mediaFamilyFromModelFamily(t.family); f != "" {
		return f
	}
	if genericModelFamily(t.family) {
		// The registry classifies this family as deliberately generic (gpt-oss:
		// its rows declare text-only input), so the empty result above is a
		// decision, not an absence: neither the surface, the model name, nor the
		// wire protocol may put an image family back.
		return ""
	}
	switch t.surface {
	case registry.SurfaceAnthropic:
		return "anthropic"
	case registry.SurfaceOpenAI:
		return "openai"
	case registry.SurfaceGoogle:
		return "google"
	}
	if f := providerTokenFamily(t.vendorNameBasis(), t.model); f != "" {
		return f
	}
	switch t.protocol {
	case registry.ProtocolAnthropic:
		return "anthropic"
	case registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses:
		return "openai"
	case registry.ProtocolGoogle:
		return "google"
	}
	return ""
}

// mediaFamilyFromModelFamily maps the registry's model family to the image-token
// family it bills as. The rule mirrors the registry's own family → surface rule
// (llm/registry §6.1) so one classification serves both: claude* and
// gemini*/gemma* keep their vendors' rules, gpt*/o/o-mini/o-pro are OpenAI's,
// and gpt-oss is deliberately generic — its rows declare text-only input, so no
// image rule is claimed for it. A family with no rule of its own (llama,
// minimax, kimi, deepseek) returns "" so its caller keeps looking.
func mediaFamilyFromModelFamily(family string) string {
	f := strings.ToLower(strings.TrimSpace(family))
	switch {
	case strings.HasPrefix(f, "claude"):
		return "anthropic"
	case strings.HasPrefix(f, "gpt-oss"):
		return ""
	case strings.HasPrefix(f, "gpt"), f == "o", f == "o-mini", f == "o-pro":
		return "openai"
	case strings.HasPrefix(f, "gemini"), strings.HasPrefix(f, "gemma"):
		return "google"
	default:
		return ""
	}
}

// genericModelFamily reports whether the registry classifies a family as
// deliberately generic -- a family whose rows carry no vendor tokenizer rules at
// all, so no image family may be inferred for them from anywhere else.
func genericModelFamily(family string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(family)), "gpt-oss")
}

// targetFromNames builds the name-based view used by the exported entry points
// that take a bare Request or message list.
func targetFromNames(provider, model string) targetInfo {
	return targetInfo{provider: provider, model: model, unsignedThinking: unsignedThinkingReplayedByName(provider, model)}
}

// targetFromResolved builds the exact view from a resolved registry row. Names
// the caller did not supply come from the row, so media estimation uses the
// resolved identity rather than the generic fallback.
func targetFromResolved(res registry.Resolved, provider, model string) targetInfo {
	providerFromCaller := strings.TrimSpace(provider) != ""
	if !providerFromCaller {
		provider = res.Instance
	}
	if strings.TrimSpace(model) == "" {
		model = res.ModelID
	}
	return targetInfo{provider: provider, model: model, protocol: res.Protocol, surface: res.Surface, family: res.Model.Family, resolved: true, providerFromCaller: providerFromCaller, unsignedThinking: unsignedThinkingReplayed(res, provider, model)}
}

// unsignedThinkingReplayed reports whether the adapter the resolved target
// selects replays a ContentThinking part that carries text and no replay
// metadata:
//
//   - the Anthropic Messages adapter emits the block it is given as
//     {"type":"thinking","thinking":text} (anthropic/request.go);
//   - the OpenAI-compatible chat adapter replays the text on the reasoning
//     field unless the row is declared non-reasoning, and merges it into
//     assistant content when the row sets ThinkingAsText
//     (chatcompletions/messages.go);
//   - the OpenAI Responses adapter re-sends a reasoning item only when it
//     carries encrypted_content and keeps raw reasoning_text for display only
//     (gateway-fronted GLM), and Google drops the part; neither replays this
//     text.
//
// A row with no resolved protocol falls back to names, because such a row still
// knows what it was named. Those are the effective provider and model — the row's
// own only where the caller supplied none — so a partially resolved row cannot
// drop the vendor the request names and undercount what a name-based caller
// bills correctly.
func unsignedThinkingReplayed(res registry.Resolved, provider, model string) bool {
	switch res.Protocol {
	case registry.ProtocolAnthropic:
		return true
	case registry.ProtocolOpenAIChat:
		return registry.BoolValue(res.Caps.ThinkingAsText) || !res.Caps.ReasoningDisabled()
	case "":
		return unsignedThinkingReplayedByName(provider, model)
	default:
		return false
	}
}

// unsignedThinkingReplayedByName is the fallback for callers with no resolved
// row. The provider name is consulted first, so a model name cannot override a
// provider that selects a non-replaying adapter; a Claude model is the last
// resort, for an alias whose name selects nothing.
//
// It can only be as good as the names: a curated OpenAI-compatible chat
// provider whose name carries no marker (ollama, groq, zai, moonshotai, a
// non-Claude openrouter row) and an instance alias that hides its base are both
// under-billed here. Callers holding a resolved row avoid that by using
// EstimateInputTokensForResolved or EstimateMessagesInputTokensForResolved.
func unsignedThinkingReplayedByName(provider, model string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(p, "anthropic"):
		// Also google-vertex-anthropic and anthropic-compatible, which the
		// Anthropic protocol serves.
		return true
	case strings.Contains(p, "google") || strings.Contains(p, "gemini"):
		// google-compatible names the Google protocol, which drops the part.
		return false
	case strings.Contains(p, "compat") || strings.Contains(p, "openai-chat"):
		return true
	case strings.Contains(p, "openai"):
		// The curated openai rows speak the Responses protocol, which keeps raw
		// reasoning text for display only.
		return false
	case strings.Contains(m, "claude"):
		return true
	default:
		return false
	}
}

func estimateImageTokens(t targetInfo, img *ImageData) int {
	if img == nil {
		return 0
	}
	width, height, ok := imageDimensions(img)
	if !ok {
		return fallbackMediaTokens + len(img.URL)/4 + len(img.MediaType)/4 + len(img.Detail)/4
	}
	switch t.mediaFamily() {
	case "google":
		return estimateGoogleImageTokens(width, height)
	case "anthropic":
		return estimateAnthropicImageTokens(width, height)
	case "openai":
		return estimateOpenAIImageTokens(width, height, img.Detail)
	default:
		return fallbackMediaTokens + len(img.MediaType)/4 + len(img.Detail)/4
	}
}

func imageDimensions(img *ImageData) (int, int, bool) {
	if len(img.Data) > 0 {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
		if err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return cfg.Width, cfg.Height, true
		}
		return 0, 0, false
	}
	u := strings.TrimSpace(img.URL)
	if !IsLocalPath(u) {
		return 0, 0, false
	}
	f, err := os.Open(ExpandTilde(u))
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func providerTokenFamily(provider, model string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	// gpt-oss is deliberately generic in the registry (§6.1) — its rows declare
	// text-only input — so no spelling of it may claim OpenAI's image rules. The
	// family map already refuses it for a resolved row, but that refusal is
	// invisible here: "gpt-" matches the OpenAI branch, and a gateway's
	// openai/gpt-oss name matches the leading-"o" one, so without this the name
	// stage re-claims the tokenizer the family map just declined.
	if strings.Contains(m, "gpt-oss") {
		return ""
	}
	switch {
	case p == "google" || p == "gemini" || strings.Contains(m, "gemini"):
		return "google"
	case strings.Contains(p, "anthropic") || strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(p, "openai") || strings.HasPrefix(m, "gpt-") || oSeriesModelName(m):
		return "openai"
	default:
		return ""
	}
}

// localInputEstimate is CountInputTokens' local half: a resolved target estimates
// from its row, and the provider name it may read as vendor evidence is the
// caller's own, never the instance the request resolved to.
func localInputEstimate(req Request, callerProvider string, t dispatchTarget) InputTokenCount {
	if !t.resolved {
		return EstimateInputTokens(req)
	}
	estimateReq := req
	estimateReq.Provider = callerProvider
	return EstimateInputTokensForResolved(t.res, estimateReq)
}

// oSeriesModelName matches the OpenAI o-series ids the name rule may claim: "o"
// alone, an "o-" prefix (o-mini, o-pro), "o" followed by a digit (o1, o3,
// o4-mini), and the namespaced spellings of those (openai/o3, openai.o3). A bare
// leading "o" is not an OpenAI claim: open-mistral, olmo, openchat, openrouter/*
// and ollama/* are other models behind other protocols.
func oSeriesModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "openai/")
	m = strings.TrimPrefix(m, "openai.")
	if m == "o" || strings.HasPrefix(m, "o-") {
		return true
	}
	return len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

func estimateGoogleImageTokens(width, height int) int {
	if width <= 384 && height <= 384 {
		return 258
	}
	return 258 * ceilDiv(width, 768) * ceilDiv(height, 768)
}

func estimateAnthropicImageTokens(width, height int) int {
	return ceilDiv(width, 28) * ceilDiv(height, 28)
}

func estimateOpenAIImageTokens(width, height int, detail string) int {
	if strings.EqualFold(strings.TrimSpace(detail), "low") {
		return 85
	}
	w, h := scaleWithin(float64(width), float64(height), 2048)
	short := math.Min(w, h)
	if short > 768 {
		scale := 768 / short
		w *= scale
		h *= scale
	}
	return 85 + 170*ceilDiv(int(math.Ceil(w)), 512)*ceilDiv(int(math.Ceil(h)), 512)
}

func scaleWithin(width, height, maxSide float64) (float64, float64) {
	long := math.Max(width, height)
	if long <= maxSide {
		return width, height
	}
	scale := maxSide / long
	return width * scale, height * scale
}

func ceilDiv(n, d int) int {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}
