package llm

import (
	"strings"
	"testing"
)

func TestProviderFailureMessage(t *testing.T) {
	cases := []struct {
		name string
		op   string
		body string
		want string
	}{
		{
			name: "prefers the provider's own message",
			op:   "responses.create(stream)",
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			want: "responses.create(stream) failed: The usage limit has been reached",
		},
		{
			name: "falls back to the raw body when there is no message field",
			op:   "messages.create",
			body: `{"detail":"upstream exploded"}`,
			want: `messages.create failed: {"detail":"upstream exploded"}`,
		},
		{
			name: "falls back to the raw body when it is not JSON at all",
			op:   "generateContent",
			body: "  502 Bad Gateway\n",
			want: "generateContent failed: 502 Bad Gateway",
		},
		{
			name: "reports an empty body rather than trailing a bare colon",
			op:   "chat.completions(stream)",
			body: "",
			want: "chat.completions(stream) failed: empty response body",
		},
		{
			name: "a plain-string error field is usable as-is",
			op:   "responses.create",
			body: `{"error":"model_not_found"}`,
			want: "responses.create failed: model_not_found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProviderFailureMessage(tc.op, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("ProviderFailureMessage = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "map[") {
				t.Errorf("message leaks a Go map dump: %q", got)
			}
		})
	}
}

// A giant HTML error page must not become the error message.
func TestProviderFailureMessageTruncatesRunawayBodies(t *testing.T) {
	body := "<html>" + strings.Repeat("x", 4000) + "</html>"
	got := ProviderFailureMessage("responses.create", []byte(body))

	if len(got) > 600 {
		t.Fatalf("message length %d, want a truncated message", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated message should be marked with an ellipsis: %q", got)
	}
}

// The endpoint-fallback classifier keys on "responses.create" appearing in the
// message, so the operation prefix is load-bearing, not decorative.
func TestProviderFailureMessagePreservesFallbackSignal(t *testing.T) {
	msg := ProviderFailureMessage("responses.create(stream)", []byte(`{"error":{"message":"This model is not supported"}}`))
	err := ErrorFromHTTPStatus("openai", 404, msg, nil, nil)
	if got := Classify(err); got != ErrorClassFallback {
		t.Fatalf("Classify = %v, want ErrorClassFallback", got)
	}
}

// classifyByMessage reads the message for 400/422 signals. Surfacing the
// provider's own wording must keep those signals intact.
func TestProviderFailureMessageKeepsClassificationSignals(t *testing.T) {
	msg := ProviderFailureMessage("messages.create", []byte(`{"error":{"message":"context length exceeded for this model"}}`))
	err := ErrorFromHTTPStatus("anthropic", 400, msg, nil, nil)
	if got := Kind(err); got != KindContextLength {
		t.Fatalf("Kind = %v, want KindContextLength", got)
	}
}
