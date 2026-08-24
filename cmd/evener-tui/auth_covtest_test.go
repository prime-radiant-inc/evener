package tui

import (
	"strings"
	"testing"

	authopenai "primeradiant.com/evener/auth/openai"
)

// ---- formatAuthStatusSummary: openai env source ------------------------------

func TestCovFormatAuthStatusSummary_OpenAIEnvOnly(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceEnv,
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "env") {
		t.Fatalf("env source = %q, want contains 'env'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIEnvWithOAuth(t *testing.T) {
	status := authStatus{
		Provider:       "openai",
		ActiveSource:   authopenai.AuthSourceEnv,
		HasStoredOAuth: true,
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "env+oauth") {
		t.Fatalf("env+oauth = %q, want contains 'env+oauth'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIOAuth(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "oauth") {
		t.Fatalf("oauth = %q, want contains 'oauth'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIOAuthExpired(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
		NeedsLogin:   true,
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "expired") {
		t.Fatalf("oauth expired = %q, want contains 'expired'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIOAuthRefreshable(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
		NeedsRefresh: true,
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "refreshable") {
		t.Fatalf("oauth refreshable = %q, want contains 'refreshable'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAISignedOut(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceSignedOut,
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "signed out") {
		t.Fatalf("signed out = %q, want contains 'signed out'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAISignedOutWithOAuthAndError(t *testing.T) {
	status := authStatus{
		Provider:       "openai",
		ActiveSource:   authopenai.AuthSourceSignedOut,
		HasStoredOAuth: true,
		Error:          "token expired",
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "login required") {
		t.Fatalf("signed out + oauth + error = %q, want contains 'login required'", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIWithEmail(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
		Email:        "user@example.com",
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "user@example.com") {
		t.Fatalf("with email = %q, want contains email", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIWithStoredEmailFallback(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
		StoredEmail:  "stored@example.com",
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "stored@example.com") {
		t.Fatalf("with stored email fallback = %q, want contains stored email", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIWithErrorAndEmail(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
		Email:        "user@example.com",
		Error:        "something broke",
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "user@example.com") || !strings.Contains(got, "something broke") {
		t.Fatalf("with error and email = %q", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIWithErrorNoEmail(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceOAuth,
		Error:        "something broke",
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "something broke") {
		t.Fatalf("with error no email = %q, want contains error", got)
	}
}

func TestCovFormatAuthStatusSummary_OpenAIErrorSignedOutNoEmail(t *testing.T) {
	status := authStatus{
		Provider:     "openai",
		ActiveSource: authopenai.AuthSourceSignedOut,
		Error:        "no token",
	}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "no token") {
		t.Fatalf("error signed out = %q", got)
	}
}

func TestCovFormatAuthStatusSummary_NoProvider(t *testing.T) {
	status := authStatus{Provider: ""}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "not available") {
		t.Fatalf("no provider = %q, want contains 'not available'", got)
	}
}

func TestCovFormatAuthStatusSummary_UnsupportedProvider(t *testing.T) {
	status := authStatus{Provider: "anthropic"}
	got := formatAuthStatusSummary(status)
	if !strings.Contains(got, "not supported") || !strings.Contains(got, "anthropic") {
		t.Fatalf("unsupported provider = %q, want contains 'not supported' and 'anthropic'", got)
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
