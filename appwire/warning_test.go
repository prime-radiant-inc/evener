package appwire

import (
	"encoding/json"
	"testing"
)

func TestWarningParamsEffectiveMessage(t *testing.T) {
	tests := []struct {
		name   string
		params WarningParams
		want   string
	}{
		{
			name:   "message set wins even when warning also carries one",
			params: WarningParams{Message: "top-level message", Warning: "provider hiccup"},
			want:   "top-level message",
		},
		{
			name:   "bare-string warning",
			params: WarningParams{Warning: "provider hiccup"},
			want:   "provider hiccup",
		},
		{
			name:   "object-form warning",
			params: WarningParams{Warning: map[string]any{"message": "nested"}},
			want:   "nested",
		},
		{
			name:   "neither message nor warning",
			params: WarningParams{},
			want:   "",
		},
		{
			name:   "blank message falls through to warning",
			params: WarningParams{Message: "   ", Warning: "fallback"},
			want:   "fallback",
		},
		{
			name:   "whitespace-only warning yields nothing",
			params: WarningParams{Warning: "   "},
			want:   "",
		},
		{
			name:   "object warning with whitespace-only message yields nothing",
			params: WarningParams{Warning: map[string]any{"message": "   "}},
			want:   "",
		},
		{
			name:   "object warning with non-string message yields nothing",
			params: WarningParams{Warning: map[string]any{"message": 42}},
			want:   "",
		},
		{
			name:   "object warning with no message field yields nothing",
			params: WarningParams{Warning: map[string]any{"other": "x"}},
			want:   "",
		},
		{
			name:   "number warning yields nothing",
			params: WarningParams{Warning: 42},
			want:   "",
		},
		{
			name:   "bool warning yields nothing",
			params: WarningParams{Warning: true},
			want:   "",
		},
		{
			name:   "array warning yields nothing",
			params: WarningParams{Warning: []any{"x"}},
			want:   "",
		},
		{
			name:   "null warning yields nothing",
			params: WarningParams{Warning: nil},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.params.EffectiveMessage(); got != tc.want {
				t.Fatalf("EffectiveMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDecodeWarningParams drives DecodeWarningParams at the wire-true level:
// raw JSON in, the message a reader should render out. Every shape that
// yields no message under EffectiveMessage falls back to the raw frame
// itself rather than rendering emptiness.
func TestDecodeWarningParams(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "message wins over warning",
			raw:  `{"message":"real message","warning":"ignored"}`,
			want: "real message",
		},
		{
			name: "bare-string warning",
			raw:  `{"warning":"bare string"}`,
			want: "bare string",
		},
		{
			name: "whitespace-only warning falls back to the raw frame",
			raw:  `{"warning":"   "}`,
			want: `{"warning":"   "}`,
		},
		{
			name: "object-form warning message",
			raw:  `{"warning":{"message":"nested"}}`,
			want: "nested",
		},
		{
			name: "object warning with whitespace-only message falls back to the raw frame",
			raw:  `{"warning":{"message":"   "}}`,
			want: `{"warning":{"message":"   "}}`,
		},
		{
			name: "object warning with non-string message falls back to the raw frame",
			raw:  `{"warning":{"message":42}}`,
			want: `{"warning":{"message":42}}`,
		},
		{
			name: "object warning with no message field falls back to the raw frame",
			raw:  `{"warning":{"other":"x"}}`,
			want: `{"warning":{"other":"x"}}`,
		},
		{
			name: "number warning falls back to the raw frame",
			raw:  `{"warning":42}`,
			want: `{"warning":42}`,
		},
		{
			name: "null warning falls back to the raw frame",
			raw:  `{"warning":null}`,
			want: `{"warning":null}`,
		},
		{
			name: "bool warning falls back to the raw frame",
			raw:  `{"warning":true}`,
			want: `{"warning":true}`,
		},
		{
			name: "array warning falls back to the raw frame",
			raw:  `{"warning":[1,2]}`,
			want: `{"warning":[1,2]}`,
		},
		{
			name: "empty params falls back to the raw frame",
			raw:  `{}`,
			want: `{}`,
		},
		{
			name: "not-json falls back to the raw frame",
			raw:  `not-json`,
			want: `not-json`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, got := DecodeWarningParams(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Fatalf("DecodeWarningParams(%s) message = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestDecodeWarningParamsRecoversMessageAndHintDespiteBogusCause pins the
// probe frame: a cause that does not decode into DiagnosticCause still
// leaves every field json did decode populated, so the message and the hint
// both survive even though cause itself does not.
func TestDecodeWarningParamsRecoversMessageAndHintDespiteBogusCause(t *testing.T) {
	raw := json.RawMessage(`{"message":"real message","cause":"bogus-not-an-object","hint":"retry"}`)

	params, message := DecodeWarningParams(raw)

	if message != "real message" {
		t.Fatalf("message = %q, want %q", message, "real message")
	}
	if params.Hint != "retry" {
		t.Fatalf("params.Hint = %q, want %q", params.Hint, "retry")
	}
}
