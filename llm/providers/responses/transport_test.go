package responses

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
	"primeradiant.com/evener/llm/registry"
)

const responseJSON = `{"id":"resp_1","status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"f","arguments":"{\"a\":1}"}],"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":16}}`

const responseSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hel\"}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"lo\"}\n\n" +
	"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"

type capturedRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

func server(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.header = r.URL.RequestURI(), r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		if strings.HasPrefix(body, "event:") {
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
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/responses", StreamEndpoint: "/responses", ModelsEndpoint: "/models", CountTokensEndpoint: "/responses/input_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	return res
}

// streamingPreparer stands in for the Codex transport: RequiresStreamingComplete.
type streamingPreparer struct{}

func (streamingPreparer) Apply(_ context.Context, req *http.Request, _ registry.Resolved) error {
	req.Header.Set("Authorization", "Bearer codex-token")
	return nil
}
func (streamingPreparer) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (streamingPreparer) RequiresStreamingComplete() bool { return true }

var registerOnce sync.Once

func TestCompleteDecodesOutputItems(t *testing.T) {
	srv, got := server(t, 200, responseJSON)
	res := liveRes(srv, openaiCaps)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "openai" || resp.Text() != "hello" || len(resp.ToolCalls()) != 1 || resp.ToolCalls()[0].ID != "call_1" || resp.Finish.Reason != "tool_calls" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 4 || resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if got.path != "/v1/responses" || got.header.Get("Authorization") != "Bearer k-1" || got.body["store"] != false {
		t.Fatalf("wire: %s %v %v", got.path, got.header, got.body)
	}
}

func TestStreamEmitsDeltasAndFinish(t *testing.T) {
	srv, got := server(t, 200, responseSSE)
	res := liveRes(srv, openaiCaps)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	var final *llm.Response
	for ev := range s.Events() {
		switch ev.Type {
		case llm.StreamEventTextDelta:
			deltas = append(deltas, ev.Delta)
		case llm.StreamEventFinish:
			final = ev.Response
		case llm.StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if strings.Join(deltas, "") != "hello" || final == nil || final.Provider != "openai" || final.Text() != "hello" || final.Usage.TotalTokens != 5 {
		t.Fatalf("deltas = %v final = %+v", deltas, final)
	}
	if got.body["stream"] != true {
		t.Fatalf("stream flag: %v", got.body)
	}
}

func TestCompleteThroughStreamWhenTheTransportRequiresIt(t *testing.T) {
	registerOnce.Do(func() { llm.RegisterAuthenticator("test-streaming-codex", streamingPreparer{}) })
	srv, got := server(t, 200, responseSSE)
	res := liveRes(srv, codexLiteCaps)
	res.Transport.Auth = "test-streaming-codex"
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hello" || got.body["stream"] != true || got.header.Get("Authorization") != "Bearer codex-token" {
		t.Fatalf("complete via stream: %+v %v", resp, got.body)
	}
}

func TestCompleteClassifiesFailures(t *testing.T) {
	srv, _ := server(t, 400, `{"error":{"message":"Unknown parameter: 'store'.","type":"invalid_request_error","param":"store","code":"unknown_parameter"}}`)
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
	if llm.Kind(err) != llm.KindInvalidRequest || !strings.Contains(llm.ErrorHint(err), "fields.store = false") || llm.ErrorProtocol(err) != registry.ProtocolOpenAIResponses {
		t.Fatalf("err = %v", err)
	}
}

func TestCountTokens(t *testing.T) {
	srv, got := server(t, 200, `{"object":"response.input_tokens","input_tokens":42}`)
	res := liveRes(srv, openaiCaps)
	req := userReq("hi")
	req.MaxTokens = new(10)
	req.Metadata = map[string]string{"k": "v"}
	n, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), req, res)
	if err != nil || n != 42 {
		t.Fatalf("n = %d err = %v", n, err)
	}
	if got.path != "/v1/responses/input_tokens" {
		t.Fatalf("path = %s", got.path)
	}
	for _, k := range []string{"max_output_tokens", "metadata", "store", "temperature"} {
		if _, has := got.body[k]; has {
			t.Fatalf("%s must be stripped from the count body: %v", k, got.body)
		}
	}
	res.Transport.CountTokensEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), req, res); !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
