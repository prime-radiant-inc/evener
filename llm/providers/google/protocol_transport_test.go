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
	for _, tc := range []struct {
		loc         string
		wantLead    string
		wantOther   []string
		wantMissing []string
	}{
		{
			loc:       "us-central1",
			wantLead:  "GOOGLE_VERTEX_LOCATION=us",
			wantOther: []string{"United States", "European Union", "no data-residency guarantee", "changes the geographic data-residency boundary"},
		},
		{
			loc:       "europe-west1",
			wantLead:  "GOOGLE_VERTEX_LOCATION=eu",
			wantOther: []string{"European Union", "United States", "no data-residency guarantee", "changes the geographic data-residency boundary"},
		},
		{
			loc:       "europe-west2", // London (non-EU) must list us, eu, global with residency disclosure, not lead with eu alone
			wantLead:  "us",
			wantOther: []string{"eu", "global", "no data-residency guarantee", "changes the geographic data-residency boundary"},
		},
		{
			loc:       "asia-northeast1",
			wantLead:  "us",
			wantOther: []string{"eu", "global", "no data-residency guarantee", "changes the geographic data-residency boundary"},
		},
	} {
		t.Run(tc.loc, func(t *testing.T) {
			body := `{"error":{"code":404,"message":"Interactions endpoint is not available in ` + tc.loc + `."}}`
			srv, _ := protoServer(t, http.StatusNotFound, body)
			res := vertexAuthorityRes(t, srv, tc.loc)
			_, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
			cfgErr, ok := errors.AsType[*llm.ConfigurationError](err)
			if !ok {
				t.Fatalf("regional Vertex 404 must be *llm.ConfigurationError, got %T %v", err, err)
			}
			if !strings.Contains(cfgErr.Message, tc.loc) || !strings.Contains(cfgErr.Message, tc.wantLead) {
				t.Fatalf("remedy %q missing loc %q or %q", cfgErr.Message, tc.loc, tc.wantLead)
			}
			for _, sub := range tc.wantOther {
				if !strings.Contains(cfgErr.Message, sub) {
					t.Fatalf("remedy %q missing residency phrase %q", cfgErr.Message, sub)
				}
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
		if se, ok := errors.AsType[*llm.StreamError](streamErr); !ok || !se.Retryable() || !strings.Contains(streamErr.Error(), res.Instance+" stream ended without completion") {
			t.Fatalf("in_progress EOF = %v, want retryable StreamError naming %q", streamErr, res.Instance)
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
}
