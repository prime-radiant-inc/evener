package protocolhttp

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
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// renamingPreparer is the test double for the Codex transport: it asserts
// the authenticator ran first and renames a field, proving the spec §8.2
// order build → prune → constants → auth → prepare.
type renamingPreparer struct{ sawAuth *bool }

func (p renamingPreparer) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
	return nil
}
func (p renamingPreparer) PrepareRequest(_ context.Context, req *http.Request, body map[string]any, _ llm.Request, _ registry.Resolved) error {
	*p.sawAuth = req.Header.Get("Authorization") != ""
	if v, ok := body["store"]; ok {
		body["store_renamed"] = v
		delete(body, "store")
	}
	req.Header.Set("x-prepared", "yes")
	return nil
}
func (renamingPreparer) RequiresStreamingComplete() bool { return true }

var registerOnce sync.Once
var preparerSawAuth bool

func registerTestSchemes() {
	registerOnce.Do(func() {
		llm.RegisterAuthenticator("test-runner-preparer", renamingPreparer{sawAuth: &preparerSawAuth})
	})
}

func testRes(baseURL, auth string) registry.Resolved {
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses)}
	caps.Fields["metadata"] = false
	caps.Fields["store"] = true
	return registry.Resolved{
		Instance: "inst", Protocol: registry.ProtocolOpenAIResponses, ModelID: "m", WireID: "m-wire",
		Transport: registry.Transport{Auth: auth, BaseURL: baseURL, Endpoint: "/responses", StreamEndpoint: "/responses", Body: map[string]any{"reasoning.context": "all_turns"}},
		Headers:   map[string]string{"X-Instance": "1"}, CredentialHeaders: map[string]string{"X-Gateway-Key": "gw-secret"},
		Credential: registry.Credential{Value: "key-secret", Source: "api_key"}, Caps: caps,
	}
}

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

func TestPrepareRunsPruneConstantsAuthPrepareInOrder(t *testing.T) {
	registerTestSchemes()
	res := testRes("https://api.example.test/v1", "test-runner-preparer")
	body := map[string]any{"metadata": map[string]string{"a": "b"}, "store": false, "reasoning": map[string]any{"effort": "high"}}
	p, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: body, Headers: map[string]string{"X-Instance": "protocol", "anthropic-version": "v"}, Res: res})
	if err != nil {
		t.Fatal(err)
	}
	if !preparerSawAuth {
		t.Fatal("preparer ran before the authenticator")
	}
	if got := strings.Join(p.PrunedFields, ","); got != "metadata" {
		t.Fatalf("pruned = %q", got)
	}
	var sent map[string]any
	_ = json.Unmarshal(p.Body, &sent)
	if _, still := sent["store"]; still || sent["store_renamed"] != false || sent["reasoning"].(map[string]any)["context"] != "all_turns" {
		t.Fatalf("body = %s", p.Body)
	}
	h := p.Request.Header
	if h.Get("Authorization") != "Bearer key-secret" || h.Get("X-Gateway-Key") != "gw-secret" || h.Get("X-Instance") != "protocol" || h.Get("anthropic-version") != "v" || h.Get("Content-Type") != "application/json" || h.Get("x-prepared") != "yes" {
		t.Fatalf("headers = %v", h)
	}
	if p.Request.URL.String() != "https://api.example.test/v1/responses" || p.Request.GetBody == nil || p.Request.ContentLength != int64(len(p.Body)) {
		t.Fatalf("request = %+v", p.Request)
	}
	if _, err := Prepare(context.Background(), &Call{Method: http.MethodPost, URL: "https://x", Body: map[string]any{}, Res: registry.Resolved{Instance: "i", Transport: registry.Transport{Auth: "no-such-scheme"}}}); err == nil {
		t.Fatal("unknown scheme must fail")
	}
}

func TestURLAndModelInBody(t *testing.T) {
	res := registry.Resolved{WireID: "models/x y", Transport: registry.Transport{BaseURL: "https://h/v1/", Endpoint: "/publishers/anthropic/models/{model}:rawPredict"}}
	if got := URL(res, res.Transport.Endpoint); got != "https://h/v1/publishers/anthropic/models/models%2Fx%20y:rawPredict" {
		t.Fatalf("URL = %q", got)
	}
	if ModelInBody(res) {
		t.Fatal("a {model} endpoint must not send model in the body")
	}
	if !ModelInBody(registry.Resolved{Transport: registry.Transport{Endpoint: "/messages"}}) {
		t.Fatal("plain endpoints send model in the body")
	}
}

func TestDoDecodesStampsAndLogs(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("x-ratelimit-remaining-requests", "7")
		_, _ = w.Write([]byte(`{"id":"r1"}`))
	}))
	defer srv.Close()
	res := testRes(srv.URL, registry.AuthBearer)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_do")), sink)
	var out llm.Response
	err := Do(ctx, &Call{Operation: "responses.create", EndpointFamily: "test", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"metadata": map[string]string{"k": "v"}, "input": "hi"}, Req: llm.Request{Model: "m"}, Res: res, Client: srv.Client()}, func(r *Result) (*llm.Response, error) {
		if r.Raw["id"] != "r1" || r.StatusCode != http.StatusOK || r.Header.Get("x-ratelimit-remaining-requests") != "7" || !strings.HasSuffix(r.EndpointURL, "/responses") {
			t.Fatalf("result = %+v", r)
		}
		out = llm.Response{Model: "m"}
		return &out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != "inst" {
		t.Fatalf("provider = %q", out.Provider)
	}
	if strings.Contains(string(gotBody), "metadata") || !strings.Contains(string(gotBody), `"reasoning":{"context":"all_turns"}`) {
		t.Fatalf("wire body = %s", gotBody)
	}
	llm.WaitForPriorAPIAttempts(ctx)
	if len(sink.attempts) != 1 {
		t.Fatalf("attempts = %d", len(sink.attempts))
	}
	rec := sink.attempts[0]
	if rec.ProviderInstance != "inst" || strings.Join(rec.Request.PrunedFields, ",") != "metadata" || *rec.Response.StatusCode != 200 {
		t.Fatalf("record = %+v", rec)
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(string(raw), "key-secret") || strings.Contains(string(raw), "gw-secret") {
		t.Fatalf("credential leaked into the attempt record: %s", raw)
	}
}

// assertOneAttemptCompleted waits for ctx's group to finish appending, then
// asserts sink recorded exactly one attempt whose response status (0 means
// no response reached the wire, e.g. a transport failure) and error message
// match what the call actually observed and returned. This proves the
// attempt was completed with real evidence rather than left as a
// nil-receiver no-op (transport.DoWithAPIAttempts hands back a nil capture,
// and Complete on nil is a silent no-op, whenever no group/sink is attached).
func assertOneAttemptCompleted(ctx context.Context, t *testing.T, sink *captureSink, wantStatus int, wantErr error) {
	t.Helper()
	llm.WaitForPriorAPIAttempts(ctx)
	if len(sink.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(sink.attempts))
	}
	rec := sink.attempts[0]
	if wantStatus == 0 {
		if rec.Response != nil {
			t.Fatalf("record response = %+v, want none", rec.Response)
		}
	} else if rec.Response == nil || rec.Response.StatusCode == nil || *rec.Response.StatusCode != wantStatus {
		t.Fatalf("record response = %+v, want status %d", rec.Response, wantStatus)
	}
	if wantErr == nil || rec.ErrorMessage != wantErr.Error() {
		t.Fatalf("record error = %q, want %q", rec.ErrorMessage, wantErr)
	}
}

func TestDoClassifiesFailuresAndReclassifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'store'.","code":"unknown_parameter","param":"store"}}`))
	}))
	defer srv.Close()
	res := testRes(srv.URL, registry.AuthBearer)
	call := &Call{Operation: "responses.create", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{}, Res: res, Client: srv.Client()}

	sink1 := &captureSink{}
	ctx1 := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_classify")), sink1)
	err := Do(ctx1, call, func(*Result) (*llm.Response, error) { t.Fatal("finish must not run on 4xx"); return nil, nil })
	if llm.Kind(err) != llm.KindInvalidRequest || !strings.Contains(llm.ErrorHint(err), "fields.store = false") {
		t.Fatalf("err = %v", err)
	}
	assertOneAttemptCompleted(ctx1, t, sink1, http.StatusBadRequest, err)

	marker := errors.New("reclassified")
	call.Reclassify = func(status int, body []byte, err error) error {
		if status != 400 || !strings.Contains(string(body), "unknown_parameter") || err == nil {
			t.Fatalf("reclassify inputs: %d %s %v", status, body, err)
		}
		return marker
	}
	sink2 := &captureSink{}
	ctx2 := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_reclassify")), sink2)
	err = Do(ctx2, call, nil)
	if !errors.Is(err, marker) {
		t.Fatalf("reclassify not applied: %v", err)
	}
	assertOneAttemptCompleted(ctx2, t, sink2, http.StatusBadRequest, err)

	srv.Close()
	sink3 := &captureSink{}
	ctx3 := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_transport_fail")), sink3)
	err = Do(ctx3, call, nil)
	if err == nil {
		t.Fatal("transport failure must surface")
	}
	assertOneAttemptCompleted(ctx3, t, sink3, 0, err)
}

func TestStreamPublishesStartThenHandsOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fail") {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"x\":1}\n\n"))
	}))
	defer srv.Close()
	res := testRes(srv.URL, registry.AuthBearer)
	// A shared attempt-group context, the way agent/session_model_call.go wraps
	// retries of one logical call: transport.DoWithAPIAttempts only ever hands
	// back a non-nil attempt (asserted by the decoder below) when a group and
	// sink are attached; without one it takes its no-op client.Do fast path.
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_stream")), sink)
	call := &Call{Operation: "responses.create(stream)", Method: http.MethodPost, URL: srv.URL + "/fail", Body: map[string]any{"stream": true}, Res: res, Client: srv.Client()}
	_, err := Stream(ctx, call, nil)
	if llm.Kind(err) != llm.KindRateLimit {
		t.Fatalf("err = %v", err)
	}
	assertOneAttemptCompleted(ctx, t, sink, http.StatusTooManyRequests, err)

	call.URL = srv.URL + "/ok"
	s, err := Stream(ctx, call, func(_ context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *Result, attempt *transport.APIAttemptCapture) {
		defer cancel()
		defer func() { _ = resp.Body.Close() }()
		defer s.CloseSend()
		data, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(data), `{"x":1}`) || r.EndpointURL == "" || attempt == nil {
			t.Errorf("decoder inputs: %q %q", data, r.EndpointURL)
		}
		if len(r.Material.HeaderNames) == 0 {
			t.Errorf("material must name the auth header")
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: 200}, llm.APITimeoutNone, nil, nil)
		s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &llm.Response{Provider: "inst"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var types []llm.StreamEventType
	for ev := range s.Events() {
		types = append(types, ev.Type)
	}
	if len(types) < 2 || types[0] != llm.StreamEventStreamStart || types[len(types)-1] != llm.StreamEventFinish {
		t.Fatalf("events = %v", types)
	}

	// Transport-error path: server closed, dial fails before any response
	// reaches the wire.
	srv.Close()
	transportSink := &captureSink{}
	transportCtx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_stream_transport_fail")), transportSink)
	_, err = Stream(transportCtx, call, nil)
	if err == nil {
		t.Fatal("closed server must surface a transport error")
	}
	assertOneAttemptCompleted(transportCtx, t, transportSink, 0, err)
}

func TestCompleteViaStreamAccumulates(t *testing.T) {
	open := func(context.Context) (llm.Stream, error) {
		s := llm.NewChanStream(func() {})
		go func() {
			s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "hi", TextID: "t1"})
			s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &llm.Response{Provider: "inst", Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}}})
			s.CloseSend()
		}()
		return s, nil
	}
	resp, err := CompleteViaStream(context.Background(), "inst", open)
	if err != nil || resp.Text() != "hi" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	failing := func(context.Context) (llm.Stream, error) { return nil, errors.New("open failed") }
	if _, err := CompleteViaStream(context.Background(), "inst", failing); err == nil {
		t.Fatal("open error must surface")
	}
	registerTestSchemes()
	if !RequiresStreamingComplete(registry.Resolved{Transport: registry.Transport{Auth: "test-runner-preparer"}}) || RequiresStreamingComplete(registry.Resolved{Transport: registry.Transport{Auth: registry.AuthBearer}}) {
		t.Fatal("RequiresStreamingComplete must follow the scheme's preparer")
	}
}
