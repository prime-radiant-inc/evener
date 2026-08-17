package llm

import "errors"

// ErrorKind names the category of a provider failure. It is the category axis,
// orthogonal to [ErrorClass] (the retry-disposition axis returned by [Classify]):
// two errors can share a Class yet differ in Kind. A 429 rate-limit and a 503
// server error are both [ErrorClassRetryable] but are [KindRateLimit] versus
// [KindServer]; a 401 and a 413 are both [ErrorClassPermanent] but are
// [KindAuthentication] versus [KindContextLength].
//
// Use [Kind] to branch on category when retry disposition is not enough — e.g.
// recovering from a content filter, or warning on context overflow.
type ErrorKind int

const (
	// KindUnknown is the category of a nil error, an error outside the llm
	// taxonomy, or an HTTP failure whose status code did not map to a category.
	KindUnknown ErrorKind = iota
	// KindInvalidRequest is a malformed or rejected request (HTTP 400/422).
	KindInvalidRequest
	// KindAuthentication is a missing or invalid credential (HTTP 401).
	KindAuthentication
	// KindAccessDenied is an authenticated-but-forbidden request (HTTP 403).
	KindAccessDenied
	// KindNotFound is a missing resource, typically an unknown model (HTTP 404).
	KindNotFound
	// KindTimeout is a request timeout — an HTTP 408 or a non-HTTP context
	// deadline surfaced through [WrapContextError].
	KindTimeout
	// KindContextLength is an input that exceeds the model's context window
	// (HTTP 413 or a "context length"/"too many tokens" message).
	KindContextLength
	// KindContentFilter is a request or response blocked by a safety or usage
	// policy.
	KindContentFilter
	// KindQuotaExceeded is an exceeded quota or billing limit.
	KindQuotaExceeded
	// KindRateLimit is a rate limit (HTTP 429).
	KindRateLimit
	// KindServer is a server-side failure (HTTP 5xx).
	KindServer
)

// String returns the kind's name for logs and diagnostics.
func (k ErrorKind) String() string {
	switch k {
	case KindInvalidRequest:
		return "invalid_request"
	case KindAuthentication:
		return "authentication"
	case KindAccessDenied:
		return "access_denied"
	case KindNotFound:
		return "not_found"
	case KindTimeout:
		return "timeout"
	case KindContextLength:
		return "context_length"
	case KindContentFilter:
		return "content_filter"
	case KindQuotaExceeded:
		return "quota_exceeded"
	case KindRateLimit:
		return "rate_limit"
	case KindServer:
		return "server"
	default:
		return "unknown"
	}
}

// Kind reports the category of err, walking the error chain with [errors.As].
// It returns KindUnknown for a nil error, an error that is not part of the llm
// error taxonomy, or an HTTP failure whose status code did not map to a known
// category. The category is the one the error was constructed with (by
// [ErrorFromHTTPStatus] or [NewRequestTimeoutError]); it does not re-derive
// category from [Error.StatusCode], which is ambiguous (429 covers both rate
// limits and quota; a timeout may be HTTP 408 or a non-HTTP deadline).
func Kind(err error) ErrorKind {
	if err == nil {
		return KindUnknown
	}
	switch {
	case errorIs[*contentFilterError](err):
		return KindContentFilter
	case errorIs[*contextLengthError](err):
		return KindContextLength
	case errorIs[*quotaExceededError](err):
		return KindQuotaExceeded
	case errorIs[*rateLimitError](err):
		return KindRateLimit
	case errorIs[*responseHeaderTimeoutError](err):
		return KindTimeout
	case errorIs[*requestTimeoutError](err):
		return KindTimeout
	case errorIs[*authenticationError](err):
		return KindAuthentication
	case errorIs[*accessDeniedError](err):
		return KindAccessDenied
	case errorIs[*notFoundError](err):
		return KindNotFound
	case errorIs[*invalidRequestError](err):
		return KindInvalidRequest
	case errorIs[*serverError](err):
		return KindServer
	default:
		return KindUnknown
	}
}

// errorIs reports whether err's chain contains an error of concrete type T.
func errorIs[T error](err error) bool {
	var t T
	return errors.As(err, &t)
}

// kindBearer is implemented by this package's own provider-error types, each
// reporting the category its doc comment names.
//
// The method is unexported, so only types declared in this package can satisfy
// the interface. That is the point: a type assertion to kindBearer can never
// dispatch into caller-supplied code, which is what makes [DeclaredKind] safe
// to call on an error of unverified provenance.
type kindBearer interface{ declaredKind() ErrorKind }

func (*invalidRequestError) declaredKind() ErrorKind        { return KindInvalidRequest }
func (*authenticationError) declaredKind() ErrorKind        { return KindAuthentication }
func (*accessDeniedError) declaredKind() ErrorKind          { return KindAccessDenied }
func (*notFoundError) declaredKind() ErrorKind              { return KindNotFound }
func (*requestTimeoutError) declaredKind() ErrorKind        { return KindTimeout }
func (*responseHeaderTimeoutError) declaredKind() ErrorKind { return KindTimeout }
func (*contextLengthError) declaredKind() ErrorKind         { return KindContextLength }
func (*contentFilterError) declaredKind() ErrorKind         { return KindContentFilter }
func (*quotaExceededError) declaredKind() ErrorKind         { return KindQuotaExceeded }
func (*rateLimitError) declaredKind() ErrorKind             { return KindRateLimit }
func (*serverError) declaredKind() ErrorKind                { return KindServer }
func (*unknownHTTPError) declaredKind() ErrorKind           { return KindUnknown }

// DeclaredKind reports the category err itself declares, without unwrapping.
//
// It differs from [Kind] only in reach: Kind walks the error chain to find a
// category anywhere inside it, while DeclaredKind asks the value in hand and
// stops. Both report the category the error was constructed with, never one
// re-derived from a status code.
//
// Use DeclaredKind where the error's provenance is not guaranteed. Walking an
// arbitrary error graph calls Unwrap/Is/As on values the caller supplied, which
// may panic, loop, or carry side effects — a hazard the API-log capture path is
// required to avoid (see docs/superpowers/plans/2026-07-17-project-2-yagni-landing.md:
// "Do not walk arbitrary error graphs"). DeclaredKind invokes no caller code at
// all, because kindBearer cannot be implemented outside this package.
//
// A wrapped error therefore reports KindUnknown here even when Kind would find
// a category inside it. Callers that hold an error they constructed themselves,
// or that accept the traversal cost, should use Kind.
func DeclaredKind(err error) ErrorKind {
	if bearer, ok := err.(kindBearer); ok {
		return bearer.declaredKind()
	}
	return KindUnknown
}
