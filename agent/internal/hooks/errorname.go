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
//
// Two categories need proof beyond llm.Kind, because that category alone would
// name the wrong thing:
//   - KindNotFound also covers an endpoint that does not speak the requested
//     protocol (UnsupportedEndpointError) and a continuation anchor the provider
//     no longer has. The model exists in both cases, so model_not_found needs a
//     404 with a code that is not previous_response_not_found.
//   - A missing or unrefreshable credential rides a ConfigurationError, whose
//     Kind is unknown and whose status is 0. It is still the common auth
//     failure here (only a provider 401 arrives as KindAuthentication), and the
//     error carries a sentinel that proves it.
func ClaudeErrorName(err error) string {
	if err == nil {
		return "unknown"
	}
	// Before the Kind switch: these sentinels prove an auth failure that Kind
	// cannot see.
	if errors.Is(err, llm.ErrSignInRequired) || errors.Is(err, llm.ErrNoCredential) {
		return "authentication_failed"
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
		// Only a 404 that is not a lost continuation anchor proves a missing
		// model. Anything else is a not-found we cannot attribute to the model.
		if hasProvider && providerErr.StatusCode() == 404 &&
			providerErr.ErrorCode() != "previous_response_not_found" {
			return "model_not_found"
		}
		return "unknown"
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
