package google

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

const generateJSON = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`

const generateSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}]}\n\n" +
	"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7}}\n\n"

type protoCapture struct {
	path   string
	header http.Header
	body   map[string]any
}

func protoServer(t *testing.T, status int, body string) (*httptest.Server, *protoCapture) {
	t.Helper()
	got := &protoCapture{}
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

func protoLive(srv *httptest.Server) registry.Resolved {
	res := protoRes(nil)
	res.WireID = "gemini-2.5-flash"
	res.Transport = registry.Transport{Auth: registry.AuthHeader, AuthHeader: "x-goog-api-key", BaseURL: srv.URL + "/v1beta", Endpoint: "/models/{model}:generateContent", StreamEndpoint: "/models/{model}:streamGenerateContent?alt=sse", ModelsEndpoint: "/models", CountTokensEndpoint: "/models/{model}:countTokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	return res
}

func TestProtocolCompleteUsesHeaderAuthAndModelInPath(t *testing.T) {
	srv, got := protoServer(t, 200, generateJSON)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "gemini-prod" || resp.Text() != "hello" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	if got.path != "/v1beta/models/gemini-2.5-flash:generateContent" || got.header.Get("x-goog-api-key") != "k-1" || strings.Contains(got.path, "key=") {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
}

func TestProtocolStreamUsageOnFinishChunk(t *testing.T) {
	srv, got := protoServer(t, 200, generateSSE)
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
	if final == nil || final.Provider != "gemini-prod" || final.Text() != "hello" || final.Usage.TotalTokens != 7 {
		t.Fatalf("final = %+v", final)
	}
	if got.path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" {
		t.Fatalf("path = %s", got.path)
	}
}

func TestProtocolReclassifiesGRPCStatus(t *testing.T) {
	srv, _ := protoServer(t, 400, `{"error":{"code":400,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	le, ok := errors.AsType[llm.Error](err)
	if llm.Kind(err) != llm.KindRateLimit || !ok || le.Provider() != "gemini-prod" {
		t.Fatalf("RESOURCE_EXHAUSTED on 400 must reclassify to a rate limit stamped with the instance: %v", err)
	}
}

func TestProtocolListModelsAndCountTokens(t *testing.T) {
	srv, got := protoServer(t, 200, `{"models":[{"name":"models/gemini-2.5-flash","inputTokenLimit":922000,"outputTokenLimit":128000,"supportedGenerationMethods":["generateContent"]},{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
	res := protoLive(srv)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil || len(rows) != 1 || rows[0].ID != "gemini-2.5-flash" || rows[0].Caps.ContextWindow != nil || *rows[0].Caps.MaxInputTokens != 922000 || *rows[0].Caps.MaxOutputTokens != 128000 {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if got.path != "/v1beta/models?pageSize=1000" || got.header.Get("x-goog-api-key") != "k-1" {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
	srv2, got2 := protoServer(t, 200, `{"totalTokens":13}`)
	n, err := (&Protocol{Client: srv2.Client()}).CountTokens(context.Background(), protoReq(""), protoLive(srv2))
	if err != nil || n != 13 || got2.path != "/v1beta/models/gemini-2.5-flash:countTokens" {
		t.Fatalf("count = %d err = %v path = %s", n, err, got2.path)
	}
	if gcr := got2.body["generateContentRequest"].(map[string]any); gcr["model"] != "models/gemini-2.5-flash" || gcr["contents"] == nil {
		t.Fatalf("count body = %v", got2.body)
	}
}

// TestProtocolEndpointFamilies pins the api-log endpoint_family of each
// operation; the stream must not report the non-streaming family.
func TestProtocolEndpointFamilies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
		run  func(p *Protocol, ctx context.Context, res registry.Resolved) error
	}{
		{"generate_content", generateJSON, "google_generate_content", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.Complete(ctx, protoReq(""), res)
			return err
		}},
		{"stream_generate_content", generateSSE, "google_stream_generate_content", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			s, err := p.Stream(ctx, protoReq(""), res)
			if err != nil {
				return err
			}
			for ev := range s.Events() {
				if ev.Type == llm.StreamEventError {
					return ev.Err
				}
			}
			return nil
		}},
		{"count_tokens", `{"totalTokens":13}`, "google_count_tokens", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.CountTokens(ctx, protoReq(""), res)
			return err
		}},
		{"models", `{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]}]}`, "google_models", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.ListModels(ctx, res)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := protoServer(t, 200, tc.body)
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

// TestProtocolIncompleteStreamNamesTheInstance pins that a stream that ends
// before a finishReason reports the instance, not the protocol's own name.
func TestProtocolIncompleteStreamNamesTheInstance(t *testing.T) {
	truncated := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}]}\n\n"
	srv, _ := protoServer(t, 200, truncated)
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
