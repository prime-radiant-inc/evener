package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
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

// classifyByMessage reads the message for 400/422 signals. Surfacing the
// provider's own wording must keep those signals intact.
func TestProviderFailureMessageKeepsClassificationSignals(t *testing.T) {
	msg := ProviderFailureMessage("messages.create", []byte(`{"error":{"message":"context length exceeded for this model"}}`))
	err := ErrorFromHTTPStatus("anthropic", 400, msg, nil, nil)
	if got := Kind(err); got != KindContextLength {
		t.Fatalf("Kind = %v, want KindContextLength", got)
	}
}

// A gateway's top-level {"message": ...} body yields the same failure text as
// the nested {"error": {"message": ...}} shape.
func TestProviderFailureMessageTopLevelMessage(t *testing.T) {
	nested := ProviderFailureMessage("test", []byte(`{"error":{"message":"top-level error message"}}`))
	topLevel := ProviderFailureMessage("test", []byte(`{"message":"top-level error message"}`))
	if topLevel != nested {
		t.Fatalf("top-level message = %q, want the same as the nested shape %q", topLevel, nested)
	}
}

// Truncation backs off a rune split by the byte cut, keeping the valid prefix.
func TestTruncateForMessageCutsOnARuneBoundary(t *testing.T) {
	// 'é' is two bytes; placing it across the cut leaves its first byte at the
	// last kept position.
	ascii := strings.Repeat("x", maxFailureMessageBody-1)
	got := truncateForMessage(ascii + "é" + strings.Repeat("y", 100))
	if !utf8.ValidString(got) || !strings.HasPrefix(got, ascii) || strings.Contains(got, "y") || len(got) > maxFailureMessageBody+len("…") {
		t.Fatalf("truncateForMessage kept %d bytes ending %q, want the valid ASCII prefix only", len(got), got[len(got)-10:])
	}
}
