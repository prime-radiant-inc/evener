package llm

import (
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func budgetTestRegistry(t *testing.T, instance string, provider registry.Provider) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true),
		registry.WithoutCache(),
		registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{instance: provider}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

// wireCatalog returns the tool definitions with the parameter schema the target
// adapter puts on the wire: the closed empty object when closeStrict is true
// and the parameterless tool has not opted out, otherwise the open empty object.
func wireCatalog(defs []ToolDefinition, closeStrict bool) []ToolDefinition {
	out := make([]ToolDefinition, len(defs))
	copy(out, defs)
	for i := range out {
		if out[i].DeclaresNoParameters() || out[i].Parameters == nil {
			if closeStrict && (out[i].Strict == nil || *out[i].Strict) {
				out[i].Parameters = map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties":           map[string]any{},
					"required":             []string{},
				}
			} else {
				out[i].Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
		}
	}
	return out
}

// TestParameterlessToolIsPricedByTheSchemaTheAdapterSends verifies that
// Generate's stamped tool catalog size agrees with the schema the resolved
// adapter actually emits: closed empty object on strict-capable built-in OpenAI
// routes, and open empty object on non-strict routes, opted-out tools, and
// registered overrides.
func TestParameterlessToolIsPricedByTheSchemaTheAdapterSends(t *testing.T) {
	t.Parallel()
	strictOff := false
	baseTools := func(strict *bool) []Tool {
		return []Tool{
			{Definition: ToolDefinition{Name: "current_time", Strict: strict}},
			{Definition: ToolDefinition{
				Name:       "read_file",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			}},
		}
	}
	cases := []struct {
		name        string
		provider    registry.Provider
		tools       []Tool
		override    bool
		closeStrict bool
	}{
		{
			name:        "strict built-in openai-responses emits closed schema",
			provider:    registry.Provider{Base: "openai", APIKey: "unused", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9"}},
			tools:       baseTools(nil),
			closeStrict: true,
		},
		{
			name: "strict built-in openai-chat emits closed schema",
			provider: registry.Provider{
				Base:      "openai",
				Protocol:  registry.ProtocolOpenAIChat,
				APIKey:    "unused",
				Transport: registry.Transport{BaseURL: "http://127.0.0.1:9"},
			},
			tools:       baseTools(nil),
			closeStrict: true,
		},
		{
			name: "non-strict built-in route keeps open schema",
			provider: registry.Provider{
				Base:      "openai",
				APIKey:    "unused",
				Caps:      registry.Caps{StrictTools: new(false)},
				Transport: registry.Transport{BaseURL: "http://127.0.0.1:9"},
			},
			tools:       baseTools(nil),
			closeStrict: false,
		},
		{
			name:        "opted-out tool on strict built-in route keeps open schema",
			provider:    registry.Provider{Base: "openai", APIKey: "unused", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9"}},
			tools:       baseTools(&strictOff),
			closeStrict: false,
		},
		{
			name:        "registered override on strict route keeps open schema",
			provider:    registry.Provider{Base: "openai", APIKey: "unused", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9"}},
			tools:       baseTools(nil),
			override:    true,
			closeStrict: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const instance = "priced"
			c := NewClient(WithRegistry(budgetTestRegistry(t, instance, tc.provider)))
			if tc.override {
				c.Register(&recordReqAdapter{name: instance})
			}
			prompt := "hello"
			opts := GenerateOptions{
				Client:   c,
				Model:    "tool-budget-probe",
				Provider: instance,
				Prompt:   &prompt,
				Tools:    tc.tools,
			}
			gs, err := prepareGeneration(opts)
			if err != nil {
				t.Fatalf("prepareGeneration: %v", err)
			}
			adapterReq := gs.request(opts, gs.history)
			wireDefs := wireCatalog(gs.toolDefs, tc.closeStrict)
			wireReq := Request{
				Model:          opts.Model,
				Provider:       instance,
				Messages:       []Message{User(prompt)},
				Tools:          wireDefs,
				ToolsJSONBytes: ToolsJSONBytes(wireDefs),
			}

			res, err := c.Resolve(instance + "/" + adapterReq.Model)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			generous := 10_000_000
			res.Caps.MaxInputTokens = &generous
			wirePrice := EstimateInputTokensForResolved(res, wireReq).Tokens
			tight := wirePrice + SafetyReserveTokens(wirePrice) - 1
			if got := wirePrice + SafetyReserveTokens(tight); got != tight+1 {
				t.Fatalf("reserve at %d makes the wire price %d, want %d: the boundary is not one token under the wire", tight, got, tight+1)
			}
			res.Caps.MaxInputTokens = &tight
			res.Caps.ContextWindow = nil

			var budgetErr *ContextBudgetError
			if _, _, err := ApplyTokenBudget(wireReq, res); !errors.As(err, &budgetErr) || budgetErr.Limit != "max_input" {
				t.Fatalf("wire catalog not refused at %d ceiling: err = %v", tight, err)
			}
			if _, _, err := ApplyTokenBudget(adapterReq, res); !errors.As(err, &budgetErr) || budgetErr.Limit != "max_input" {
				t.Fatalf("adapter catalog admitted under %d ceiling, but emitted schema exceeds it: err = %v", tight, err)
			}

			// The exact ceiling admits both requests with identical output headroom,
			// proving non-strict, opted-out, and override paths are not over-billed
			// by the closed schema's extra bytes.
			exact := wirePrice + SafetyReserveTokens(tight)
			res.Caps.MaxInputTokens = &exact
			if _, _, err := ApplyTokenBudget(adapterReq, res); err != nil {
				t.Fatalf("adapter catalog refused at exact emitted ceiling %d: %v", exact, err)
			}

			window := exact + 100
			res.Caps.MaxInputTokens = nil
			res.Caps.ContextWindow = &window
			_, adapterBudget, err := ApplyTokenBudget(adapterReq, res)
			if err != nil {
				t.Fatalf("ApplyTokenBudget (adapter) at %d window: %v", window, err)
			}
			_, wireBudget, err := ApplyTokenBudget(wireReq, res)
			if err != nil {
				t.Fatalf("ApplyTokenBudget (wire) at %d window: %v", window, err)
			}
			if adapterBudget.AdmittedOutput != wireBudget.AdmittedOutput {
				t.Fatalf("admitted output %d for adapter catalog, %d for wire catalog",
					adapterBudget.AdmittedOutput, wireBudget.AdmittedOutput)
			}
			if adapterBudget.AdmittedOutput <= 0 {
				t.Fatalf("admitted output = %d, want positive headroom", adapterBudget.AdmittedOutput)
			}
		})
	}

	t.Run("unprepared nil parameterless tool matches each built-in protocol emission", func(t *testing.T) {
		t.Parallel()
		nilDef := []ToolDefinition{{Name: "current_time"}}
		openDef := []ToolDefinition{{Name: "current_time", Parameters: defaultToolParameters()}}
		closedDef := []ToolDefinition{{Name: "current_time", Parameters: closedEmptyToolParameters()}}

		protoCases := []struct {
			name     string
			res      registry.Resolved
			override bool
			want     int
		}{
			{
				name: "strict openai-responses closes nil",
				res:  registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{StrictTools: new(true)}},
				want: ToolsJSONBytes(closedDef),
			},
			{
				name: "non-strict openai-responses synthesizes open object",
				res:  registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{StrictTools: new(false)}},
				want: ToolsJSONBytes(openDef),
			},
			{
				name: "strict openai-chat closes nil",
				res:  registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{StrictTools: new(true)}},
				want: ToolsJSONBytes(closedDef),
			},
			{
				name: "non-strict openai-chat omits nil",
				res:  registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{StrictTools: new(false)}},
				want: ToolsJSONBytes(nilDef),
			},
			{
				name: "anthropic synthesizes open object",
				res:  registry.Resolved{Protocol: registry.ProtocolAnthropic},
				want: ToolsJSONBytes(openDef),
			},
			{
				name: "google synthesizes open object",
				res:  registry.Resolved{Protocol: registry.ProtocolGoogle},
				want: ToolsJSONBytes(openDef),
			},
			{
				name:     "override keeps caller nil",
				res:      registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{StrictTools: new(true)}},
				override: true,
				want:     ToolsJSONBytes(nilDef),
			},
		}
		for _, tc := range protoCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if got := toolsJSONBytesForResolved(nilDef, tc.res, tc.override); got != tc.want {
					t.Fatalf("toolsJSONBytesForResolved = %d, want %d", got, tc.want)
				}
			})
		}
	})
}
