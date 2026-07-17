package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type observingReadCloser struct {
	reader io.Reader

	mu         sync.Mutex
	readCount  int
	closeCount int
}

func (b *observingReadCloser) Read(p []byte) (int, error) {
	b.mu.Lock()
	b.readCount++
	b.mu.Unlock()
	return b.reader.Read(p)
}

func (b *observingReadCloser) Close() error {
	b.mu.Lock()
	b.closeCount++
	b.mu.Unlock()
	return nil
}

func (b *observingReadCloser) counts() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readCount, b.closeCount
}

type bodyObservationReporter interface {
	observedBytes() []byte
	observedExactly() bool
}

type terminalReadCloser struct {
	data []byte
	err  error
	done bool
}

func (r *terminalReadCloser) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), r.err
}

func (*terminalReadCloser) Close() error { return nil }

type concurrentReadCloseBody struct {
	data        []byte
	readStarted chan struct{}
	closeCalled chan struct{}
	releaseRead chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
	releaseOnce sync.Once

	mu         sync.Mutex
	readCount  int
	closeCount int
}

func (b *concurrentReadCloseBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	b.readCount++
	b.mu.Unlock()
	b.readOnce.Do(func() { close(b.readStarted) })
	<-b.releaseRead
	return copy(p, b.data), nil
}

func (b *concurrentReadCloseBody) Close() error {
	b.mu.Lock()
	b.closeCount++
	b.mu.Unlock()
	b.closeOnce.Do(func() { close(b.closeCalled) })
	return nil
}

func (b *concurrentReadCloseBody) release() {
	b.releaseOnce.Do(func() { close(b.releaseRead) })
}

func (b *concurrentReadCloseBody) counts() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readCount, b.closeCount
}

func TestAPIAttemptCompleteDoesNotDrainOrCloseResponseBody(t *testing.T) {
	body := &observingReadCloser{reader: bytes.NewReader([]byte("observed-unconsumed"))}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: -1,
		}, nil
	})}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_observation_only_complete")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}

	buf := make([]byte, len("observed-"))
	if _, err := io.ReadFull(response.Body, buf); err != nil {
		t.Fatalf("adapter read: %v", err)
	}
	decodeErr := errors.New("adapter stopped decoding")
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode, Err: decodeErr}, llm.APITimeoutNone, decodeErr, nil)

	reads, closes := body.counts()
	if reads != 1 {
		t.Fatalf("underlying reads after completion = %d, want the adapter's one read", reads)
	}
	if closes != 0 {
		t.Fatalf("underlying closes after completion = %d, want none", closes)
	}
	reporter := response.Body.(bodyObservationReporter)
	if reporter.observedExactly() {
		t.Fatal("partial response became exact after decode failure")
	}
	if got := string(reporter.observedBytes()); got != "observed-" {
		t.Fatalf("observed response = %q, want only adapter bytes", got)
	}
}

func TestAPIAttemptCompleteDoesNotWaitForUnconsumedRequestBody(t *testing.T) {
	requestBody := &observingReadCloser{reader: bytes.NewReader([]byte("unconsumed request"))}
	var observedRequestBody interface {
		io.Closer
		bodyObservationReporter
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		var ok bool
		observedRequestBody, ok = request.Body.(interface {
			io.Closer
			bodyObservationReporter
		})
		if !ok {
			t.Fatalf("request body %T does not report observations", request.Body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	})}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_unconsumed_request")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/v1", requestBody)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}

	completed := make(chan struct{})
	go func() {
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		_ = observedRequestBody.Close()
		<-completed
		t.Fatal("attempt completion blocked waiting for an unconsumed request body")
	}
	reads, closes := requestBody.counts()
	if reads != 0 || closes != 0 {
		t.Fatalf("completion touched request body: reads=%d closes=%d", reads, closes)
	}
	if observedRequestBody.observedExactly() {
		t.Fatal("unconsumed request body was marked exact")
	}
	if got := observedRequestBody.observedBytes(); len(got) != 0 {
		t.Fatalf("unconsumed request recorded %d bytes", len(got))
	}
}

func TestAPIAttemptBodyObserversReportOnlyReturnedBytesAndEOFExactness(t *testing.T) {
	tests := []struct {
		name      string
		body      func() io.ReadCloser
		read      func(io.ReadCloser) error
		want      string
		wantExact bool
	}{
		{
			name: "EOF is exact",
			body: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader([]byte("provider-body")))
			},
			read: func(reader io.ReadCloser) error {
				_, err := io.ReadAll(reader)
				return err
			},
			want:      "provider-body",
			wantExact: true,
		},
		{
			name: "partial read is inexact",
			body: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader([]byte("provider-body")))
			},
			read: func(reader io.ReadCloser) error {
				_, err := io.ReadFull(reader, make([]byte, len("provider")))
				return err
			},
			want: "provider",
		},
		{
			name: "early close is inexact",
			body: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader([]byte("provider-body")))
			},
			read: func(reader io.ReadCloser) error {
				if _, err := io.ReadFull(reader, make([]byte, len("provider"))); err != nil {
					return err
				}
				return reader.Close()
			},
			want: "provider",
		},
		{
			name: "cancellation is inexact",
			body: func() io.ReadCloser {
				return &terminalReadCloser{data: []byte("canceled"), err: context.Canceled}
			},
			read: func(reader io.ReadCloser) error {
				buf := make([]byte, len("canceled"))
				n, err := reader.Read(buf)
				if n != len(buf) {
					return errors.New("cancellation read returned wrong byte count")
				}
				if !errors.Is(err, context.Canceled) {
					return errors.New("cancellation read lost context error")
				}
				return nil
			},
			want: "canceled",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					Body:          testCase.body(),
					ContentLength: -1,
				}, nil
			})}
			sink := &responseAssociationSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_observer_exactness")),
				sink,
			)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, _, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
				return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
			})
			if err != nil {
				t.Fatalf("DoWithAPIAttempts: %v", err)
			}
			if err := testCase.read(response.Body); err != nil {
				t.Fatalf("adapter read: %v", err)
			}
			reporter, ok := response.Body.(bodyObservationReporter)
			if !ok {
				t.Fatalf("response body %T does not report observation exactness", response.Body)
			}
			if got := string(reporter.observedBytes()); got != testCase.want {
				t.Fatalf("observed bytes = %q, want %q", got, testCase.want)
			}
			if got := reporter.observedExactly(); got != testCase.wantExact {
				t.Fatalf("observed exactness = %v, want %v", got, testCase.wantExact)
			}
		})
	}
}

func TestAPIAttemptKnownContentLengthIsExactWithoutEOF(t *testing.T) {
	requestBytes := []byte("known request")
	responseBytes := []byte("known response")
	var requestObservation bodyObservationReporter
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		var ok bool
		requestObservation, ok = request.Body.(bodyObservationReporter)
		if !ok {
			t.Fatalf("request body %T does not report observations", request.Body)
		}
		buf := make([]byte, len(requestBytes))
		n, err := request.Body.Read(buf)
		if n != len(requestBytes) || err != nil {
			t.Fatalf("transport request read = %d, %v; want %d, nil", n, err, len(requestBytes))
		}
		if err := request.Body.Close(); err != nil {
			t.Fatalf("transport request close: %v", err)
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          &terminalReadCloser{data: responseBytes},
			ContentLength: int64(len(responseBytes)),
		}, nil
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_known_length", sink),
		http.MethodPost,
		"https://provider.test/v1",
		&terminalReadCloser{data: requestBytes},
	)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = int64(len(requestBytes))
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	responseObservation := response.Body.(bodyObservationReporter)
	buf := make([]byte, len(responseBytes))
	n, err := response.Body.Read(buf)
	if n != len(responseBytes) || err != nil {
		t.Fatalf("adapter response read = %d, %v; want %d, nil", n, err, len(responseBytes))
	}

	if !requestObservation.observedExactly() {
		t.Fatal("known-length request remained inexact after all bytes were observed without EOF")
	}
	if !responseObservation.observedExactly() {
		t.Fatal("known-length response remained inexact after all bytes were observed without EOF")
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
}

func TestAPIAttemptKnownZeroContentLengthIsExactWithoutRead(t *testing.T) {
	knownZeroRequest, err := http.NewRequest(http.MethodPost, "https://provider.test/v1", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	requestSnapshot := captureRequestBody(knownZeroRequest)
	requestObservation := requestSnapshot()
	if !requestObservation.exact {
		t.Fatal("known-zero request was not exact before a read")
	}
	if len(requestObservation.bytes) != 0 {
		t.Fatal("known-zero request invented observed bytes")
	}

	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          &terminalReadCloser{data: []byte("must not be read")},
			ContentLength: 0,
		}, nil
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_known_zero", sink),
		http.MethodGet,
		"https://provider.test/v1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	responseObservation := response.Body.(bodyObservationReporter)
	if !responseObservation.observedExactly() {
		t.Fatal("known-zero response was not exact before a read")
	}
	if len(responseObservation.observedBytes()) != 0 {
		t.Fatal("known-zero body invented observed bytes")
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
}

func TestAPIAttemptConcurrentReadAndCloseSnapshotsPromptlyWithoutExtraOperations(t *testing.T) {
	body := &concurrentReadCloseBody{
		data:        []byte("read returns after snapshot"),
		readStarted: make(chan struct{}),
		closeCalled: make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
	defer body.release()
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: -1,
		}, nil
	})}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(attemptContext("ag_concurrent_read_close", sink), http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}

	type readResult struct {
		n   int
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		buf := make([]byte, len(body.data))
		n, err := response.Body.Read(buf)
		readDone <- readResult{n: n, err: err}
	}()
	select {
	case <-body.readStarted:
	case <-time.After(time.Second):
		t.Fatal("response Read did not enter the underlying body")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- response.Body.Close() }()
	select {
	case <-body.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("pass-through Close did not reach the underlying body")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("response Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pass-through Close waited for the in-flight Read")
	}

	completeDone := make(chan struct{})
	go func() {
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
		close(completeDone)
	}()
	select {
	case <-completeDone:
	case <-time.After(time.Second):
		t.Fatal("attempt completion waited for the in-flight Read")
	}
	reporter := response.Body.(bodyObservationReporter)
	if reporter.observedExactly() {
		t.Fatal("snapshot became exact while a Read was still in flight")
	}
	if got := reporter.observedBytes(); len(got) != 0 {
		t.Fatalf("snapshot captured %d bytes before Read returned", len(got))
	}
	reads, closes := body.counts()
	if reads != 1 || closes != 1 {
		t.Fatalf("underlying operations = reads:%d closes:%d, want 1/1", reads, closes)
	}
	recorded, err := apilog.DecodeBody(onlyAttempt(t, sink).Response.Body)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	if len(recorded) != 0 {
		t.Fatalf("attempt recorded %d bytes before Read returned", len(recorded))
	}

	body.release()
	select {
	case result := <-readDone:
		if result.n != len(body.data) || result.err != nil {
			t.Fatalf("in-flight Read result = %d, %v; want %d, nil", result.n, result.err, len(body.data))
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight Read did not finish after release")
	}
}
