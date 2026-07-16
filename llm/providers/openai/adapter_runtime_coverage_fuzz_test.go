package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

type adapterRuntimeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f adapterRuntimeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type adapterRuntimeReadCloser struct{ err error }

func (r adapterRuntimeReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (adapterRuntimeReadCloser) Close() error               { return nil }

type adapterRuntimeStream struct {
	events   chan llm.StreamEvent
	closeErr error
}

func (s *adapterRuntimeStream) Events() <-chan llm.StreamEvent { return s.events }
func (s *adapterRuntimeStream) Close() error                   { return s.closeErr }

func adapterRuntimeHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// FuzzOpenAIAdapterRuntimeCoverage deterministically replays the adapter's
// runtime orchestration paths. The byte selects ordering only; every invocation
// exercises the same external contracts through local transports and streams.
func FuzzOpenAIAdapterRuntimeCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, selector byte) {
		if selector%2 == 0 {
			adapterRuntimeExerciseComplete(t)
			adapterRuntimeExerciseStreams(t)
		} else {
			adapterRuntimeExerciseStreams(t)
			adapterRuntimeExerciseComplete(t)
		}
		adapterRuntimeExerciseHelpers(t)
		adapterRuntimeExerciseInjectedErrors(t)
	})
}

func adapterRuntimeExerciseComplete(t *testing.T) {
	t.Helper()
	okBody := `{"id":"resp_runtime","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`
	a := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := adapterRuntimeHTTPResponse(req, http.StatusOK, okBody)
		resp.Header.Set("x-ratelimit-limit-requests", "10")
		return resp, nil
	})}}
	resp, err := a.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
	if err != nil || resp.ID != "resp_runtime" {
		t.Fatalf("Complete success = (%q, %v)", resp.ID, err)
	}

	for _, tc := range []struct {
		name string
		rt   http.RoundTripper
	}{
		{"transport", adapterRuntimeRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial failed") })},
		{"read", adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp := adapterRuntimeHTTPResponse(req, http.StatusOK, "")
			resp.Body = adapterRuntimeReadCloser{err: errors.New("read failed")}
			return resp, nil
		})},
		{"decode", adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return adapterRuntimeHTTPResponse(req, http.StatusOK, "not-json"), nil
		})},
		{"http", adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return adapterRuntimeHTTPResponse(req, http.StatusBadRequest, `{"error":{"message":"bad"}}`), nil
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", DisableChatFallback: true, Client: &http.Client{Transport: tc.rt}}
			if _, err := x.Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
				t.Fatal("Complete unexpectedly succeeded")
			}
		})
	}

	badURL := &Adapter{BaseURL: ":", Client: &http.Client{}}
	if _, err := badURL.Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
		t.Fatal("invalid URL unexpectedly succeeded")
	}
	nilClientBadURL := &Adapter{BaseURL: ":"}
	_, _ = nilClientBadURL.Complete(context.Background(), llm.Request{Model: "m"})

	chatSSE := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"fallback\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	var calls int
	fallback := &Adapter{APIKey: "k", BaseURL: "https://runtime.test/v1", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(req.URL.Path, "/responses") {
			return adapterRuntimeHTTPResponse(req, http.StatusNotFound, `{"error":{"code":"model_not_found"}}`), nil
		}
		return adapterRuntimeHTTPResponse(req, http.StatusOK, chatSSE), nil
	})}}
	if got, err := fallback.Complete(context.Background(), llm.Request{Model: "m"}); err != nil || calls != 2 || got.Message.Text() != "fallback" {
		t.Fatalf("Complete fallback = (%q, calls=%d, err=%v)", got.Message.Text(), calls, err)
	}

	streamSSE := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"streamed\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"m\"}}\n\n"
	streaming := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", ResponsesPath: defaultCodexResponses, Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return adapterRuntimeHTTPResponse(req, http.StatusOK, streamSSE), nil
	})}}
	if _, err := streaming.Complete(context.Background(), llm.Request{Model: "m"}); err != nil {
		t.Fatalf("streaming Complete: %v", err)
	}
	streaming.ResponsesPath = defaultCodexResponses
	streaming.DisableChatFallback = true
	streaming.Client = &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return adapterRuntimeHTTPResponse(req, http.StatusOK, ""), nil
	})}
	if _, err := streaming.Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
		t.Fatal("empty streaming Complete unexpectedly succeeded")
	}

	doubleFailure := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return adapterRuntimeHTTPResponse(req, http.StatusNotFound, `{}`), nil
	})}}
	if _, err := doubleFailure.Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
		t.Fatal("double endpoint Complete failure unexpectedly succeeded")
	}
}

func adapterRuntimeExerciseStreams(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		a    *Adapter
	}{
		{"invalid-url", &Adapter{BaseURL: ":"}},
		{"hard-http", &Adapter{BaseURL: "https://runtime.test", DisableChatFallback: true, Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return adapterRuntimeHTTPResponse(req, http.StatusBadRequest, `{}`), nil
		})}}},
		{"transport", &Adapter{BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial")
		})}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.a.Stream(context.Background(), llm.Request{Model: "m"}); err == nil {
				t.Fatal("Stream unexpectedly succeeded")
			}
		})
	}

	chatSSE := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"cc\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	var calls int
	a := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return adapterRuntimeHTTPResponse(req, http.StatusOK, ""), nil
		}
		return adapterRuntimeHTTPResponse(req, http.StatusOK, chatSSE), nil
	})}}
	s, err := a.Stream(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	if err := s.Close(); err != nil || calls != 2 {
		t.Fatalf("empty fallback close=%v calls=%d", err, calls)
	}

	failBoth := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/responses") {
			return adapterRuntimeHTTPResponse(req, http.StatusOK, ""), nil
		}
		return adapterRuntimeHTTPResponse(req, http.StatusBadRequest, `{}`), nil
	})}}
	s, err = failBoth.Stream(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var sawError bool
	for ev := range s.Events() {
		sawError = sawError || ev.Type == llm.StreamEventError
	}
	_ = s.Close()
	if !sawError {
		t.Fatal("double failure emitted no stream error")
	}

	continuation := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return adapterRuntimeHTTPResponse(req, http.StatusOK, ""), nil
	})}}
	s, err = continuation.Stream(context.Background(), llm.Request{Model: "m", PreviousResponseID: "prev"})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	_ = s.Close()

	directFallback := &Adapter{APIKey: "k", BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/responses") {
			return adapterRuntimeHTTPResponse(req, http.StatusNotFound, `{"error":{"code":"model_not_found"}}`), nil
		}
		return adapterRuntimeHTTPResponse(req, http.StatusOK, chatSSE), nil
	})}}
	s, err = directFallback.Stream(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	_ = s.Close()

	contentThenEmpty := &adapterRuntimeStream{events: make(chan llm.StreamEvent, 2)}
	contentThenEmpty.events <- llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "x"}
	contentThenEmpty.events <- llm.StreamEvent{Type: llm.StreamEventError, Err: errEmptyResponsesStream}
	close(contentThenEmpty.events)
	proxy := llm.NewChanStream(nil)
	a.decodeStream(context.Background(), proxy, contentThenEmpty, llm.Request{Model: "m"})
	for range proxy.Events() {
	}

	respClose := errors.New("response close")
	proxyClose := errors.New("proxy close")
	rp := &responsesFallbackProxyStream{
		responsesStream: &adapterRuntimeStream{events: closedAdapterRuntimeEvents(), closeErr: respClose},
		proxy:           &adapterRuntimeStream{events: closedAdapterRuntimeEvents(), closeErr: proxyClose},
	}
	if err := rp.Close(); !errors.Is(err, respClose) {
		t.Fatalf("close precedence = %v", err)
	}
	rp = &responsesFallbackProxyStream{
		responsesStream: &adapterRuntimeStream{events: closedAdapterRuntimeEvents()},
		proxy:           &adapterRuntimeStream{events: closedAdapterRuntimeEvents(), closeErr: proxyClose},
	}
	if err := rp.Close(); !errors.Is(err, proxyClose) {
		t.Fatalf("proxy close = %v", err)
	}
}

func closedAdapterRuntimeEvents() chan llm.StreamEvent {
	c := make(chan llm.StreamEvent)
	close(c)
	return c
}

func adapterRuntimeExerciseHelpers(t *testing.T) {
	t.Helper()
	a := &Adapter{}
	if a.shouldFallbackToChatCompletions(llm.Request{}, nil) {
		t.Fatal("nil error eligible for fallback")
	}
	a.DisableChatFallback = true
	if a.shouldFallbackToChatCompletions(llm.Request{}, errEmptyResponsesStream) {
		t.Fatal("disabled fallback eligible")
	}
	a.DisableChatFallback = false
	modelErr := llm.ErrorFromHTTPStatus("openai", http.StatusNotFound, "model unsupported", map[string]any{"error": map[string]any{"code": "unsupported_model"}}, nil)
	continuationReq := llm.Request{PreviousResponseID: "prev"}
	if a.shouldFallbackToChatCompletions(continuationReq, modelErr) {
		t.Fatal("continuation without history eligible")
	}
	continuationReq.FullHistoryFallbackMessages = []llm.Message{llm.User("hi")}
	if !a.shouldFallbackToChatCompletions(continuationReq, modelErr) {
		t.Fatal("model endpoint with history not eligible")
	}
	permanent404 := llm.ErrorFromHTTPStatus("openai", http.StatusNotFound, "bad request", map[string]any{}, nil)
	if a.shouldFallbackToChatCompletions(continuationReq, permanent404) {
		t.Fatal("permanent continuation error eligible")
	}

	for _, tc := range []struct {
		err  error
		want llm.ResponsesErrorClass
	}{
		{nil, llm.ResponsesErrorPermanentOther},
		{context.DeadlineExceeded, llm.ResponsesErrorTransient},
		{errors.New("plain"), llm.ResponsesErrorPermanentOther},
		{llm.ErrorFromHTTPStatus("openai", 400, "bad request", map[string]any{}, nil), llm.ResponsesErrorPermanentOther},
		{llm.ErrorFromHTTPStatus("openai", 400, "previous response expired", map[string]any{}, nil), llm.ResponsesErrorContinuationRejected},
		{llm.ErrorFromHTTPStatus("openai", 400, "model not supported", map[string]any{}, nil), llm.ResponsesErrorModelEndpoint},
	} {
		if got := a.ClassifyResponsesError(continuationReq, tc.err); got != tc.want {
			t.Fatalf("classification %v = %s, want %s", tc.err, got, tc.want)
		}
	}

	if got := chatFallbackRequest(llm.Request{Messages: []llm.Message{llm.User("current")}, FullHistoryFallbackMessages: []llm.Message{llm.User("full")}, PreviousResponseID: "p", ConversationID: "c", Continuation: &llm.ContinuationMetadata{}}); got.Messages[0].Text() != "full" || hasResponsesContinuationState(got) {
		t.Fatalf("chat fallback request = %#v", got)
	}

	for _, base := range []string{"https://x.test", "https://x.test/v1", "https://x.test/"} {
		x := &Adapter{BaseURL: base}
		_ = x.chatCompletionsURL()
		_ = x.responsesURL()
	}
	x := &Adapter{BaseURL: "https://x.test", ResponsesPath: "custom"}
	if x.responsesURL() != "https://x.test/custom" {
		t.Fatal(x.responsesURL())
	}
	for _, x = range []*Adapter{
		{BaseURL: "https://x.test"},
		{BaseURL: "https://x.test", ChatGPTAccountID: "acct"},
		{BaseURL: "https://x.test/backend-api/codex", ResponsesPath: defaultCodexResponses},
		{BaseURL: "https://x.test/backend-api/codex?existing=1", ResponsesPath: defaultCodexResponses},
	} {
		_ = x.modelsURL()
		_ = x.codexBackendURL("/models")
	}

	if !isUnconfigured(errNoCredentials) || isUnconfigured(errors.New("configured")) {
		t.Fatal("isUnconfigured mismatch")
	}
	if firstPositiveInt(-1, 0, 3, 4) != 3 || firstPositiveInt(-1, 0) != 0 {
		t.Fatal("firstPositiveInt mismatch")
	}
	if firstNonEmpty("", "  ", "x", "y") != "x" || firstNonEmpty("", " ") != "" {
		t.Fatal("firstNonEmpty mismatch")
	}

	usage := parseUsage(map[string]any{
		"input_tokens": 2, "output_tokens": 3, "total_tokens": 5,
		"input_tokens_details":  map[string]any{"cached_tokens": 8},
		"output_tokens_details": map[string]any{"reasoning_tokens": 1},
	})
	if usage.InputTokens != 0 || usage.CacheReadTokens == nil || usage.ReasoningTokens == nil {
		t.Fatalf("usage = %#v", usage)
	}
	_ = parseUsage(map[string]any{})

	var nilAdapter *Adapter
	nilAdapter.stampResponseIDHash(nil)
	a.stampResponseIDHash(&llm.Response{})
	badHasher := llm.NewContinuationHasher(bytes.Repeat([]byte{1}, 32))
	a.ContinuationHasher = badHasher
	r := &llm.Response{ID: "id"}
	a.stampResponseIDHash(r)
	if r.Raw["id_hash"] == nil {
		t.Fatal("response id hash missing")
	}
}

func adapterRuntimeExerciseInjectedErrors(t *testing.T) {
	t.Helper()
	originalBuild := adapterRuntimeBuildRequestBody
	originalMarshal := adapterRuntimeMarshal
	originalHash := adapterRuntimeHashResponseID
	originalStream := adapterRuntimeOpenStream
	originalFallback := adapterRuntimeFallbackStream
	defer func() {
		adapterRuntimeBuildRequestBody = originalBuild
		adapterRuntimeMarshal = originalMarshal
		adapterRuntimeHashResponseID = originalHash
		adapterRuntimeOpenStream = originalStream
		adapterRuntimeFallbackStream = originalFallback
	}()

	a := &Adapter{BaseURL: "https://runtime.test", Client: &http.Client{Transport: adapterRuntimeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return adapterRuntimeHTTPResponse(req, http.StatusOK, `{"id":"r","output":[]}`), nil
	})}}
	adapterRuntimeBuildRequestBody = func(*Adapter, llm.Request) (map[string]any, error) { return nil, errors.New("build") }
	if _, err := a.Complete(context.Background(), llm.Request{}); err == nil {
		t.Fatal("injected build error ignored")
	}
	adapterRuntimeBuildRequestBody = originalBuild
	adapterRuntimeMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal") }
	if _, err := a.Complete(context.Background(), llm.Request{}); err == nil {
		t.Fatal("injected marshal error ignored")
	}
	adapterRuntimeMarshal = originalMarshal
	a.ContinuationHasher = llm.NewContinuationHasher(bytes.Repeat([]byte{2}, 32))
	adapterRuntimeHashResponseID = func(*llm.ContinuationHasher, string) (string, error) { return "", errors.New("hash") }
	r := &llm.Response{ID: "r"}
	a.stampResponseIDHash(r)
	adapterRuntimeHashResponseID = originalHash

	adapterRuntimeOpenStream = func(*Adapter, context.Context, llm.Request) (llm.Stream, error) {
		return nil, errors.New("stream setup")
	}
	if _, err := a.completeViaStream(context.Background(), llm.Request{}); err == nil {
		t.Fatal("stream setup error ignored")
	}
	for _, tc := range []struct {
		name   string
		events []llm.StreamEvent
	}{
		{"nil-error", []llm.StreamEvent{{Type: llm.StreamEventError}}},
		{"typed-error", []llm.StreamEvent{{Type: llm.StreamEventError, Err: errors.New("event")}}},
		{"no-finish", []llm.StreamEvent{{Type: llm.StreamEventStreamStart}}},
	} {
		t.Run("complete-stream-"+tc.name, func(t *testing.T) {
			adapterRuntimeOpenStream = func(*Adapter, context.Context, llm.Request) (llm.Stream, error) {
				return adapterRuntimeStaticStream(tc.events), nil
			}
			if _, err := a.completeViaStream(context.Background(), llm.Request{}); err == nil {
				t.Fatal("injected stream unexpectedly completed")
			}
		})
	}
	adapterRuntimeOpenStream = originalStream

	adapterRuntimeFallbackStream = func(*Adapter, context.Context, llm.Request, error) (llm.Stream, error) {
		return nil, errors.New("fallback setup")
	}
	if _, err := a.completeViaChatCompletionsFallback(context.Background(), llm.Request{}, errors.New("responses")); err == nil {
		t.Fatal("fallback setup error ignored")
	}
	for _, tc := range []struct {
		name   string
		events []llm.StreamEvent
	}{
		{"nil-error", []llm.StreamEvent{{Type: llm.StreamEventError}}},
		{"typed-error", []llm.StreamEvent{{Type: llm.StreamEventError, Err: errors.New("event")}}},
		{"no-finish", []llm.StreamEvent{{Type: llm.StreamEventStreamStart}}},
	} {
		t.Run("complete-fallback-"+tc.name, func(t *testing.T) {
			adapterRuntimeFallbackStream = func(*Adapter, context.Context, llm.Request, error) (llm.Stream, error) {
				return adapterRuntimeStaticStream(tc.events), nil
			}
			if _, err := a.completeViaChatCompletionsFallback(context.Background(), llm.Request{}, errors.New("responses")); err == nil {
				t.Fatal("injected fallback unexpectedly completed")
			}
		})
	}
}

func adapterRuntimeStaticStream(events []llm.StreamEvent) llm.Stream {
	c := make(chan llm.StreamEvent, len(events))
	for _, ev := range events {
		c <- ev
	}
	close(c)
	return &adapterRuntimeStream{events: c}
}
