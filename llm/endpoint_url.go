package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// UnknownServingOrigin marks an HTTP response whose serving origin could not
// be verified. No HTTP recipient can claim its native history.
const UnknownServingOrigin = "unknown"

// ReasoningEndpoint returns transport evidence or the recorded legacy URL.
func (r Response) ReasoningEndpoint() string {
	if r.ServingOrigin != "" {
		return r.ServingOrigin
	}
	endpoint, _ := r.Raw["endpoint_url"].(string)
	return SanitizeEndpointURL(endpoint)
}

// HasReasoningEndpoints reports whether the HTTP recipient affects this request.
func (r Request) HasReasoningEndpoints() bool {
	return (r.Continuation != nil && r.Continuation.AnchorEndpoint != "") ||
		slices.ContainsFunc(r.Messages, func(m Message) bool { return m.ReasoningEndpoint != "" })
}

// servingOriginDigest identifies an HTTP origin without retaining URL text.
func servingOriginDigest(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	switch u.Scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if port == "" {
			port = "80"
		}
	default:
		return ""
	}
	sum := sha256.Sum256([]byte(u.Scheme + "\x00" + strings.ToLower(u.Hostname()) + "\x00" + port))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// reasoningOrigin accepts either an opaque transport stamp or a URL.
func reasoningOrigin(endpoint string) string {
	if len(endpoint) == len("sha256:")+2*sha256.Size && strings.HasPrefix(endpoint, "sha256:") {
		return endpoint
	}
	return servingOriginDigest(endpoint)
}

// HistoryModeForEndpoint describes the history a receiving origin can consume.
func (r Request) HistoryModeForEndpoint(endpoint string) HistoryMode {
	if r.Continuation != nil && r.Continuation.AnchorEndpoint != "" {
		to := reasoningOrigin(endpoint)
		if to == "" || to != reasoningOrigin(r.Continuation.AnchorEndpoint) {
			return HistoryModeFullHistoryFallback
		}
	}
	return r.HistoryMode
}

// ForReasoningEndpoint projects the original request for one HTTP recipient.
func (r Request) ForReasoningEndpoint(endpoint string, res registry.Resolved) (Request, bool) {
	to := reasoningOrigin(endpoint)
	// Consecutive turns usually share a serving origin.
	var last, origin string
	owns := func(source string) bool {
		if last != source {
			last, origin = source, reasoningOrigin(source)
		}
		return to != "" && to == origin
	}
	changed := false
	if mode := r.HistoryModeForEndpoint(endpoint); mode != r.HistoryMode {
		r.Messages = r.Continuation.FullHistory
		r.PreviousResponseID = ""
		r.ConversationID = ""
		r.Continuation = nil
		r.HistoryMode = mode
		r.InputTokensEstimate = r.FullHistoryInputTokensEstimate
		changed = true
	}
	keepDisplay := res.Protocol == registry.ProtocolOpenAIChat && registry.BoolValue(res.Caps.ThinkingAsText)
	var messages []Message
	for i, m := range r.Messages {
		if m.ReasoningEndpoint == "" || owns(m.ReasoningEndpoint) {
			continue
		}
		if messages == nil {
			messages = slices.Clone(r.Messages)
		}
		messages[i] = m.WithoutNativeReasoning(keepDisplay)
		messages[i].ReasoningEndpoint = ""
	}
	if messages != nil {
		r.Messages, changed = messages, true
	}
	return r, changed
}

func carriesNativeReasoning(p ContentPart) bool {
	return p.Kind == ContentThinking || p.Kind == ContentRedThinking ||
		(p.Kind == ContentToolCall && p.ToolCall != nil && p.ToolCall.ThoughtSignature != "")
}

// BindReasoningEndpoint ties surviving native state to the origin that issued it.
func (m Message) BindReasoningEndpoint(endpoint string) Message {
	if endpoint != "" && slices.ContainsFunc(m.Content, carriesNativeReasoning) {
		m.ReasoningEndpoint = endpoint
	}
	return m
}

// WithoutNativeReasoning preserves visible text and the tool exchange. A Chat
// row that renders thinking as ordinary text can keep its unsigned display.
func (m Message) WithoutNativeReasoning(keepDisplay bool) Message {
	i := slices.IndexFunc(m.Content, carriesNativeReasoning)
	if i < 0 {
		return m
	}
	parts := make([]ContentPart, i, len(m.Content))
	copy(parts, m.Content[:i])
	for _, p := range m.Content[i:] {
		switch {
		case p.Kind == ContentThinking || p.Kind == ContentRedThinking:
			if !keepDisplay {
				continue
			}
			text := p.UnsignedChatDisplayText()
			if text == "" {
				continue
			}
			p = ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{Text: text}}
		case p.Kind == ContentToolCall && p.ToolCall != nil && p.ToolCall.ThoughtSignature != "":
			call := *p.ToolCall
			call.ThoughtSignature = ""
			p.ToolCall = &call
		}
		parts = append(parts, p)
	}
	m.Content = parts
	return m
}

// SanitizeEndpointURL retains only non-credential URL components suitable for
// durable endpoint provenance. Invalid endpoint text has no durable value.
func SanitizeEndpointURL(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return ""
	}
	return (&url.URL{
		Scheme:  parsed.Scheme,
		Host:    parsed.Host,
		Path:    parsed.Path,
		RawPath: parsed.RawPath,
	}).String()
}

// FinalResponseEndpointURL returns the endpoint that produced an HTTP response.
// Synthetic responses without request provenance fall back to the constructed
// request endpoint.
func FinalResponseEndpointURL(resp *http.Response, fallback string) string {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return SanitizeEndpointURL(resp.Request.URL.String())
	}
	return SanitizeEndpointURL(fallback)
}
