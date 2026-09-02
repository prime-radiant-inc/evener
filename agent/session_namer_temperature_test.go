package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Temperature gate (issue #834): the namer must not send temperature to models
// whose resolved row cannot vouch for it. A Bedrock-style wire id with no
// catalog row resolves to a synthesized row whose temperature baseline is
// "send" — nothing prunes it — and the provider 400s, costing one dead request
// per session.

// temperatureSpyAdapter records each request's temperature so tests can
// assert presence/absence across calls.
type temperatureSpyAdapter struct {
	name        string
	respondWith func(req llm.Request, call int) (llm.Response, error)
	calls       []llm.Request
}

func (a *temperatureSpyAdapter) Name() string { return a.name }
func (a *temperatureSpyAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.calls = append(a.calls, req)
	return a.respondWith(req, len(a.calls))
}
func (a *temperatureSpyAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// bedrockishRegistry builds a hermetic registry whose "bedrock" instance
// speaks the OpenAI responses protocol over a custom base URL — the shape from
// the issue: models resolve (so requests dispatch) but no catalog row exists,
// so every model on it resolves synthesized.
func bedrockishRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"bedrock": {
				Base: "openai", Protocol: registry.ProtocolOpenAIResponses, Surface: registry.SurfaceGeneric,
				APIKey: "test", Transport: registry.Transport{BaseURL: "https://bedrock.example.com/v1"},
			},
			// moonshotai carries provider-level fields with temperature=false
			// (providers_overlay.toml), so its models resolve with the
			// capability explicitly disabled.
			"moonshotai": {APIKey: "test"},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

// TestNameSession_OmitsTemperatureForSynthesizedRows: an unknown wire id
// (no catalog row, no live listing) resolves synthesized; its temperature
// capability is unknown, so the namer must omit the parameter entirely.
func TestNameSession_OmitsTemperatureForSynthesizedRows(t *testing.T) {
	t.Parallel()
	adapter := &temperatureSpyAdapter{
		name: "bedrock",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			if req.Temperature != nil {
				t.Errorf("call %d: namer sent temperature %v to a synthesized-row model", call, *req.Temperature)
			}
			return llm.Response{Message: llm.Assistant(`{"name":"Fix Parser Bug"}`)}, nil
		},
	}
	client := llm.NewClient(llm.WithRegistry(bedrockishRegistry(t)))
	client.Register(adapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "bedrock/us.openai.gpt-5.6-luna")
	got, err := nameSession(context.Background(), client, profile, sessionNameSourcePrompt, "fix the parser", "", noNamerSleep)
	if err != nil {
		t.Fatalf("nameSession: %v", err)
	}
	if got.Name != "Fix Parser Bug" {
		t.Fatalf("Name = %q, want Fix Parser Bug", got.Name)
	}
	if len(adapter.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no retry needed when temperature is gated)", len(adapter.calls))
	}
}

// TestNameSession_OmitsTemperatureForFalseCapability: moonshotai's catalog rows
// carry fields.temperature=false; the namer must omit the parameter.
func TestNameSession_OmitsTemperatureForFalseCapability(t *testing.T) {
	t.Parallel()
	adapter := &temperatureSpyAdapter{
		name: "moonshotai",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			if req.Temperature != nil {
				t.Errorf("call %d: namer sent temperature %v to a temperature=false model", call, *req.Temperature)
			}
			return llm.Response{Message: llm.Assistant(`{"name":"Fix Parser Bug"}`)}, nil
		},
	}
	client := llm.NewClient(llm.WithRegistry(bedrockishRegistry(t)))
	client.Register(adapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "moonshotai/kimi-k3")
	got, err := nameSession(context.Background(), client, profile, sessionNameSourcePrompt, "fix the parser", "", noNamerSleep)
	if err != nil {
		t.Fatalf("nameSession: %v", err)
	}
	if got.Name != "Fix Parser Bug" {
		t.Fatalf("Name = %q, want Fix Parser Bug", got.Name)
	}
}

// TestNameSession_SendsTemperatureForKnownCapability: the openai catalog row
// vouches for temperature; the namer still sends it (determinism for naming
// stays on models that support it).
func TestNameSession_SendsTemperatureForKnownCapability(t *testing.T) {
	t.Parallel()
	adapter := &temperatureSpyAdapter{
		name: "openai",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			if req.Temperature == nil {
				t.Errorf("call %d: namer omitted temperature on a model whose row supports it", call)
			}
			return llm.Response{Message: llm.Assistant(`{"name":"Fix Flaky Test"}`)}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	got, err := nameSession(context.Background(), client, WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano"), sessionNameSourcePrompt, "fix the flaky test", "", noNamerSleep)
	if err != nil {
		t.Fatalf("nameSession: %v", err)
	}
	if got.Name != "Fix Flaky Test" {
		t.Fatalf("Name = %q, want Fix Flaky Test", got.Name)
	}
}

// TestNameSession_RetriesOnceWithoutTemperature: a row that claims temperature
// support but the provider rejects it (Bedrock 400 "Unsupported parameter:
// 'temperature'") must be retried exactly once with the parameter dropped,
// and the naming must succeed on the retry.
func TestNameSession_RetriesOnceWithoutTemperature(t *testing.T) {
	t.Parallel()
	adapter := &temperatureSpyAdapter{
		name: "openai",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			if call == 1 {
				return llm.Response{}, llm.ErrorFromHTTPStatus(req.Provider, 400,
					"responses.create failed: Unsupported parameter: 'temperature' is not supported with this model.", nil, nil)
			}
			if req.Temperature != nil {
				t.Errorf("call %d: retry still carried temperature", call)
			}
			return llm.Response{Message: llm.Assistant(`{"name":"Fix Flaky Test"}`)}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	got, err := nameSession(context.Background(), client, WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano"), sessionNameSourcePrompt, "fix the flaky test", "", noNamerSleep)
	if err != nil {
		t.Fatalf("nameSession: %v", err)
	}
	if got.Name != "Fix Flaky Test" {
		t.Fatalf("Name = %q, want Fix Flaky Test", got.Name)
	}
	if len(adapter.calls) != 2 {
		t.Fatalf("calls = %d, want exactly 2 (original + one temperature-free retry)", len(adapter.calls))
	}
}

// TestNameSession_NoRetryWithoutTemperatureInMessage: an invalid-request
// error that does not name temperature must not trigger the retry.
func TestNameSession_NoRetryWithoutTemperatureInMessage(t *testing.T) {
	t.Parallel()
	adapter := &temperatureSpyAdapter{
		name: "openai",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus(req.Provider, 400, "model not found", nil, nil)
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	_, err := nameSession(context.Background(), client, WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano"), sessionNameSourcePrompt, "fix the flaky test", "", noNamerSleep)
	if err == nil {
		t.Fatal("expected error to surface")
	}
	if len(adapter.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no retry for unrelated invalid request)", len(adapter.calls))
	}
	if !strings.Contains(err.Error(), "session namer:") {
		t.Fatalf("error = %v, want wrapped namer error", err)
	}
}
