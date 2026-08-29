package anthropic

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
	if resp.Provider != "anthropic" || resp.Text() != "hello" || resp.Finish.Reason != "stop" || resp.Usage.InputTokens != 7 {
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
	if final == nil || final.Provider != "anthropic" || final.Text() != "hello" || final.Usage.OutputTokens != 3 || got.body["stream"] != true {
		t.Fatalf("final = %+v body = %v", final, got.body)
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

func TestProtocolClassifiesPromptTooLong(t *testing.T) {
	srv, _ := protoServer(t, func(*http.Request) (int, string) {
		return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`
	})
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	if llm.Kind(err) != llm.KindContextLength || llm.ErrorProtocol(err) != registry.ProtocolAnthropic {
		t.Fatalf("err = %v", err)
	}
}
