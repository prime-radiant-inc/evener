package transport

import (
	"context"
	"net/http"
	"sync"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

// APIAttemptCapture owns one canonical record for one actual HTTP request.
// It is inert unless the supplied context contains an explicitly attached
// attempt group and sink.
type APIAttemptCapture struct {
	attempt            *llm.APIAttempt
	owner              llm.APIAttemptContextOwnership
	request            *http.Request
	credentialMaterial llm.APILogCredentialMaterial
	requestBody        func() bodyObservation
	responseBody       func() bodyObservation
	completeOnce       sync.Once
	scheduleCompletion func(func())
}

// Active reports whether observed response bytes need to be retained for this
// explicitly attached canonical attempt.
func (c *APIAttemptCapture) Active() bool {
	return c != nil && c.attempt.Active()
}

// BeginAPIAttempt snapshots the credential-free request immediately before its
// RoundTrip. The caller supplies semantic provenance; this function owns final
// method, endpoint, headers, and time.
func BeginAPIAttempt(parentCtx, attemptCtx context.Context, request *http.Request, meta llm.APIAttemptMeta) *APIAttemptCapture {
	meta.CredentialMaterial = llm.APILogCredentialMaterialForRequest(request, meta.CredentialMaterial)
	meta.Method = request.Method
	meta.Endpoint, meta.Headers = llm.SanitizeRequestForAPILog(request, meta.CredentialMaterial)
	meta.StartedAt = time.Now()
	attempt := llm.BeginAPIAttempt(attemptCtx, meta)
	return &APIAttemptCapture{
		attempt:            attempt,
		request:            request,
		credentialMaterial: attempt.CredentialMaterial(),
		owner: llm.APIAttemptContextOwnership{
			Parent:  parentCtx,
			Attempt: attemptCtx,
		},
	}
}

func (c *APIAttemptCapture) mergeCredentialMaterial(material llm.APILogCredentialMaterial) {
	if c == nil {
		return
	}
	c.attempt.MergeCredentialMaterial(material)
}

// Complete freezes provider-result and response evidence, then queues the
// canonical append until the request body cannot produce more credentials.
func (c *APIAttemptCapture) Complete(result llm.APIAttemptResult, timeoutSource llm.APITimeoutSource, decodeErr, transportErr error) {
	if c == nil {
		return
	}
	c.completeOnce.Do(func() {
		result, owner, responseTimedOut := c.captureCompletionEvidence(result)
		c.scheduleCapturedCompletion(result, owner, responseTimedOut, timeoutSource, decodeErr, transportErr)
	})
}

func (c *APIAttemptCapture) captureCompletionEvidence(result llm.APIAttemptResult) (llm.APIAttemptResult, llm.APIAttemptContextOwnership, bool) {
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	responseTimedOut := false
	if c.responseBody != nil {
		observation := c.responseBody()
		result.ResponseBody = observation.bytes
		result.ResponseBodyInexact = !observation.exact
		responseTimedOut = observation.timeout
	}
	return result, freezeAPIAttemptContextOwnership(c.owner), responseTimedOut
}

func (c *APIAttemptCapture) completeWithCapturedEvidence(result llm.APIAttemptResult, owner llm.APIAttemptContextOwnership, responseTimedOut bool, timeoutSource llm.APITimeoutSource, decodeErr, transportErr error) {
	if c == nil {
		return
	}
	c.completeOnce.Do(func() {
		c.scheduleCapturedCompletion(result, owner, responseTimedOut, timeoutSource, decodeErr, transportErr)
	})
}

func (c *APIAttemptCapture) scheduleCapturedCompletion(result llm.APIAttemptResult, owner llm.APIAttemptContextOwnership, responseTimedOut bool, timeoutSource llm.APITimeoutSource, decodeErr, transportErr error) {
	completion := func() { c.completeNow(result, owner, responseTimedOut, timeoutSource, decodeErr, transportErr) }
	if c.scheduleCompletion != nil {
		c.scheduleCompletion(completion)
		return
	}
	completion()
}

func freezeAPIAttemptContextOwnership(owner llm.APIAttemptContextOwnership) llm.APIAttemptContextOwnership {
	// Deferred persistence must classify the provider result using only the
	// cancellation evidence that existed when Complete was called.
	if owner.Parent == nil || owner.Parent.Err() == nil {
		owner.Parent = context.Background()
	}
	if owner.Attempt == nil || owner.Attempt.Err() == nil {
		owner.Attempt = context.Background()
	}
	return owner
}

func (c *APIAttemptCapture) completeNow(result llm.APIAttemptResult, owner llm.APIAttemptContextOwnership, responseTimedOut bool, timeoutSource llm.APITimeoutSource, decodeErr, transportErr error) {
	material := llm.APILogCredentialMaterialForRequest(c.request, c.credentialMaterial)
	c.attempt.MergeCredentialMaterial(material)
	observedTimeout := responseTimedOut
	observeTerminalContext := func(observation bodyObservation) {
		observedTimeout = observedTimeout || observation.timeout
	}
	if c.requestBody != nil {
		observation := c.requestBody()
		c.attempt.SetRequestBody(observation.bytes, observation.exact)
		observeTerminalContext(observation)
	}
	owner.TimeoutSource = timeoutSource
	if owner.TimeoutSource == llm.APITimeoutNone && observedTimeout {
		owner.TimeoutSource = llm.APITimeoutTransport
	}
	if result.Outcome == "" {
		result.Outcome = llm.ClassifyAPIAttemptOutcome(owner, result.StatusCode, result.Err, decodeErr, transportErr)
	}
	if result.Err != nil && result.ErrorClass == "" {
		result.ErrorClass = explicitAPIAttemptErrorClass(result.Outcome, result.StatusCode)
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	c.attempt.Complete(result)
}

func explicitAPIAttemptErrorClass(outcome apilog.AttemptOutcomeClass, statusCode int) string {
	if outcome == apilog.AttemptProviderTimeout {
		return llm.KindTimeout.String()
	}
	if outcome != apilog.AttemptProviderReject {
		return llm.KindUnknown.String()
	}
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return llm.KindInvalidRequest.String()
	case http.StatusUnauthorized:
		return llm.KindAuthentication.String()
	case http.StatusForbidden:
		return llm.KindAccessDenied.String()
	case http.StatusNotFound:
		return llm.KindNotFound.String()
	case http.StatusRequestTimeout:
		return llm.KindTimeout.String()
	case http.StatusRequestEntityTooLarge:
		return llm.KindContextLength.String()
	case http.StatusTooManyRequests:
		return llm.KindRateLimit.String()
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return llm.KindServer.String()
	default:
		return llm.KindUnknown.String()
	}
}
