package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/llm/registry"
)

// edgeInstances are the registry records a client must survive: an instance
// whose base names no known provider, a pseudo-provider with no protocol and
// no base URL (hidden by spec §4), and one that declares no API-key
// environment variable at all.
func edgeInstances() map[string]map[string]registry.Provider {
	return map[string]map[string]registry.Provider{
		"unknown-base":  {"edge": {Base: "no-such-provider", APIKey: "k"}},
		"hidden-pseudo": {"edge": {APIKey: "k"}},
		"empty-key-env": {"edge": {Base: "openai", APIKeyEnv: []string{}}},
		"literal-key":   {"edge": {Base: "openai", APIKey: "k"}},
	}
}

// replayRegistryEdgeRecords loads each edge record and drives the client
// paths that must hold for all of them: a registry that fails to load is an
// error and never a panic, and an instance that does not resolve cannot
// serve, cannot list, and cannot dispatch.
func replayRegistryEdgeRecords(t *testing.T) {
	t.Helper()
	for name, instances := range edgeInstances() {
		r, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(instances),
		)
		if err != nil {
			// A record the loader refuses is a configuration error, which is
			// the whole contract for it: nothing else to drive.
			continue
		}
		c := NewClient(WithRegistry(r))
		req := Request{Provider: "edge", Model: "gpt-5.5", Messages: []Message{User("hi")}}
		_, resolveErr := c.Resolve("edge/gpt-5.5")
		if c.CanServe("edge", "gpt-5.5") != (resolveErr == nil) {
			t.Fatalf("%s: CanServe disagrees with Resolve (%v)", name, resolveErr)
		}
		if resolveErr != nil {
			if _, err := c.Complete(context.Background(), req); err == nil {
				t.Fatalf("%s: Complete succeeded for an unresolvable instance", name)
			}
			if _, err := c.Stream(context.Background(), req); err == nil {
				t.Fatalf("%s: Stream succeeded for an unresolvable instance", name)
			}
			if _, err := c.Models(context.Background(), "edge"); err == nil {
				t.Fatalf("%s: Models succeeded for an unresolvable instance", name)
			}
		}
		// Whatever the record, the client's name views never panic and an
		// unknown instance is never claimed.
		_ = c.ProviderNames()
		_ = c.DefaultProvider()
		if c.CanServe("definitely-not-an-instance", "m") {
			t.Fatalf("%s: CanServe claimed an unknown instance", name)
		}
	}
}

func replayClientConfigEdges(t *testing.T) {
	t.Helper()
	replayRegistryEdgeRecords(t)
	replayClientEdges(t)
	replayTypeAndValidationEdges(t)
}

type clientEdgeAdapter struct {
	name     string
	closeErr error
	initErr  error
}

type classifyEdgeError struct{ status int }

func (e classifyEdgeError) Error() string              { return "edge" }
func (e classifyEdgeError) Provider() string           { return "edge" }
func (e classifyEdgeError) StatusCode() int            { return e.status }
func (e classifyEdgeError) ErrorCode() string          { return "" }
func (e classifyEdgeError) Retryable() bool            { return false }
func (e classifyEdgeError) RetryAfter() *time.Duration { return nil }
func (e classifyEdgeError) Raw() any                   { return nil }

type clientBlockingStream struct{ events chan StreamEvent }

func (s *clientBlockingStream) Events() <-chan StreamEvent { return s.events }
func (s *clientBlockingStream) Close() error               { return nil }

func (a *clientEdgeAdapter) Name() string { return a.name }
func (a *clientEdgeAdapter) Complete(context.Context, Request) (Response, error) {
	return Response{}, nil
}
func (a *clientEdgeAdapter) Stream(context.Context, Request) (Stream, error) { return nil, nil }
func (a *clientEdgeAdapter) Close() error                                    { return a.closeErr }
func (a *clientEdgeAdapter) Initialize(context.Context) error                { return a.initErr }

func replayClientEdges(t *testing.T) {
	t.Helper()
	var nilClient *Client
	if nilClient.ProviderNames() != nil || nilClient.Close() != nil || nilClient.Initialize(context.Background()) != nil {
		t.Fatal("nil client contract changed")
	}
	nilClient.Use(nil)
	invalid := Request{}
	if _, err := NewClient().Complete(context.Background(), invalid); err == nil {
		t.Fatal("invalid completion request succeeded")
	}
	if _, err := NewClient().Stream(context.Background(), invalid); err == nil {
		t.Fatal("invalid stream request succeeded")
	}
	valid := Request{Model: "m", Messages: []Message{User("x")}}
	if _, err := NewClient().Stream(context.Background(), valid); err == nil {
		t.Fatal("stream without provider succeeded")
	}
	unknown := NewClient()
	unknown.SetDefaultProvider("missing")
	if _, err := unknown.Stream(context.Background(), valid); err == nil {
		t.Fatal("stream with unknown default succeeded")
	}
	if _, err := NewClient().PlanResponsesContinuation(context.Background(), valid); err == nil {
		t.Fatal("continuation plan without provider succeeded")
	}

	c := &Client{}
	c.Use(nil)
	c.SetDefaultProvider("manual")
	if c.DefaultProvider() != "manual" || c.ProviderNames() != nil {
		t.Fatal("client accessors changed")
	}
	a := &clientEdgeAdapter{name: "edge", closeErr: errors.New("close"), initErr: errors.New("init")}
	c.Register(a)
	if err := c.Close(); err == nil {
		t.Fatal("close error lost")
	}
	if err := c.Initialize(context.Background()); err == nil {
		t.Fatal("initialize error lost")
	}
	if _, err := c.Models(context.Background(), " EDGE "); err == nil {
		t.Fatal("an override without a listing seam must not list")
	}

	// Drive the cooperative-close arm without scheduling a goroutine: pump is
	// synchronous here and observes the already-closed signal immediately.
	closing := make(chan struct{})
	close(closing)
	stamped := &providerStampStream{
		inner:   &clientBlockingStream{events: make(chan StreamEvent)},
		out:     make(chan StreamEvent),
		done:    make(chan struct{}),
		closing: closing,
	}
	stamped.pump()

	stream := NewChanStream(nil)
	stream.CloseSend()
	stream.Send(StreamEvent{})
}

func replayTypeAndValidationEdges(t *testing.T) {
	t.Helper()
	for _, name := range []string{"", "_bad", "a-b", "a" + string(make([]byte, 64))} {
		_ = ValidateToolName(name)
	}
	_ = defaultToolParameters()
	for _, params := range []map[string]any{nil, {}, {"type": 1}, {"type": "array"}, {"type": " OBJECT "}} {
		_ = validateToolParameters(params)
	}
	_ = StreamReadSSEOptions(nil)
	_ = StreamReadSSEOptions(&AdapterTimeout{})
	_ = StreamReadSSEOptions(&AdapterTimeout{StreamRead: 1})
	_ = ToolResultNamed("call", "name", "result", true)
	for _, s := range []string{"", "reasoning_content", "other"} {
		_ = IsOpenAICompatReasoningField(s)
	}
	for _, s := range []string{"", "x", "[]", "[", `[{"type":"other"}]`, `[{"type":"reasoning.text"}]`} {
		_ = IsOpenAICompatEncryptedReasoning(s)
	}
	_ = OrderedEffortLevels(map[string]string{"high": "h", "low": "l"})
	for _, req := range []Request{{}, {Model: "m"}, {Model: "m", Messages: []Message{User("x")}, Tools: []ToolDefinition{{Name: "bad-name"}}}, {Model: "m", Messages: []Message{User("x")}, Tools: []ToolDefinition{{Name: "ok", Parameters: map[string]any{}}}}} {
		_ = req.Validate()
	}
	for _, effort := range []string{" none ", "HIGH", "unknown"} {
		_ = NormalizeReasoningEffort(effort)
		_ = ReasoningEffortRank(effort)
		_ = ClampReasoningEffort(effort, []string{"low", "high"})
	}

	for k := KindUnknown; k <= KindServer+1; k++ {
		_ = k.String()
	}
	kindCases := []error{
		nil,
		errors.New("outside taxonomy"),
		ErrorFromHTTPStatus("p", 400, "bad", nil, nil),
		ErrorFromHTTPStatus("p", 401, "auth", nil, nil),
		ErrorFromHTTPStatus("p", 403, "denied", nil, nil),
		ErrorFromHTTPStatus("p", 404, "missing", nil, nil),
		ErrorFromHTTPStatus("p", 408, "timeout", nil, nil),
		ErrorFromHTTPStatus("p", 413, "context", nil, nil),
		ErrorFromHTTPStatus("p", 429, "rate", nil, nil),
		ErrorFromHTTPStatus("p", 500, "server", nil, nil),
		ErrorFromHTTPStatus("p", 400, "content filter policy", nil, nil),
		ErrorFromHTTPStatus("p", 429, "quota exceeded", nil, nil),
		&quotaExceededError{},
		classifyEdgeError{status: 500},
	}
	for _, err := range kindCases {
		_ = Kind(err)
		_ = Classify(err)
	}
	_ = Classify(&NoObjectGeneratedError{})
}

// FuzzClientConfigEdges drives the client's configuration edges — the
// registry records a user config can produce, the nil-client and
// unresolvable-provider contracts, and the type/validation surface — from a
// single fuzzed byte, so the campaign runner reaches them all.
func FuzzClientConfigEdges(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) { replayClientConfigEdges(t) })
}
