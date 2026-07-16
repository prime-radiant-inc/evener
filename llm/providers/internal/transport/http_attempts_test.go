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

type claimedReadAfterCloseBody struct {
	data          []byte
	readErr       error
	readEntered   chan struct{}
	closeReturned chan struct{}
	appendEntered <-chan struct{}
	readDone      chan struct{}
	closeErr      error
	wrapper       *apiAttemptResponseBody
	readOnce      sync.Once
	closeOnce     sync.Once
}

func (b *claimedReadAfterCloseBody) Read(p []byte) (int, error) {
	b.readOnce.Do(func() { close(b.readEntered) })
	<-b.closeReturned
	b.wrapper.mu.Lock()
	closing := b.wrapper.closing
	b.wrapper.mu.Unlock()
	if !closing {
		<-b.appendEntered
	}
	n := copy(p, b.data)
	close(b.readDone)
	return n, b.readErr
}

func (b *claimedReadAfterCloseBody) Close() error {
	b.closeOnce.Do(func() { close(b.closeReturned) })
	return b.closeErr
}

type waitForReadAttemptSink struct {
	mu            sync.Mutex
	attempts      []apilog.APIAttemptRecord
	appendEntered chan struct{}
	readDone      <-chan struct{}
}

func (s *waitForReadAttemptSink) AppendAttempt(_ context.Context, attempt apilog.APIAttemptRecord) error {
	close(s.appendEntered)
	<-s.readDone
	s.mu.Lock()
	s.attempts = append(s.attempts, attempt)
	s.mu.Unlock()
	return nil
}

func (*waitForReadAttemptSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

type countedCloseBody struct {
	io.Reader
	mu         sync.Mutex
	closeCount int
	firstErr   error
	nextErr    error
}

func (b *countedCloseBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeCount++
	if b.closeCount == 1 {
		return b.firstErr
	}
	return b.nextErr
}

func (b *countedCloseBody) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCount
}

type redirectAdmittedReadBody struct {
	data              []byte
	readErr           error
	closeErr          error
	readEntered       chan struct{}
	closeStarted      chan struct{}
	concurrentEntered <-chan struct{}
	readOnce          sync.Once
	closeOnce         sync.Once
	mu                sync.Mutex
	closeCount        int
}

func (b *redirectAdmittedReadBody) Read(p []byte) (int, error) {
	b.readOnce.Do(func() { close(b.readEntered) })
	<-b.closeStarted
	<-b.concurrentEntered
	return copy(p, b.data), b.readErr
}

func (b *redirectAdmittedReadBody) Close() error {
	b.mu.Lock()
	b.closeCount++
	b.mu.Unlock()
	b.closeOnce.Do(func() { close(b.closeStarted) })
	return b.closeErr
}

func (b *redirectAdmittedReadBody) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCount
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

func TestAPIAttemptRedirectCloseUnblocksAdmittedCheckRedirectRead(t *testing.T) {
	readErr := errors.New("redirect admitted read sentinel")
	closeErr := errors.New("redirect underlying close sentinel")
	concurrentEntered := make(chan struct{})
	body := &redirectAdmittedReadBody{
		data:              []byte("redirect bytes consumed by CheckRedirect"),
		readErr:           readErr,
		closeErr:          closeErr,
		readEntered:       make(chan struct{}),
		closeStarted:      make(chan struct{}),
		concurrentEntered: concurrentEntered,
	}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_redirect_admitted_read")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/start", nil)
	if err != nil {
		t.Fatal(err)
	}

	var firstResponse *http.Response
	var secondHopSawAppend bool
	baseCalls := 0
	roundTripper := &apiAttemptRoundTripper{
		base: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
			baseCalls++
			if baseCalls == 1 {
				return &http.Response{
					StatusCode:    http.StatusFound,
					Header:        http.Header{"Location": []string{"/next"}},
					Body:          body,
					ContentLength: 2<<10 + 1,
				}, nil
			}
			secondHopSawAppend = sink.count() == 1
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("final response")),
			}, nil
		}),
		parentCtx: context.Background(),
		build: func(*http.Request, []byte) llm.APIAttemptMeta {
			return llm.APIAttemptMeta{ProviderInstance: "test"}
		},
	}

	type readResult struct {
		body []byte
		err  error
	}
	checkRedirectRead := make(chan readResult, 1)
	concurrentClose := make(chan error, 1)
	client := &http.Client{
		Transport: roundTripper,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			firstResponse = request.Response
			wrappedBody, ok := firstResponse.Body.(*apiAttemptResponseBody)
			if !ok {
				return errors.New("redirect response did not retain canonical attempt body")
			}
			wrappedBody.mu.Lock()
			wrappedBody.waitForClose = func(done <-chan struct{}) {
				close(concurrentEntered)
				<-done
			}
			wrappedBody.mu.Unlock()
			go func() {
				buf := make([]byte, len(body.data))
				n, readResponseErr := firstResponse.Body.Read(buf)
				checkRedirectRead <- readResult{body: buf[:n], err: readResponseErr}
			}()
			<-body.readEntered
			go func() {
				<-body.closeStarted
				concurrentClose <- firstResponse.Body.Close()
			}()
			return nil
		},
	}

	type clientResult struct {
		response *http.Response
		err      error
	}
	clientDone := make(chan clientResult, 1)
	go func() {
		response, clientErr := client.Do(request)
		clientDone <- clientResult{response: response, err: clientErr}
	}()
	deadlockGuard := time.NewTimer(5 * time.Second)
	defer deadlockGuard.Stop()
	var response *http.Response
	select {
	case result := <-clientDone:
		response = result.response
		if result.err != nil {
			t.Fatalf("client.Do: %v", result.err)
		}
	case <-deadlockGuard.C:
		t.Fatal("client.Do deadlocked closing a redirect response with an admitted CheckRedirect read")
	}
	if !secondHopSawAppend {
		t.Fatal("second redirect hop began before the unclaimed attempt append")
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("attempts appended before client return = %d, want 1 redirect attempt", got)
	}
	read := <-checkRedirectRead
	if !bytes.Equal(read.body, body.data) {
		t.Fatalf("CheckRedirect read = %q, want %q", read.body, body.data)
	}
	if read.err != readErr {
		t.Fatalf("CheckRedirect read error = %v, want %v", read.err, readErr)
	}
	if got := <-concurrentClose; got != closeErr {
		t.Fatalf("concurrent Close error = %v, want cached %v", got, closeErr)
	}
	if got := body.count(); got != 1 {
		t.Fatalf("underlying Close count = %d, want exactly 1", got)
	}

	sink.mu.Lock()
	redirectAttempt := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(redirectAttempt.Response.Body)
	if err != nil {
		t.Fatalf("decode redirect response body: %v", err)
	}
	if !bytes.Equal(recordedBody, body.data) {
		t.Fatalf("recorded redirect body = %q, want admitted bytes %q", recordedBody, body.data)
	}
	if redirectAttempt.Outcome != apilog.AttemptProviderReject {
		t.Fatalf("redirect outcome = %q, want %q", redirectAttempt.Outcome, apilog.AttemptProviderReject)
	}
	if want := readErr.Error() + "\n" + closeErr.Error(); redirectAttempt.ErrorMessage != want {
		t.Fatalf("redirect error = %q, want %q", redirectAttempt.ErrorMessage, want)
	}
	roundTripper.mu.Lock()
	_, firstStillAssociated := roundTripper.responses[firstResponse]
	roundTripper.mu.Unlock()
	if firstStillAssociated {
		t.Fatal("redirect response association was not released")
	}

	finalAttempt := roundTripper.claimResponse(response)
	if finalAttempt == nil {
		t.Fatal("final response did not retain its canonical attempt")
	}
	_, _ = io.ReadAll(response.Body)
	finalAttempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close final response: %v", err)
	}
	roundTripper.mu.Lock()
	remainingAssociations := len(roundTripper.responses)
	roundTripper.mu.Unlock()
	if remainingAssociations != 0 {
		t.Fatalf("response associations after final cleanup = %d, want 0", remainingAssociations)
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

func TestAPIAttemptClaimedCloseWaitsForAdmittedResponseReadBeforeSnapshot(t *testing.T) {
	readDone := make(chan struct{})
	sink := &waitForReadAttemptSink{
		appendEntered: make(chan struct{}),
		readDone:      readDone,
	}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_claimed_read_after_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("final admitted read error")
	closeErr := errors.New("visible response close error")
	body := &claimedReadAfterCloseBody{
		data:          []byte("final-response-bytes"),
		readErr:       readErr,
		readEntered:   make(chan struct{}),
		closeReturned: make(chan struct{}),
		appendEntered: sink.appendEntered,
		readDone:      readDone,
		closeErr:      closeErr,
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
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
	sharedBody, ok := response.Body.(*apiAttemptSharedResponseBody)
	if !ok {
		t.Fatalf("response body = %T, want shared final body", response.Body)
	}
	wrapper, ok := sharedBody.ReadCloser.(*apiAttemptResponseBody)
	if !ok {
		t.Fatalf("shared body inner = %T, want canonical attempt body", sharedBody.ReadCloser)
	}
	body.wrapper = wrapper
	readResult := make(chan error, 1)
	go func() {
		_, readResultErr := response.Body.Read(make([]byte, len(body.data)))
		readResult <- readResultErr
	}()
	<-body.readEntered
	attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, readErr, nil)
	if err := <-readResult; !errors.Is(err, readErr) {
		t.Fatalf("response Read error = %v, want %v", err, readErr)
	}
	sink.mu.Lock()
	if len(sink.attempts) != 1 {
		sink.mu.Unlock()
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	recorded := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(recorded.Response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if !bytes.Equal(recordedBody, body.data) {
		t.Fatalf("recorded response body = %q, want final admitted bytes %q", recordedBody, body.data)
	}
	if recorded.Outcome != apilog.AttemptDecodeFail {
		t.Fatalf("admitted response read outcome = %q, want %q", recorded.Outcome, apilog.AttemptDecodeFail)
	}
}

func TestAPIAttemptFinalVisibleBodySharesOneIdempotentClose(t *testing.T) {
	firstCloseErr := errors.New("first visible close sentinel")
	duplicateCloseErr := errors.New("duplicate underlying close")
	underlying := &countedCloseBody{
		Reader:   strings.NewReader("response"),
		firstErr: firstCloseErr,
		nextErr:  duplicateCloseErr,
	}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_shared_final_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/request", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: time.Hour,
		Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: underlying}, nil
		}),
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if attempt == nil || attempt.responseClose == nil {
		t.Fatal("final response did not bind canonical close ownership")
	}
	firstErr := attempt.responseClose()
	secondErr := response.Body.Close()
	attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
	thirdErr := response.Body.Close()
	for i, closeResult := range []error{firstErr, secondErr, thirdErr} {
		if closeResult != firstCloseErr {
			t.Fatalf("Close result %d = %v, want identical sentinel %v", i+1, closeResult, firstCloseErr)
		}
	}
	if got := underlying.count(); got != 1 {
		t.Fatalf("underlying Close count = %d, want exactly 1", got)
	}
}

func TestAPIAttemptClaimedResponseRejectsReadAfterTerminalClose(t *testing.T) {
	underlying := &countedCloseBody{Reader: strings.NewReader("unread-response")}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_response_read_after_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/request", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: underlying}, nil
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
	if err := response.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := response.Body.Read(make([]byte, 1)); !errors.Is(err, http.ErrBodyReadAfterClose) {
		t.Fatalf("Read after terminal Close error = %v, want %v", err, http.ErrBodyReadAfterClose)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: http.StatusOK}, llm.APITimeoutNone, nil, nil)
	if got := underlying.count(); got != 1 {
		t.Fatalf("underlying Close count = %d, want 1", got)
	}
}

func TestAPIAttemptInactiveFinalBodyRemainsUnwrapped(t *testing.T) {
	underlying := &countedCloseBody{Reader: strings.NewReader("response")}
	request, err := http.NewRequest(http.MethodGet, "https://provider.test/request", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: underlying}, nil
	})}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if attempt != nil {
		t.Fatal("inactive call returned canonical attempt")
	}
	if response.Body != underlying {
		t.Fatalf("inactive response body = %T/%p, want original %T/%p", response.Body, response.Body, underlying, underlying)
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
