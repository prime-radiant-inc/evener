package llm

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

type preflightSpyAdapter struct {
	name   string
	called bool
}

func (a *preflightSpyAdapter) Name() string { return a.name }
func (a *preflightSpyAdapter) Complete(context.Context, Request) (Response, error) {
	a.called = true
	return Response{Message: Assistant("ok"), Finish: FinishReason{Reason: FinishReasonStop}}, nil
}
func (a *preflightSpyAdapter) Stream(context.Context, Request) (Stream, error) {
	a.called = true
	return nil, ErrStreamUnsupported
}

func TestGenerateGooglePreflightRejectsAnchoredAndCombinedBeforeTurn1(t *testing.T) {
	t.Parallel()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(k string) (string, bool) {
			if k == "GEMINI_API_KEY" {
				return "test-key", true
			}
			return "", false
		}),
		registry.WithInstances(map[string]registry.Provider{
			"google": {Caps: registry.Caps{WebSearch: new(true)}},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}

	activeToolExecRan := false
	activeTool := Tool{
		Definition: ToolDefinition{Name: "ping", Parameters: map[string]any{"type": "object"}},
		Execute: func(context.Context, any) (any, error) {
			activeToolExecRan = true
			return "pong", nil
		},
	}
	passiveTool := Tool{
		Definition: ToolDefinition{Name: "ping", Parameters: map[string]any{"type": "object"}},
	}

	t.Run("anchored PreviousResponseID rejected before HTTP", func(t *testing.T) {
		spy := &preflightSpyAdapter{name: "google"}
		c := NewClient(WithRegistry(r))
		c.Register(spy)
		_, err := Generate(context.Background(), GenerateOptions{
			Client:             c,
			Provider:           "google",
			Model:              "gemini-3.8-flash",
			Prompt:             new("hi"),
			PreviousResponseID: "int_prev",
			Tools:              []Tool{activeTool},
		})
		if _, ok := errors.AsType[*ConfigurationError](err); !ok || spy.called || activeToolExecRan {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v called=%v exec=%v", err, spy.called, activeToolExecRan)
		}
	})

	t.Run("anchored provider_options rejected on StreamGenerate before HTTP", func(t *testing.T) {
		spy := &preflightSpyAdapter{name: "google"}
		c := NewClient(WithRegistry(r))
		c.Register(spy)
		_, err := StreamGenerate(context.Background(), GenerateOptions{
			Client:   c,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hi"),
			Tools:    []Tool{activeTool},
			ProviderOptions: map[string]any{
				"google": map[string]any{"previous_interaction_id": "int_prev"},
			},
		})
		if _, ok := errors.AsType[*ConfigurationError](err); !ok || spy.called {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v called=%v", err, spy.called)
		}
	})

	t.Run("non-string previous_interaction_id rejected before HTTP", func(t *testing.T) {
		spy := &preflightSpyAdapter{name: "google"}
		c := NewClient(WithRegistry(r))
		c.Register(spy)
		_, err := Generate(context.Background(), GenerateOptions{
			Client:   c,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hi"),
			Tools:    []Tool{activeTool},
			ProviderOptions: map[string]any{
				"google": map[string]any{"previous_interaction_id": 123},
			},
		})
		if _, ok := errors.AsType[*ConfigurationError](err); !ok || spy.called {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v called=%v", err, spy.called)
		}
	})

	t.Run("combined web_search and active tools rejected before HTTP", func(t *testing.T) {
		spy := &preflightSpyAdapter{name: "google"}
		c := NewClient(WithRegistry(r))
		c.Register(spy)
		_, err := Generate(context.Background(), GenerateOptions{
			Client:    c,
			Provider:  "google",
			Model:     "gemini-3.8-flash",
			Prompt:    new("search and call tool"),
			Tools:     []Tool{activeTool},
			WebSearch: true,
		})
		if _, ok := errors.AsType[*ConfigurationError](err); !ok || spy.called {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v called=%v", err, spy.called)
		}
	})

	t.Run("MaxToolRounds=0 and passive tools and unanchored function loop remain allowed", func(t *testing.T) {
		zero := 0
		for _, tc := range []struct {
			name string
			opts GenerateOptions
		}{
			{
				name: "MaxToolRounds=0",
				opts: GenerateOptions{
					Provider: "google", Model: "gemini-3.8-flash", Prompt: new("hi"),
					PreviousResponseID: "int_prev", Tools: []Tool{activeTool}, MaxToolRounds: &zero,
				},
			},
			{
				name: "passive tools",
				opts: GenerateOptions{
					Provider: "google", Model: "gemini-3.8-flash", Prompt: new("hi"),
					PreviousResponseID: "int_prev", Tools: []Tool{passiveTool},
				},
			},
			{
				name: "unanchored function loop",
				opts: GenerateOptions{
					Provider: "google", Model: "gemini-3.8-flash", Prompt: new("hi"),
					Tools: []Tool{activeTool},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				spy := &preflightSpyAdapter{name: "google"}
				c := NewClient(WithRegistry(r))
				c.Register(spy)
				tc.opts.Client = c
				if _, err := Generate(context.Background(), tc.opts); err != nil {
					t.Fatalf("%s unexpectedly failed: %v", tc.name, err)
				}
				if !spy.called {
					t.Fatalf("%s did not reach adapter", tc.name)
				}
			})
		}
	})
}
