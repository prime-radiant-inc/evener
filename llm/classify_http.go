package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"primeradiant.com/evener/llm/registry"
)

// ClassifyHTTPError turns a non-2xx provider response into a typed Error
// (spec §12). Evaluation order: 413 first (Groq's per-request TPM ceiling
// arrives as 413 with code rate_limit_exceeded and recurs on retry), then
// the structured code, then the status (400 and 422 defer to the message
// rows), then the message patterns, then the generic type. operation labels
// the call in the message ("messages.create"); headers supply Retry-After
// and the x-ratelimit-reset-* delay; res names the instance and protocol
// stamped on the error and the caps the field hints are chosen from.
func ClassifyHTTPError(operation string, status int, headers http.Header, body []byte, res registry.Resolved) error {
	var raw map[string]any
	_ = json.Unmarshal(body, &raw) // a non-JSON body classifies by status alone
	now := time.Now()
	base := httpBaseError{
		provider:    res.Instance,
		protocol:    res.Protocol,
		statusCode:  status,
		message:     ProviderFailureMessage(operation, body),
		errorCode:   extractErrorCode(raw),
		retryAfter:  retryDelayFromHeaders(headers, now),
		rawResponse: raw,
	}
	code := base.errorCode
	switch {
	case status == 413, code == "context_length_exceeded", code == "request_too_large":
		base.retryable = false
		return &contextLengthError{base}
	case code == "unknown_parameter", code == "unsupported_parameter":
		base.retryable = false
		base.rejectedParam = paramFromError(raw)
		base.hint = fieldHint(base.rejectedParam, res)
		return &invalidRequestError{base}
	}
	if usageLimitCodes[code] {
		if limit, ok := parseUsageLimit(raw, now); ok {
			base.retryable = false
			base.message = usageLimitMessage(limit, now)
			return &quotaExceededError{httpBaseError: base, usageLimitResetsAt: limit.resetsAt}
		}
	}
	if status != 400 && status != 422 {
		// 401/403/404/408/429/5xx classify by status, including the 403 and
		// 429 usage-limit phrase checks errorFromHTTPStatus makes.
		return errorFromHTTPStatus(base)
	}
	base.retryable = false
	if err := classifyByMessage(base); err != nil {
		return err
	}
	base.rejectedParam = parameterNameFromMessage(base.message)
	base.hint = fieldHint(base.rejectedParam, res)
	return &invalidRequestError{base}
}

// ErrorHint returns the configuration hint ClassifyHTTPError attached to an
// error (spec §12), or "" when it carries none.
func ErrorHint(err error) string {
	var h interface{ Hint() string }
	if errors.As(err, &h) {
		return h.Hint()
	}
	return ""
}

// ErrorProtocol returns the protocol id ClassifyHTTPError stamped on an
// error, or "" for errors raised outside a protocol exchange.
func ErrorProtocol(err error) string {
	var p interface{ Protocol() string }
	if errors.As(err, &p) {
		return p.Protocol()
	}
	return ""
}

// paramFromError reads error.param from a decoded provider body, or "" when
// the body has no error object or the param is absent or non-string
// (including JSON null, which OpenAI sends when no parameter is implicated).
func paramFromError(raw map[string]any) string {
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		return ""
	}
	param, _ := errObj["param"].(string)
	return param
}

// retryDelayFromHeaders honors Retry-After first, then the
// x-ratelimit-reset-* headers ParseRateLimitHeaders understands.
func retryDelayFromHeaders(h http.Header, now time.Time) *time.Duration {
	if h == nil {
		return nil
	}
	if d := ParseRetryAfter(h.Get("Retry-After"), now); d != nil {
		return d
	}
	if info := ParseRateLimitHeaders(h); info != nil && info.ResetAt != nil && info.ResetAt.After(now) {
		d := info.ResetAt.Sub(now)
		return &d
	}
	return nil
}

var parameterMessagePatterns = []*regexp.Regexp{
	regexp.MustCompile(`Unrecognized request argument supplied: ([A-Za-z0-9_.]+)`),
	regexp.MustCompile(`Unknown parameter: '([^']+)'`),
	regexp.MustCompile(`Unsupported parameter: '([^']+)'`),
	regexp.MustCompile(`(?i)unknown field ([A-Za-z0-9_.]+)`),
}

// parameterNameFromMessage extracts the rejected field from the message
// shapes of spec §12, or "" when the message names none.
func parameterNameFromMessage(message string) string {
	for _, re := range parameterMessagePatterns {
		if m := re.FindStringSubmatch(message); m != nil {
			return m[1]
		}
	}
	return ""
}

// fieldHint picks the spec §12 hint for a rejected field: the other
// max-tokens spelling when the name is the row's current one, a
// fields.<name> = false pointer when the name is prunable, else the generic
// inspect hint (a cap-governed or nested path is not a valid fields key).
func fieldHint(name string, res registry.Resolved) string {
	ref := res.Instance + "/" + res.ModelID
	if name == "" {
		return genericFieldHint(ref)
	}
	if res.Protocol == registry.ProtocolOpenAIChat {
		current := registry.StringValue(res.Caps.MaxTokensField)
		if current == "" {
			current = "max_tokens"
		}
		if name == current {
			other := "max_completion_tokens"
			if current == "max_completion_tokens" {
				other = "max_tokens"
			}
			return fmt.Sprintf("set max_tokens_field = %q on %s", other, ref)
		}
	}
	if slices.Contains(registry.PrunablePaths(res.Protocol), name) {
		return fmt.Sprintf("run `evener models inspect %s` and set fields.%s = false", ref, name)
	}
	return genericFieldHint(ref)
}

func genericFieldHint(ref string) string {
	return fmt.Sprintf("run `evener models inspect %s`; this endpoint rejected a field the registry sends; compare the pruned-field list against the provider's documentation", ref)
}
