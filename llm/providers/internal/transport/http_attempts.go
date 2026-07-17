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
	if err != nil {
		attempt := roundTripper.takeTransportFailure()
		roundTripper.seal(attempt)
		return response, attempt, err
	}
	attempt := roundTripper.claimResponse(response)
	roundTripper.seal(attempt)
	return response, attempt, nil
}

type apiAttemptRoundTripper struct {
	base         http.RoundTripper
	parentCtx    context.Context
	build        APIAttemptMetaBuilder
	hasCookieJar bool

	mu                 sync.Mutex
	transportFailure   *APIAttemptCapture
	pendingRedirects   []*pendingAPIAttemptCompletion
	attempts           []*APIAttemptCapture
	credentialMaterial llm.APILogCredentialMaterial
	responses          map[*http.Response]*apiAttemptResponseBody
	requestBodies      int
	sealed             bool
	finalizeRequested  bool
	outerCompletion    func()
	finalizeOnce       sync.Once
}

func (t *apiAttemptRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	meta := t.build(request, nil)
	attempt := BeginAPIAttempt(t.parentCtx, request.Context(), request, meta)
	t.registerAttempt(attempt)
	attempt.requestBody = captureRequestBody(request, t.requestBodyDone)

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
		observedBody: newObservedBody(request.Context(), responseBody, response.ContentLength),
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
	priorMaterial := t.credentialMaterial
	t.attempts = append(t.attempts, attempt)
	t.requestBodies++
	t.mu.Unlock()
	attempt.mergeCredentialMaterial(priorMaterial)
	t.mergeCredentialMaterial(attempt.credentialMaterial)
}

func (t *apiAttemptRoundTripper) requestBodyDone() {
	t.mu.Lock()
	if t.requestBodies > 0 {
		t.requestBodies--
	}
	ready := t.readyToFinalizeLocked()
	t.mu.Unlock()
	if ready {
		t.finalizeAttempts()
	}
}

func (t *apiAttemptRoundTripper) seal(outer *APIAttemptCapture) {
	t.mu.Lock()
	t.sealed = true
	if outer != nil {
		outer.scheduleCompletion = t.scheduleOuterCompletion
	}
	if outer == nil {
		t.finalizeRequested = true
	}
	ready := t.readyToFinalizeLocked()
	t.mu.Unlock()
	if ready {
		t.finalizeAttempts()
	}
}

func (t *apiAttemptRoundTripper) scheduleOuterCompletion(completion func()) {
	t.mu.Lock()
	if t.outerCompletion == nil {
		t.outerCompletion = completion
	}
	t.finalizeRequested = true
	ready := t.readyToFinalizeLocked()
	t.mu.Unlock()
	if ready {
		t.finalizeAttempts()
	}
}

func (t *apiAttemptRoundTripper) readyToFinalizeLocked() bool {
	return t.sealed && t.finalizeRequested && t.requestBodies == 0
}

func (t *apiAttemptRoundTripper) finalizeAttempts() {
	t.finalizeOnce.Do(func() {
		t.collectCredentialMaterial()
		t.flushPendingRedirects()
		t.mu.Lock()
		outerCompletion := t.outerCompletion
		t.outerCompletion = nil
		t.mu.Unlock()
		if outerCompletion != nil {
			outerCompletion()
		}
	})
}

func (t *apiAttemptRoundTripper) collectCredentialMaterial() {
	t.mu.Lock()
	attempts := append([]*APIAttemptCapture(nil), t.attempts...)
	configured := t.credentialMaterial
	t.mu.Unlock()
	for _, attempt := range attempts {
		t.mergeCredentialMaterial(llm.APILogCredentialMaterialForRequest(attempt.request, configured))
	}
}

func (t *apiAttemptRoundTripper) mergeCredentialMaterial(material llm.APILogCredentialMaterial) {
	if len(material.HeaderNames) == 0 && len(material.QueryNames) == 0 && len(material.Values) == 0 {
		return
	}
	t.mu.Lock()
	t.credentialMaterial = mergeTransportCredentialMaterial(t.credentialMaterial, material)
	attempts := append([]*APIAttemptCapture(nil), t.attempts...)
	t.mu.Unlock()
	for _, attempt := range attempts {
		attempt.mergeCredentialMaterial(material)
	}
}

func mergeTransportCredentialMaterial(left, right llm.APILogCredentialMaterial) llm.APILogCredentialMaterial {
	headerNames := make([]string, 0, len(left.HeaderNames)+len(right.HeaderNames))
	for name := range left.HeaderNames {
		headerNames = append(headerNames, name)
	}
	for name := range right.HeaderNames {
		headerNames = append(headerNames, name)
	}
	queryNames := make([]string, 0, len(left.QueryNames)+len(right.QueryNames))
	for name := range left.QueryNames {
		queryNames = append(queryNames, name)
	}
	for name := range right.QueryNames {
		queryNames = append(queryNames, name)
	}
	values := append([]string(nil), left.Values...)
	values = append(values, right.Values...)
	return llm.NewAPILogCredentialMaterial(headerNames, queryNames, values...)
}

func (t *apiAttemptRoundTripper) mergeResponseCookieMaterial(response *http.Response) {
	if !t.hasCookieJar || response == nil {
		return
	}
	credentialRequest := &http.Request{Header: make(http.Header)}
	for _, cookie := range response.Cookies() {
		if cookie != nil && cookie.Value != "" {
			credentialRequest.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}
	t.mergeCredentialMaterial(llm.APILogCredentialMaterialForRequest(
		credentialRequest,
		llm.NewAPILogCredentialMaterial([]string{"Cookie"}, nil),
	))
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
	configured := t.credentialMaterial
	t.mu.Unlock()
	t.mergeCredentialMaterial(llm.APILogCredentialMaterialForRequest(candidate, configured))
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

// The canonical 128 MiB API-log record can contain both bodies, and JSON may
// expand one retained byte to six bytes. Two 8 MiB prefixes leave headroom for
// that expansion and the rest of the attempt metadata.
const maxObservedBodyBytes = 8 << 20

// observedBody records only bytes returned by application Read calls. EOF or a
// known content length proves completeness; Close never does.
type observedBody struct {
	io.ReadCloser

	mu                sync.Mutex
	buf               bytes.Buffer
	retentionLimit    int
	retentionExceeded bool
	exact             bool
	knownLength       int64
	sawEOF            bool
	ctx               context.Context
	timeout           bool
}

func newObservedBody(ctx context.Context, body io.ReadCloser, contentLength int64) *observedBody {
	return newObservedBodyWithLimit(ctx, body, contentLength, maxObservedBodyBytes)
}

func newObservedBodyWithLimit(ctx context.Context, body io.ReadCloser, contentLength int64, retentionLimit int) *observedBody {
	return &observedBody{
		ReadCloser:     body,
		retentionLimit: retentionLimit,
		exact:          body == http.NoBody || contentLength == 0,
		knownLength:    contentLength,
		ctx:            ctx,
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
		retained := n
		if remaining := b.retentionLimit - b.buf.Len(); retained > remaining {
			retained = max(remaining, 0)
			b.retentionExceeded = true
		}
		if retained > 0 {
			_, _ = b.buf.Write(p[:retained])
		}
	}
	if err == io.EOF {
		b.sawEOF = true
	}
	if b.retentionExceeded {
		b.exact = false
	} else if b.sawEOF {
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
	requestDone func()
	doneOnce    sync.Once
	mu          sync.Mutex
	activeReads int
	terminal    bool
}

func captureRequestBody(request *http.Request, requestDone func()) func() bodyObservation {
	if request.Body == nil || request.Body == http.NoBody {
		requestDone()
		return func() bodyObservation { return bodyObservation{exact: true} }
	}
	contentLength := request.ContentLength
	// net/http treats zero with a non-nil client request body as unknown.
	if contentLength == 0 {
		contentLength = -1
	}
	body := &apiAttemptRequestBody{
		observedBody: newObservedBody(request.Context(), request.Body, contentLength),
		requestDone:  requestDone,
	}
	request.Body = body
	return body.snapshot
}

func (b *apiAttemptRequestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	b.activeReads++
	b.mu.Unlock()
	n, err := b.observedBody.Read(p)
	b.mu.Lock()
	b.activeReads--
	b.terminal = b.terminal || err == io.EOF //nolint:errorlint // Request-body errors are untrusted; do not invoke arbitrary Is methods.
	ready := b.terminal && b.activeReads == 0
	b.mu.Unlock()
	if ready {
		b.finishRequest()
	}
	return n, err
}

func (b *apiAttemptRequestBody) Close() error {
	err := b.observedBody.Close()
	b.mu.Lock()
	b.terminal = true
	ready := b.activeReads == 0
	b.mu.Unlock()
	if ready {
		b.finishRequest()
	}
	return err
}

func (b *apiAttemptRequestBody) finishRequest() {
	b.doneOnce.Do(func() {
		if b.requestDone != nil {
			b.requestDone()
		}
	})
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
