package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type responseAssociationSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

func (s *responseAssociationSink) AppendAttempt(_ context.Context, attempt apilog.APIAttemptRecord) error {
	s.mu.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	return nil
}

func (*responseAssociationSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *responseAssociationSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts)
}

type responseAssociationRoundTripper func(*http.Request) (*http.Response, error)

func (f responseAssociationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type concurrentCloseResponseBody struct {
	readEntered chan struct{}
	releaseRead chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

type readAfterCloseRequestBody struct {
	data          []byte
	readEntered   chan struct{}
	releaseRead   chan struct{}
	readCompleted chan struct{}
	readOnce      sync.Once
	closeOnce     sync.Once
}

func (b *readAfterCloseRequestBody) Read(p []byte) (int, error) {
	b.readOnce.Do(func() { close(b.readEntered) })
	<-b.releaseRead
	n := copy(p, b.data)
	b.closeOnce.Do(func() { close(b.readCompleted) })
	return n, io.EOF
}

func (*readAfterCloseRequestBody) Close() error { return nil }

type responseCloseActionBody struct {
	io.Reader
	closeAction func()
	closed      chan struct{}
	closeOnce   sync.Once
}

func (b *responseCloseActionBody) Close() error {
	b.closeOnce.Do(func() {
		if b.closeAction != nil {
			b.closeAction()
		}
		close(b.closed)
	})
	return nil
}

func (b *concurrentCloseResponseBody) Read(p []byte) (int, error) {
	b.readOnce.Do(func() { close(b.readEntered) })
	select {
	case <-b.releaseRead:
		return copy(p, "complete-response-body"), io.EOF
	case <-b.closed:
		return 0, errors.New("response body closed during drain")
	}
}

func (b *concurrentCloseResponseBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestAPIAttemptResponseAssociationReleasesOnClaimAndUnclaimedCloseExactlyOnce(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		claim bool
	}{
		{name: "final response claim", claim: true},
		{name: "unclaimed redirect close"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sink := &responseAssociationSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_response_association")),
				sink,
			)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/request", nil)
			if err != nil {
				t.Fatal(err)
			}
			roundTripper := &apiAttemptRoundTripper{
				base: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("response-body")),
					}, nil
				}),
				parentCtx: context.Background(),
				build: func(*http.Request, []byte) llm.APIAttemptMeta {
					return llm.APIAttemptMeta{ProviderInstance: "test"}
				},
			}
			response, err := roundTripper.RoundTrip(request)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			roundTripper.mu.Lock()
			associated := len(roundTripper.responses)
			roundTripper.mu.Unlock()
			if associated != 1 {
				t.Fatalf("response associations after RoundTrip = %d, want 1", associated)
			}

			if testCase.claim {
				attempt := roundTripper.claimResponse(response)
				if attempt == nil {
					t.Fatal("final response association did not return its attempt")
				}
				_, _ = io.ReadAll(response.Body)
				attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
			} else {
				_, _ = io.ReadAll(response.Body)
				if got := sink.count(); got != 0 {
					t.Fatalf("unclaimed response completed before Close: %d", got)
				}
			}
			if err := response.Body.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			roundTripper.mu.Lock()
			associated = len(roundTripper.responses)
			roundTripper.mu.Unlock()
			if associated != 0 {
				t.Fatalf("response associations after ownership end = %d, want 0", associated)
			}
			if got := sink.count(); got != 1 {
				t.Fatalf("attempt completions = %d, want exactly 1", got)
			}
		})
	}
}

func TestAPIAttemptRequestSnapshotWaitsForAsynchronousTransportBodyClose(t *testing.T) {
	requestBody := []byte("actual-asynchronously-consumed-request-body")
	allowRead := make(chan struct{})
	readFinished := make(chan struct{})
	allowClose := make(chan struct{})
	defer func() {
		select {
		case <-allowRead:
		default:
			close(allowRead)
		}
		select {
		case <-allowClose:
		default:
			close(allowClose)
		}
	}()
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_async_request_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/request", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		go func() {
			<-allowRead
			_, _ = io.ReadAll(request.Body)
			close(readFinished)
			<-allowClose
			_ = request.Body.Close()
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response-body")),
		}, nil
	})}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	defer response.Body.Close() //nolint:errcheck
	if attempt == nil {
		t.Fatal("final response did not claim canonical attempt")
	}
	completionDone := make(chan struct{})
	go func() {
		attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
		close(completionDone)
	}()
	select {
	case <-completionDone:
		t.Fatal("canonical completion returned before asynchronous request read began")
	default:
	}
	close(allowRead)
	<-readFinished
	select {
	case <-completionDone:
		t.Fatal("canonical completion returned before transport closed request body")
	default:
	}
	close(allowClose)
	<-completionDone

	if got := sink.count(); got != 1 {
		t.Fatalf("canonical attempts = %d, want 1", got)
	}
	sink.mu.Lock()
	recorded := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(recorded.Request.Body)
	if err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if !bytes.Equal(recordedBody, requestBody) {
		t.Fatalf("recorded request body = %q, want actual consumed bytes %q", recordedBody, requestBody)
	}
}

func TestAPIAttemptRequestBodyRejectsReadAfterTerminalClose(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://provider.test/request", strings.NewReader("unread-body"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := captureRequestBody(request)
	if snapshot == nil {
		t.Fatal("captureRequestBody returned no snapshot")
	}
	if err := request.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := request.Body.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := request.Body.Read(make([]byte, 1)); !errors.Is(err, http.ErrBodyReadAfterClose) {
		t.Fatalf("Read after terminal Close error = %v, want %v", err, http.ErrBodyReadAfterClose)
	}
	if got := snapshot(); len(got) != 0 {
		t.Fatalf("snapshot after unread terminal Close = %q, want empty", got)
	}
}

func TestAPIAttemptRequestSnapshotWaitsForReadThatOutlivesClose(t *testing.T) {
	requestBody := &readAfterCloseRequestBody{
		data:          []byte("final-bytes-returned-after-close"),
		readEntered:   make(chan struct{}),
		releaseRead:   make(chan struct{}),
		readCompleted: make(chan struct{}),
	}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_read_after_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/request", requestBody)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := &responseCloseActionBody{
		Reader:      strings.NewReader("response"),
		closed:      make(chan struct{}),
		closeAction: func() { close(requestBody.releaseRead) },
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		go func() { _, _ = io.ReadAll(request.Body) }()
		<-requestBody.readEntered
		if err := request.Body.Close(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: responseBody}, nil
	})}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if attempt == nil {
		t.Fatal("final response did not claim canonical attempt")
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
	_ = response.Body.Close()
	<-requestBody.readCompleted

	if got := sink.count(); got != 1 {
		t.Fatalf("canonical attempts = %d, want 1", got)
	}
	sink.mu.Lock()
	recorded := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(recorded.Request.Body)
	if err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if got, want := string(recordedBody), string(requestBody.data); got != want {
		t.Fatalf("recorded request body = %q, want bytes returned by in-flight Read %q", got, want)
	}
}

func TestAPIAttemptStreamingTransportClosesVisibleResponseBeforeRequestSnapshot(t *testing.T) {
	requestBytes := []byte("streaming-request-body")
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_deferred_stream_request_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/stream", bytes.NewReader(requestBytes))
	if err != nil {
		t.Fatal(err)
	}
	var transportRequestBody io.ReadCloser
	responseBody := &responseCloseActionBody{Reader: strings.NewReader("data: done\n\n"), closed: make(chan struct{})}
	client := &http.Client{
		Timeout: time.Hour,
		Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
			transportRequestBody = request.Body
			if _, err := io.ReadAll(request.Body); err != nil {
				return nil, err
			}
			responseBody.closeAction = func() { _ = transportRequestBody.Close() }
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: responseBody}, nil
		}),
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if attempt == nil {
		t.Fatal("final response did not claim canonical attempt")
	}
	originalSnapshot := attempt.requestBody
	snapshotEntered := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	attempt.requestBody = func() []byte {
		close(snapshotEntered)
		<-releaseSnapshot
		return originalSnapshot()
	}
	completionDone := make(chan struct{})
	go func() {
		attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
		close(completionDone)
	}()
	<-snapshotEntered
	closedBeforeSnapshot := false
	select {
	case <-responseBody.closed:
		closedBeforeSnapshot = true
	default:
	}
	_ = response.Body.Close()
	close(releaseSnapshot)
	<-completionDone
	if !closedBeforeSnapshot {
		t.Fatal("visible final response body was not closed before request snapshot")
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("canonical attempts = %d, want 1", got)
	}
	sink.mu.Lock()
	recorded := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(recorded.Request.Body)
	if err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if !bytes.Equal(recordedBody, requestBytes) {
		t.Fatalf("recorded request body = %q, want %q", recordedBody, requestBytes)
	}
}

func TestAPIAttemptConcurrentResponseCloseCompletesOnlyAfterOwningDrain(t *testing.T) {
	body := &concurrentCloseResponseBody{
		readEntered: make(chan struct{}),
		releaseRead: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_concurrent_response_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/request", nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTripper := &apiAttemptRoundTripper{
		base: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusFound, Header: make(http.Header), Body: body}, nil
		}),
		parentCtx: context.Background(),
		build: func(*http.Request, []byte) llm.APIAttemptMeta {
			return llm.APIAttemptMeta{ProviderInstance: "test"}
		},
	}
	response, err := roundTripper.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	wrappedBody, ok := response.Body.(*apiAttemptResponseBody)
	if !ok {
		t.Fatalf("response body = %T, want canonical attempt body", response.Body)
	}
	waiterEntered := make(chan struct{})
	wrappedBody.mu.Lock()
	wrappedBody.waitForClose = func(done <-chan struct{}) {
		close(waiterEntered)
		<-done
	}
	wrappedBody.mu.Unlock()
	firstCloseDone := make(chan error, 1)
	go func() { firstCloseDone <- response.Body.Close() }()
	<-body.readEntered
	secondCloseDone := make(chan error, 1)
	go func() { secondCloseDone <- response.Body.Close() }()
	<-waiterEntered
	select {
	case <-body.closed:
		t.Fatal("concurrent Close reached the underlying body while the owning drain was active")
	default:
	}
	if got := sink.count(); got != 0 {
		t.Fatalf("concurrent non-owning Close completed attempt before drain: %d", got)
	}
	close(body.releaseRead)
	if err := <-firstCloseDone; err != nil {
		t.Fatalf("owning Close: %v", err)
	}
	if err := <-secondCloseDone; err != nil {
		t.Fatalf("concurrent Close: %v", err)
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("attempt completions = %d, want exactly 1", got)
	}
	sink.mu.Lock()
	recorded := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(recorded.Response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if got, want := string(recordedBody), "complete-response-body"; got != want {
		t.Fatalf("recorded response body = %q, want %q", got, want)
	}
	roundTripper.mu.Lock()
	associated := len(roundTripper.responses)
	roundTripper.mu.Unlock()
	if associated != 0 {
		t.Fatalf("response associations after concurrent Close = %d, want 0", associated)
	}
}
