package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/providertest"
	"primeradiant.com/evener/llm/registry"
)

const streamingRequiredScheme = "google-test-streaming-required"

func TestCompleteRoutesThroughStreamWhenPreparerRequiresIt(t *testing.T) {
	providertest.RegisterStreamingRequiredAuth(t, streamingRequiredScheme, providertest.StreamingRequiredAuth{})
	srv, _ := protoServer(t, 200, interactionsSSE)
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

type callScopedMutatingPreparer struct{}

func (callScopedMutatingPreparer) Apply(context.Context, *http.Request, registry.Resolved) error {
	return nil
}
func (callScopedMutatingPreparer) PrepareRequest(_ context.Context, _ *http.Request, body map[string]any, _ llm.Request, _ registry.Resolved) error {
	body["input"] = "tampered"
	return nil
}
func (callScopedMutatingPreparer) RequiresStreamingComplete() bool { return false }

func TestFinalizeBodyLocksAndCallScopedPreparerSnapshot(t *testing.T) {
	t.Parallel()
	t.Run("protected Transport.Body keys rejected before HTTP", func(t *testing.T) {
		for _, k := range []string{"input", "model", "tools.0", "response_format", "response_format.mime_type", "agent", "previous_interaction_id", "store", "continuation_token", "cached_content", "background", "generationConfig.temperature"} {
			res := protoRes(nil)
			res.Transport.Body = map[string]any{k: "x"}
			if _, err := (&Protocol{}).BuildBody(protoReq(""), res); err == nil {
				t.Errorf("key %q in Transport.Body must fail BuildBody", k)
			}
		}
	})

	t.Run("legacy generateContent endpoint override rejected", func(t *testing.T) {
		for _, ep := range []string{"/models/{model}:generateContent", "/models/{model}:streamGenerateContent?alt=sse"} {
			res := protoRes(nil)
			res.Transport.Endpoint = ep
			if _, err := (&Protocol{}).BuildBody(protoReq(""), res); err == nil {
				t.Errorf("endpoint %q must fail BuildBody", ep)
			}
		}
	})

	t.Run("unsupported Vertex Gemini model fails fast", func(t *testing.T) {
		res := protoRes(nil)
		res.Transport.Preset = registry.PresetVertexGemini
		res.WireID = "gemini-2.5-flash"
		if _, err := (&Protocol{}).BuildBody(protoReq(""), res); err == nil {
			t.Fatal("Vertex gemini-2.5-flash must fail BuildBody")
		}
	})

	t.Run("Transport.Body labels invalid and service_tier deferred rejected", func(t *testing.T) {
		srv, _ := protoServer(t, 200, interactionsJSON)
		res := protoLive(srv)
		res.Transport.Body = map[string]any{"labels.bad/key": "v"}
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res); err == nil {
			t.Fatal("invalid labels.* in Transport.Body must fail")
		}

		res2 := protoLive(srv)
		res2.Transport.Body = map[string]any{"service_tier": "deferred"}
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res2); err == nil {
			t.Fatal("service_tier=deferred in Transport.Body must fail")
		}
	})

	t.Run("Transport.Body reasoning=false strips thinking controls and respects max_output_tokens=false", func(t *testing.T) {
		srv, got := protoServer(t, 200, interactionsJSON)
		res := protoLive(srv)
		res.Caps.Reasoning = new(false)
		res.Caps.Fields["generation_config.max_output_tokens"] = false
		res.Transport.Body = map[string]any{
			"generation_config.thinking_level":     "high",
			"generation_config.thinking_summaries": "auto",
			"generation_config.max_output_tokens":  500,
			"generation_config.seed":               7,
		}
		req := protoReq("high")
		req.MaxTokens = new(100)
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res); err != nil {
			t.Fatal(err)
		}
		gen := got.body["generation_config"].(map[string]any)
		if _, has := gen["thinking_level"]; has {
			t.Fatalf("reasoning=false must strip thinking_level: %v", gen)
		}
		if _, has := gen["thinking_summaries"]; has {
			t.Fatalf("reasoning=false must strip thinking_summaries: %v", gen)
		}
		if _, has := gen["max_output_tokens"]; has {
			t.Fatalf("fields.generation_config.max_output_tokens=false must strip max_output_tokens: %v", gen)
		}
		if gen["seed"] != float64(7) {
			t.Fatalf("sibling seed must survive: %v", gen)
		}
	})

	t.Run("call-scoped RequestPreparer mutating input is rejected", func(t *testing.T) {
		srv, _ := protoServer(t, 200, interactionsJSON)
		res := protoLive(srv)
		res.Transport.Auth = registry.AuthOAuthOpenAICodex
		ctx := llm.WithAuthenticatorOverride(context.Background(), callScopedMutatingPreparer{})
		if _, err := (&Protocol{Client: srv.Client()}).Complete(ctx, protoReq(""), res); err == nil {
			t.Fatal("call-scoped RequestPreparer mutating input must fail FinalizeBody snapshot lock")
		}
	})
}

func TestLegacyUnsignedToolCall400FallbackCompleteAndStream(t *testing.T) {
	t.Parallel()
	legacyReq := llm.Request{
		Model: "gemini-3.8-flash",
		Messages: []llm.Message{
			llm.User("check files"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go"}`),
					ThoughtSignature: "legacy_sig", GoogleInteraction: false,
				}},
			}},
			llm.ToolResult("c1", "main.go", false),
			llm.User("summarize"),
		},
	}

	t.Run("Complete retries once with structured text blocks", func(t *testing.T) {
		var attempts int32
		var secondBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&attempts, 1)
			raw, _ := io.ReadAll(r.Body)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Function call is missing a thought_signature.","status":"INVALID_ARGUMENT"}}`))
				return
			}
			_ = json.Unmarshal(raw, &secondBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(interactionsJSON))
		}))
		defer srv.Close()

		resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), legacyReq, protoLive(srv))
		if err != nil {
			t.Fatalf("Complete fallback failed: %v", err)
		}
		if resp.Text() != "hello" || atomic.LoadInt32(&attempts) != 2 {
			t.Fatalf("attempts=%d text=%q", attempts, resp.Text())
		}
		for _, rawStep := range secondBody["input"].([]any) {
			st := rawStep.(map[string]any)
			if st["type"] == "function_call" || st["type"] == "function_result" {
				t.Fatalf("fallback request still contained raw function_call/function_result: %#v", secondBody["input"])
			}
		}
	})

	t.Run("Stream pre-2xx HTTP 400 retries once with single StreamEventStreamStart", func(t *testing.T) {
		var attempts int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&attempts, 1)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Function call is missing a thought_signature.","status":"INVALID_ARGUMENT"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(interactionsSSE))
		}))
		defer srv.Close()

		st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), legacyReq, protoLive(srv))
		if err != nil {
			t.Fatalf("Stream fallback failed: %v", err)
		}
		var starts int
		var finalText string
		for ev := range st.Events() {
			if ev.Type == llm.StreamEventStreamStart {
				starts++
			}
			if ev.Type == llm.StreamEventError {
				t.Fatalf("unexpected stream error: %v", ev.Err)
			}
			if ev.Type == llm.StreamEventFinish && ev.Response != nil {
				finalText = ev.Response.Text()
			}
		}
		if starts != 1 || finalText != "hello" || atomic.LoadInt32(&attempts) != 2 {
			t.Fatalf("starts=%d text=%q attempts=%d", starts, finalText, attempts)
		}
	})

	t.Run("Stream post-2xx in-band SSE error never retries internally", func(t *testing.T) {
		var attempts int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: error\ndata: {\"event_type\":\"error\",\"error\":{\"code\":\"invalid_argument\",\"message\":\"missing thought_signature\"}}\n\n"))
		}))
		defer srv.Close()

		st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), legacyReq, protoLive(srv))
		if err != nil {
			t.Fatalf("Stream open failed: %v", err)
		}
		var streamErr error
		for ev := range st.Events() {
			if ev.Type == llm.StreamEventError {
				streamErr = ev.Err
			}
		}
		if streamErr == nil || atomic.LoadInt32(&attempts) != 1 {
			t.Fatalf("expected error without internal retry on post-2xx SSE; err=%v attempts=%d", streamErr, attempts)
		}
	})

	t.Run("Stream after output event never retries internally", func(t *testing.T) {
		var attempts int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n" +
					"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n" +
					"event: error\ndata: {\"event_type\":\"error\",\"error\":{\"code\":\"invalid_argument\",\"message\":\"missing thought_signature\"}}\n\n",
			))
		}))
		defer srv.Close()

		st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), legacyReq, protoLive(srv))
		if err != nil {
			t.Fatalf("Stream open failed: %v", err)
		}
		var streamErr error
		for ev := range st.Events() {
			if ev.Type == llm.StreamEventError {
				streamErr = ev.Err
			}
		}
		if streamErr == nil || atomic.LoadInt32(&attempts) != 1 {
			t.Fatalf("expected error without internal retry after output; err=%v attempts=%d", streamErr, attempts)
		}
	})
}

func TestBuildBodyErrorsPropagateThroughCompleteStreamCountTokens(t *testing.T) {
	t.Parallel()
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
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
				t.Fatalf("BuildBody failure must surface as a configuration error, got %T %v", err, err)
			}
		})
	}
}

func TestEffectiveToolsReplacementCombinedSearchAndFunctionsPolicy(t *testing.T) {
	t.Parallel()

	combinedRes := func() registry.Resolved {
		return protoRes(func(c *registry.Caps) {
			c.CombinedSearchAndFunctions = new(true)
			c.Fields["previous_interaction_id"] = true
		})
	}

	combinedSlice := []map[string]any{
		{"type": "google_search"},
		{"type": "function", "name": "fn", "description": "d", "parameters": map[string]any{"type": "object"}},
	}
	combinedAny := []any{
		map[string]any{"type": "google_search"},
		map[string]any{"type": "function", "name": "fn", "description": "d", "parameters": map[string]any{"type": "object"}},
	}
	combinedRaw := json.RawMessage(`[{"type":"google_search"},{"type":"function","name":"fn","description":"d","parameters":{"type":"object"}}]`)
	funcOnly := []map[string]any{
		{"type": "function", "name": "fn", "description": "d", "parameters": map[string]any{"type": "object"}},
	}

	t.Run("combined replacement with default store=false fails before HTTP", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			modReq func(*llm.Request)
			modRes func(*registry.Resolved)
		}{
			{
				name: "ProviderOptions []map[string]any",
				modReq: func(r *llm.Request) {
					r.ProviderOptions = map[string]any{"google": map[string]any{"tools": combinedSlice}}
				},
			},
			{
				name: "ProviderOptions []any",
				modReq: func(r *llm.Request) {
					r.ProviderOptions = map[string]any{"google": map[string]any{"tools": combinedAny}}
				},
			},
			{
				name: "ProviderOptions json.RawMessage",
				modReq: func(r *llm.Request) {
					r.ProviderOptions = map[string]any{"google": map[string]any{"tools": combinedRaw}}
				},
			},
			{
				name: "ProviderOptions []map[string]any with json.RawMessage type",
				modReq: func(r *llm.Request) {
					r.ProviderOptions = map[string]any{"google": map[string]any{"tools": []map[string]any{
						{"type": json.RawMessage(`"google_search"`)},
						{"type": "function", "name": "fn", "description": "d", "parameters": map[string]any{"type": "object"}},
					}}}
				},
			},
			{
				name: "Transport.Body []map[string]any with json.RawMessage type",
				modRes: func(res *registry.Resolved) {
					res.Transport.Body = map[string]any{"tools": []map[string]any{
						{"type": json.RawMessage(`"google_search"`)},
						{"type": "function", "name": "fn", "description": "d", "parameters": map[string]any{"type": "object"}},
					}}
				},
			},
			{
				name:   "Transport.Body tools",
				modRes: func(res *registry.Resolved) { res.Transport.Body = map[string]any{"tools": combinedSlice} },
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(interactionsJSON))
				}))
				defer srv.Close()

				res := protoLive(srv)
				res.Caps.CombinedSearchAndFunctions = new(true)
				res.Caps.Fields["previous_interaction_id"] = true
				if tc.modRes != nil {
					tc.modRes(&res)
				}
				req := protoReq("")
				if tc.modReq != nil {
					tc.modReq(&req)
				}
				_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res)
				if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != 0 {
					t.Fatalf("expected pre-HTTP ConfigurationError (hits=0), got err=%T %v hits=%d", err, err, hits.Load())
				}
			})
		}
	})

	t.Run("stateful combined replacement defaults tool_choice=validated and rejects invalid caps or auto", func(t *testing.T) {
		srv, got := protoServer(t, http.StatusOK, interactionsJSON)
		res := protoLive(srv)
		res.Caps.CombinedSearchAndFunctions = new(true)
		res.Caps.Fields["previous_interaction_id"] = true

		req := protoReq("")
		req.Store = new(true)
		req.ProviderOptions = map[string]any{"google": map[string]any{"tools": combinedRaw}}
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res); err != nil {
			t.Fatalf("stateful combined replacement failed: %v", err)
		}
		if got.body["store"] != true {
			t.Fatalf("wire store = %v, want true", got.body["store"])
		}
		if genCfg, _ := got.body["generation_config"].(map[string]any); genCfg["tool_choice"] != "validated" {
			t.Fatalf("wire generation_config.tool_choice = %v, want validated", genCfg["tool_choice"])
		}

		// Explicit generation_config.tool_choice = "auto" rejected.
		reqAuto := protoReq("")
		reqAuto.Store = new(true)
		reqAuto.ProviderOptions = map[string]any{"google": map[string]any{
			"tools":             combinedSlice,
			"generation_config": map[string]any{"tool_choice": "auto"},
		}}
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), reqAuto, res); err == nil {
			t.Fatal("expected explicit generation_config.tool_choice=auto with combined replacement to fail")
		}

		// json.RawMessage "auto" and Transport.Body typed map[string]string generation_config with "auto" rejected.
		reqAutoRaw := protoReq("")
		reqAutoRaw.Store = new(true)
		reqAutoRaw.ProviderOptions = map[string]any{"google": map[string]any{
			"tools":             combinedSlice,
			"generation_config": map[string]any{"tool_choice": json.RawMessage(`"auto"`)},
		}}
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), reqAutoRaw, res); err == nil {
			t.Fatal("expected json.RawMessage generation_config.tool_choice=auto with combined replacement to fail")
		}
		reqStoreCombined := protoReq("")
		reqStoreCombined.Store = new(true)
		reqStoreCombined.ProviderOptions = map[string]any{"google": map[string]any{"tools": combinedSlice}}
		resTypedGen := protoLive(srv)
		resTypedGen.Caps.CombinedSearchAndFunctions = new(true)
		resTypedGen.Caps.Fields["previous_interaction_id"] = true
		resTypedGen.Transport.Body = map[string]any{"generation_config": map[string]string{"tool_choice": "auto"}}
		if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), reqStoreCombined, resTypedGen); err == nil {
			t.Fatal("expected Transport.Body map[string]string generation_config tool_choice=auto to fail")
		}

		// CombinedSearchAndFunctions=false rejected (e.g. gemini-2.5-flash).
		noCombRes := combinedRes()
		noCombRes.Caps.CombinedSearchAndFunctions = new(false)
		if _, err := (&Protocol{}).BuildBody(req, noCombRes); err == nil {
			t.Fatal("expected CombinedSearchAndFunctions=false to reject combined replacement")
		}

		// Tools=false rejected (e.g. gemini-3-pro-image).
		noToolsRes := combinedRes()
		noToolsRes.Caps.Tools = new(false)
		if _, err := (&Protocol{}).BuildBody(req, noToolsRes); err == nil {
			t.Fatal("expected Tools=false to reject function tool replacement")
		}
	})

	t.Run("function-only replacement overrides req.WebSearch=true with store=false and tool_choice=auto", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			modReq func(*llm.Request)
			modRes func(*registry.Resolved)
		}{
			{
				name:   "ProviderOptions tools",
				modReq: func(r *llm.Request) { r.ProviderOptions = map[string]any{"google": map[string]any{"tools": funcOnly}} },
			},
			{
				name:   "Transport.Body tools",
				modRes: func(res *registry.Resolved) { res.Transport.Body = map[string]any{"tools": funcOnly} },
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				srv, got := protoServer(t, http.StatusOK, interactionsJSON)
				res := protoLive(srv)
				res.Caps.WebSearch = new(true)
				res.Caps.CombinedSearchAndFunctions = new(true)
				res.Caps.Fields["previous_interaction_id"] = true
				if tc.modRes != nil {
					tc.modRes(&res)
				}
				req := protoReq("")
				req.WebSearch = true
				req.Tools = []llm.ToolDefinition{{Name: "orig", Parameters: map[string]any{"type": "object"}}}
				req.ToolChoice = &llm.ToolChoice{Mode: "auto"}
				if tc.modReq != nil {
					tc.modReq(&req)
				}
				if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res); err != nil {
					t.Fatalf("function-only replacement failed: %v", err)
				}
				if got.body["store"] != false {
					t.Fatalf("wire store = %v, want false", got.body["store"])
				}
				if genCfg, _ := got.body["generation_config"].(map[string]any); genCfg["tool_choice"] != "auto" {
					t.Fatalf("wire generation_config.tool_choice = %v, want auto", genCfg["tool_choice"])
				}
			})
		}
	})
}

func TestListModelsEndpointWithExistingQuery(t *testing.T) {
	t.Parallel()
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

func TestVertexListingURLAndDecodeErrors(t *testing.T) {
	t.Parallel()
	srv, _ := protoServer(t, 200, `{"models":[]}`)
	vertexRes := func() registry.Resolved {
		res := protoLive(srv)
		res.Transport.Preset = registry.PresetVertexGemini
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
