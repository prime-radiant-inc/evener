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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

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

const interactionsJSON = `{"id":"int_ok","model":"gemini-3.8-flash","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}],"usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}`

const interactionsSSE = "event: interaction.created\n" +
	`data: {"event_type":"interaction.created","interaction":{"id":"int_sse_ok","model":"gemini-3.8-flash","status":"in_progress"}}` + "\n\n" +
	"event: step.start\n" +
	`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}` + "\n\n" +
	"event: step.delta\n" +
	`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hel"}}` + "\n\n" +
	"event: step.delta\n" +
	`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"lo"}}` + "\n\n" +
	"event: step.stop\n" +
	`data: {"event_type":"step.stop","index":0}` + "\n\n" +
	"event: interaction.completed\n" +
	`data: {"event_type":"interaction.completed","interaction":{"id":"int_sse_ok","model":"gemini-3.8-flash","status":"completed","usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}}` + "\n\n" +
	"event: done\ndata: [DONE]\n\n"

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
		if strings.HasPrefix(body, "event:") || strings.HasPrefix(body, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func protoLive(srv *httptest.Server) registry.Resolved {
	res := protoRes(nil)
	res.WireID = "gemini-3.8-flash"
	res.Transport = registry.Transport{
		Auth:                registry.AuthHeader,
		AuthHeader:          "x-goog-api-key",
		BaseURL:             srv.URL + "/v1beta",
		Endpoint:            "/interactions",
		StreamEndpoint:      "/interactions",
		ModelsEndpoint:      "/models",
		CountTokensEndpoint: registry.EndpointUnsupported,
	}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	return res
}

func vertexAuthorityRes(t *testing.T, srv *httptest.Server, loc string) registry.Resolved {
	t.Helper()
	res := protoLive(srv)
	res.Instance = "google-vertex"
	res.WireID = "gemini-3.8-flash"
	res.Transport.Preset = registry.PresetVertexGemini
	res.Transport.HostRule = registry.HostRuleVertexLocation
	res.Transport.BaseURL = srv.URL + "/v1/projects/p/locations/" + loc
	res.Transport.Vars = map[string]string{"GOOGLE_VERTEX_LOCATION": loc, "GOOGLE_VERTEX_PROJECT": "p"}
	res.HostDerivedByRule = true
	return res
}

func TestProtocolCompleteUsesInteractionsEndpointAndStampsContinuationHash(t *testing.T) {
	t.Parallel()
	srv, got := protoServer(t, 200, interactionsJSON)
	hasher := llm.NewContinuationHasher([]byte("complete-hash-secret"))
	p := &Protocol{Client: srv.Client(), Hasher: hasher}
	resp, err := p.Complete(context.Background(), protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "gemini-prod" || resp.ID != "int_ok" || resp.Text() != "hello" || resp.Usage.TotalTokens != 7 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Raw["id_hash"] == nil || resp.Raw["id_hash"] == "" {
		t.Fatalf("expected StampContinuationIDHash id_hash on Complete resp.Raw, got %+v", resp.Raw)
	}
	if got.path != "/v1beta/interactions" || got.header.Get("x-goog-api-key") != "k-1" || got.body["model"] != "gemini-3.8-flash" || got.body["store"] != false {
		t.Fatalf("wire: path=%s body=%v", got.path, got.body)
	}
}

func TestProtocolStreamStampsContinuationHashAndUsesInteractionsSSE(t *testing.T) {
	t.Parallel()
	srv, got := protoServer(t, 200, interactionsSSE)
	hasher := llm.NewContinuationHasher([]byte("stream-hash-secret"))
	s, err := (&Protocol{Client: srv.Client(), Hasher: hasher}).Stream(context.Background(), protoReq(""), protoLive(srv))
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
	if final == nil || final.Provider != "gemini-prod" || final.ID != "int_sse_ok" || final.Text() != "hello" || final.Usage.TotalTokens != 7 {
		t.Fatalf("final = %+v", final)
	}
	if final.Raw["id_hash"] == nil || final.Raw["id_hash"] == "" {
		t.Fatalf("expected StampContinuationIDHash id_hash on Stream resp.Raw, got %+v", final.Raw)
	}
	if got.path != "/v1beta/interactions" || got.body["stream"] != true {
		t.Fatalf("path=%s stream=%v", got.path, got.body["stream"])
	}
}

func TestProtocolCompleteSettlementErrorCarriesResolvedInstance(t *testing.T) {
	t.Parallel()
	srv, _ := protoServer(t, 200, `{"id":"int_q","status":"queued"}`)
	res := vertexAuthorityRes(t, srv, "global")
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if !errors.Is(err, ErrPendingInteractionStatus) {
		t.Fatalf("err = %v, want ErrPendingInteractionStatus", err)
	}
	le, ok := errors.AsType[llm.Error](err)
	if !ok || le.Provider() != "google-vertex" {
		t.Fatalf("error provider = %v (ok=%v), want google-vertex", err, ok)
	}
}

func TestProtocolCompletionFinalizesOutputBudgetAfterTransportOverlay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		run  func(*Protocol, llm.Request, registry.Resolved) error
	}{
		{"complete", interactionsJSON, func(p *Protocol, req llm.Request, res registry.Resolved) error {
			_, err := p.Complete(context.Background(), req, res)
			return err
		}},
		{"stream", interactionsSSE, func(p *Protocol, req llm.Request, res registry.Resolved) error {
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
			srv, got := protoServer(t, http.StatusOK, tc.body)
			res := protoLive(srv)
			res.Transport.Body = map[string]any{"generation_config.max_output_tokens": 1000}
			req := protoReq("")
			req.MaxTokens = new(100)
			if err := tc.run(&Protocol{Client: srv.Client()}, req, res); err != nil {
				t.Fatal(err)
			}
			gen, _ := got.body["generation_config"].(map[string]any)
			if gen["max_output_tokens"] != float64(100) {
				t.Fatalf("wire max_output_tokens = %v, want admitted 100; body = %v", gen["max_output_tokens"], got.body)
			}
		})
	}
}

func TestProtocolCompleteStorePolicyBlocks2xxFailedSignatureRetry(t *testing.T) {
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

	const failed2xxSig400 = `{"id":"int_failed","model":"gemini-3.8-flash","status":"failed","errors":[{"code":"invalid_argument","message":"missing thought_signature"}]}`

	for _, tc := range []struct {
		name         string
		reqMod       func(*llm.Request)
		resMod       func(*registry.Resolved)
		pre2xx400    bool
		wantAttempts int32
		wantSuccess  bool
	}{
		{
			name:         "store=true blocks retry on HTTP 200 failed signature error",
			reqMod:       func(r *llm.Request) { r.Store = new(true) },
			wantAttempts: 1,
		},
		{
			name:         "fields.store=false (omitted store) blocks retry on HTTP 200 failed signature error",
			resMod:       func(res *registry.Resolved) { res.Caps.Fields = map[string]bool{"store": false} },
			wantAttempts: 1,
		},
		{
			name:         "store=true still recovers on pre-2xx HTTP 400 signature rejection",
			reqMod:       func(r *llm.Request) { r.Store = new(true) },
			pre2xx400:    true,
			wantAttempts: 2,
			wantSuccess:  true,
		},
		{
			name:         "explicit store=false still retries once on HTTP 200 failed signature error",
			reqMod:       func(r *llm.Request) { r.Store = new(false) },
			wantAttempts: 2,
			wantSuccess:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := attempts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if n == 1 && tc.pre2xx400 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":400,"message":"missing thought_signature","status":"INVALID_ARGUMENT"}}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				if n == 1 {
					_, _ = w.Write([]byte(failed2xxSig400))
					return
				}
				_, _ = w.Write([]byte(interactionsJSON))
			}))
			defer srv.Close()

			req := legacyReq
			if tc.reqMod != nil {
				tc.reqMod(&req)
			}
			res := protoLive(srv)
			if tc.resMod != nil {
				tc.resMod(&res)
			}
			resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res)
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if tc.wantSuccess {
				if err != nil || resp.Text() != "hello" {
					t.Fatalf("expected recovered completion, resp=%+v err=%v", resp, err)
				}
			} else {
				if le, ok := errors.AsType[llm.Error](err); !ok || le.StatusCode() != http.StatusBadRequest {
					t.Fatalf("err = %T %v, want 400 llm.Error", err, err)
				}
			}
		})
	}
}

func TestProtocolReclassifiesGRPCStatus(t *testing.T) {
	t.Parallel()
	srv, _ := protoServer(t, 400, `{"error":{"code":400,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))
	le, ok := errors.AsType[llm.Error](err)
	if llm.Kind(err) != llm.KindRateLimit || !ok || le.Provider() != "gemini-prod" {
		t.Fatalf("RESOURCE_EXHAUSTED on 400 must reclassify to a rate limit stamped with the instance: %v", err)
	}
}

func TestProtocolRegionalVertexLocation404ResidencyRemedy(t *testing.T) {
	t.Parallel()
	for _, loc := range []string{"us-central1", "europe-west1", "europe-west2", "asia-northeast1"} {
		t.Run(loc, func(t *testing.T) {
			body := `{"error":{"code":404,"message":"Interactions endpoint is not available in ` + loc + `."}}`
			srv, _ := protoServer(t, http.StatusNotFound, body)
			res := vertexAuthorityRes(t, srv, loc)
			_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
			cfgErr, ok := errors.AsType[*llm.ConfigurationError](err)
			if !ok {
				t.Fatalf("regional Vertex 404 must be *llm.ConfigurationError, got %T %v", err, err)
			}
			if cfgErr.Provider() != res.Instance || strings.TrimSpace(cfgErr.Message) == "" {
				t.Fatalf("ConfigurationError provider=%q message=%q, want instance %q with non-empty message", cfgErr.Provider(), cfgErr.Message, res.Instance)
			}
			causeErr, ok := errors.AsType[llm.Error](errors.Unwrap(cfgErr))
			if !ok || causeErr.StatusCode() != http.StatusNotFound {
				t.Fatalf("expected wrapped 404 llm.Error cause, got %T %v", errors.Unwrap(cfgErr), errors.Unwrap(cfgErr))
			}
		})
	}
}

func TestProtocolNonVertexTransport404StaysProviderError(t *testing.T) {
	t.Parallel()
	body := `{"error":{"code":404,"message":"Not found in us-central1."}}`
	srv, _ := protoServer(t, http.StatusNotFound, body)
	res := vertexAuthorityRes(t, srv, "us-central1")
	res.Transport.HostRule = ""

	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if _, ok := errors.AsType[*llm.ConfigurationError](err); ok {
		t.Fatalf("a transport without the vertex-location host rule must not get the Vertex remedy: %v", err)
	}
	if le, ok := errors.AsType[llm.Error](err); !ok || le.StatusCode() != http.StatusNotFound {
		t.Fatalf("the provider's 404 must survive: %v", err)
	}
}

func TestProtocolUnderivedHost404StaysProviderError(t *testing.T) {
	t.Parallel()
	body := `{"error":{"code":404,"message":"Not found in us-central1."}}`
	srv, _ := protoServer(t, http.StatusNotFound, body)
	res := protoLive(srv)
	res.Transport.HostRule = registry.HostRuleVertexLocation
	res.Transport.Vars = map[string]string{
		"GOOGLE_VERTEX_LOCATION": "us-central1",
		"GOOGLE_VERTEX_PROJECT":  "p",
		"GOOGLE_VERTEX_HOST":     "https://gw.example.test",
	}

	_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if _, ok := errors.AsType[*llm.ConfigurationError](err); ok {
		t.Fatalf("an underived authority must not get the location-derived remedy: %v", err)
	}
	if le, ok := errors.AsType[llm.Error](err); !ok || le.StatusCode() != http.StatusNotFound {
		t.Fatalf("the provider's 404 must survive: %v", err)
	}
}

func TestProtocolGlobalAndUSEUVertex404StaysProviderError(t *testing.T) {
	t.Parallel()
	for _, loc := range []string{"global", "us", "eu"} {
		t.Run(loc, func(t *testing.T) {
			srv, _ := protoServer(t, http.StatusNotFound, `{"error":{"code":404,"message":"Not found"}}`)
			res := vertexAuthorityRes(t, srv, loc)
			_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
			if _, ok := errors.AsType[*llm.ConfigurationError](err); ok {
				t.Fatalf("%s 404 must not claim regional remedy: %v", loc, err)
			}
			if le, ok := errors.AsType[llm.Error](err); !ok || le.StatusCode() != http.StatusNotFound {
				t.Fatalf("%s 404 must survive: %v", loc, err)
			}
		})
	}
}

func TestProtocolListModelsAndUnsupportedCountTokens(t *testing.T) {
	t.Parallel()
	srv, got := protoServer(t, 200, `{"models":[{"name":"models/gemini-3.8-flash","inputTokenLimit":922000,"outputTokenLimit":128000,"supportedGenerationMethods":["generateContent"]},{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
	res := protoLive(srv)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil || len(rows) != 1 || rows[0].ID != "gemini-3.8-flash" || *rows[0].Caps.MaxInputTokens != 922000 || *rows[0].Caps.MaxOutputTokens != 128000 {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if got.path != "/v1beta/models?pageSize=1000" || got.header.Get("x-goog-api-key") != "k-1" {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}

	_, err = (&Protocol{Client: srv.Client()}).CountTokens(context.Background(), protoReq(""), res)
	if !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
		t.Fatalf("CountTokens err = %v, want ErrInputTokenCountUnsupported", err)
	}
}

func TestProtocolEndpointFamilies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want string
		run  func(p *Protocol, ctx context.Context, res registry.Resolved) error
	}{
		{"interactions", interactionsJSON, "google_interactions", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
			_, err := p.Complete(ctx, protoReq(""), res)
			return err
		}},
		{"interactions_stream", interactionsSSE, "google_interactions_stream", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
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
		{"models", `{"models":[{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"]}]}`, "google_models", func(p *Protocol, ctx context.Context, res registry.Resolved) error {
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

func TestProtocolIncompleteStreamNamesTheInstanceAndDistinguishesQueuedEOF(t *testing.T) {
	t.Parallel()
	t.Run("in_progress EOF is retryable StreamError", func(t *testing.T) {
		truncated := "event: interaction.created\n" +
			`data: {"event_type":"interaction.created","interaction":{"id":"int_inc","status":"in_progress"}}` + "\n\n" +
			"event: step.start\n" +
			`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}` + "\n\n" +
			"event: step.delta\n" +
			`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hel"}}` + "\n\n"
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
		if se, ok := errors.AsType[*llm.StreamError](streamErr); !ok || !se.Retryable() || se.Provider() != res.Instance {
			t.Fatalf("in_progress EOF = %v, want retryable *llm.StreamError with Provider()=%q", streamErr, res.Instance)
		}
	})

	t.Run("queued EOF is permanent ErrPendingInteractionStatus", func(t *testing.T) {
		queued := "event: interaction.created\n" +
			`data: {"event_type":"interaction.created","interaction":{"id":"int_q","status":"queued"}}` + "\n\n"
		srv, _ := protoServer(t, 200, queued)
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
		if !errors.Is(streamErr, ErrPendingInteractionStatus) {
			t.Fatalf("queued EOF = %v, want ErrPendingInteractionStatus", streamErr)
		}
		if le, ok := errors.AsType[llm.Error](streamErr); !ok || le.Retryable() || le.Provider() != res.Instance {
			t.Fatalf("queued EOF error = %v, want non-retryable stamped with %q", streamErr, res.Instance)
		}
	})

	t.Run("stored in_progress EOF after interaction.created is non-retryable and prevents duplicate StreamGenerate POST", func(t *testing.T) {
		createdThenDrop := "event: interaction.created\n" +
			`data: {"event_type":"interaction.created","interaction":{"id":"int_stored_1","status":"in_progress"}}` + "\n\n"

		var storedHits atomic.Int32
		srvStored := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := storedHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				_, _ = w.Write([]byte(createdThenDrop))
				return
			}
			_, _ = w.Write([]byte(interactionsSSE))
		}))
		t.Cleanup(srvStored.Close)

		rStored, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"google": {Base: "google", APIKey: "k", Transport: registry.Transport{BaseURL: srvStored.URL + "/v1beta"}},
			}),
		)
		if err != nil {
			t.Fatalf("registry.Load: %v", err)
		}
		cStored := llm.NewClient(llm.WithRegistry(rStored))
		noSleep := func(context.Context, time.Duration) error { return nil }
		sr, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
			Client:   cStored,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hello"),
			Store:    new(true),
			Sleep:    noSleep,
		})
		if err != nil {
			t.Fatalf("StreamGenerate open: %v", err)
		}
		var storedStreamErr error
		for ev := range sr.Events() {
			if ev.Type == llm.StreamEventError {
				storedStreamErr = ev.Err
			}
		}
		if storedHits.Load() != 1 {
			t.Fatalf("stored StreamGenerate HTTP POSTs = %d, want 1 (must not submit duplicate stored interaction)", storedHits.Load())
		}
		le, ok := errors.AsType[llm.Error](storedStreamErr)
		if !ok || le.Retryable() || !errors.Is(storedStreamErr, ErrPendingInteractionStatus) {
			t.Fatalf("stored in_progress EOF error = %T %v, want non-retryable ErrPendingInteractionStatus", storedStreamErr, storedStreamErr)
		}
		rawMap, _ := le.Raw().(map[string]any)
		if rawMap["interaction_id"] != "int_stored_1" {
			t.Fatalf("stored in_progress EOF raw = %#v, want interaction_id=int_stored_1", le.Raw())
		}

		// Stateless (Store=false) stream disconnect before content still retries automatically and succeeds on POST #2.
		var statelessHits atomic.Int32
		srvStateless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := statelessHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				_, _ = w.Write([]byte(createdThenDrop))
				return
			}
			_, _ = w.Write([]byte(interactionsSSE))
		}))
		t.Cleanup(srvStateless.Close)
		rStateless, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"google": {Base: "google", APIKey: "k", Transport: registry.Transport{BaseURL: srvStateless.URL + "/v1beta"}},
			}),
		)
		if err != nil {
			t.Fatalf("registry.Load: %v", err)
		}
		cStateless := llm.NewClient(llm.WithRegistry(rStateless))
		srStateless, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
			Client:   cStateless,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hello"),
			Sleep:    noSleep,
		})
		if err != nil {
			t.Fatalf("stateless StreamGenerate open: %v", err)
		}
		var statelessErr error
		for ev := range srStateless.Events() {
			if ev.Type == llm.StreamEventError {
				statelessErr = ev.Err
			}
		}
		if statelessErr != nil || statelessHits.Load() != 2 {
			t.Fatalf("stateless StreamGenerate err=%v hits=%d, want nil and 2 POSTs", statelessErr, statelessHits.Load())
		}

		// Stored (Store=true) stream that drops BEFORE interaction.created supplies an ID also retries automatically.
		var preIDHits atomic.Int32
		srvPreID := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := preIDHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				return
			}
			_, _ = w.Write([]byte(interactionsSSE))
		}))
		t.Cleanup(srvPreID.Close)
		rPreID, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"google": {Base: "google", APIKey: "k", Transport: registry.Transport{BaseURL: srvPreID.URL + "/v1beta"}},
			}),
		)
		if err != nil {
			t.Fatalf("registry.Load: %v", err)
		}
		cPreID := llm.NewClient(llm.WithRegistry(rPreID))
		srPreID, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
			Client:   cPreID,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hello"),
			Store:    new(true),
			Sleep:    noSleep,
		})
		if err != nil {
			t.Fatalf("pre-ID stored StreamGenerate open: %v", err)
		}
		var preIDErr error
		for ev := range srPreID.Events() {
			if ev.Type == llm.StreamEventError {
				preIDErr = ev.Err
			}
		}
		if preIDErr != nil || preIDHits.Load() != 2 {
			t.Fatalf("pre-ID stored StreamGenerate err=%v hits=%d, want nil and 2 POSTs", preIDErr, preIDHits.Load())
		}

		// Omitted store (fields.store = false) defaults to stored on Google servers, so disconnect after interaction.created is also non-retryable.
		createdOmittedThenDrop := "event: interaction.created\n" +
			`data: {"event_type":"interaction.created","interaction":{"id":"int_omitted_1","status":"in_progress"}}` + "\n\n"
		var omittedHits atomic.Int32
		srvOmitted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := omittedHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				_, _ = w.Write([]byte(createdOmittedThenDrop))
				return
			}
			_, _ = w.Write([]byte(interactionsSSE))
		}))
		t.Cleanup(srvOmitted.Close)
		rOmitted, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"google": {
					Base:      "google",
					APIKey:    "k",
					Caps:      registry.Caps{Fields: map[string]bool{"store": false}},
					Transport: registry.Transport{BaseURL: srvOmitted.URL + "/v1beta"},
				},
			}),
		)
		if err != nil {
			t.Fatalf("registry.Load: %v", err)
		}
		cOmitted := llm.NewClient(llm.WithRegistry(rOmitted))
		srOmitted, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
			Client:   cOmitted,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hello"),
			Sleep:    noSleep,
		})
		if err != nil {
			t.Fatalf("omitted-store StreamGenerate open: %v", err)
		}
		var omittedErr error
		for ev := range srOmitted.Events() {
			if ev.Type == llm.StreamEventError {
				omittedErr = ev.Err
			}
		}
		if omittedHits.Load() != 1 {
			t.Fatalf("omitted-store StreamGenerate HTTP POSTs = %d, want 1", omittedHits.Load())
		}
		leOmitted, ok := errors.AsType[llm.Error](omittedErr)
		if !ok || leOmitted.Retryable() || !errors.Is(omittedErr, ErrPendingInteractionStatus) {
			t.Fatalf("omitted-store in_progress EOF error = %T %v, want non-retryable ErrPendingInteractionStatus", omittedErr, omittedErr)
		}
		rawOmitted, _ := leOmitted.Raw().(map[string]any)
		if rawOmitted["interaction_id"] != "int_omitted_1" {
			t.Fatalf("omitted-store in_progress EOF raw = %#v, want interaction_id=int_omitted_1", leOmitted.Raw())
		}
	})
}

// Not parallel: temporarily overrides DefaultProtocol.Client for deterministic in-memory StreamGenerate testing.
func TestStreamGenerateStoredPostAcceptanceAdapterDeadlines(t *testing.T) {
	prevClient := DefaultProtocol.Client
	t.Cleanup(func() { DefaultProtocol.Client = prevClient })

	reg, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"google": {Base: "google", APIKey: "k", Transport: registry.Transport{BaseURL: "https://example.invalid/v1beta"}},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}

	noSleep := func(context.Context, time.Duration) error { return nil }
	createdFrame := "event: interaction.created\n" +
		`data: {"event_type":"interaction.created","interaction":{"id":"int_deadline_1","status":"in_progress"}}` + "\n\n"

	runBubbleStream := func(t *testing.T, makeCtx func() (context.Context, context.CancelFunc), firstPrefix string, opts llm.GenerateOptions, onEvent func(llm.StreamEvent)) (int32, error) {
		t.Helper()
		var hits atomic.Int32
		var gotErr error
		synctest.Test(t, func(t *testing.T) {
			stall := make(chan struct{})
			defer close(stall)
			DefaultProtocol.Client = &http.Client{Transport: idleAttemptRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Body != nil {
					_, _ = io.Copy(io.Discard, req.Body)
					_ = req.Body.Close()
				}
				n := hits.Add(1)
				if n == 1 {
					pr, pw := io.Pipe()
					go func() {
						if firstPrefix != "" {
							_, _ = io.WriteString(pw, firstPrefix)
						}
						select {
						case <-req.Context().Done():
							_ = pw.CloseWithError(req.Context().Err())
						case <-stall:
							_ = pw.Close()
						}
					}()
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       pr,
						Request:    req,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(interactionsSSE)),
					Request:    req,
				}, nil
			})}

			ctx, cancel := makeCtx()
			defer cancel()
			opts.Client = llm.NewClient(llm.WithRegistry(reg))
			opts.Provider = "google"
			opts.Model = "gemini-3.8-flash"
			opts.Prompt = new("hello")
			opts.Sleep = noSleep
			sr, err := llm.StreamGenerate(ctx, opts)
			if err != nil {
				t.Fatalf("StreamGenerate open: %v", err)
			}
			for ev := range sr.Events() {
				if onEvent != nil {
					onEvent(ev)
				}
				if ev.Type == llm.StreamEventError {
					gotErr = ev.Err
				}
			}
		})
		return hits.Load(), gotErr
	}

	bgCtx := func() (context.Context, context.CancelFunc) { return context.Background(), func() {} }

	for _, tc := range []struct {
		name    string
		timeout *llm.AdapterTimeout
	}{
		{"adapter Request deadline", &llm.AdapterTimeout{Request: time.Second}},
		{"adapter StreamRead deadline", &llm.AdapterTimeout{StreamRead: time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits, gotErr := runBubbleStream(t, bgCtx, createdFrame, llm.GenerateOptions{
				Store:          new(true),
				AdapterTimeout: tc.timeout,
			}, nil)
			if hits != 1 {
				t.Fatalf("stored %s HTTP POSTs = %d, want 1", tc.name, hits)
			}
			le, ok := errors.AsType[llm.Error](gotErr)
			if !ok || le.Retryable() || !errors.Is(gotErr, ErrPendingInteractionStatus) {
				t.Fatalf("stored %s error = %T %v, want non-retryable ErrPendingInteractionStatus", tc.name, gotErr, gotErr)
			}
			rawMap, _ := le.Raw().(map[string]any)
			if rawMap["interaction_id"] != "int_deadline_1" {
				t.Fatalf("stored %s raw = %#v, want interaction_id=int_deadline_1", tc.name, le.Raw())
			}
		})
	}

	t.Run("stateless adapter Request deadline after interaction.created retries and succeeds", func(t *testing.T) {
		hits, gotErr := runBubbleStream(t, bgCtx, createdFrame, llm.GenerateOptions{
			AdapterTimeout: &llm.AdapterTimeout{Request: time.Second},
		}, nil)
		if gotErr != nil || hits != 2 {
			t.Fatalf("stateless adapter Request deadline err=%v hits=%d, want nil and 2 POSTs", gotErr, hits)
		}
	})

	t.Run("stored adapter Request deadline before interaction.created retries and succeeds", func(t *testing.T) {
		hits, gotErr := runBubbleStream(t, bgCtx, "", llm.GenerateOptions{
			Store:          new(true),
			AdapterTimeout: &llm.AdapterTimeout{Request: time.Second},
		}, nil)
		if gotErr != nil || hits != 2 {
			t.Fatalf("pre-ID stored adapter Request deadline err=%v hits=%d, want nil and 2 POSTs", gotErr, hits)
		}
	})

	t.Run("caller cancellation after interaction.created stays AbortError", func(t *testing.T) {
		var callerCancel context.CancelFunc
		hits, gotErr := runBubbleStream(t, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			callerCancel = cancel
			return ctx, cancel
		}, createdFrame+"event: custom.ping\ndata: {\"ok\":true}\n\n", llm.GenerateOptions{
			Store: new(true),
		}, func(ev llm.StreamEvent) {
			if ev.Type == llm.StreamEventProviderEvent {
				callerCancel()
			}
		})
		if hits != 1 {
			t.Fatalf("caller cancellation HTTP POSTs = %d, want 1", hits)
		}
		if _, ok := errors.AsType[*llm.AbortError](gotErr); !ok || !errors.Is(gotErr, context.Canceled) {
			t.Fatalf("caller cancellation error = %T %v, want *llm.AbortError wrapping context.Canceled", gotErr, gotErr)
		}
	})
}

func TestProtocolStreamWrapperCloseDuringS1DoesNotDeadlock(t *testing.T) {
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

	releaseServer := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: interaction.created\n" + `data: {"event_type":"interaction.created","interaction":{"id":"int_open","status":"in_progress"}}` + "\n\n"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-releaseServer:
		}
	}))
	defer srv.Close()
	defer close(releaseServer)

	st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), legacyReq, protoLive(srv))
	if err != nil {
		t.Fatalf("Stream open failed: %v", err)
	}

	select {
	case ev, ok := <-st.Events():
		if !ok || ev.Type != llm.StreamEventStreamStart {
			t.Fatalf("first event = (%+v, ok=%v), want StreamEventStreamStart", ev, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for StreamEventStreamStart")
	}

	closed := make(chan error, 1)
	go func() {
		closed <- st.Close()
	}()
	select {
	case cerr := <-closed:
		if cerr != nil {
			t.Fatalf("st.Close() = %v, want nil", cerr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outer.Close() deadlocked while s1 remained open")
	}
}

func TestProtocolStreamPre2xxRetryS2InBandSignature400SurfacesError(t *testing.T) {
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

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"missing thought_signature","status":"INVALID_ARGUMENT"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\ndata: {\"event_type\":\"error\",\"error\":{\"code\":\"invalid_argument\",\"message\":\"missing thought_signature\"}}\n\n"))
	}))
	defer srv.Close()

	res := protoLive(srv)
	st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), legacyReq, res)
	if err != nil {
		t.Fatalf("Stream open failed: %v", err)
	}
	var starts int
	var streamErr error
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventStreamStart {
			starts++
		}
		if ev.Type == llm.StreamEventError {
			streamErr = ev.Err
		}
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if starts != 1 {
		t.Fatalf("StreamEventStreamStart count = %d, want 1", starts)
	}
	le, ok := errors.AsType[llm.Error](streamErr)
	if !ok || le.StatusCode() != http.StatusBadRequest || le.Provider() != res.Instance {
		t.Fatalf("s2 streamErr = %v, want 400 llm.Error with Provider()=%q", streamErr, res.Instance)
	}

	// Also confirm CompleteViaStream preserves the 400 llm.Error instead of dropping it.
	// (Uses the same AuthOAuthOpenAICodex override gate as llm/auth_override.go.)
	completeViaStreamCheck(t, srv, legacyReq, res)
}

type callScopedStreamingPreparer struct{}

func (callScopedStreamingPreparer) Apply(context.Context, *http.Request, registry.Resolved) error {
	return nil
}
func (callScopedStreamingPreparer) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (callScopedStreamingPreparer) RequiresStreamingComplete() bool { return true }

func completeViaStreamCheck(t *testing.T, srv *httptest.Server, legacyReq llm.Request, res registry.Resolved) {
	t.Helper()
	res.Transport.Auth = registry.AuthOAuthOpenAICodex
	ctx := llm.WithAuthenticatorOverride(context.Background(), callScopedStreamingPreparer{})
	_, cerr := (&Protocol{Client: srv.Client()}).Complete(ctx, legacyReq, res)
	if le, ok := errors.AsType[llm.Error](cerr); !ok || le.StatusCode() != http.StatusBadRequest || le.Provider() != res.Instance {
		t.Fatalf("CompleteViaStream err = %v, want 400 llm.Error with Provider()=%q", cerr, res.Instance)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type manualDeadlineCtx struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newManualDeadlineCtx(parent context.Context) (*manualDeadlineCtx, func()) {
	c := &manualDeadlineCtx{Context: parent, done: make(chan struct{})}
	return c, func() { c.once.Do(func() { close(c.done) }) }
}

func (c *manualDeadlineCtx) Deadline() (time.Time, bool) { return time.Time{}, true }
func (c *manualDeadlineCtx) Done() <-chan struct{}       { return c.done }
func (c *manualDeadlineCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestProtocolStreamWrapperCallerDeadlineAndCancel(t *testing.T) {
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

	newBlockingServer := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: interaction.created\n" + `data: {"event_type":"interaction.created","interaction":{"id":"int_wait","status":"in_progress"}}` + "\n\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("direct Stream emits KindTimeout StreamEventError on caller deadline", func(t *testing.T) {
		srv := newBlockingServer(t)
		res := protoLive(srv)
		ctx, triggerDeadline := newManualDeadlineCtx(context.Background())
		defer triggerDeadline()
		st, err := (&Protocol{Client: srv.Client()}).Stream(ctx, legacyReq, res)
		if err != nil {
			t.Fatalf("Stream open failed: %v", err)
		}
		ev, ok := <-st.Events()
		if !ok || ev.Type != llm.StreamEventStreamStart {
			t.Fatalf("first event = (%+v, ok=%v), want StreamEventStreamStart", ev, ok)
		}
		triggerDeadline()
		var streamErr error
		for ev := range st.Events() {
			if ev.Type == llm.StreamEventError {
				streamErr = ev.Err
			}
		}
		if llm.Kind(streamErr) != llm.KindTimeout {
			t.Fatalf("Stream error = %T %v (kind=%v), want KindTimeout", streamErr, streamErr, llm.Kind(streamErr))
		}
	})

	t.Run("CompleteViaStream returns KindTimeout on caller deadline", func(t *testing.T) {
		srv := newBlockingServer(t)
		res := protoLive(srv)
		res.Transport.Auth = registry.AuthOAuthOpenAICodex
		headersReceived := make(chan struct{})
		var once sync.Once
		baseRT := srv.Client().Transport
		httpClient := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := baseRT.RoundTrip(r)
				if err == nil {
					once.Do(func() { close(headersReceived) })
				}
				return resp, err
			}),
		}
		ctx, triggerDeadline := newManualDeadlineCtx(llm.WithAuthenticatorOverride(context.Background(), callScopedStreamingPreparer{}))
		defer triggerDeadline()
		go func() {
			<-headersReceived
			triggerDeadline()
		}()
		_, err := (&Protocol{Client: httpClient}).Complete(ctx, legacyReq, res)
		if llm.Kind(err) != llm.KindTimeout {
			t.Fatalf("CompleteViaStream err = %T %v (kind=%v), want KindTimeout", err, err, llm.Kind(err))
		}
	})

	t.Run("direct Stream emits KindAbort StreamEventError on caller cancel", func(t *testing.T) {
		srv := newBlockingServer(t)
		res := protoLive(srv)
		ctx, cancel := context.WithCancel(context.Background())
		st, err := (&Protocol{Client: srv.Client()}).Stream(ctx, legacyReq, res)
		if err != nil {
			t.Fatalf("Stream open failed: %v", err)
		}
		ev, ok := <-st.Events()
		if !ok || ev.Type != llm.StreamEventStreamStart {
			t.Fatalf("first event = (%+v, ok=%v), want StreamEventStreamStart", ev, ok)
		}
		cancel()
		var streamErr error
		for ev := range st.Events() {
			if ev.Type == llm.StreamEventError {
				streamErr = ev.Err
			}
		}
		if _, ok := errors.AsType[*llm.AbortError](streamErr); !ok {
			t.Fatalf("Stream error = %T %v, want *llm.AbortError", streamErr, streamErr)
		}
	})

	t.Run("finish followed immediately by caller deadline emits no trailing StreamEventError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(interactionsSSE))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		res := protoLive(srv)
		ctx, triggerDeadline := newManualDeadlineCtx(context.Background())
		defer triggerDeadline()
		st, err := (&Protocol{Client: srv.Client()}).Stream(ctx, legacyReq, res)
		if err != nil {
			t.Fatalf("Stream open failed: %v", err)
		}
		var sawFinish bool
		for ev := range st.Events() {
			if ev.Type == llm.StreamEventFinish {
				sawFinish = true
				triggerDeadline()
			}
			if ev.Type == llm.StreamEventError {
				t.Fatalf("unexpected StreamEventError after Finish=%v: %v", sawFinish, ev.Err)
			}
		}
		if !sawFinish {
			t.Fatal("expected StreamEventFinish")
		}
	})
}

func TestProtocolStreamPre2xxRetryAndPost2xxPassthrough(t *testing.T) {
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

	const inBandSig400 = "event: interaction.created\n" +
		`data: {"event_type":"interaction.created","interaction":{"id":"int_persisted","status":"in_progress"}}` + "\n\n" +
		"event: error\n" +
		`data: {"event_type":"error","error":{"code":"invalid_argument","message":"missing thought_signature"}}` + "\n\n"

	for _, tc := range []struct {
		name         string
		reqMod       func(*llm.Request)
		resMod       func(*registry.Resolved)
		pre2xx400    bool
		wantAttempts int32
		wantSuccess  bool
	}{
		{
			name:         "store=true blocks post-2xx in-band SSE signature retry",
			reqMod:       func(r *llm.Request) { r.Store = new(true) },
			wantAttempts: 1,
		},
		{
			name:         "fields.store=false (omitted store) blocks post-2xx in-band SSE signature retry",
			resMod:       func(res *registry.Resolved) { res.Caps.Fields = map[string]bool{"store": false} },
			wantAttempts: 1,
		},
		{
			name:         "store=true still recovers on pre-2xx HTTP 400 signature rejection",
			reqMod:       func(r *llm.Request) { r.Store = new(true) },
			pre2xx400:    true,
			wantAttempts: 2,
			wantSuccess:  true,
		},
		{
			name:         "explicit store=false recovers on pre-2xx HTTP 400 signature rejection",
			reqMod:       func(r *llm.Request) { r.Store = new(false) },
			pre2xx400:    true,
			wantAttempts: 2,
			wantSuccess:  true,
		},
		{
			name:         "fields.store=false (omitted store) recovers on pre-2xx HTTP 400 signature rejection",
			resMod:       func(res *registry.Resolved) { res.Caps.Fields = map[string]bool{"store": false} },
			pre2xx400:    true,
			wantAttempts: 2,
			wantSuccess:  true,
		},
		{
			name:         "explicit store=false passes post-2xx in-band SSE signature 400 through without retry",
			reqMod:       func(r *llm.Request) { r.Store = new(false) },
			wantAttempts: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := attempts.Add(1)
				if tc.pre2xx400 && n == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":400,"message":"missing thought_signature","status":"INVALID_ARGUMENT"}}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if n == 1 && !tc.pre2xx400 {
					_, _ = w.Write([]byte(inBandSig400))
					return
				}
				_, _ = w.Write([]byte(interactionsSSE))
			}))
			defer srv.Close()

			req := legacyReq
			if tc.reqMod != nil {
				tc.reqMod(&req)
			}
			res := protoLive(srv)
			if tc.resMod != nil {
				tc.resMod(&res)
			}
			st, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), req, res)
			if err != nil {
				t.Fatalf("Stream open failed: %v", err)
			}
			var streamErr error
			var finished *llm.Response
			for ev := range st.Events() {
				if ev.Type == llm.StreamEventError {
					streamErr = ev.Err
				}
				if ev.Type == llm.StreamEventFinish {
					finished = ev.Response
				}
			}
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if tc.wantSuccess {
				if streamErr != nil || finished == nil || finished.Text() != "hello" {
					t.Fatalf("expected recovered completion, err=%v finished=%+v", streamErr, finished)
				}
			} else {
				if le, ok := errors.AsType[llm.Error](streamErr); !ok || le.StatusCode() != http.StatusBadRequest {
					t.Fatalf("streamErr = %T %v, want 400 llm.Error", streamErr, streamErr)
				}
			}
		})
	}
}
