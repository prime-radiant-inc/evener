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
func EstimateInputTokens(req Request) InputTokenCount {
	tokens := estimateMessagesInputTokens(req.Provider, req.Model, req.Messages)
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
// history-only accounting that does not yet have a full Request.
func EstimateMessagesInputTokens(messages []Message) InputTokenCount {
	return InputTokenCount{
		Tokens: estimateMessagesInputTokens("", "", messages),
		Exact:  false,
		Source: TokenCountSourceLocalEstimate,
	}
}

// CountInputTokens resolves the target provider and uses its exact counter when
// available. Providers without exact support fall back to the local estimate.
func (c *Client) CountInputTokens(ctx context.Context, req Request) (InputTokenCount, error) {
	if err := req.Validate(); err != nil {
		return InputTokenCount{}, err
	}
	if req.AdapterTimeout == nil {
		at := DefaultAdapterTimeout()
		req.AdapterTimeout = &at
	}
	prov := req.Provider
	if prov == "" {
		prov = c.defaultProvider
	}
	if prov == "" {
		return InputTokenCount{}, &ConfigurationError{Message: "no provider specified and no default provider configured"}
	}
	prov = normalizeProviderName(prov)
	adapter, ok := c.providers[prov]
	if !ok {
		return InputTokenCount{}, &ConfigurationError{Message: "unknown provider: " + prov}
	}
	req.Provider = prov

	if counter, ok := adapter.(InputTokenCounter); ok {
		out, err := counter.CountInputTokens(ctx, req)
		if err != nil {
			if errors.Is(err, ErrInputTokenCountUnsupported) {
				out := EstimateInputTokens(req)
				out.Provider = prov
				return out, nil
			}
			tag := c.behaviorTagFor(prov)
			err = RewriteErrorProvider(err, prov)
			err = StampErrorBehaviorTag(err, tag)
			return InputTokenCount{}, err
		}
		if out.Source == "" {
			out.Source = TokenCountSourceProvider
		}
		if out.Source == TokenCountSourceProvider {
			out.Exact = true
		}
		if out.Provider == "" {
			out.Provider = prov
		}
		if out.Model == "" {
			out.Model = req.Model
		}
		return out, nil
	}

	out := EstimateInputTokens(req)
	out.Provider = prov
	return out, nil
}

func estimateMessagesInputTokens(provider, model string, messages []Message) int {
	chars := 0
	tokens := 0
	for _, m := range messages {
		c, t := estimateMessageInputParts(provider, model, m)
		chars += c
		tokens += t
	}
	return tokens + chars/4
}

func estimateMessageInputParts(provider, model string, m Message) (int, int) {
	chars := len(m.Name) + len(m.ToolCallID)
	tokens := 0
	for _, p := range m.Content {
		switch p.Kind {
		case ContentText:
			chars += len(p.Text)
		case ContentImage:
			if p.Image != nil {
				tokens += estimateImageTokens(provider, model, p.Image)
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
					tokens += estimateImageTokens(provider, model, &ImageData{Data: p.ToolResult.ImageData, MediaType: p.ToolResult.ImageMediaType})
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
			if p.Thinking != nil {
				chars += len(p.Thinking.Text)
				chars += len(p.Thinking.Signature)
			}
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

func estimateImageTokens(provider, model string, img *ImageData) int {
	if img == nil {
		return 0
	}
	width, height, ok := imageDimensions(img)
	if !ok {
		return fallbackMediaTokens + len(img.URL)/4 + len(img.MediaType)/4 + len(img.Detail)/4
	}
	switch providerTokenFamily(provider, model) {
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
	switch {
	case p == "google" || p == "gemini" || strings.Contains(m, "gemini"):
		return "google"
	case strings.Contains(p, "anthropic") || strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(p, "openai") || strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o"):
		return "openai"
	default:
		return ""
	}
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
