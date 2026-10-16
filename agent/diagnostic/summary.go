package diagnostic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/llm"
)

// SummaryAttempt contains bounded route metadata, never provider response text.
type SummaryAttempt struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Route     string `json:"route"`
	Kind      string `json:"kind"`
	Stage     string `json:"stage"`
	Reason    string `json:"reason,omitempty"`
	Status    int    `json:"status,omitempty"`
	Retryable *bool  `json:"retryable,omitempty"`
}

// SummaryFailure separates the terminal route from earlier failed routes.
type SummaryFailure struct {
	Terminal SummaryAttempt   `json:"terminal"`
	Previous []SummaryAttempt `json:"previous,omitempty"`
}

// SummaryAttemptFromError classifies only typed evidence, without error text.
func SummaryAttemptFromError(provider, model, route string, err error) SummaryAttempt {
	if route != "cheap" && route != "session" {
		route = "none"
	}
	attempt := SummaryAttempt{
		Provider: summaryIdentifier(provider), Model: summaryIdentifier(model), Route: route,
		Kind: llm.Kind(err).String(), Stage: "unknown",
	}
	if providerErr, ok := errors.AsType[llm.Error](err); ok {
		attempt.Stage = "provider"
		if status := providerErr.StatusCode(); status >= 100 && status <= 599 {
			attempt.Status = status
		}
		retryable := providerErr.Retryable()
		attempt.Retryable = &retryable
	}
	var config *llm.ConfigurationError
	var budget *llm.ContextBudgetError
	switch {
	case errors.As(err, &config):
		attempt.Kind, attempt.Stage = "configuration", "pre_dispatch"
		switch {
		case errors.Is(err, llm.ErrSignInRequired):
			attempt.Reason = "sign_in_required"
		case errors.Is(err, llm.ErrNoCredential):
			attempt.Reason = "missing_credential"
		}
	case errors.As(err, &budget):
		attempt.Kind, attempt.Stage = "local_budget", "pre_dispatch"
		retryable := false
		attempt.Retryable = &retryable
	case errors.Is(err, context.Canceled):
		attempt.Kind = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		attempt.Kind = "timeout"
	}
	return attempt
}

func summaryIdentifier(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			r = ' '
		}
		if out.Len()+utf8.RuneLen(r) > 256 {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

// SummaryFailureInfo formats a safe diagnostic for existing warning surfaces.
func SummaryFailureInfo(failure SummaryFailure) (string, Info) {
	a := failure.Terminal
	message := "Summary failed"
	switch a.Route {
	case "cheap":
		message += " for fast/cheap model " + a.Provider + "/" + a.Model
	case "session":
		message += " for session model " + a.Provider + "/" + a.Model
	}
	message += ": " + a.Kind
	if a.Status != 0 {
		message += fmt.Sprintf(" (HTTP %d)", a.Status)
	}
	if a.Reason != "" {
		message += ", " + a.Reason
	}
	info := Info{Source: SourceEvener, Title: "Summary failure", Hint: "The cause is unknown. Inspect the safe summary diagnostic in the daemon log."}
	if a.Stage == "provider" {
		info.Source = SourceProvider
	}
	switch a.Kind {
	case "rate_limit":
		info.Title, info.Hint = "Summary rate limited", "Wait before retrying, or choose a summary route with available rate capacity."
	case "quota_exceeded":
		info.Title, info.Hint = "Summary allowance spent", "Wait for the allowance to reset or use a provider account with available allowance."
	case "authentication":
		info.Title, info.Hint = "Summary authentication failed", "Check credentials or sign in to the provider for this route."
	case "access_denied":
		info.Title, info.Hint = "Summary access denied", "Check this provider account's access to the selected summary model."
	case "not_found":
		info.Title, info.Hint = "Summary model unavailable", "Check that this model is available on its configured provider."
	case "timeout":
		info.Title, info.Hint = "Summary timed out", "Retry when the provider is responsive."
	case "server":
		info.Title, info.Hint = "Summary provider failed", "Retry later or select a healthy provider for this summary route."
	case "cancelled":
		info.Title, info.Hint = "Summary cancelled", "Retry compaction when ready."
	case "configuration":
		info.Title, info.Hint = "Summary configuration error", "Check the provider and model configuration for this summary route."
		switch a.Reason {
		case "sign_in_required":
			info.Hint = "Sign in to the provider for this summary route, then retry."
		case "missing_credential":
			info.Hint = "Configure credentials for the provider on this summary route, then retry."
		}
	case "local_budget":
		info.Title, info.Hint = "Summary input exceeds local budget", "Evener rejected the request before provider dispatch. Choose a summary model with enough context capacity."
	case "context_length":
		info.Title, info.Hint = "Summary context limit reached", "Choose a summary model with enough context capacity."
	case "content_filter":
		info.Title, info.Hint = "Summary blocked by provider policy", "Check the provider's policy for this summary route."
	case "invalid_request":
		info.Title, info.Hint = "Summary request rejected", "Check that this model supports the summary request."
	case "rejected_output":
		info.Title, info.Hint = "Summary output rejected", "The response could not safely replace the conversation. Retry or choose a more capable summary model."
	}
	if a.Route == "cheap" {
		info.Hint += " This is the fast/cheap model, which may be configured separately from the main model."
	}
	info.Hint += " Existing history or the checkpoint remains available."
	return message, info
}
