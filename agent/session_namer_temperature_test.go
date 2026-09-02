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

// TestNameSession_OmitsTemperatureForLiveOnlyRows: a live-listed model with no
// catalog row resolves non-synthesized, but its temperature flag is the
// protocol baseline — not a catalog fact. The gate must still omit the
// parameter (roborev on #835: live-only rows bypassed the synthesized check).
func TestNameSession_OmitsTemperatureForLiveOnlyRows(t *testing.T) {
	t.Parallel()
	reg := bedrockishRegistry(t)
	// A live listing on the openai instance advertising a model the catalog
	// does not know: resolves via the "live" step, non-synthesized.
	reg.ApplyLive("bedrock", []registry.Model{{ID: "us.openai.gpt-live-only-9"}})
	adapter := &temperatureSpyAdapter{
		name: "bedrock",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			if req.Temperature != nil {
				t.Errorf("call %d: namer sent temperature to a live-only model", call)
			}
			return llm.Response{Message: llm.Assistant(`{"name":"Fix Parser Bug"}`)}, nil
		},
	}
	client := llm.NewClient(llm.WithRegistry(reg))
	client.Register(adapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "bedrock/us.openai.gpt-live-only-9")
	got, err := nameSession(context.Background(), client, profile, sessionNameSourcePrompt, "fix the parser", "", noNamerSleep)
	if err != nil {
		t.Fatalf("nameSession: %v", err)
	}
	if got.Name != "Fix Parser Bug" {
		t.Fatalf("Name = %q, want Fix Parser Bug", got.Name)
	}
	if len(adapter.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(adapter.calls))
	}
}

// TestNameSession_SendsTemperatureForGoogleRows: Google rows key their
// temperature capability on generationConfig.temperature, not temperature;
// a supported Google model must still receive the parameter (roborev on
// #835: the fixed key read as missing and dropped it for all Google models).
func TestNameSession_SendsTemperatureForGoogleRows(t *testing.T) {
	t.Parallel()
	adapter := &temperatureSpyAdapter{
		name: "google",
		respondWith: func(req llm.Request, call int) (llm.Response, error) {
			if req.Temperature == nil {
				t.Errorf("call %d: namer omitted temperature on a supported Google model", call)
			}
			return llm.Response{Message: llm.Assistant(`{"name":"Fix Parser Bug"}`)}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "google/gemini-2.5-pro")
	got, err := nameSession(context.Background(), client, profile, sessionNameSourcePrompt, "fix the parser", "", noNamerSleep)
	if err != nil {
		t.Fatalf("nameSession: %v", err)
	}
	if got.Name != "Fix Parser Bug" {
		t.Fatalf("Name = %q, want Fix Parser Bug", got.Name)
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

// TestIsTemperatureUnsupported pins the retry predicate: every rejection
// shape the classifier recognizes (spec §12 message patterns and structured
// error.param codes) triggers it for temperature, other parameters and
// non-rejection messages do not, and non-invalid-request kinds never do.
func TestIsTemperatureUnsupported(t *testing.T) {
	// Message shapes the classifier's parameterMessagePatterns match; these
	// flow through ErrorFromHTTPStatus → parameterNameFromMessage.
	msgCases := []struct {
		name string
		msg  string
		want bool
	}{
		{"bedrock quoted", `responses.create failed: Unsupported parameter: 'temperature' is not supported with this model.`, true},
		{"openai unknown parameter", `Unknown parameter: 'temperature'.`, true},
		{"unrecognized argument", `Unrecognized request argument supplied: temperature`, true},
		{"unknown field", `Invalid value for 'temperature': unknown field temperature`, true},
		{"other parameter rejected", `Unsupported parameter: 'top_p' is not supported with this model.`, false},
		{"mentions temperature without rejection", `temperature value out of range`, false},
		{"unrelated", `model not found`, false},
	}
	for _, tc := range msgCases {
		err := llm.ErrorFromHTTPStatus("openai", 400, tc.msg, nil, nil)
		if got := isTemperatureUnsupported(err); got != tc.want {
			t.Errorf("%s: isTemperatureUnsupported(%q) = %v, want %v", tc.name, tc.msg, got, tc.want)
		}
	}

	// Structured rejections: code unknown_parameter/unsupported_parameter
	// with error.param naming the parameter. These classify through
	// ClassifyHTTPError, whose paramFromError reads error.param.
	structured := []struct {
		name string
		body string
		want bool
	}{
		{"structured param temperature", `{"error":{"message":"Unsupported parameter","type":"invalid_request_error","param":"temperature","code":"unknown_parameter"}}`, true},
		{"structured param other", `{"error":{"message":"Unsupported parameter","type":"invalid_request_error","param":"top_p","code":"unknown_parameter"}}`, false},
	}
	res := registry.Resolved{Instance: "openai", Protocol: registry.ProtocolOpenAIResponses}
	for _, tc := range structured {
		err := llm.ClassifyHTTPError("responses.create", 400, nil, []byte(tc.body), res)
		if got := isTemperatureUnsupported(err); got != tc.want {
			t.Errorf("%s: isTemperatureUnsupported(body %q) = %v, want %v", tc.name, tc.body, got, tc.want)
		}
	}

	// Non-invalid-request kinds never trigger, even naming temperature.
	quota := llm.ErrorFromHTTPStatus("openai", 429, `Unsupported parameter: 'temperature'`, nil, nil)
	if isTemperatureUnsupported(quota) {
		t.Error("non-400 status naming temperature must not trigger the retry")
	}
}
