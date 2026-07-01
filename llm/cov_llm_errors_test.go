package llm

import (
	"errors"
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

func behaviorTagOf(t *testing.T, err error) string {
	t.Helper()
	if bt, ok := err.(interface{ BehaviorTag() string }); ok {
		return bt.BehaviorTag()
	}
	return ""
}

func TestStampErrorBehaviorTag(t *testing.T) {
	t.Run("nil error is returned unchanged", func(t *testing.T) {
		if got := StampErrorBehaviorTag(nil, "openai"); got != nil {
			t.Errorf("StampErrorBehaviorTag(nil) = %v, want nil", got)
		}
	})

	t.Run("blank tag is a no-op", func(t *testing.T) {
		err := ErrorFromHTTPStatus("openai", 500, "boom", nil, nil)
		out := StampErrorBehaviorTag(err, "   ")
		if behaviorTagOf(t, out) != "" {
			t.Errorf("blank tag stamped: %q", behaviorTagOf(t, out))
		}
	})

	t.Run("non-setter error is returned unchanged", func(t *testing.T) {
		plain := errors.New("plain")
		if got := StampErrorBehaviorTag(plain, "openai"); got != plain {
			t.Errorf("StampErrorBehaviorTag(plain) = %v, want same error", got)
		}
	})

	t.Run("empty provider is a no-op", func(t *testing.T) {
		err := ErrorFromHTTPStatus("", 500, "boom", nil, nil)
		out := StampErrorBehaviorTag(err, "openai")
		if behaviorTagOf(t, out) != "" {
			t.Errorf("stamped despite empty provider: %q", behaviorTagOf(t, out))
		}
	})

	t.Run("stamps when provider is set", func(t *testing.T) {
		err := ErrorFromHTTPStatus("openai", 500, "boom", nil, nil)
		out := StampErrorBehaviorTag(err, "openai-responses")
		if got := behaviorTagOf(t, out); got != "openai-responses" {
			t.Errorf("BehaviorTag = %q, want %q", got, "openai-responses")
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
