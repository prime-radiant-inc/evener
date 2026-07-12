package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/llm/providercfg"
)

func replayClientConfigEdges(t *testing.T) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"env-state-option", TestNewFromEnv_PassesStateDirOptionToFactories},
		{"env-state-default", TestNewFromEnv_UsesSERFStateDirEnvByDefault},
		{"env-state-home", TestNewFromEnv_PassesXDGStateHomeToFactories},
		{"env-factories", TestNewFromEnv_UsesRegisteredFactories},
		{"default-lazy", TestDefaultClient_LazyInitializationFromEnv},
		{"default-explicit", TestSetDefaultClient_OverridesLazyInit},
		{"providers-headers", TestNewFromProviders_ResolvesHeaderEnvRefs},
		{"providers-header-error", TestNewFromProviders_MissingHeaderVar_FailsInstance},
		{"providers-key", TestNewFromProviders_ResolvesAPIKeyEnvReferences},
		{"providers-key-error", TestNewFromProviders_MissingEnvKeyFailsInstance},
		{"providers-openai-key", TestNewFromProviders_OpenAIUnresolvedKeyDefersToFactory},
		{"providers-register", TestNewFromProviders_RegistersAllInstances},
		{"providers-default", TestNewFromProviders_DefaultIsSet},
		{"providers-tags", TestNewFromProviders_BehaviorTagsAreSet},
		{"providers-routing", TestNewFromProviders_RoutingReachesCorrectAdapter},
		{"providers-chat", TestNewFromProviders_ChatCompletionsStyleIsOpenAICompat},
		{"providers-auto", TestNewFromProviders_OpenAIAutoStyleKeepsOpenAIBehavior},
		{"providers-unknown", TestNewFromProviders_UnknownTypeErrors},
		{"providers-partial", TestNewFromAvailableProviders_SkippedDefaultNotElected},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.fn)
	}

	// Registry no-ops and env-factory outcomes are intentionally tiny and are
	// easier to make deterministic here than through process environment state.
	RegisterEnvAdapterFactory(nil)
	withEnvFactories(t, []EnvAdapterFactory{
		func(EnvConfig) (ProviderAdapter, bool, error) { return nil, false, nil },
	})
	if _, err := NewFromEnv(nil); err == nil {
		t.Fatal("NewFromEnv with no configured adapters unexpectedly succeeded")
	}
	withEnvFactories(t, []EnvAdapterFactory{
		func(EnvConfig) (ProviderAdapter, bool, error) { return nil, false, errors.New("env sentinel") },
	})
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("NewFromEnv factory error unexpectedly succeeded")
	}

	RegisterInstanceAdapterFactory("ignored", "", nil)
	withInstanceFactories(t, map[instanceFactoryKey]InstanceAdapterFactory{
		{typ: "edge"}: func(inst providercfg.InstanceConfig, _ string) (ProviderAdapter, error) {
			return &fakeAdapter{name: inst.Name}, nil
		},
	})
	c, errs, err := NewFromAvailableProviders(providercfg.Config{
		Instances: []providercfg.InstanceConfig{{Name: "edge", Type: "edge", APIStyle: "special"}},
	}, nil)
	if err != nil || len(errs) != 0 || c.DefaultProvider() != "edge" {
		t.Fatalf("catch-all factory = (%v, %v, %v)", c, errs, err)
	}
	instanceFactoriesMu.Lock()
	instanceFactories = map[instanceFactoryKey]InstanceAdapterFactory{
		{typ: "broken"}: func(providercfg.InstanceConfig, string) (ProviderAdapter, error) {
			return nil, errors.New("factory sentinel")
		},
	}
	instanceFactoriesMu.Unlock()
	brokenCfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "broken", Type: "broken"}}}
	if _, errs, err := NewFromAvailableProviders(brokenCfg, WithStateDir("unused")); err == nil || len(errs) != 1 {
		t.Fatalf("all-broken partial init = errs %v, err %v", errs, err)
	}
	if _, err := NewFromProviders(brokenCfg); err == nil {
		t.Fatal("strict factory failure unexpectedly succeeded")
	}

	replayClientEdges(t)
	replayTypeAndValidationEdges(t)
}

func withEnvFactories(t *testing.T, factories []EnvAdapterFactory) {
	t.Helper()
	envFactoriesMu.Lock()
	old := envFactories
	envFactories = factories
	envFactoriesMu.Unlock()
	t.Cleanup(func() {
		envFactoriesMu.Lock()
		envFactories = old
		envFactoriesMu.Unlock()
	})
}

func withInstanceFactories(t *testing.T, factories map[instanceFactoryKey]InstanceAdapterFactory) {
	t.Helper()
	instanceFactoriesMu.Lock()
	old := instanceFactories
	instanceFactories = factories
	instanceFactoriesMu.Unlock()
	t.Cleanup(func() {
		instanceFactoriesMu.Lock()
		instanceFactories = old
		instanceFactoriesMu.Unlock()
	})
}

type clientEdgeAdapter struct {
	name      string
	closeErr  error
	initErr   error
	toolOK    bool
	modelsErr error
}

type classifyEdgeError struct{ status int }

func (e classifyEdgeError) Error() string              { return "edge" }
func (e classifyEdgeError) Provider() string           { return "edge" }
func (e classifyEdgeError) BehaviorTag() string        { return "" }
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
func (a *clientEdgeAdapter) SupportsToolChoice(string) bool                  { return a.toolOK }
func (a *clientEdgeAdapter) ListModels(context.Context) ([]ModelInfo, error) { return nil, a.modelsErr }

func replayClientEdges(t *testing.T) {
	t.Helper()
	var nilClient *Client
	if nilClient.ProviderNames() != nil || nilClient.Close() != nil || nilClient.Initialize(context.Background()) != nil {
		t.Fatal("nil client contract changed")
	}
	if nilClient.SupportsToolChoice("p", "auto") {
		t.Fatal("nil client supports tools")
	}
	if _, err := nilClient.ListModels(context.Background(), "p"); err == nil {
		t.Fatal("nil client listed models")
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
	a := &clientEdgeAdapter{name: "edge", closeErr: errors.New("close"), initErr: errors.New("init"), modelsErr: errors.New("models")}
	c.Register(a)
	if err := c.Close(); err == nil {
		t.Fatal("close error lost")
	}
	if err := c.Initialize(context.Background()); err == nil {
		t.Fatal("initialize error lost")
	}
	if c.SupportsToolChoice("missing", "auto") || c.SupportsToolChoice("edge", "auto") {
		t.Fatal("tool support contract changed")
	}
	if _, err := c.ListModels(context.Background(), " EDGE "); err == nil {
		t.Fatal("model-list error lost")
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

func FuzzClientConfigEdges(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) { replayClientConfigEdges(t) })
}
