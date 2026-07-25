package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// maxFailureMessageBody caps how much of a provider's error body reaches the
// message. An error page can be megabytes of HTML; the message is read by
// humans and copied into logs, so a bounded excerpt beats the whole document.
const maxFailureMessageBody = 512

// ProviderFailureMessage renders the message for a non-2xx provider response as
// "<operation> failed: <detail>", where detail is the provider's own error text
// when the body carries one and a trimmed excerpt of the raw body otherwise.
//
// Adapters share this so every provider reports a failure the same way. It also
// keeps Go's map formatting out of user-facing text: printing a decoded body
// with %v yields "map[error:map[message:...]]", which is unreadable and leaks
// the implementation's shape.
//
// operation names the API call ("responses.create(stream)", "messages.create").
// It stays in the message because the endpoint-fallback classifier and several
// diagnostics match on it.
func ProviderFailureMessage(operation string, body []byte) string {
	detail := strings.TrimSpace(string(body))
	if msg := providerErrorText(body); msg != "" {
		detail = msg
	}
	if detail == "" {
		detail = "empty response body"
	}
	return strings.TrimSpace(operation) + " failed: " + truncateForMessage(detail)
}

// providerErrorText pulls the human-readable error text out of a provider error
// body, handling the shapes the supported providers use:
//
//	{"error":{"message":"..."}}  OpenAI, Anthropic, Google, OpenAI-compatible
//	{"error":"..."}              some compatible servers
//	{"message":"..."}            top-level, seen on gateway errors
func providerErrorText(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return ""
	}
	switch e := raw["error"].(type) {
	case map[string]any:
		if msg, _ := e["message"].(string); strings.TrimSpace(msg) != "" {
			return strings.TrimSpace(msg)
		}
	case string:
		if strings.TrimSpace(e) != "" {
			return strings.TrimSpace(e)
		}
	}
	if msg, _ := raw["message"].(string); strings.TrimSpace(msg) != "" {
		return strings.TrimSpace(msg)
	}
	return ""
}

// truncateForMessage bounds s to maxFailureMessageBody, cutting on a rune
// boundary and marking the cut so a reader knows the text is incomplete.
func truncateForMessage(s string) string {
	if len(s) <= maxFailureMessageBody {
		return s
	}
	cut := s[:maxFailureMessageBody]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + "…"
}
