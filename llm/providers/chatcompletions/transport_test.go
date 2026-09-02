package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const chatJSON = `{"id":"chatcmpl-1","model":"m-wire","choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning_content":"why","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

const chatSSE = "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{\\\"a\\\":\"}}]}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
	"data: [DONE]\n\n"

type capturedRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

// server answers every chat.completions call with the given body and
// records what it received.
func server(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.header = r.URL.RequestURI(), r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		if strings.HasPrefix(body, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func liveRes(srv *httptest.Server, mutate func(c *registry.Caps)) registry.Resolved {
	res := resolved(mutate)
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	res.Headers = map[string]string{"X-Portkey-Provider": "openai"}
	return res
}

func TestCompleteDecodesTextToolCallsAndUsage(t *testing.T) {
	srv, got := server(t, 200, chatJSON)
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	req.SessionID = "s1"
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "work" || resp.Text() != "hello" || len(resp.ToolCalls()) != 1 || resp.ToolCalls()[0].Name != "f" || resp.Finish.Reason != "tool_calls" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if got.path != "/v1/chat/completions" || got.header.Get("Authorization") != "Bearer k-1" || got.header.Get("X-Portkey-Provider") != "openai" || got.body["model"] != "m-wire" {
		t.Fatalf("wire: %s %v %v", got.path, got.header, got.body)
	}
	if got.header.Get("session_id") != "" {
		t.Fatal("no affinity headers unless the cap says so")
	}
	affinity := liveRes(srv, func(c *registry.Caps) { c.SessionAffinityHeaders = new(true) })
	if _, err := p.Complete(context.Background(), req, affinity); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("session_id") != "s1" || got.header.Get("x-client-request-id") != "s1" || got.header.Get("x-session-affinity") != "s1" {
		t.Fatalf("affinity headers: %v", got.header)
	}
}

func TestStreamEmitsTextThenToolCallThenFinish(t *testing.T) {
	srv, got := server(t, 200, chatSSE)
	res := liveRes(srv, func(c *registry.Caps) { c.Fields["stream_options"] = false })
	p := &Protocol{Client: srv.Client()}
	s, err := p.Stream(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	var types []llm.StreamEventType
	var final *llm.Response
	for ev := range s.Events() {
		types = append(types, ev.Type)
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil || final.Provider != "work" || final.Text() != "hello" || len(final.ToolCalls()) != 1 || string(final.ToolCalls()[0].Arguments) != `{"a":1}` || final.Usage.TotalTokens != 15 {
		t.Fatalf("final = %+v events = %v", final, types)
	}
	if types[0] != llm.StreamEventStreamStart || types[len(types)-1] != llm.StreamEventFinish {
		t.Fatalf("events = %v", types)
	}
	if got.body["stream"] != true {
		t.Fatalf("stream flag missing: %v", got.body)
	}
	if _, has := got.body["stream_options"]; has {
		t.Fatalf("Fields[stream_options]=false must prune it from the wire: %v", got.body)
	}
}

func TestCompletionFinalizesOutputBudgetAfterTransportOverlay(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		run  func(*Protocol, llm.Request, registry.Resolved) error
	}{
		{"complete", chatJSON, func(p *Protocol, req llm.Request, res registry.Resolved) error {
			_, err := p.Complete(context.Background(), req, res)
			return err
		}},
		{"stream", chatSSE, func(p *Protocol, req llm.Request, res registry.Resolved) error {
			stream, err := p.Stream(context.Background(), req, res)
			if err != nil {
				return err
			}
			for event := range stream.Events() {
				if event.Type == llm.StreamEventError {
					return event.Err
				}
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := server(t, http.StatusOK, tc.body)
			res := liveRes(srv, nil)
			res.Transport.Body = map[string]any{"max_tokens": 1000}
			req := userReq("hi")
			req.MaxTokens = new(100)
			if err := tc.run(&Protocol{Client: srv.Client()}, req, res); err != nil {
				t.Fatal(err)
			}
			if got.body["max_tokens"] != float64(100) {
				t.Fatalf("wire max_tokens = %v, want admitted 100; body = %v", got.body["max_tokens"], got.body)
			}
		})
	}
}

func TestCompletionFinalizerDoesNotRestoreDisabledOutputField(t *testing.T) {
	srv, got := server(t, http.StatusOK, chatJSON)
	res := liveRes(srv, func(caps *registry.Caps) { caps.Fields[registry.FieldMaxTokens] = false })
	res.Transport.Body = map[string]any{"max_tokens": 1000}
	req := userReq("hi")
	req.MaxTokens = new(100)
	if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}
	if _, exists := got.body["max_tokens"]; exists {
		t.Fatalf("disabled max_tokens restored after prune: %v", got.body)
	}
}

func TestStreamInbandErrorBecomesTypedError(t *testing.T) {
	srv, _ := server(t, 200, "data: {\"error\":{\"message\":\"Rate limit reached\",\"type\":\"rate_limit_error\",\"code\":429}}\n\n")
	p := &Protocol{Client: srv.Client()}
	s, err := p.Stream(context.Background(), userReq("hi"), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			streamErr = ev.Err
		}
	}
	var le llm.Error
	if streamErr == nil || !errors.As(streamErr, &le) || le.StatusCode() != 429 || le.Provider() != "work" {
		t.Fatalf("stream err = %v", streamErr)
	}
}

func TestCompleteClassifiesHTTPErrorsWithHints(t *testing.T) {
	srv, _ := server(t, 400, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`)
	p := &Protocol{Client: srv.Client()}
	_, err := p.Complete(context.Background(), userReq("hi"), liveRes(srv, nil))
	if llm.Kind(err) != llm.KindInvalidRequest || !strings.Contains(llm.ErrorHint(err), `set max_tokens_field = "max_completion_tokens" on work/m`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCountTokensIsUnsupported(t *testing.T) {
	srv, _ := server(t, 200, chatJSON)
	res := liveRes(srv, nil)
	if _, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), userReq("hi"), res); !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
		t.Fatalf("err = %v", err)
	}
	res.Transport.CountTokensEndpoint = "/count"
	if _, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), userReq("hi"), res); err == nil {
		t.Fatal("a configured endpoint on a protocol without one is a configuration error")
	}
}

// TestCompleteWrapsImmediateCancel ports the deleted openaicompat pin (#696):
// a Complete whose context is already canceled classifies as the caller's
// abort — never as a retryable timeout. The server holds the connection open
// until the request context dies, and the client carries no http.Client
// timeout, so net/http's timeoutError wrapper cannot win the race and
// misreport the cancellation on a slow runner.
func TestCompleteWrapsImmediateCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &Protocol{Client: srv.Client()}
	_, err := p.Complete(ctx, userReq("hi"), liveRes(srv, nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a context.Canceled wrap", err)
	}
	if llm.Classify(err) == llm.ErrorClassRetryable {
		t.Fatalf("err classifies retryable, want non-retryable abort: %v", err)
	}
}
