package hooks

import (
	"errors"

	"primeradiant.com/evener/llm"
)

// ClaudeErrorName maps an evener provider failure onto the Claude StopFailure
// error vocabulary. Anything not provable maps to "unknown": a plausible-looking
// wrong name is worse than an honest one, because a hook matcher keys on it.
//
// The provider error is read through the error chain because a turn's terminal
// error is often a retry-group verdict wrapping the last attempt, and a status
// or code that identifies 529/overloaded must survive that wrapping.
func ClaudeErrorName(err error) string {
	if err == nil {
		return "unknown"
	}
	var providerErr llm.Error
	hasProvider := errors.As(err, &providerErr)
	if hasProvider {
		if providerErr.StatusCode() == 529 || providerErr.ErrorCode() == "overloaded_error" {
			return "overloaded"
		}
	}
	switch llm.Kind(err) {
	case llm.KindRateLimit:
		return "rate_limit"
	case llm.KindAuthentication:
		return "authentication_failed"
	case llm.KindQuotaExceeded:
		return "billing_error"
	case llm.KindNotFound:
		return "model_not_found"
	case llm.KindInvalidRequest:
		return "invalid_request"
	case llm.KindServer:
		return "server_error"
	case llm.KindAccessDenied:
		// A 403 is only provable as oauth_org_not_allowed when the provider's
		// own code says so; otherwise it is an ordinary forbidden request.
		if hasProvider && providerErr.ErrorCode() == "oauth_org_not_allowed" {
			return "oauth_org_not_allowed"
		}
		return "unknown"
	default:
		return "unknown"
	}
}
