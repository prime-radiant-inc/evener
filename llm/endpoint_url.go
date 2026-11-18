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

type servingURL struct{ origin, resource string }

func parseServingURL(endpoint string) servingURL {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return servingURL{}
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
		return servingURL{}
	}
	return servingURL{"sha256:" + recipientDigest(u.Scheme+"\x00"+strings.ToLower(u.Hostname())+"\x00"+port), u.RequestURI()}
}

func recipientDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (u servingURL) exactRecipient() string {
	if u.origin == "" {
		return UnknownServingOrigin
	}
	return u.origin + "/endpoint:" + recipientDigest(u.resource)
}

// ConfiguredServingRecipient identifies the deployment shared by the row's
// Complete, Stream and CountTokens operations.
func ConfiguredServingRecipient(res registry.Resolved) string {
	origin := parseServingURL(res.Transport.BaseURL).origin
	if origin == "" {
		return UnknownServingOrigin
	}
	return origin + "/configured:" + res.ReasoningReplayRoute()
}

// ServingRecipient identifies the deployment reached by a wire URL, or accepts
// a previously computed proof. Outside the row's configured operation set,
// the escaped path and query identify the recipient without storing URL text.
func ServingRecipient(endpoint string, res registry.Resolved) string {
	recipient := endpoint
	if !strings.HasPrefix(endpoint, "sha256:") {
		recipient = parseServingURL(endpoint).exactRecipient()
	}
	if res.Transport.BaseURL == "" || !strings.Contains(recipient, "/endpoint:") {
		return recipient
	}
	for _, path := range [...]string{res.Transport.Endpoint, res.Transport.StreamEndpoint, res.Transport.CountTokensEndpoint} {
		if path != registry.EndpointUnsupported {
			configured := parseServingURL(res.RequestURL(path))
			if configured.exactRecipient() == recipient {
				return configured.origin + "/configured:" + res.ReasoningReplayRoute()
			}
		}
	}
	return recipient
}

// Older history can carry only a serving origin or a display URL. Its
// best-effort origin proof remains usable by that origin's deployments.
func ownsServingState(recipient, source string, res registry.Resolved) bool {
	if recipient == UnknownServingOrigin || source == UnknownServingOrigin {
		return false
	}
	if strings.HasPrefix(source, "sha256:") {
		if recipient == source || strings.Contains(recipient, "/configured:") && recipient == ServingRecipient(source, res) {
			return true
		}
	} else {
		source = parseServingURL(source).origin
	}
	if source == "" {
		return false
	}
	origin, _, _ := strings.Cut(recipient, "/")
	return origin == source
}

// HistoryModeForEndpoint describes the history a receiving deployment can consume.
func (r Request) HistoryModeForEndpoint(endpoint string, res registry.Resolved) HistoryMode {
	if r.Continuation != nil && r.Continuation.AnchorEndpoint != "" && !ownsServingState(ServingRecipient(endpoint, res), r.Continuation.AnchorEndpoint, res) {
		return HistoryModeFullHistoryFallback
	}
	return r.HistoryMode
}

// ForReasoningEndpoint projects the original request for one HTTP recipient.
func (r Request) ForReasoningEndpoint(endpoint string, res registry.Resolved) (Request, bool) {
	to := ServingRecipient(endpoint, res)
	// Consecutive turns usually share a serving deployment.
	var last string
	var owned bool
	owns := func(source string) bool {
		if last != source {
			last, owned = source, ownsServingState(to, source, res)
		}
		return owned
	}
	changed := false
	if mode := r.HistoryModeForEndpoint(to, res); mode != r.HistoryMode {
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
