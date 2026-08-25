package tui

import (
	"testing"

	authopenai "primeradiant.com/evener/auth/openai"
)

// ---- formatAuthStatusSummary: openai env source ------------------------------

func TestCovFormatAuthStatusSummary(t *testing.T) {
	tests := []struct {
		name   string
		status authStatus
		want   string
	}{
		{"environment", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceEnv}, "OpenAI auth: env"},
		{"environment and stored OAuth", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceEnv, HasStoredOAuth: true}, "OpenAI auth: env+oauth"},
		{"OAuth", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth}, "OpenAI auth: oauth"},
		{"expired OAuth", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, NeedsLogin: true}, "OpenAI auth: oauth expired"},
		{"refreshable OAuth", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, NeedsRefresh: true}, "OpenAI auth: oauth refreshable"},
		{"signed out", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceSignedOut}, "OpenAI auth: signed out"},
		{"stored OAuth needs login", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceSignedOut, HasStoredOAuth: true, Error: "token expired"}, "OpenAI auth: login required: token expired"},
		{"account email", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, Email: "user@example.com"}, "OpenAI auth: oauth (user@example.com)"},
		{"stored email fallback", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, StoredEmail: "stored@example.com"}, "OpenAI auth: oauth (stored@example.com)"},
		{"email and error", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, Email: "user@example.com", Error: "something broke"}, "OpenAI auth: oauth (user@example.com): something broke"},
		{"error without email", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceOAuth, Error: "something broke"}, "OpenAI auth: oauth: something broke"},
		{"signed-out error suppresses email", authStatus{Provider: "openai", ActiveSource: authopenai.AuthSourceSignedOut, StoredEmail: "stored@example.com", Error: "no token"}, "OpenAI auth: signed out: no token"},
		{"no provider", authStatus{}, "Auth is not available until a provider is selected."},
		{"unsupported provider", authStatus{Provider: "anthropic"}, `Auth is not supported for provider "anthropic".`},
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
	if got := firstNonEmptyString("", "", "third", "fourth"); got != "third" {
		t.Fatalf("firstNonEmptyString = %q, want third", got)
	}
}

func TestCovFirstNonEmptyString_AllEmpty(t *testing.T) {
	if got := firstNonEmptyString(" ", "  ", "\t"); got != "" {
		t.Fatalf("all whitespace = %q, want empty", got)
	}
}

func TestCovFirstNonEmptyString_None(t *testing.T) {
	if got := firstNonEmptyString(); got != "" {
		t.Fatalf("no args = %q, want empty", got)
	}
}

func TestCovFirstNonEmptyString_FirstValue(t *testing.T) {
	if got := firstNonEmptyString("first", "second"); got != "first" {
		t.Fatalf("first value = %q, want first", got)
	}
}
