package tui

import (
	"testing"

	"primeradiant.com/evener/envvars"
)

// ---- formatAuthStatusSummary: the cases the hub corpus cannot produce -------
//
// Every real credential source is pinned against the hub's own recorded
// answers in hub_auth_wire_test.go. What is left here is the handful of
// shapes no live hub sends: a status with no instance at all, an activeSource
// this build has no words for, and the wire's Error field, which the hub
// reserves but does not currently set.

func TestCovFormatAuthStatusSummary(t *testing.T) {
	tests := []struct {
		name   string
		status authStatus
		want   string
	}{
		{"no instance", authStatus{}, "Auth is not available until an instance is selected."},
		{"unsupported instance", authStatus{Provider: "anthropic"}, `Auth is not supported for instance "anthropic".`},
		{
			"unknown source falls through to the wire value",
			authStatus{Provider: "work", Supported: true, ActiveSource: "something-new"},
			"work auth: something-new",
		},
		{
			"error detail is appended",
			authStatus{Provider: "openai-codex", Supported: true, ActiveSource: "oauth", Email: "user@example.com", Error: "something broke"},
			"openai-codex auth: OAuth (user@example.com): something broke",
		},
		{
			"error without an account",
			authStatus{Provider: "openai-codex", Supported: true, ActiveSource: "oauth", Error: "something broke"},
			"openai-codex auth: OAuth: something broke",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAuthStatusSummary(tt.status); got != tt.want {
				t.Fatalf("formatAuthStatusSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---- firstNonEmptyString ----------------------------------------------------

func TestCovFirstNonEmptyString_FirstNonEmpty(t *testing.T) {
	if got := envvars.FirstNonEmpty("", "", "third", "fourth"); got != "third" {
		t.Fatalf("firstNonEmptyString = %q, want third", got)
	}
}

func TestCovFirstNonEmptyString_AllEmpty(t *testing.T) {
	if got := envvars.FirstNonEmpty(" ", "  ", "\t"); got != "" {
		t.Fatalf("all whitespace = %q, want empty", got)
	}
}

func TestCovFirstNonEmptyString_None(t *testing.T) {
	if got := envvars.FirstNonEmpty(); got != "" {
		t.Fatalf("no args = %q, want empty", got)
	}
}

func TestCovFirstNonEmptyString_FirstValue(t *testing.T) {
	if got := envvars.FirstNonEmpty("first", "second"); got != "first" {
		t.Fatalf("first value = %q, want first", got)
	}
}
