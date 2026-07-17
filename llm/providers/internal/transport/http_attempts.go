package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"primeradiant.com/serf/llm"
)

// APIAttemptMetaBuilder derives provider metadata and credential material from
// the actual request handed to RoundTrip after redirect rewriting.
type APIAttemptMetaBuilder func(request *http.Request, requestBody []byte) llm.APIAttemptMeta

// DoWithAPIAttempts preserves the supplied client's redirect and transport
// behavior while recording each actual RoundTrip request separately. Ordinary
// calls without an explicit canonical group and sink use client.Do directly.
func DoWithAPIAttempts(parentCtx context.Context, client *http.Client, request *http.Request, build APIAttemptMetaBuilder) (*http.Response, *APIAttemptCapture, error) {
	if !llm.APIAttemptContextActive(request.Context()) {
		response, err := client.Do(request)
		return response, nil, err
	}

	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	roundTripper := &apiAttemptRoundTripper{
		base:         base,
		parentCtx:    parentCtx,
		build:        build,
		hasCookieJar: client.Jar != nil,
	}
	clientCopy := *client
	clientCopy.Transport = roundTripper
	clientCopy.CheckRedirect = roundTripper.observeRedirectCandidates(client.CheckRedirect)
	response, err := clientCopy.Do(request)
	roundTripper.flushPendingRedirects()
	if err != nil {
		return response, roundTripper.takeTransportFailure(), err
	}
	return response, roundTripper.claimResponse(response), nil
}

type apiAttemptRoundTripper struct {
	base         http.RoundTripper
	parentCtx    context.Context
	build        APIAttemptMetaBuilder
	hasCookieJar bool

	mu                  sync.Mutex
	transportFailure    *APIAttemptCapture
	pendingRedirects    []*pendingAPIAttemptCompletion
	attempts            []*APIAttemptCapture
	credentialMaterials []llm.APILogCredentialMaterial
	responses           map[*http.Response]*apiAttemptResponseBody
}

func (t *apiAttemptRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	requestBodySnapshot := captureRequestBody(request)
	meta := t.build(request, nil)
	attempt := BeginAPIAttempt(t.parentCtx, request.Context(), request, meta)
	attempt.requestBody = requestBodySnapshot
	t.registerAttempt(attempt)

	response, err := t.base.RoundTrip(request)
	if err != nil {
		t.retainTransportFailure(attempt)
		return response, err
	}
	if response == nil {
		t.retainTransportFailure(attempt)
		return nil, nil
	}
	responseBody := response.Body
	if responseBody == nil {
		if response.ContentLength > 0 && request.Method != http.MethodHead {
			t.retainTransportFailure(attempt)
			return response, nil
		}
		responseBody = http.NoBody
	}
	body := &apiAttemptResponseBody{
		observedBody: newObservedBody(responseBody, response.ContentLength, request.Context()),
		attempt:      attempt,
		statusCode:   response.StatusCode,
	}
	body.associationDone = func() { t.releaseResponse(response, body) }
	body.deferCompletion = t.holdPendingRedirect
	attempt.responseBody = body.snapshot
	response.Body = body

	t.mu.Lock()
	if t.responses == nil {
		t.responses = make(map[*http.Response]*apiAttemptResponseBody)
	}
	t.responses[response] = body
	t.mu.Unlock()
	t.mergeResponseCookieMaterial(response)
	return response, nil
}

func (t *apiAttemptRoundTripper) retainTransportFailure(attempt *APIAttemptCapture) {
	t.mu.Lock()
	t.transportFailure = attempt
	t.mu.Unlock()
}

func (t *apiAttemptRoundTripper) registerAttempt(attempt *APIAttemptCapture) {
	t.mu.Lock()
	priorMaterials := append([]llm.APILogCredentialMaterial(nil), t.credentialMaterials...)
	t.attempts = append(t.attempts, attempt)
	t.mu.Unlock()
	for _, material := range priorMaterials {
		attempt.mergeCredentialMaterial(material)
	}
	t.mergeCredentialMaterial(attempt.credentialMaterial)
}

func (t *apiAttemptRoundTripper) mergeCredentialMaterial(material llm.APILogCredentialMaterial) {
	if len(material.HeaderNames) == 0 && len(material.QueryNames) == 0 && len(material.Values) == 0 {
		return
	}
	t.mu.Lock()
	t.credentialMaterials = append(t.credentialMaterials, material)
	attempts := append([]*APIAttemptCapture(nil), t.attempts...)
	t.mu.Unlock()
	for _, attempt := range attempts {
		attempt.mergeCredentialMaterial(material)
	}
}

func (t *apiAttemptRoundTripper) mergeResponseCookieMaterial(response *http.Response) {
	if !t.hasCookieJar || response == nil {
		return
	}
	var values []string
	for _, cookie := range response.Cookies() {
		if cookie != nil && cookie.Value != "" {
			values = append(values, cookie.Value)
		}
	}
	t.mergeCredentialMaterial(llm.NewAPILogCredentialMaterial(nil, nil, values...))
}

// observeRedirectCandidates preserves the copied client's redirect policy while
// exposing the final candidate after the policy has applied any mutations.
func (t *apiAttemptRoundTripper) observeRedirectCandidates(policy func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(candidate *http.Request, via []*http.Request) error {
		var err error
		if policy != nil {
			err = policy(candidate, via)
		} else if len(via) >= 10 {
			// Match net/http's default policy for the otherwise nil hook.
			err = errors.New("stopped after 10 redirects")
		}
		t.mergeRedirectCandidateMaterial(candidate)
		return err
	}
}

func (t *apiAttemptRoundTripper) mergeRedirectCandidateMaterial(candidate *http.Request) {
	if candidate == nil {
		return
	}
	t.mu.Lock()
	configuredMaterials := append([]llm.APILogCredentialMaterial(nil), t.credentialMaterials...)
	t.mu.Unlock()
	t.mergeCredentialMaterial(llm.APILogCredentialMaterialForRequest(candidate, llm.APILogCredentialMaterial{}))
	for _, configured := range configuredMaterials {
		t.mergeCredentialMaterial(llm.APILogCredentialMaterialForRequest(candidate, configured))
	}
}

type pendingAPIAttemptCompletion struct {
	attempt   *APIAttemptCapture
	result    llm.APIAttemptResult
	decodeErr error
}

func (p *pendingAPIAttemptCompletion) complete() {
	if p == nil {
		return
	}
	p.attempt.Complete(p.result, llm.APITimeoutNone, p.decodeErr, nil)
}

func (t *apiAttemptRoundTripper) holdPendingRedirect(pending *pendingAPIAttemptCompletion) {
	t.mu.Lock()
	t.pendingRedirects = append(t.pendingRedirects, pending)
	t.mu.Unlock()
}

func (t *apiAttemptRoundTripper) flushPendingRedirects() {
	t.mu.Lock()
	pending := t.pendingRedirects
	t.pendingRedirects = nil
	t.mu.Unlock()
	for _, completion := range pending {
		completion.complete()
	}
}

func (t *apiAttemptRoundTripper) takeTransportFailure() *APIAttemptCapture {
	t.mu.Lock()
	defer t.mu.Unlock()
	attempt := t.transportFailure
	t.transportFailure = nil
	return attempt
}

func (t *apiAttemptRoundTripper) claimResponse(response *http.Response) *APIAttemptCapture {
	if response == nil {
		return nil
	}
	t.mu.Lock()
	body := t.responses[response]
	t.mu.Unlock()
	if body == nil {
		return nil
	}
	return body.claim()
}

func (t *apiAttemptRoundTripper) releaseResponse(response *http.Response, body *apiAttemptResponseBody) {
	t.mu.Lock()
	if t.responses[response] == body {
		delete(t.responses, response)
	}
	t.mu.Unlock()
}

type bodyObservation struct {
	bytes   []byte
	exact   bool
	timeout bool
}

// observedBody records only bytes returned by application Read calls. EOF or a
// known content length proves completeness; Close never does.
type observedBody struct {
	io.ReadCloser

	mu          sync.Mutex
	buf         bytes.Buffer
	exact       bool
	knownLength int64
	sawEOF      bool
	ctx         context.Context
	timeout     bool
}

func newObservedBody(body io.ReadCloser, contentLength int64, ctx context.Context) *observedBody {
	return &observedBody{
		ReadCloser:  body,
		exact:       body == http.NoBody || contentLength == 0,
		knownLength: contentLength,
		ctx:         ctx,
	}
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	timeout := false
	if err != nil && err != io.EOF && b.ctx != nil {
		timeout = b.ctx.Err() == context.DeadlineExceeded
		if deadline, ok := b.ctx.Deadline(); ok {
			timeout = timeout || !time.Now().Before(deadline)
		}
	}
	b.mu.Lock()
	if n > 0 {
		_, _ = b.buf.Write(p[:n])
	}
	if err == io.EOF {
		b.sawEOF = true
	}
	if b.sawEOF {
		b.exact = true
	} else if b.knownLength >= 0 {
		b.exact = int64(b.buf.Len()) == b.knownLength && err == nil
	} else {
		b.exact = false
	}
	if timeout {
		b.timeout = true
	}
	b.mu.Unlock()
	return n, err
}

func (b *observedBody) Close() error {
	return b.ReadCloser.Close()
}

func (b *observedBody) snapshot() bodyObservation {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bodyObservation{
		bytes:   append([]byte(nil), b.buf.Bytes()...),
		exact:   b.exact,
		timeout: b.timeout,
	}
}

func (b *observedBody) observedBytes() []byte {
	return b.snapshot().bytes
}

func (b *observedBody) observedExactly() bool {
	return b.snapshot().exact
}

type apiAttemptRequestBody struct {
	*observedBody
}

func captureRequestBody(request *http.Request) func() bodyObservation {
	if request.Body == nil || request.Body == http.NoBody {
		return func() bodyObservation { return bodyObservation{exact: true} }
	}
	contentLength := request.ContentLength
	// net/http treats zero with a non-nil client request body as unknown.
	if contentLength == 0 {
		contentLength = -1
	}
	body := &apiAttemptRequestBody{observedBody: newObservedBody(request.Body, contentLength, request.Context())}
	request.Body = body
	return body.snapshot
}

type apiAttemptResponseBody struct {
	*observedBody

	attempt    *APIAttemptCapture
	statusCode int

	mu              sync.Mutex
	claimed         bool
	associationDone func()
	deferCompletion func(*pendingAPIAttemptCompletion)
	completionOnce  sync.Once
	doneOnce        sync.Once
}

func (b *apiAttemptResponseBody) Close() error {
	closeErr := b.observedBody.Close()
	b.completionOnce.Do(func() { b.completeUnclaimed(closeErr) })
	return closeErr
}

func (b *apiAttemptResponseBody) claim() *APIAttemptCapture {
	b.mu.Lock()
	if b.claimed {
		b.mu.Unlock()
		return nil
	}
	b.claimed = true
	attempt := b.attempt
	b.mu.Unlock()
	b.finishAssociation()
	return attempt
}

func (b *apiAttemptResponseBody) completeUnclaimed(closeErr error) {
	b.mu.Lock()
	claimed := b.claimed
	b.mu.Unlock()
	if claimed {
		return
	}
	observation := b.snapshot()
	pending := &pendingAPIAttemptCompletion{
		attempt: b.attempt,
		result: llm.APIAttemptResult{
			StatusCode:   b.statusCode,
			ResponseBody: observation.bytes,
			Err:          closeErr,
		},
		decodeErr: closeErr,
	}
	if b.deferCompletion != nil {
		b.deferCompletion(pending)
	} else {
		pending.complete()
	}
	b.finishAssociation()
}

func (b *apiAttemptResponseBody) finishAssociation() {
	b.doneOnce.Do(func() {
		if b.associationDone != nil {
			b.associationDone()
		}
	})
}
