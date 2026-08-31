package llm

import (
	"testing"
	"time"
)

func TestParseRetryAfterEdgeCases(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)

	t.Run("empty is nil", func(t *testing.T) {
		if d := ParseRetryAfter("   ", now); d != nil {
			t.Errorf("ParseRetryAfter(empty) = %v, want nil", d)
		}
	})

	t.Run("negative seconds falls through to nil", func(t *testing.T) {
		// -5 parses as an int but is negative, so the seconds branch is skipped;
		// it is not a valid HTTP-date either, so the result is nil.
		if d := ParseRetryAfter("-5", now); d != nil {
			t.Errorf("ParseRetryAfter(-5) = %v, want nil", d)
		}
	})

	t.Run("past http-date clamps to zero", func(t *testing.T) {
		d := ParseRetryAfter("Sat, 07 Feb 2026 00:00:00 GMT", now.Add(time.Hour))
		if d == nil || *d != 0 {
			t.Errorf("ParseRetryAfter(past date) = %v, want 0", d)
		}
	})

	t.Run("garbage is nil", func(t *testing.T) {
		if d := ParseRetryAfter("not-a-date", now); d != nil {
			t.Errorf("ParseRetryAfter(garbage) = %v, want nil", d)
		}
	})
}

func TestNonHTTPBaseErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"provider and message", NewStreamError("openai", "boom", nil), "openai error: boom"},
		{"message without provider", NewAbortError("cancelled by user", nil), "cancelled by user"},
		{"empty message without provider", NewAbortError("", nil), "request failed"},
		{"empty message with provider", NewStreamError("openai", "   ", nil), "openai error: request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewUnsupportedToolChoiceError(t *testing.T) {
	t.Run("empty mode is labeled", func(t *testing.T) {
		err := NewUnsupportedToolChoiceError("openai", "  ")
		if got := err.Error(); got != "openai error: unsupported tool_choice mode: (empty)" {
			t.Errorf("Error() = %q", got)
		}
	})
	t.Run("named mode", func(t *testing.T) {
		err := NewUnsupportedToolChoiceError("anthropic", "required")
		if got := err.Error(); got != "anthropic error: unsupported tool_choice mode: required" {
			t.Errorf("Error() = %q", got)
		}
	})
}
