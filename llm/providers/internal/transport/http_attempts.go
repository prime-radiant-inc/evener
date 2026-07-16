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
	return response, claimResponseAttempt(response), nil
}

type apiAttemptRoundTripper struct {
	base      http.RoundTripper
	parentCtx context.Context
	build     APIAttemptMetaBuilder

	mu               sync.Mutex
	transportFailure *APIAttemptCapture
}

func (t *apiAttemptRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	requestBody, requestBodySnapshot := captureRequestBody(request)
	meta := t.build(request, requestBody)
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
	}
	attempt.responseBody = body.snapshot
	response.Body = body
	return response, nil
}

func (t *apiAttemptRoundTripper) takeTransportFailure() *APIAttemptCapture {
	t.mu.Lock()
	defer t.mu.Unlock()
	attempt := t.transportFailure
	t.transportFailure = nil
	return attempt
}

type apiAttemptRequestBody struct {
	io.ReadCloser
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *apiAttemptRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		_, _ = b.buf.Write(p[:n])
		b.mu.Unlock()
	}
	return n, err
}

func (b *apiAttemptRequestBody) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func captureRequestBody(request *http.Request) ([]byte, func() []byte) {
	if request.Body == nil || request.Body == http.NoBody {
		return nil, nil
	}
	if request.GetBody != nil {
		clone, err := request.GetBody()
		if err == nil {
			body, readErr := io.ReadAll(clone)
			closeErr := clone.Close()
			if readErr == nil && closeErr == nil {
				body = append([]byte(nil), body...)
				return body, func() []byte { return append([]byte(nil), body...) }
			}
		}
	}
	recorder := &apiAttemptRequestBody{ReadCloser: request.Body}
	request.Body = recorder
	return nil, recorder.snapshot
}

type apiAttemptResponseBody struct {
	io.ReadCloser
	attempt    *APIAttemptCapture
	statusCode int

	mu        sync.Mutex
	buf       bytes.Buffer
	claimed   bool
	completed bool
}

func (b *apiAttemptResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		_, _ = b.buf.Write(p[:n])
		b.mu.Unlock()
	}
	if err != nil {
		b.completeUnclaimed(nonEOFError(err))
	}
	return n, err
}

func (b *apiAttemptResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.completeUnclaimed(err)
	return err
}

func (b *apiAttemptResponseBody) claim() *APIAttemptCapture {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.completed {
		return nil
	}
	b.claimed = true
	return b.attempt
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
}

func claimResponseAttempt(response *http.Response) *APIAttemptCapture {
	if response == nil {
		return nil
	}
	body, ok := response.Body.(*apiAttemptResponseBody)
	if !ok {
		return nil
	}
	return body.claim()
}

func nonEOFError(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
