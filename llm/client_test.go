package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apilog "primeradiant.com/evener/llm/apilog"
)

type fakeAdapter struct {
	name string
}

func (a *fakeAdapter) Name() string { return a.name }
func (a *fakeAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *fakeAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, ErrStreamUnsupported
}

type recordReqAdapter struct {
	name    string
	lastReq Request
	mu      sync.Mutex
}

func (a *recordReqAdapter) Name() string { return a.name }
func (a *recordReqAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	a.mu.Lock()
	a.lastReq = req
	a.mu.Unlock()
	return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *recordReqAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	a.mu.Lock()
	a.lastReq = req
	a.mu.Unlock()

	sctx, cancel := context.WithCancel(ctx)
	_ = sctx
	s := NewChanStream(cancel)
	go func() {
		defer s.CloseSend()
		s.Send(StreamEvent{Type: StreamEventStreamStart})
		s.Send(StreamEvent{Type: StreamEventFinish, Response: &Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}})
	}()
	return s, nil
}

type planReqAdapter struct {
	name          string
	lastReq       Request
	planCalls     int
	completeCalls int
	streamCalls   int
}

func (a *planReqAdapter) Name() string { return a.name }
func (a *planReqAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	_ = req
	a.completeCalls++
	return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *planReqAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	a.streamCalls++
	return nil, ErrStreamUnsupported
}
func (a *planReqAdapter) PlanResponsesContinuation(req Request) (ResponsesContinuationPlan, error) {
	a.lastReq = req
	a.planCalls++
	return PlanResponsesContinuation(ResponsesContinuationPlanInput{
		EndpointFamily: ResponsesEndpointFamilyOpenAIPublic,
		AuthScopeIdentity: AuthScopeIdentity{
			Version:        "cont-scope-v1",
			AuthSource:     "api_key",
			CredentialHash: "cont-scope-v1:credential:abc",
		},
		Request: req,
	}), nil
}

type stepAdapter struct {
	name  string
	i     int
	steps []func() (Response, error)
}

func (a *stepAdapter) Name() string { return a.name }
func (a *stepAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	_ = req
	if a.i >= len(a.steps) {
		return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
	}
	fn := a.steps[a.i]
	a.i++
	return fn()
}
func (a *stepAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, ErrStreamUnsupported
}

func TestClient_DefaultProviderRouting(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "openai" {
		t.Fatalf("provider: %q", resp.Provider)
	}
}

func TestClient_DefaultAdapterTimeout_AppliedWhenNil(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	a := &recordReqAdapter{name: "openai"}
	c.Register(a)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	a.mu.Lock()
	got := a.lastReq.AdapterTimeout
	a.mu.Unlock()

	if got == nil {
		t.Fatal("expected AdapterTimeout to be set by default")
	}
	want := DefaultAdapterTimeout()
	if got.Connect != want.Connect || got.Request != want.Request || got.StreamRead != want.StreamRead {
		t.Fatalf("AdapterTimeout: got=%+v want=%+v", *got, want)
	}
}

func TestClient_DefaultAdapterTimeout_DoesNotOverrideExplicit(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	a := &recordReqAdapter{name: "openai"}
	c.Register(a)

	explicit := &AdapterTimeout{Connect: 123 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}, AdapterTimeout: explicit}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	a.mu.Lock()
	got := a.lastReq.AdapterTimeout
	a.mu.Unlock()

	if got != explicit {
		t.Fatalf("expected explicit AdapterTimeout pointer to be preserved")
	}
}

// TestClient_GoogleProviderRoutes verifies that the "google" provider key routes
// to the registered "google" adapter directly. After PRI-1880, the Gemini profile
// id is "google" so req.Provider=="google" hits c.overrides["google"] without any
// rewrite. The old gemini→google alias in normalizeProviderName is removed.
func TestClient_GoogleProviderRoutes(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	c.Register(&fakeAdapter{name: "google"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Complete(ctx, Request{Provider: "google", Model: "gemini-2.5-pro", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "google" {
		t.Fatalf("provider: %q", resp.Provider)
	}
}

func TestClient_UnknownProviderError(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Provider: "missing", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if _, ok := errors.AsType[*ConfigurationError](err); !ok {
		t.Fatalf("expected ConfigurationError, got %T", err)
	}
}

func TestClient_NoProviderConfiguredError(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if _, ok := errors.AsType[*ConfigurationError](err); !ok {
		t.Fatalf("expected ConfigurationError, got %T", err)
	}
}

func TestClient_PlanResponsesContinuation_RoutesExplicitProvider(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	a := &planReqAdapter{name: "openai"}
	c.Register(a)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	plan, err := c.PlanResponsesContinuation(ctx, Request{Provider: "openai", Model: "gpt-5.4"})
	if err != nil {
		t.Fatalf("PlanResponsesContinuation: %v", err)
	}
	if plan.EndpointFamily != ResponsesEndpointFamilyOpenAIPublic {
		t.Fatalf("EndpointFamily = %q", plan.EndpointFamily)
	}
	if a.planCalls != 1 || a.completeCalls != 0 || a.streamCalls != 0 {
		t.Fatalf("calls plan/complete/stream = %d/%d/%d, want 1/0/0", a.planCalls, a.completeCalls, a.streamCalls)
	}
	if a.lastReq.Provider != "openai" || a.lastReq.Model != "gpt-5.4" {
		t.Fatalf("lastReq = %+v", a.lastReq)
	}
}

func TestClient_PlanResponsesContinuation_UsesDefaultProvider(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	a := &planReqAdapter{name: "openai"}
	c.Register(a)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.PlanResponsesContinuation(ctx, Request{Model: "gpt-5.4"}); err != nil {
		t.Fatalf("PlanResponsesContinuation: %v", err)
	}
	if a.lastReq.Provider != "openai" {
		t.Fatalf("lastReq.Provider = %q, want openai", a.lastReq.Provider)
	}
}

func TestClient_PlanResponsesContinuation_UnknownProvider(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.PlanResponsesContinuation(ctx, Request{Provider: "missing", Model: "m"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := errors.AsType[*ConfigurationError](err); !ok {
		t.Fatalf("expected ConfigurationError, got %T", err)
	}
}

func TestClient_PlanResponsesContinuation_UnsupportedProvider(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.PlanResponsesContinuation(ctx, Request{Provider: "openai", Model: "m"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if _, ok := errors.AsType[*ConfigurationError](err); !ok {
		t.Fatalf("expected ConfigurationError, got %T", err)
	}
}

func TestClient_Complete_DoesNotRetryAutomatically(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	a := &stepAdapter{
		name: "openai",
		steps: []func() (Response, error){
			func() (Response, error) { return Response{}, err429 },
			func() (Response, error) {
				return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
			},
		},
	}
	c.Register(a)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Provider: "openai", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if a.i != 1 {
		t.Fatalf("adapter calls: got %d want 1", a.i)
	}
}

func TestClient_MiddlewareChainOrder(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	var order []string
	c.Use(
		MiddlewareFunc{
			Complete: func(ctx context.Context, req Request, next CompleteFunc) (Response, error) {
				order = append(order, "mw1:req")
				resp, err := next(ctx, req)
				order = append(order, "mw1:resp")
				return resp, err
			},
		},
		MiddlewareFunc{
			Complete: func(ctx context.Context, req Request, next CompleteFunc) (Response, error) {
				order = append(order, "mw2:req")
				resp, err := next(ctx, req)
				order = append(order, "mw2:resp")
				return resp, err
			},
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Complete(ctx, Request{Provider: "openai", Model: "m", Messages: []Message{User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Registration order on request; reverse order on response.
	want := []string{"mw1:req", "mw2:req", "mw2:resp", "mw1:resp"}
	if len(order) != len(want) {
		t.Fatalf("order: got %v want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d]: got %q want %q (full=%v)", i, order[i], want[i], order)
		}
	}
}

type streamAdapter struct {
	name  string
	calls int
	fail  bool
}

func (a *streamAdapter) Name() string { return a.name }
func (a *streamAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	return Response{Provider: a.name, Model: req.Model, Message: Assistant("ok")}, nil
}
func (a *streamAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = req
	a.calls++
	if a.fail {
		return nil, ErrorFromHTTPStatus(a.name, 429, "rate limited", nil, nil)
	}
	sctx, cancel := context.WithCancel(ctx)
	_ = sctx
	s := NewChanStream(cancel)
	go func() {
		defer s.CloseSend()
		s.Send(StreamEvent{Type: StreamEventStreamStart})
		s.Send(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
		s.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Hello"})
		s.Send(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})
		r := Response{Provider: a.name, Model: "m", Message: Assistant("Hello"), Finish: FinishReason{Reason: "stop"}}
		rp := r
		s.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &rp})
	}()
	return s, nil
}

type wrappedStream struct {
	inner  Stream
	events chan StreamEvent
	done   chan struct{}
	once   sync.Once
}

func wrapStream(inner Stream, onEvent func(StreamEvent)) *wrappedStream {
	w := &wrappedStream{
		inner:  inner,
		events: make(chan StreamEvent, 32),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		defer close(w.events)
		for ev := range inner.Events() {
			if onEvent != nil {
				onEvent(ev)
			}
			w.events <- ev
		}
	}()
	return w
}

func (s *wrappedStream) Events() <-chan StreamEvent { return s.events }
func (s *wrappedStream) Close() error {
	var err error
	s.once.Do(func() { err = s.inner.Close() })
	<-s.done
	return err
}

// nonClosingStream is an adversarial Stream whose Close() deliberately does NOT
// close its events channel. A correct wrapper must own its own shutdown rather
// than relying on the inner stream's Close() to unblock its pump.
type nonClosingStream struct{ events chan StreamEvent }

func (s *nonClosingStream) Events() <-chan StreamEvent { return s.events }
func (s *nonClosingStream) Close() error               { return nil }

// TestProviderStampStream_CloseWithoutDraining verifies that Close() on the
// provider-stamping wrapper returns promptly and that Events() closes, even when
// the consumer never drains and the inner stream never closes its events
// channel. providerStampStream wraps every provider's stream, so a consumer that
// aborts mid-stream (Close without draining) must not leak the pump goroutine.
// (Ported from the ollama rewriteStream regression after F7 folded that wrapper
// away; the robustness now lives in the canonical wrapper.)
func TestProviderStampStream_CloseWithoutDraining(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	inner := &nonClosingStream{events: make(chan StreamEvent)}
	ps := newProviderStampStream(inner, "ollama")

	// out is buffered at 128. Sending 129 events to the unbuffered inner channel
	// fills the buffer and parks the pump on the 129th forward-to-out; when the
	// 129th send returns, the pump is deterministically blocked mid-forward.
	for range 129 {
		inner.events <- StreamEvent{Type: StreamEventTextDelta, Delta: "x"}
	}

	closed := make(chan error, 1)
	go func() { closed <- ps.Close() }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung — pump did not respond to the shutdown signal")
	}

	drained := make(chan struct{})
	go func() {
		for range ps.Events() {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("Events() did not close after Close() — pump goroutine leaked")
	}
}

// TestClient_Stream_RefusesANilStreamFromAnAdapter verifies that an adapter
// returning a nil stream with no error cannot reach the stamping wrapper.
// pump reads the inner stream's channel on every iteration, so a nil inner
// dereferences it and kills the process; an adapter is a plugin, and a plugin
// that reports success with nothing to read is a broken contract, not a
// stream. The caller gets an error it can act on instead of a crash.
func TestClient_Stream_RefusesANilStreamFromAnAdapter(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls its neighbours with a shared t.
	c := NewClient()
	c.Register(&nilStreamAdapter{name: "openai"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.Stream(ctx, Request{Provider: "openai", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("Stream returned a nil stream with no error: st=%v", st)
	}
	if st != nil {
		t.Fatalf("Stream returned a stream alongside its error: %v", st)
	}
}

// nilStreamAdapter reports success with no stream, the shape a benchmark-only
// adapter has.
type nilStreamAdapter struct{ name string }

func (a *nilStreamAdapter) Name() string { return a.name }

func (a *nilStreamAdapter) Complete(context.Context, Request) (Response, error) {
	return Response{Provider: a.name, Message: Assistant("done")}, nil
}

func (a *nilStreamAdapter) Stream(context.Context, Request) (Stream, error) { return nil, nil }

// TestClient_Stream_LogsARefusedNilStreamAsAFailure verifies that the durable
// API log records the refusal, not a successful call that produced nothing.
// The refusal has to happen inside the adapter handler: middleware wraps that
// handler, and APILogger settles the call as soon as the handler returns, so a
// guard above the middleware would leave a success in the log for a call that
// never produced a stream.
func TestClient_Stream_LogsARefusedNilStreamAsAFailure(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls its neighbours with a shared t.
	dir := t.TempDir()
	logger, err := NewSessionAPILogger(dir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	if err := logger.ReserveSession("s1"); err != nil {
		t.Fatalf("ReserveSession: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.ReleaseSession("s1")
		_ = logger.Close()
	})

	c := NewClient()
	c.Register(&nilStreamAdapter{name: "openai"})
	c.Use(logger)

	ctx := WithAPILogContext(context.Background(), "s1")
	if _, err := c.Stream(ctx, Request{Provider: "openai", Model: "m", Messages: []Message{User("hi")}}); err == nil {
		t.Fatal("Stream accepted a nil stream from an adapter")
	}

	body, err := os.ReadFile(filepath.Join(dir, "sessions", "s1.api.jsonl"))
	if err != nil {
		t.Fatalf("read api log: %v", err)
	}
	var outcome string
	for line := range strings.SplitSeq(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Kind    string `json:"kind"`
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode api log line: %v", err)
		}
		if rec.Kind == "attempt_group_settlement" {
			outcome = rec.Outcome
		}
	}
	if outcome == "" {
		t.Fatalf("no settlement record written to the API log:\n%s", body)
	}
	if outcome == string(apilog.AttemptSuccess) {
		t.Fatalf("API log recorded a refused nil stream as %q:\n%s", outcome, body)
	}
}

func TestClient_Stream_DoesNotRetryAutomatically(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	a := &streamAdapter{name: "openai", fail: true}
	c.Register(a)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Stream(ctx, Request{Provider: "openai", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if a.calls != 1 {
		t.Fatalf("adapter calls: got %d want 1", a.calls)
	}
}

// --- 9a: Optional adapter interfaces ---

type closableFakeAdapter struct {
	fakeAdapter
	closed bool
}

func (a *closableFakeAdapter) Close() error {
	a.closed = true
	return nil
}

type initializableFakeAdapter struct {
	fakeAdapter
	initialized bool
}

func (a *initializableFakeAdapter) Initialize(ctx context.Context) error {
	a.initialized = true
	return nil
}

// blockingInitAdapter's Initialize closes started the instant it begins and
// then blocks on release, letting a test synchronize on "initialization is
// in progress" without sleeping.
type blockingInitAdapter struct {
	fakeAdapter
	started chan struct{}
	release chan struct{}
}

func (a *blockingInitAdapter) Initialize(context.Context) error {
	close(a.started)
	<-a.release
	return nil
}

func TestClient_Close_CallsClosableAdapters(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	closable := &closableFakeAdapter{name: "closable"}
	nonClosable := &fakeAdapter{name: "nonclosable"}
	c.Register(closable)
	c.Register(nonClosable)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !closable.closed {
		t.Fatalf("expected closable adapter to be closed")
	}
}

func TestClient_Initialize_CallsInitializableAdapters(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	initable := &initializableFakeAdapter{name: "initable"}
	nonInitable := &fakeAdapter{name: "plain"}
	c.Register(initable)
	c.Register(nonInitable)

	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !initable.initialized {
		t.Fatalf("expected initializable adapter to be initialized")
	}
}

func TestClient_Register_CallsInitialize(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	adapter := &initializableFakeAdapter{name: "test"}
	c := NewClient()
	c.Register(adapter)
	if !adapter.initialized {
		t.Fatal("Register should call Initialize on adapters that implement Initializer")
	}
}

func TestClient_Register_NonInitializable_NoPanic(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// Registering a plain adapter that does not implement Initializer should not panic.
	c := NewClient()
	c.Register(&fakeAdapter{name: "plain"})
}

// TestClient_Register_DispatchCannotReachAdapterBeforeInitializeReturns pins
// the ordering Complete's dispatch depends on: an adapter is only reachable
// by name once its Initializer has returned. A blocking Initialize proves
// this deterministically — while Register is still inside Initialize,
// neither Complete nor ProviderNames may see the adapter, whatever else is
// running concurrently; once Initialize returns, both do.
func TestClient_Register_DispatchCannotReachAdapterBeforeInitializeReturns(t *testing.T) {
	t.Parallel()
	adapter := &blockingInitAdapter{
		name:    "blockinit",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := NewClient()

	registerDone := make(chan struct{})
	go func() {
		c.Register(adapter)
		close(registerDone)
	}()

	<-adapter.started // Initialize is running.

	req := Request{Provider: "blockinit", Model: "m", Messages: []Message{User("hi")}}
	if _, err := c.Complete(context.Background(), req); err == nil {
		t.Fatal("Complete reached the adapter while its Initialize was still running")
	}
	if names := c.ProviderNames(); len(names) != 0 {
		t.Fatalf("ProviderNames = %v while Initialize was still running, want none", names)
	}

	close(adapter.release) // let Initialize return
	<-registerDone

	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete after Initialize returned: %v", err)
	}
	if resp.Provider != "blockinit" {
		t.Fatalf("Complete resp.Provider = %q, want blockinit", resp.Provider)
	}
}

// TestClient_LookupNormalizesProviderCase verifies that provider lookup
// is case-insensitive: a CLI passing --provider OLLAMA must still hit
// the registered "ollama" adapter. Without normalization, callers that
// forward unmodified user input would silently fail.
func TestClient_LookupNormalizesProviderCase(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	c.Register(&fakeAdapter{name: "ollama"})

	if got := normalizeProviderName("OLLAMA"); got != "ollama" {
		t.Errorf("normalizeProviderName(\"OLLAMA\") = %q, want ollama", got)
	}
	if got := normalizeProviderName("Ollama"); got != "ollama" {
		t.Errorf("normalizeProviderName(\"Ollama\") = %q, want ollama", got)
	}
	if got := normalizeProviderName("  OLLAMA  "); got != "ollama" {
		t.Errorf("normalizeProviderName whitespace-trimmed lowercase = %q", got)
	}
	if got := normalizeProviderName("Gemini"); got != "gemini" {
		t.Errorf("normalizeProviderName(\"Gemini\") = %q, want gemini (lowercase only, no rewrite)", got)
	}

	// End-to-end: a non-streaming Complete with mixed-case provider must
	// route to the registered lowercase adapter.
	resp, err := c.Complete(context.Background(), Request{
		Provider: "OLLAMA",
		Model:    "llama3.1:8b",
		Messages: []Message{User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete with mixed-case provider: %v", err)
	}
	if resp.Provider != "ollama" {
		t.Errorf("resp.Provider = %q, want ollama", resp.Provider)
	}
}

// TestClient_HasProvider pins the case-insensitive "is this provider
// registered" check shared by every provider-name validation that needs one
// (--fast-cheap-model, --vision-model, and their session-side twin): a single
// Client method rather than three identical copies of this loop.
func TestClient_HasProvider(t *testing.T) {
	t.Parallel()
	var nilClient *Client
	if nilClient.HasProvider("openai") {
		t.Error("nil client should never have a provider")
	}

	c := NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	if !c.HasProvider("openai") {
		t.Error("expected registered provider to be found")
	}
	// Match is case-insensitive.
	if !c.HasProvider("OpenAI") {
		t.Error("provider match should be case-insensitive")
	}
	if c.HasProvider("anthropic") {
		t.Error("unregistered provider should not be found")
	}
}

// TestClient_ConcurrentRegisterAndSetDefaultProviderNoRace hammers Register
// and SetDefaultProvider against concurrent readers of the same state —
// ProviderNames, DefaultProvider, CanServe, and Complete's dispatch — to
// pin that a client stays safe to register providers on after it is already
// shared across goroutines. Run with -race: overrides is a plain map, so an
// unsynchronized Register racing a reader is a "concurrent map writes"
// fatal unconditionally, not just a finding the race detector reports.
func TestClient_ConcurrentRegisterAndSetDefaultProviderNoRace(t *testing.T) {
	t.Parallel()
	c := NewClient()

	const n = 64
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			c.Register(&fakeAdapter{name: fmt.Sprintf("prov%d", i)})
		})
	}
	for i := range n {
		wg.Go(func() {
			c.SetDefaultProvider(fmt.Sprintf("prov%d", i))
		})
	}
	for i := range n {
		wg.Go(func() {
			_ = c.ProviderNames()
			_ = c.DefaultProvider()
			_ = c.CanServe(fmt.Sprintf("prov%d", i), "model")
			_, _ = c.Complete(context.Background(), Request{
				Provider: fmt.Sprintf("prov%d", i),
				Model:    "model",
				Messages: []Message{User("hi")},
			})
		})
	}
	wg.Wait()

	if got := len(c.ProviderNames()); got != n {
		t.Fatalf("ProviderNames() len = %d, want %d (a lost registration under concurrent writes)", got, n)
	}
}

// TestNormalizeProviderName_GeminiNoRewrite verifies that after removing the
// gemini→google routing alias, normalizeProviderName("gemini") returns "gemini"
// unchanged. Routing now relies on the profile id being "google" (PRI-1880).
func TestNormalizeProviderName_GeminiNoRewrite(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	if got := normalizeProviderName("gemini"); got != "gemini" {
		t.Errorf("normalizeProviderName(\"gemini\") = %q, want \"gemini\" (no rewrite after PRI-1880)", got)
	}
	if got := normalizeProviderName("Gemini"); got != "gemini" {
		t.Errorf("normalizeProviderName(\"Gemini\") = %q, want \"gemini\" (lowercase only, no rewrite)", got)
	}
	if got := normalizeProviderName("GEMINI"); got != "gemini" {
		t.Errorf("normalizeProviderName(\"GEMINI\") = %q, want \"gemini\"", got)
	}
}

type instanceAdapter struct {
	typeName    string // hardcoded type, e.g. "openaicompat"
	errToReturn error  // if set, Complete returns this error and Stream emits it
}

func (a *instanceAdapter) Name() string { return a.typeName }

func (a *instanceAdapter) Complete(_ context.Context, req Request) (Response, error) {
	if a.errToReturn != nil {
		return Response{}, a.errToReturn
	}
	return Response{Provider: a.typeName, Model: req.Model, Message: Assistant("ok")}, nil
}

func (a *instanceAdapter) Stream(_ context.Context, req Request) (Stream, error) {
	if a.errToReturn != nil {
		// Emit the error as a stream event (as real adapters do at runtime).
		sctx, cancel := context.WithCancel(context.Background())
		_ = sctx
		s := NewChanStream(cancel)
		go func() {
			defer s.CloseSend()
			s.Send(StreamEvent{Type: StreamEventError, Err: a.errToReturn})
		}()
		return s, nil //nolint:nilerr // error is surfaced via a stream event, as real adapters do
	}
	sctx, cancel := context.WithCancel(context.Background())
	_ = sctx
	s := NewChanStream(cancel)
	go func() {
		defer s.CloseSend()
		resp := Response{Provider: a.typeName, Model: req.Model, Message: Assistant("ok")}
		s.Send(StreamEvent{Type: StreamEventFinish, Response: &resp})
	}()
	return s, nil
}

func TestClient_Complete_StampsInstanceNameOnResponse(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	// Register adapter under instance name "work"; it hardcodes "openaicompat".
	adapter := &instanceAdapter{typeName: "openaicompat"}
	c.overrides["work"] = adapter
	c.pinnedDefault = "work"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Complete(ctx, Request{Provider: "work", Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "work" {
		t.Fatalf("resp.Provider = %q, want \"work\"", resp.Provider)
	}
}

func TestClient_Complete_StampsInstanceNameOnError(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	// Adapter hardcodes "openaicompat" in its error; instance name is "work".
	adapterErr := ErrorFromHTTPStatus("openaicompat", 429, "rate limited", nil, nil)
	adapter := &instanceAdapter{typeName: "openaicompat", errToReturn: adapterErr}
	c.overrides["work"] = adapter
	c.pinnedDefault = "work"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Provider: "work", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var llmErr Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected llm.Error, got %T: %v", err, err)
	}
	if llmErr.Provider() != "work" {
		t.Fatalf("error provider = %q, want \"work\"", llmErr.Provider())
	}
}

func TestClient_Complete_PreservesEmptyProviderError(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// Errors with empty Provider() (e.g. context.Canceled wrappers) must not
	// be stamped — RewriteErrorProvider no-ops on them.
	c := NewClient()
	// A ConfigurationError has Provider()==""; the client must leave it alone.
	adapter := &instanceAdapter{typeName: "openaicompat", errToReturn: &ConfigurationError{Message: "simulated"}}
	c.overrides["work"] = adapter
	c.pinnedDefault = "work"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Provider: "work", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var llmErr Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected llm.Error, got %T: %v", err, err)
	}
	if llmErr.Provider() != "" {
		t.Fatalf("error provider = %q, want \"\" (empty provider must not be stamped)", llmErr.Provider())
	}
}

func TestClient_Stream_StampsInstanceNameOnFinishResponse(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	adapter := &instanceAdapter{typeName: "openaicompat"}
	c.overrides["work"] = adapter
	c.pinnedDefault = "work"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.Stream(ctx, Request{Provider: "work", Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var finishResp *Response
	for ev := range st.Events() {
		if ev.Type == StreamEventFinish && ev.Response != nil {
			finishResp = ev.Response
		}
	}
	if finishResp == nil {
		t.Fatal("no StreamEventFinish with Response received")
	}
	if finishResp.Provider != "work" {
		t.Fatalf("finish response provider = %q, want \"work\"", finishResp.Provider)
	}
}

func TestClient_Stream_StampsInstanceNameOnErrorEvent(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	adapterErr := ErrorFromHTTPStatus("openaicompat", 500, "server error", nil, nil)
	adapter := &instanceAdapter{typeName: "openaicompat", errToReturn: adapterErr}
	c.overrides["work"] = adapter
	c.pinnedDefault = "work"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.Stream(ctx, Request{Provider: "work", Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Stream open: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var gotErr error
	for ev := range st.Events() {
		if ev.Type == StreamEventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected StreamEventError, got none")
	}
	var llmErr Error
	if !errors.As(gotErr, &llmErr) {
		t.Fatalf("expected llm.Error, got %T: %v", gotErr, gotErr)
	}
	if llmErr.Provider() != "work" {
		t.Fatalf("stream error provider = %q, want \"work\"", llmErr.Provider())
	}
}

func TestClient_Stream_PreservesEmptyProviderErrorEvent(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	// ConfigurationError has Provider()=="" — must not be stamped.
	adapter := &instanceAdapter{typeName: "openaicompat", errToReturn: &ConfigurationError{Message: "simulated"}}
	c.overrides["work"] = adapter
	c.pinnedDefault = "work"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.Stream(ctx, Request{Provider: "work", Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Stream open: %v", err)
	}
	defer st.Close() //nolint:errcheck

	var gotErr error
	for ev := range st.Events() {
		if ev.Type == StreamEventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected StreamEventError, got none")
	}
	var llmErr Error
	if !errors.As(gotErr, &llmErr) {
		t.Fatalf("expected llm.Error, got %T: %v", gotErr, gotErr)
	}
	if llmErr.Provider() != "" {
		t.Fatalf("stream error provider = %q, want \"\" (empty provider must not be stamped)", llmErr.Provider())
	}
}

func TestClient_Stream_MiddlewareChainOrder(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	c := NewClient()
	a := &streamAdapter{name: "openai"}
	c.Register(a)

	var mu sync.Mutex
	var order []string
	log := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}

	c.Use(
		MiddlewareFunc{
			Stream: func(ctx context.Context, req Request, next StreamFunc) (Stream, error) {
				log("mw1:req")
				st, err := next(ctx, req)
				if err != nil {
					return nil, err
				}
				return wrapStream(st, func(ev StreamEvent) { log("mw1:ev:" + string(ev.Type)) }), nil
			},
		},
		MiddlewareFunc{
			Stream: func(ctx context.Context, req Request, next StreamFunc) (Stream, error) {
				log("mw2:req")
				st, err := next(ctx, req)
				if err != nil {
					return nil, err
				}
				return wrapStream(st, func(ev StreamEvent) { log("mw2:ev:" + string(ev.Type)) }), nil
			},
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.Stream(ctx, Request{Provider: "openai", Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck

	for range st.Events() {
		// drain
	}

	wantPrefix := []string{"mw1:req", "mw2:req"}
	if len(order) < len(wantPrefix) {
		t.Fatalf("order: got %v want prefix %v", order, wantPrefix)
	}
	for i := range wantPrefix {
		if order[i] != wantPrefix[i] {
			t.Fatalf("order[%d]: got %q want %q (full=%v)", i, order[i], wantPrefix[i], order)
		}
	}

	// For each event, middleware sees it in reverse order.
	// Adapter emits 5 events.
	wantEvents := []StreamEventType{
		StreamEventStreamStart,
		StreamEventTextStart,
		StreamEventTextDelta,
		StreamEventTextEnd,
		StreamEventFinish,
	}

	// Each middleware should observe the full event sequence in order.
	extract := func(prefix string) []StreamEventType {
		var out []StreamEventType
		for _, it := range order {
			if !strings.HasPrefix(it, prefix) {
				continue
			}
			out = append(out, StreamEventType(strings.TrimPrefix(it, prefix)))
		}
		return out
	}
	mw2Seen := extract("mw2:ev:")
	mw1Seen := extract("mw1:ev:")
	if len(mw2Seen) != len(wantEvents) || len(mw1Seen) != len(wantEvents) {
		t.Fatalf("event counts: mw2=%v mw1=%v want=%v (order=%v)", mw2Seen, mw1Seen, wantEvents, order)
	}
	for i := range wantEvents {
		if mw2Seen[i] != wantEvents[i] || mw1Seen[i] != wantEvents[i] {
			t.Fatalf("event order: mw2=%v mw1=%v want=%v (order=%v)", mw2Seen, mw1Seen, wantEvents, order)
		}
	}

	// Reverse-order event observation constraint: for each event type, mw2 must log it before mw1.
	indexOf := func(s string) int {
		for i := range order {
			if order[i] == s {
				return i
			}
		}
		return -1
	}
	for _, ev := range wantEvents {
		i2 := indexOf("mw2:ev:" + string(ev))
		i1 := indexOf("mw1:ev:" + string(ev))
		if i2 == -1 || i1 == -1 {
			t.Fatalf("missing event logs for %s (order=%v)", ev, order)
		}
		if i2 > i1 {
			t.Fatalf("expected mw2 to observe %s before mw1; mw2=%d mw1=%d (order=%v)", ev, i2, i1, order)
		}
	}
}

// ReleaseSessionAPILog asks every middleware that routes session API logs to
// release the session, and reports what they fail with.
func TestReleaseSessionAPILogReleasesEveryMiddleware(t *testing.T) {
	t.Parallel()
	releaseErr := errors.New("release failed")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"released", nil},
		{"release fails", releaseErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient()
			var released []string
			c.Use(&sessionReleasingMiddleware{onRelease: func(id string) error {
				released = append(released, id)
				return tc.err
			}})
			if err := c.ReleaseSessionAPILog("sess-a"); !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
				t.Fatalf("ReleaseSessionAPILog = %v, want %v", err, tc.err)
			}
			if len(released) != 1 || released[0] != "sess-a" {
				t.Fatalf("released = %v, want [sess-a]", released)
			}
		})
	}
}

// sessionReleasingMiddleware implements Middleware and sessionAPILogReleaser.
type sessionReleasingMiddleware struct {
	onRelease func(string) error
}

func (m *sessionReleasingMiddleware) WrapComplete(next CompleteFunc) CompleteFunc { return next }
func (m *sessionReleasingMiddleware) WrapStream(next StreamFunc) StreamFunc       { return next }
func (m *sessionReleasingMiddleware) ReleaseSession(id string) error              { return m.onRelease(id) }

// A provider operation begun without a context still gets one, owning a fresh
// attempt group bound to the client's sink middleware.
func TestBeginProviderOperationOwnsAGroupForANilContext(t *testing.T) {
	t.Parallel()
	c := NewClient()
	sink := &sinkMiddleware{sink: &recordingAPIAttemptSink{}}
	c.Use(sink)
	var input context.Context
	ctx, op := c.beginProviderOperation(input)
	if ctx == nil {
		t.Fatal("beginProviderOperation(nil) should return a non-nil context")
	}
	if op == nil {
		t.Fatal("beginProviderOperation should return a non-nil operation when a sink is present")
	}
	if !op.ownsGroup || op.group == nil || apiAttemptGroupFromContext(ctx) != op.group {
		t.Fatalf("operation = %+v, context group = %p; want one owned attached group", op, apiAttemptGroupFromContext(ctx))
	}
	state, _ := ctx.Value(apiAttemptSinkContextKey{}).(apiAttemptSinkContext)
	if state.sink != sink {
		t.Fatalf("context sink = %T %p, want middleware %p", state.sink, state.sink, sink)
	}
}

type sinkMiddleware struct {
	sink *recordingAPIAttemptSink
}

func (m *sinkMiddleware) WrapComplete(next CompleteFunc) CompleteFunc { return next }
func (m *sinkMiddleware) WrapStream(next StreamFunc) StreamFunc       { return next }
func (m *sinkMiddleware) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error {
	return m.sink.AppendAttempt(ctx, rec)
}
func (m *sinkMiddleware) AppendSettlement(ctx context.Context, rec apilog.APIAttemptGroupSettlement) error {
	return m.sink.AppendSettlement(ctx, rec)
}

// A nil client has no middleware to read: the snapshot helper is what every
// entry point consults, so it must answer nil rather than dereference.
func TestNilClientMiddlewareSnapshotIsEmpty(t *testing.T) {
	t.Parallel()
	var c *Client
	if got := c.middlewareSnapshot(); got != nil {
		t.Fatalf("nil client middleware = %v, want nil", got)
	}
}
