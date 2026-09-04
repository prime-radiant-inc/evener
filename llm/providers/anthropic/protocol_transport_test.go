package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

// captureSink collects the api-log attempt records a call emits.
type captureSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

func (s *captureSink) AppendAttempt(_ context.Context, r apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, r)
	return nil
}

func (s *captureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

// records returns a copy of the attempts appended so far. Every read goes
// through it: the producer appends from its own goroutine, so an unguarded
// read is only accidentally safe.
func (s *captureSink) records() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

func (s *captureSink) families() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.attempts))
	for _, a := range s.attempts {
		out = append(out, a.Request.EndpointFamily)
	}
	return out
}

const messagesJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x-wire","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`

const messagesSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x-wire\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

type protoCapture struct {
	paths  []string
	header http.Header
	body   map[string]any
}

func protoServer(t *testing.T, handler func(r *http.Request) (int, string)) (*httptest.Server, *protoCapture) {
	t.Helper()
	got := &protoCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.paths = append(got.paths, r.URL.RequestURI())
		got.header = r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		status, body := handler(r)
		if strings.HasPrefix(body, "event:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func protoLive(srv *httptest.Server) registry.Resolved {
	res := protoRes(nil)
	res.Transport = registry.Transport{Auth: registry.AuthHeader, AuthHeader: "x-api-key", BaseURL: srv.URL + "/v1", Endpoint: "/messages", StreamEndpoint: "/messages", ModelsEndpoint: "/models", CountTokensEndpoint: "/messages/count_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	res.Headers = map[string]string{"anthropic-beta": "context-1m-2025-08-07"}
	return res
}

func TestProtocolCompleteAndHeaders(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return 200, messagesJSON })
	res := protoLive(srv)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "anthropic-prod" || resp.Text() != "hello" || resp.Finish.Reason != "stop" || resp.Usage.InputTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	h := got.header
	if got.paths[0] != "/v1/messages" || h.Get("x-api-key") != "k-1" || h.Get("anthropic-version") != anthropicVersion || h.Get("anthropic-beta") != "context-1m-2025-08-07" || h.Get("Authorization") != "" {
		t.Fatalf("wire: %v %v", got.paths, h)
	}
}

func TestProtocolStreamDecodesThroughTheSharedDecoder(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return 200, messagesSSE })
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil || final.Provider != "anthropic-prod" || final.Text() != "hello" || final.Usage.OutputTokens != 3 || got.body["stream"] != true {
		t.Fatalf("final = %+v body = %v", final, got.body)
	}
}

func TestProtocolCompletionFinalizesOutputBudgetAfterTransportOverlay(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		run  func(*Protocol, llm.Request, registry.Resolved) error
	}{
		{"complete", messagesJSON, func(p *Protocol, req llm.Request, res registry.Resolved) error {
			_, err := p.Complete(context.Background(), req, res)
			return err
		}},
		{"stream", messagesSSE, func(p *Protocol, req llm.Request, res registry.Resolved) error {
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
			srv, got := protoServer(t, func(*http.Request) (int, string) { return http.StatusOK, tc.body })
			res := protoLive(srv)
			res.Transport.Body = map[string]any{"max_tokens": 1000}
			req := protoReq("")
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

func TestProtocolCompletionFinalizerDoesNotRestoreDisabledOutputField(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return http.StatusOK, messagesJSON })
	res := protoLive(srv)
	res.Caps.Fields[registry.FieldMaxTokens] = false
	res.Transport.Body = map[string]any{"max_tokens": 1000}
	req := protoReq("")
	req.MaxTokens = new(100)
	if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}
	if _, exists := got.body["max_tokens"]; exists {
		t.Fatalf("disabled max_tokens restored after prune: %v", got.body)
	}
}

// TestProtocolCompletionClampsEffortBudgetUnderTransportMaxTokensConstant
// pins the transport-overlay interaction roborev flagged on 312ce22: the
// transport's body constants run after the body builder and override the
// max_tokens it left there, and the completion finalizer then enforces
// max_tokens > budget_tokens against the overlaid value — so a row-level
// max_tokens constant must bound the effort-derived thinking budget too.
func TestProtocolCompletionClampsEffortBudgetUnderTransportMaxTokensConstant(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		run  func(*Protocol, llm.Request, registry.Resolved) error
	}{
		{"complete", messagesJSON, func(p *Protocol, req llm.Request, res registry.Resolved) error {
			_, err := p.Complete(context.Background(), req, res)
			return err
		}},
		{"stream", messagesSSE, func(p *Protocol, req llm.Request, res registry.Resolved) error {
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
			srv, got := protoServer(t, func(*http.Request) (int, string) { return http.StatusOK, tc.body })
			res := protoLive(srv)
			res.Caps.ThinkingShape = new("budget+effort")
			res.Caps.MaxOutputTokens = new(131072)
			res.Transport.Body = map[string]any{"max_tokens": 8192}
			req := protoReq("high") // a 32768-token table budget over the 8192 constant
			req.MaxTokens = new(131072)
			if err := tc.run(&Protocol{Client: srv.Client()}, req, res); err != nil {
				t.Fatal(err)
			}
			thinking, _ := got.body["thinking"].(map[string]any)
			if budget := intFromAny(thinking["budget_tokens"]); budget != 8191 {
				t.Fatalf("wire budget_tokens = %d, want 8191 under the transport's 8192 constant: %v", budget, got.body)
			}
			if mt := intFromAny(got.body["max_tokens"]); mt != 8192 {
				t.Fatalf("wire max_tokens = %d, want the 8192 transport constant: %v", mt, got.body)
			}
		})
	}
}

func TestProtocolListModelsPaginatesAndCountTokensStrips(t *testing.T) {
	srv, got := protoServer(t, func(r *http.Request) (int, string) {
		switch {
		case strings.Contains(r.URL.RawQuery, "after_id=m2"):
			return 200, `{"data":[{"id":"m3","display_name":"M3"}],"has_more":false,"last_id":"m3"}`
		case strings.HasPrefix(r.URL.Path, "/v1/models"):
			return 200, `{"data":[{"id":"m2","display_name":"M2"},{"id":"m1","display_name":"M1"}],"has_more":true,"last_id":"m2"}`
		default:
			return 200, `{"input_tokens":21}`
		}
	})
	p := &Protocol{Client: srv.Client()}
	res := protoLive(srv)
	rows, err := p.ListModels(context.Background(), res)
	if err != nil || len(rows) != 3 || rows[0].ID != "m1" || rows[2].ID != "m3" {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if len(got.paths) != 2 || !strings.HasPrefix(got.paths[0], "/v1/models?limit=1000") || !strings.Contains(got.paths[1], "after_id=m2") {
		t.Fatalf("paths = %v", got.paths)
	}
	req := protoReq("high")
	req.MaxTokens = new(10)
	req.StopSequences = []string{"x"}
	n, err := p.CountTokens(context.Background(), req, res)
	if err != nil || n != 21 || got.paths[len(got.paths)-1] != "/v1/messages/count_tokens" {
		t.Fatalf("count = %d err = %v paths = %v", n, err, got.paths)
	}
	for _, k := range []string{"max_tokens", "temperature", "top_p", "stop_sequences", "service_tier", "cache_control"} {
		if _, has := got.body[k]; has {
			t.Fatalf("%s must be stripped from the count body: %v", k, got.body)
		}
	}
	res.Transport.ModelsEndpoint, res.Transport.CountTokensEndpoint = registry.EndpointUnsupported, registry.EndpointUnsupported
	if _, err := p.ListModels(context.Background(), res); !errors.Is(err, llm.ErrModelListingUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.CountTokens(context.Background(), req, res); !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestProtocolCountTokensDoesNotEnforceCompletionThinkingBudget(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return http.StatusOK, `{"input_tokens":21}` })
	req := protoReq("")
	req.MaxTokens = new(1000)
	req.Tools = []llm.ToolDefinition{{Name: "lookup"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	req.ProviderOptions = map[string]any{
		registry.ProtocolAnthropic: map[string]any{
			"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024},
		},
	}
	n, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), req, protoLive(srv))
	if err != nil || n != 21 {
		t.Fatalf("CountTokens = %d, %v; want 21, nil", n, err)
	}
	if _, exists := got.body["max_tokens"]; exists {
		t.Fatalf("count body must not include max_tokens: %v", got.body)
	}
	toolChoice, _ := got.body["tool_choice"].(map[string]any)
	if gotType, _ := toolChoice["type"].(string); gotType != "auto" {
		t.Fatalf("count body tool_choice type = %q, want auto: %v", gotType, got.body)
	}
}

// TestProtocolCountTokensKeepsEffortBudgetUnclamped pins the count-tokens
// counterpart of the completion clamp: the clamp exists to satisfy the
// completion contract that max_tokens strictly exceeds budget_tokens, which
// as ReasoningBudget produced it.
func TestProtocolCountTokensKeepsEffortBudgetUnclamped(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return http.StatusOK, `{"input_tokens":21}` })
	res := protoLive(srv)
	res.Caps.ThinkingShape = new("budget")
	req := protoReq("high") // a 32768-token table budget over the 8192 admitted
	req.MaxTokens = new(8192)
	n, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), req, res)
	if err != nil || n != 21 {
		t.Fatalf("CountTokens = %d, %v; want 21, nil", n, err)
	}
	thinking, _ := got.body["thinking"].(map[string]any)
	if budget := intFromAny(thinking["budget_tokens"]); budget != llm.ReasoningBudget("high") {
		t.Fatalf("count body budget_tokens = %d, want the unclamped effort budget %d", budget, llm.ReasoningBudget("high"))
	}
}

// TestProtocolEndpointFamilies pins the api-log endpoint_family of each
// operation; count_tokens and models must not inherit anthropic_messages.
func TestProtocolEndpointFamilies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
		run  func(p *Protocol, ctx context.Context, res registry.Resolved) error
	}{
		{"messages", messagesJSON, "anthropic_messages", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.Complete(ctx, protoReq(""), res)
			return err
		}},
		{"count_tokens", `{"input_tokens":21}`, "anthropic_count_tokens", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.CountTokens(ctx, protoReq(""), res)
			return err
		}},
		{"models", `{"data":[{"id":"m1"}],"has_more":false,"last_id":"m1"}`, "anthropic_models", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.ListModels(ctx, res)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, tc.body })
			sink := &captureSink{}
			ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_"+tc.name)), sink)
			if err := tc.run(&Protocol{Client: srv.Client()}, ctx, protoLive(srv)); err != nil {
				t.Fatal(err)
			}
			llm.WaitForPriorAPIAttempts(ctx)
			if got := sink.families(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("endpoint families = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestProtocolClassifiesPromptTooLong(t *testing.T) {
	srv, _ := protoServer(t, func(*http.Request) (int, string) {
		return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`
	})
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	if llm.Kind(err) != llm.KindContextLength || llm.ErrorProtocol(err) != registry.ProtocolAnthropic {
		t.Fatalf("err = %v", err)
	}
}

// TestProtocolIncompleteStreamNamesTheInstance pins that a stream that ends
// before message_stop reports the instance, not the protocol's own name.
func TestProtocolIncompleteStreamNamesTheInstance(t *testing.T) {
	truncated := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x-wire\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n"
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, truncated })
	res := protoLive(srv)
	st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventError {
			streamErr = ev.Err
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), res.Instance+" stream ended without completion") {
		t.Fatalf("incomplete stream error = %v, want it to name %q", streamErr, res.Instance)
	}
}
