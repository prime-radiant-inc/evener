package llm

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// HasReasoningEndpoints reports whether the HTTP recipient affects this request.
func (r Request) HasReasoningEndpoints() bool {
	return (r.Continuation != nil && r.Continuation.AnchorEndpoint != "") ||
		slices.ContainsFunc(r.Messages, func(m Message) bool { return m.ReasoningEndpoint != "" })
}

type httpOrigin struct{ scheme, host, port string }

func endpointOrigin(endpoint string) httpOrigin {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return httpOrigin{}
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
		return httpOrigin{}
	}
	return httpOrigin{u.Scheme, strings.ToLower(u.Hostname()), port}
}

// HistoryModeForEndpoint describes the history a receiving origin can consume.
func (r Request) HistoryModeForEndpoint(endpoint string) HistoryMode {
	if r.Continuation != nil && r.Continuation.AnchorEndpoint != "" {
		to := endpointOrigin(endpoint)
		if to.host == "" || to != endpointOrigin(r.Continuation.AnchorEndpoint) {
			return HistoryModeFullHistoryFallback
		}
	}
	return r.HistoryMode
}

// ForReasoningEndpoint projects the original request for one HTTP recipient.
func (r Request) ForReasoningEndpoint(endpoint string, res registry.Resolved) (Request, bool) {
	to := endpointOrigin(endpoint)
	// Consecutive turns usually share a serving URL.
	var last string
	var origin httpOrigin
	owns := func(source string) bool {
		if last != source {
			last, origin = source, endpointOrigin(source)
		}
		return to.host != "" && to == origin
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

// BindReasoningEndpoint ties surviving native state to the URL that issued it.
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
