package llm_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

// wireProvider is one protocol leg of the core wire tests: the registry
// instance that points a protocol at the test server, plus the response
// bodies that protocol speaks.
type wireProvider struct {
	name         string
	protocol     string
	base         string
	completeBody string
	streamBody   string
	streamPrefix string
}

// wireProviders covers every protocol the registry can dispatch: the two
// OpenAI wire shapes under distinct instance names (the openai id speaks
// Responses; a renamed instance speaks Chat Completions), plus Anthropic and
// Google.
func wireProviders() []wireProvider {
	return []wireProvider{
		{
			name:         "openai",
			protocol:     registry.ProtocolOpenAIResponses,
			base:         "openai",
			completeBody: `{"id":"resp-1","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			streamBody: strings.Join([]string{
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"hello"}`,
				``,
				`event: response.completed`,
				`data: {"type":"response.completed","response":{"id":"resp-1","model":"test-model","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"partial"}`,
				``,
				``,
			}, "\n"),
		},
		{
			name:         "anthropic",
			protocol:     registry.ProtocolAnthropic,
			base:         "anthropic",
			completeBody: `{"id":"msg_1","model":"test-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			streamBody: strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","model":"test-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				``,
				`event: content_block_start`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				``,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
				``,
				`event: content_block_stop`,
				`data: {"type":"content_block_stop","index":0}`,
				``,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				``,
				`event: message_stop`,
				`data: {"type":"message_stop"}`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
				``,
				``,
			}, "\n"),
		},
		{
			name:         "google",
			protocol:     registry.ProtocolGoogle,
			base:         "google",
			completeBody: `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"test-model"}`,
			streamBody: strings.Join([]string{
				`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`,
				``,
				``,
			}, "\n"),
		},
		{
			name:         "work",
			protocol:     registry.ProtocolOpenAIChat,
			base:         "openai",
			completeBody: `{"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			streamBody: strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n"),
			streamPrefix: strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}`,
				``,
				``,
			}, "\n"),
		},
	}
}

// instance is the registry record that points this leg at baseURL, with the
// default headers the redirect tests assert on.
func (p wireProvider) instance(baseURL string, headers map[string]string) registry.Provider {
	return registry.Provider{
		Base:      p.base,
		Protocol:  p.protocol,
		APIKey:    "test-key",
		Headers:   headers,
		Transport: registry.Transport{BaseURL: baseURL},
	}
}

// wireClient builds a hermetic registry client serving this leg at baseURL and
// points the leg's protocol at httpClient for the test's duration. The
// protocols are process singletons, so no test in this file may run in
// parallel.
func (p wireProvider) wireClient(t *testing.T, baseURL string, httpClient *http.Client, headers map[string]string) *llm.Client {
	t.Helper()
	useWireHTTPClient(t, p.protocol, httpClient)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{p.name: p.instance(baseURL, headers)}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

// useWireHTTPClient swaps a protocol's HTTP client for the test's duration,
// the seam the redirect and timeout cases need to observe the transport.
func useWireHTTPClient(t *testing.T, protocol string, c *http.Client) {
	t.Helper()
	var restore func()
	switch protocol {
	case registry.ProtocolOpenAIResponses:
		prev := responses.DefaultProtocol.Client
		responses.DefaultProtocol.Client = c
		restore = func() { responses.DefaultProtocol.Client = prev }
	case registry.ProtocolOpenAIChat:
		prev := chatcompletions.DefaultProtocol.Client
		chatcompletions.DefaultProtocol.Client = c
		restore = func() { chatcompletions.DefaultProtocol.Client = prev }
	case registry.ProtocolAnthropic:
		prev := anthropic.DefaultProtocol.Client
		anthropic.DefaultProtocol.Client = c
		restore = func() { anthropic.DefaultProtocol.Client = prev }
	case registry.ProtocolGoogle:
		prev := google.DefaultProtocol.Client
		google.DefaultProtocol.Client = c
		restore = func() { google.DefaultProtocol.Client = prev }
	default:
		t.Fatalf("no protocol client seam for %q", protocol)
	}
	t.Cleanup(restore)
}

type outcomeSink struct {
	attempts []apilog.APIAttemptRecord
}

func (s *outcomeSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	s.attempts = append(s.attempts, record)
	return nil
}

func (*outcomeSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close() //nolint:errcheck // Test transport honors the RoundTripper request-body contract.
	}
	return f(request)
}

func TestCoreCompleteWireCaptureOutcomes(t *testing.T) {
	binaryBody := []byte{0x00, 0xff, 0x80, '\n'}
	responseCases := []struct {
		name    string
		status  int
		body    []byte
		outcome apilog.AttemptOutcomeClass
	}{
		{name: "provider rejection", status: http.StatusTooManyRequests, body: []byte(`{"error":{"message":"limited"}}`), outcome: apilog.AttemptProviderReject},
		{name: "binary provider rejection", status: http.StatusBadGateway, body: binaryBody, outcome: apilog.AttemptProviderReject},
		{name: "malformed JSON", status: http.StatusOK, body: []byte(`{"broken"`), outcome: apilog.AttemptDecodeFail},
		{name: "binary response", status: http.StatusOK, body: binaryBody, outcome: apilog.AttemptDecodeFail},
	}

	for _, provider := range wireProviders() {
		t.Run(provider.name, func(t *testing.T) {
			for _, testCase := range responseCases {
				t.Run(testCase.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(testCase.status)
						_, _ = w.Write(testCase.body)
					}))
					t.Cleanup(server.Close)
					client := provider.wireClient(t, server.URL, server.Client(), nil)
					assertCompleteOutcome(context.Background(), t, client, provider.name, nil, testCase.outcome, testCase.body)
				})
			}

			t.Run("caller cancellation", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					return nil, request.Context().Err()
				})}
				client := provider.wireClient(t, "https://provider.test", httpClient, nil)
				assertCompleteOutcome(ctx, t, client, provider.name, nil, apilog.AttemptCallerCancel, nil)
			})

			t.Run("provider timeout", func(t *testing.T) {
				httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					<-request.Context().Done()
					return nil, request.Context().Err()
				})}
				client := provider.wireClient(t, "https://provider.test", httpClient, nil)
				timeout := &llm.AdapterTimeout{Request: time.Millisecond}
				assertCompleteOutcome(context.Background(), t, client, provider.name, timeout, apilog.AttemptProviderTimeout, nil)
			})

			t.Run("response header timeout", func(t *testing.T) {
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					<-release
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(server.Close)
				t.Cleanup(func() { close(release) })
				client := provider.wireClient(t, server.URL, server.Client(), nil)
				timeout := &llm.AdapterTimeout{Request: 10 * time.Millisecond}
				assertCompleteOutcome(context.Background(), t, client, provider.name, timeout, apilog.AttemptProviderTimeout, nil)
			})

			t.Run("transport failure", func(t *testing.T) {
				httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("round trip failed")
				})}
				client := provider.wireClient(t, "https://provider.test", httpClient, nil)
				assertCompleteOutcome(context.Background(), t, client, provider.name, nil, apilog.AttemptTransportFail, nil)
			})

			t.Run("configured request budget does not relabel generic transport failure", func(t *testing.T) {
				httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("round trip failed")
				})}
				client := provider.wireClient(t, "https://provider.test", httpClient, nil)
				timeout := &llm.AdapterTimeout{Request: time.Second}
				assertCompleteOutcome(context.Background(), t, client, provider.name, timeout, apilog.AttemptTransportFail, nil)
			})
		})
	}
}

func assertCompleteOutcome(ctx context.Context, t *testing.T, client *llm.Client, provider string, timeout *llm.AdapterTimeout, want apilog.AttemptOutcomeClass, wantResponseBody []byte) {
	t.Helper()
	sink := &outcomeSink{}
	ctx = llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(ctx, llm.NewAPIAttemptGroup("ag_outcome_"+provider)),
		sink,
	)
	req := providerRequest(provider, "test-model")
	req.AdapterTimeout = timeout
	_, callErr := client.Complete(ctx, req)
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1 (call error: %v)", len(sink.attempts), callErr)
	}
	record := sink.attempts[0]
	if record.Outcome != want {
		t.Fatalf("outcome = %q, want %q (record=%+v)", record.Outcome, want, record)
	}
	switch want {
	case apilog.AttemptProviderReject:
		if callErr == nil {
			t.Fatal("provider rejection returned nil error")
		}
	case apilog.AttemptCallerCancel:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("caller cancellation error = %v, want context.Canceled identity", callErr)
		}
	case apilog.AttemptProviderTimeout:
		if !errors.Is(callErr, context.DeadlineExceeded) && llm.Kind(callErr) != llm.KindTimeout {
			t.Fatalf("provider timeout error = %v, want context deadline identity or timeout kind", callErr)
		}
	case apilog.AttemptTransportFail:
		if callErr == nil || !strings.Contains(callErr.Error(), "round trip failed") {
			t.Fatalf("transport error = %v, want original cause text", callErr)
		}
	}
	if wantResponseBody == nil {
		return
	}
	if record.Response == nil {
		t.Fatal("attempt has no response evidence")
	}
	got, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if !bytes.Equal(got, wantResponseBody) {
		t.Fatalf("response body = %v, want %v", got, wantResponseBody)
	}
	if !utf8.Valid(wantResponseBody) && record.Response.Body.Encoding != apilog.BodyBase64 {
		t.Fatalf("binary response encoding = %q, want base64", record.Response.Body.Encoding)
	}
}

// TestCoreResponsesEmptyStreamIsPermanentEndToEnd pins the sentinel's class on
// a real dispatch: a Responses endpoint that answers 200 OK with no events at
// all means the model does not speak this protocol, so the stream's terminal
// error must short-circuit the retry chain rather than burn its budget.
func TestCoreResponsesEmptyStreamIsPermanentEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	provider := wireProviders()[0]
	client := provider.wireClient(t, server.URL, server.Client(), nil)
	stream, err := client.Stream(context.Background(), providerRequest(provider.name, "test-model"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var streamErr error
	for event := range stream.Events() {
		if event.Type == llm.StreamEventError {
			streamErr = event.Err
		}
	}
	if streamErr == nil {
		t.Fatal("empty Responses stream ended without a terminal error")
	}
	if got := llm.Classify(streamErr); got != llm.ErrorClassPermanent {
		t.Fatalf("Classify(%v) = %v, want Permanent", streamErr, got)
	}
	if got := llm.Kind(streamErr); got != llm.KindNotFound {
		t.Fatalf("Kind(%v) = %v, want KindNotFound", streamErr, got)
	}
}
