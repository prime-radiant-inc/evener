package google

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// streamingRequiredAuth is a stub authenticator whose transport answers every
// request as a stream: it is what routes Complete through CompleteViaStream
// (protocolhttp.RequiresStreamingComplete). Registered under a test-only scheme
// so the google transport can be driven through the streaming-complete path
// without a real Codex backend.
type streamingRequiredAuth struct{}

func (streamingRequiredAuth) Apply(context.Context, *http.Request, registry.Resolved) error {
	return nil
}
func (streamingRequiredAuth) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (streamingRequiredAuth) RequiresStreamingComplete() bool { return true }

const streamingRequiredScheme = "google-test-streaming-required"

var registerStreamingRequiredOnce sync.Once

func registerStreamingRequiredAuth(t *testing.T) {
	t.Helper()
	registerStreamingRequiredOnce.Do(func() {
		llm.RegisterAuthenticator(streamingRequiredScheme, streamingRequiredAuth{})
	})
}

// A transport whose auth scheme requires streaming completion must serve
// Complete from the SSE stream and still return the accumulated response.
func TestCompleteRoutesThroughStreamWhenPreparerRequiresIt(t *testing.T) {
	registerStreamingRequiredAuth(t)
	srv, _ := protoServer(t, 200, generateSSE)
	res := protoLive(srv)
	res.Transport.Auth = streamingRequiredScheme
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hello" || resp.Provider != "gemini-prod" {
		t.Fatalf("streamed Complete = %+v", resp)
	}
}

// A request BuildBody rejects (named tool_choice with no name) must fail
// Complete, Stream, and CountTokens before any HTTP traffic, as a
// configuration error stamped with the instance.
func TestBuildBodyErrorsPropagateThroughCompleteStreamCountTokens(t *testing.T) {
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "named"}
	res := protoRes(nil)
	for name, run := range map[string]func() error{
		"complete": func() error {
			_, err := (&Protocol{}).Complete(context.Background(), req, res)
			return err
		},
		"stream": func() error {
			_, err := (&Protocol{}).Stream(context.Background(), req, res)
			return err
		},
		"count_tokens": func() error {
			_, err := (&Protocol{}).CountTokens(context.Background(), req, res)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
				t.Fatalf("BuildBody failure must surface as a configuration error, got %T %v", err, err)
			}
		})
	}
}

// A models endpoint that already carries a query must append pageSize with &
// rather than a second question mark.
func TestListModelsEndpointWithExistingQuery(t *testing.T) {
	srv, got := protoServer(t, 200, `{"models":[]}`)
	res := protoLive(srv)
	res.Transport.ModelsEndpoint = "/models?key=x"
	if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1beta/models?key=x&pageSize=1000" {
		t.Fatalf("wire path = %s", got.path)
	}
}

func TestCountTokensUnsupportedEndpoint(t *testing.T) {
	res := protoRes(nil)
	res.Transport.CountTokensEndpoint = registry.EndpointUnsupported
	_, err := (&Protocol{}).CountTokens(context.Background(), protoReq(""), res)
	if !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
		t.Fatalf("err = %v, want ErrInputTokenCountUnsupported", err)
	}
}

// A 404 whose body does not name a publisher model is not the regional-model
// shape, so the remedy must not rewrite it: it stays the provider's own 404.
func TestRegionalVertex404WithoutPublisherPhraseStaysProviderError(t *testing.T) {
	srv, _ := protoServer(t, http.StatusNotFound, `{"error":{"code":404,"message":"Requested entity was not found."}}`)
	res := vertexAuthorityRes(t, srv, "us-central1")
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if _, ok := errors.AsType[*llm.ConfigurationError](err); ok {
		t.Fatalf("a 404 without the publisher-model phrase must not claim the regional remedy: %v", err)
	}
	le, ok := errors.AsType[llm.Error](err)
	if !ok || le.StatusCode() != http.StatusNotFound {
		t.Fatalf("the provider's 404 must survive: %v", err)
	}
}

// Transport body constants run after the builder, so a generationConfig
// constant that is not an object must fail the completion loudly, and a nil
// constant must leave the finalizer rebuilding an empty generationConfig that
// still receives the output budget.
func TestTransportGenerationConfigConstantTypes(t *testing.T) {
	t.Run("non-object fails", func(t *testing.T) {
		srv, _ := protoServer(t, 200, generateJSON)
		res := protoLive(srv)
		res.Transport.Body = map[string]any{"generationConfig": "not-an-object"}
		req := protoReq("")
		req.MaxTokens = new(100)
		_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res)
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("a non-object generationConfig constant must fail as a configuration error, got %T %v", err, err)
		}
	})
	t.Run("nil is rebuilt with the output budget", func(t *testing.T) {
		srv, got := protoServer(t, 200, generateJSON)
		res := protoLive(srv)
		res.Transport.Body = map[string]any{"generationConfig": nil}
		req := protoReq("")
		req.MaxTokens = new(100)
		resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Text() != "hello" {
			t.Fatalf("resp = %+v", resp)
		}
		gen, _ := got.body["generationConfig"].(map[string]any)
		if gen == nil || gen["maxOutputTokens"] != float64(100) {
			t.Fatalf("a nil generationConfig constant must be rebuilt with the output budget, body = %v", got.body)
		}
	})
}

// The Vertex publisher-model listing composes its URL from the base URL's
// scheme and host: a base URL with neither must fail listing, and a page that
// is not JSON must fail too.
func TestVertexListingURLAndDecodeErrors(t *testing.T) {
	srv, _ := protoServer(t, 200, `{"models":[]}`)
	vertexRes := func() registry.Resolved {
		res := protoLive(srv)
		res.Transport.Endpoint = "/publishers/google/models/{model}:generateContent"
		res.Transport.StreamEndpoint = "/publishers/google/models/{model}:streamGenerateContent?alt=sse"
		res.Transport.ModelsEndpoint = "/publisherModels"
		return res
	}

	res := vertexRes()
	res.Transport.BaseURL = "no-scheme-or-host"
	if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res); err == nil {
		t.Fatal("a vertex base URL without scheme or host must fail models.list")
	}

	srv2, _ := protoServer(t, 200, `not-json`)
	res2 := vertexRes()
	res2.Transport.BaseURL = srv2.URL + "/v1beta"
	if _, err := (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), res2); err == nil {
		t.Fatal("a non-JSON publisher-model page must fail models.list")
	}
}

// classifyGeminiError maps the ambiguous 400 to the HTTP status whose error
// class the gRPC status names, and keeps the provider's own 400 for any status
// outside the remapped set.
func TestClassifyGeminiStatusRemapArms(t *testing.T) {
	for _, tc := range []struct {
		grpc string
		want int
	}{
		{"DEADLINE_EXCEEDED", http.StatusRequestTimeout},
		{"UNAVAILABLE", http.StatusServiceUnavailable},
		{"INTERNAL", http.StatusServiceUnavailable},
		{"INVALID_ARGUMENT", http.StatusBadRequest},
	} {
		t.Run(tc.grpc, func(t *testing.T) {
			body := `{"error":{"code":400,"message":"m","status":"` + tc.grpc + `"}}`
			srv, _ := protoServer(t, http.StatusBadRequest, body)
			_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
			le, ok := errors.AsType[llm.Error](err)
			if !ok {
				t.Fatalf("err = %v, want a typed llm.Error", err)
			}
			if le.StatusCode() != tc.want {
				t.Fatalf("gRPC %q on a 400 must reclassify to status %d, got %d (%v)", tc.grpc, tc.want, le.StatusCode(), err)
			}
		})
	}
}

func TestTokenCountIntValueShapes(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int
	}{
		{7, 7},
		{int64(9), 9},
		{1.9, 1},
		{json.Number("42"), 42},
		{"not-a-number", 0},
	} {
		if got := tokenCountInt(tc.in); got != tc.want {
			t.Fatalf("tokenCountInt(%#v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestFromGeminiResponseDecoderArms pins the non-streaming decoder's fallback
// thought-signature lookups, its skip of non-object parts, and the
// groundingMetadata web-search projection (with non-string queries filtered).
func TestFromGeminiResponseDecoderArms(t *testing.T) {
	fcRaw := func(partExtra string) map[string]any {
		part := map[string]any{"functionCall": map[string]any{"name": "t", "args": map[string]any{}}}
		if partExtra != "" {
			var extra map[string]any
			if err := json.Unmarshal([]byte(partExtra), &extra); err != nil {
				t.Fatalf("test fixture: %v", err)
			}
			for k, v := range extra {
				part[k] = v
			}
		}
		return map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{part}},
		}}}
	}

	t.Run("thought signature fallbacks", func(t *testing.T) {
		for name, tc := range map[string]struct {
			part string
			want string
		}{
			"part thoughtSignature":  {`{"thoughtSignature":"psig"}`, "psig"},
			"part thought_signature": {`{"thought_signature":"psnake"}`, "psnake"},
			"fc thoughtSignature":    {`{"functionCall":{"thoughtSignature":"fsig"}}`, "fsig"},
			"fc thought_signature":   {`{"functionCall":{"thought_signature":"fsnake"}}`, "fsnake"},
		} {
			t.Run(name, func(t *testing.T) {
				r := fromGeminiResponse(fcRaw(tc.part), "m")
				calls := r.ToolCalls()
				if len(calls) != 1 || calls[0].ThoughtSignature != tc.want {
					t.Fatalf("ThoughtSignature = %v, want %q (%+v)", calls, tc.want, r.Message)
				}
			})
		}
	})

	t.Run("non-object part skipped", func(t *testing.T) {
		raw := map[string]any{"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{"nope", map[string]any{"text": "hi"}}},
		}}}
		r := fromGeminiResponse(raw, "m")
		if len(r.Message.Content) != 1 || r.Message.Content[0].Text != "hi" {
			t.Fatalf("content = %#v, want the single decodable text part", r.Message.Content)
		}
	})

	t.Run("grounding metadata web search", func(t *testing.T) {
		raw := map[string]any{"candidates": []any{map[string]any{
			"groundingMetadata": map[string]any{"webSearchQueries": []any{"q1", 42, "q2"}},
		}}}
		r := fromGeminiResponse(raw, "m")
		var web *llm.WebSearchData
		for _, p := range r.Message.Content {
			if p.Kind == llm.ContentWebSearch {
				web = p.WebSearch
			}
		}
		if web == nil {
			t.Fatalf("groundingMetadata must surface a web-search part: %#v", r.Message.Content)
		}
		if web.Query != "q1; q2" {
			t.Fatalf("Query = %q, want non-string queries filtered and the rest joined", web.Query)
		}
		if len(web.Raw) == 0 {
			t.Fatal("the raw groundingMetadata must ride along")
		}
	})
}
