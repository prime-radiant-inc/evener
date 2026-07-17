package transport

import (
	"context"
	"net/http"
	"time"

	"primeradiant.com/serf/llm"
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
	return &APIAttemptCapture{
		attempt:            llm.BeginAPIAttempt(attemptCtx, meta),
		request:            request,
		credentialMaterial: meta.CredentialMaterial,
		owner: llm.APIAttemptContextOwnership{
			Parent:  parentCtx,
			Attempt: attemptCtx,
		},
	}
}

// Complete appends the attempt synchronously without changing the provider's
// response, error, or retry classification.
func (c *APIAttemptCapture) Complete(result llm.APIAttemptResult, timeoutSource llm.APITimeoutSource, decodeErr, transportErr error) {
	if c == nil {
		return
	}
	material := llm.APILogCredentialMaterialForRequest(c.request, c.credentialMaterial)
	c.attempt.MergeCredentialMaterial(material)
	if c.requestBody != nil {
		observation := c.requestBody()
		c.attempt.SetRequestBody(observation.bytes, observation.exact)
	}
	if c.responseBody != nil {
		observation := c.responseBody()
		result.ResponseBody = observation.bytes
		result.ResponseBodyInexact = !observation.exact
	}
	owner := c.owner
	owner.TimeoutSource = llm.APITimeoutSourceForAttempt(
		owner.Parent,
		owner.Attempt,
		timeoutSource,
		result.Err,
		decodeErr,
		transportErr,
	)
	result.Outcome = llm.ClassifyAPIAttemptOutcome(owner, result.StatusCode, decodeErr, transportErr)
	if result.Err != nil {
		result.ErrorClass = llm.Kind(result.Err).String()
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	c.attempt.Complete(result)
}
