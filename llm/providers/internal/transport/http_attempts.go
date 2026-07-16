package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

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
		base:      base,
		parentCtx: parentCtx,
		build:     build,
	}
	clientCopy := *client
	clientCopy.Transport = roundTripper
	response, err := clientCopy.Do(request)
	if err != nil {
		return response, roundTripper.takeTransportFailure(), err
	}
	attempt := roundTripper.claimResponse(response)
	if attempt != nil {
		sharedBody := &apiAttemptSharedResponseBody{ReadCloser: response.Body}
		response.Body = sharedBody
		attempt.bindResponseBody(sharedBody)
	}
	return response, attempt, nil
}

type apiAttemptRoundTripper struct {
	base      http.RoundTripper
	parentCtx context.Context
	build     APIAttemptMetaBuilder

	mu               sync.Mutex
	transportFailure *APIAttemptCapture
	responses        map[*http.Response]*apiAttemptResponseBody
}

func (t *apiAttemptRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	requestBodySnapshot := captureRequestBody(request)
	meta := t.build(request, nil)
	attempt := BeginAPIAttempt(t.parentCtx, request.Context(), request, meta)
	attempt.requestBody = requestBodySnapshot

	response, err := t.base.RoundTrip(request)
	if err != nil {
		t.mu.Lock()
		t.transportFailure = attempt
		t.mu.Unlock()
		return response, err
	}
	responseBody := response.Body
	if responseBody == nil {
		responseBody = http.NoBody
	}

	body := &apiAttemptResponseBody{
		ReadCloser: responseBody,
		attempt:    attempt,
		statusCode: response.StatusCode,
		closeDone:  make(chan struct{}),
		readsDone:  make(chan struct{}),
	}
	body.associationDone = func() { t.releaseResponse(response, body) }
	attempt.responseBody = body.snapshot
	response.Body = body
	t.mu.Lock()
	if t.responses == nil {
		t.responses = make(map[*http.Response]*apiAttemptResponseBody)
	}
	t.responses[response] = body
	t.mu.Unlock()
	return response, nil
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

type apiAttemptRequestBody struct {
	io.ReadCloser
	mu          sync.Mutex
	buf         bytes.Buffer
	activeReads int
	closing     bool
	closed      bool
	ready       chan struct{}
	readyOnce   sync.Once
	closeOnce   sync.Once
	closeErr    error
}

func (b *apiAttemptRequestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return 0, http.ErrBodyReadAfterClose
	}
	b.activeReads++
	b.mu.Unlock()

	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if n > 0 {
		_, _ = b.buf.Write(p[:n])
	}
	b.activeReads--
	if b.closed && b.activeReads == 0 {
		b.readyOnce.Do(func() { close(b.ready) })
	}
	b.mu.Unlock()
	return n, err
}

func (b *apiAttemptRequestBody) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closing = true
		b.mu.Unlock()

		closeErr := b.ReadCloser.Close()
		b.mu.Lock()
		b.closeErr = closeErr
		b.closed = true
		if b.activeReads == 0 {
			b.readyOnce.Do(func() { close(b.ready) })
		}
		b.mu.Unlock()
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeErr
}

func (b *apiAttemptRequestBody) snapshot() []byte {
	<-b.ready
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func captureRequestBody(request *http.Request) func() []byte {
	if request.Body == nil || request.Body == http.NoBody {
		return nil
	}
	recorder := &apiAttemptRequestBody{ReadCloser: request.Body, ready: make(chan struct{})}
	request.Body = recorder
	return recorder.snapshot
}

type apiAttemptResponseBody struct {
	io.ReadCloser
	attempt    *APIAttemptCapture
	statusCode int

	mu          sync.Mutex
	buf         bytes.Buffer
	readErr     error
	activeReads int
	claimed     bool
	closing     bool
	completed   bool
	closeDone   chan struct{}
	readsDone   chan struct{}
	closeErr    error
	readsOnce   sync.Once
	doneOnce    sync.Once

	associationDone func()
	waitForClose    func(<-chan struct{})
}

func (b *apiAttemptResponseBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return 0, http.ErrBodyReadAfterClose
	}
	b.activeReads++
	b.mu.Unlock()

	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if n > 0 {
		_, _ = b.buf.Write(p[:n])
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if b.readErr == nil {
			b.readErr = err
		}
	}
	b.activeReads--
	if b.closing && b.activeReads == 0 {
		b.readsOnce.Do(func() { close(b.readsDone) })
	}
	b.mu.Unlock()
	return n, err
}

func (b *apiAttemptResponseBody) Close() error {
	b.mu.Lock()
	if b.closing {
		closeDone := b.closeDone
		waitForClose := b.waitForClose
		b.mu.Unlock()
		if waitForClose != nil {
			waitForClose(closeDone)
		} else {
			<-closeDone
		}
		b.mu.Lock()
		closeErr := b.closeErr
		b.mu.Unlock()
		return closeErr
	}
	b.closing = true
	claimed := b.claimed
	if b.activeReads == 0 {
		b.readsOnce.Do(func() { close(b.readsDone) })
	}
	b.mu.Unlock()
	if claimed {
		closeErr := b.ReadCloser.Close()
		<-b.readsDone
		b.mu.Lock()
		b.closeErr = closeErr
		b.mu.Unlock()
		close(b.closeDone)
		return closeErr
	}

	<-b.readsDone
	b.mu.Lock()
	readErr := b.readErr
	b.mu.Unlock()
	if readErr == nil {
		_, _ = io.Copy(io.Discard, apiAttemptResponseDrainReader{body: b})
	}
	closeErr := b.ReadCloser.Close()
	b.mu.Lock()
	readErr = b.readErr
	b.closeErr = closeErr
	b.mu.Unlock()
	b.completeUnclaimed(errors.Join(readErr, closeErr))
	close(b.closeDone)
	return closeErr
}

type apiAttemptResponseDrainReader struct {
	body *apiAttemptResponseBody
}

func (r apiAttemptResponseDrainReader) Read(p []byte) (int, error) {
	n, err := r.body.ReadCloser.Read(p)
	r.body.mu.Lock()
	if n > 0 {
		_, _ = r.body.buf.Write(p[:n])
	}
	if err != nil && !errors.Is(err, io.EOF) && r.body.readErr == nil {
		r.body.readErr = err
	}
	r.body.mu.Unlock()
	return n, err
}

type apiAttemptSharedResponseBody struct {
	io.ReadCloser
	closeOnce sync.Once
	closeErr  error
}

func (b *apiAttemptSharedResponseBody) Close() error {
	b.closeOnce.Do(func() { b.closeErr = b.ReadCloser.Close() })
	return b.closeErr
}

func (b *apiAttemptResponseBody) claim() *APIAttemptCapture {
	b.mu.Lock()
	if b.claimed || b.closing || b.completed {
		b.mu.Unlock()
		return nil
	}
	b.claimed = true
	attempt := b.attempt
	b.mu.Unlock()
	b.finishAssociation()
	return attempt
}

func (b *apiAttemptResponseBody) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *apiAttemptResponseBody) completeUnclaimed(readErr error) {
	b.mu.Lock()
	if b.claimed || b.completed {
		b.mu.Unlock()
		return
	}
	b.completed = true
	responseBody := append([]byte(nil), b.buf.Bytes()...)
	b.mu.Unlock()
	b.attempt.Complete(llm.APIAttemptResult{
		StatusCode:   b.statusCode,
		ResponseBody: responseBody,
		Err:          readErr,
	}, llm.APITimeoutNone, readErr, nil)
	b.finishAssociation()
}

func (b *apiAttemptResponseBody) finishAssociation() {
	b.doneOnce.Do(func() {
		if b.associationDone != nil {
			b.associationDone()
		}
	})
}
