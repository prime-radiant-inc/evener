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
	firstCloseDone := make(chan error, 1)
	go func() { firstCloseDone <- response.Body.Close() }()
	<-body.readEntered
	secondCloseDone := make(chan error, 1)
	secondCloseStarted := make(chan struct{})
	go func() {
		close(secondCloseStarted)
		secondCloseDone <- response.Body.Close()
	}()
	<-secondCloseStarted
	interruptedDrain := false
	select {
	case <-body.closed:
		interruptedDrain = true
	case <-time.After(100 * time.Millisecond):
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
	if interruptedDrain {
		t.Fatal("concurrent Close interrupted the owning response drain")
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
