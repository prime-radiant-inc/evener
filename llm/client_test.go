package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestClient_ProviderAlias_GeminiRoutesToGoogle(t *testing.T) {
	c := NewClient()
	c.Register(&fakeAdapter{name: "google"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c.Complete(ctx, Request{Provider: "gemini", Model: "m", Messages: []Message{User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "google" {
		t.Fatalf("provider: %q", resp.Provider)
	}
}

func TestClient_UnknownProviderError(t *testing.T) {
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Provider: "missing", Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var ce *ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T", err)
	}
}

func TestClient_NoProviderConfiguredError(t *testing.T) {
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var ce *ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T", err)
	}
}

func TestClient_Complete_DoesNotRetryAutomatically(t *testing.T) {
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

func TestClient_Stream_DoesNotRetryAutomatically(t *testing.T) {
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

type toolChoiceFakeAdapter struct {
	fakeAdapter
}

func (a *toolChoiceFakeAdapter) SupportsToolChoice(mode string) bool {
	return mode == "auto" || mode == "required"
}

func TestClient_Close_CallsClosableAdapters(t *testing.T) {
	c := NewClient()
	closable := &closableFakeAdapter{fakeAdapter: fakeAdapter{name: "closable"}}
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
	c := NewClient()
	initable := &initializableFakeAdapter{fakeAdapter: fakeAdapter{name: "initable"}}
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
	adapter := &initializableFakeAdapter{fakeAdapter: fakeAdapter{name: "test"}}
	c := NewClient()
	c.Register(adapter)
	if !adapter.initialized {
		t.Fatal("Register should call Initialize on adapters that implement Initializer")
	}
}

func TestClient_Register_NonInitializable_NoPanic(t *testing.T) {
	// Registering a plain adapter that does not implement Initializer should not panic.
	c := NewClient()
	c.Register(&fakeAdapter{name: "plain"})
}

// TestClient_LookupNormalizesProviderCase verifies that provider lookup
// is case-insensitive: a CLI passing --provider OLLAMA must still hit
// the registered "ollama" adapter. Without normalization, callers that
// forward unmodified user input would silently fail.
func TestClient_LookupNormalizesProviderCase(t *testing.T) {
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
	if got := normalizeProviderName("Gemini"); got != "google" {
		t.Errorf("normalizeProviderName(\"Gemini\") = %q, want google (alias still works)", got)
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

func TestClient_SupportsToolChoice(t *testing.T) {
	c := NewClient()
	c.Register(&toolChoiceFakeAdapter{fakeAdapter: fakeAdapter{name: "openai"}})

	if !c.SupportsToolChoice("openai", "auto") {
		t.Fatalf("expected auto to be supported")
	}
	if !c.SupportsToolChoice("openai", "required") {
		t.Fatalf("expected required to be supported")
	}
	if c.SupportsToolChoice("openai", "named") {
		t.Fatalf("expected named to be unsupported")
	}
	// Non-implementing adapter returns true by default.
	c.Register(&fakeAdapter{name: "plain"})
	if !c.SupportsToolChoice("plain", "auto") {
		t.Fatalf("expected default true for non-implementing adapter")
	}
}

func TestClient_Stream_MiddlewareChainOrder(t *testing.T) {
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
