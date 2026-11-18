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
	endpoint, _ := r.Raw["endpoint_url"].(string)
	return RecordedReasoningEndpoint(r.ServingOrigin, endpoint)
}

// RecordedReasoningEndpoint retains a saved path alongside an origin-only
// stamp when they name the same HTTP origin. A fully identified deployment
// or an explicitly unknown recipient needs no display URL.
func RecordedReasoningEndpoint(proof, endpoint string) string {
	if proof == "" || (endpoint != "" && !strings.Contains(proof, "/") && parseServingURL(endpoint).origin == proof) {
		return SanitizeEndpointURL(endpoint)
	}
	return proof
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
	if res.Protocol == registry.ProtocolGoogle {
		return origin + "/google:" + res.GoogleToolDeployment()
	}
	return bindInstanceRecipient(origin+"/configured:"+res.ReasoningReplayRoute(), res)
}

// Bind configured and redirected OpenAI protocol recipients to the provider instance.
// Credential rotation keeps the same instance, and no credential enters the proof.
func bindInstanceRecipient(recipient string, res registry.Resolved) string {
	const instanceTag = "/instance:"
	_, instance, bound := strings.Cut(recipient, instanceTag)
	if (res.Protocol != registry.ProtocolOpenAIChat && res.Protocol != registry.ProtocolOpenAIResponses) || res.Instance == "" {
		if bound {
			return UnknownServingOrigin
		}
		return recipient
	}
	if !strings.Contains(recipient, "/") {
		return recipient
	}
	expected := recipientDigest(res.Instance)
	if bound {
		if instance != expected {
			return UnknownServingOrigin
		}
		return recipient
	}
	return recipient + instanceTag + expected
}

// ServingRecipient identifies the deployment reached by a wire URL, or accepts
// a previously computed proof. Outside the row's configured operation set,
// the escaped path and query identify the recipient without storing URL text.
func ServingRecipient(endpoint string, res registry.Resolved) string {
	return servingRecipient(endpoint, res, false)
}

// RecordedServingRecipient can recognize the path of a configured operation
// whose query was removed when its response URL was saved.
func RecordedServingRecipient(endpoint string, res registry.Resolved) string {
	return servingRecipient(endpoint, res, true)
}

func servingRecipient(endpoint string, res registry.Resolved, recorded bool) string {
	recipient := endpoint
	matchPathOnly := false
	if !strings.HasPrefix(endpoint, "sha256:") {
		u := parseServingURL(endpoint)
		matchPathOnly = recorded && !strings.Contains(u.resource, "?")
		recipient = u.exactRecipient()
	} else if res.Protocol == registry.ProtocolGoogle && strings.Contains(recipient, "/configured:") {
		origin := parseServingURL(res.Transport.BaseURL).origin
		if origin != "" && recipient == origin+"/configured:"+res.ReasoningReplayRoute() {
			return ConfiguredServingRecipient(res)
		}
	}
	if res.Transport.BaseURL == "" || !strings.Contains(recipient, "/endpoint:") {
		return bindInstanceRecipient(recipient, res)
	}
	for _, path := range [...]string{res.Transport.Endpoint, res.Transport.StreamEndpoint, res.Transport.CountTokensEndpoint} {
		if path != registry.EndpointUnsupported {
			configured := parseServingURL(res.RequestURL(path))
			if matchPathOnly {
				configured.resource, _, _ = strings.Cut(configured.resource, "?")
			}
			if configured.exactRecipient() == recipient {
				return ConfiguredServingRecipient(res)
			}
		}
	}
	return bindInstanceRecipient(recipient, res)
}

// An old digest without a recorded URL establishes only the serving origin.
// A recorded operation path or a deployment stamp gives a narrower owner.
func ownsServingState(recipient, source string, res registry.Resolved) bool {
	if recipient == UnknownServingOrigin || source == UnknownServingOrigin || source == "" {
		return false
	}
	if recipient == source {
		return true
	}
	if strings.HasPrefix(source, "sha256:") && !strings.Contains(source, "/") {
		origin, _, _ := strings.Cut(recipient, "/")
		return origin == source
	}
	return recipient == RecordedServingRecipient(source, res)
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

// HasNativeReasoning reports whether a message contains state bound to its producer.
func (m Message) HasNativeReasoning() bool {
	return slices.ContainsFunc(m.Content, carriesNativeReasoning)
}

// BindReasoningEndpoint ties surviving native state to the origin that issued it.
func (m Message) BindReasoningEndpoint(endpoint string) Message {
	if endpoint != "" && m.HasNativeReasoning() {
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
