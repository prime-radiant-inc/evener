package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

type preflightSpyAdapter struct {
	name   string
	called bool
}

func (a *preflightSpyAdapter) Name() string { return a.name }
func (a *preflightSpyAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	a.called = true
	return llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: llm.FinishReasonStop}}, nil
}
func (a *preflightSpyAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	a.called = true
	s := llm.NewChanStream(func() {})
	s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
	s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
	s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "ok"})
	s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "t0"})
	s.Send(llm.StreamEvent{
		Type:         llm.StreamEventFinish,
		FinishReason: &llm.FinishReason{Reason: llm.FinishReasonStop},
		Response:     &llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: llm.FinishReasonStop}},
	})
	s.CloseSend()
	return s, nil
}

func newGooglePreflightTestServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"int_ok","model":"gemini-3.8-flash","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func loadGooglePreflightRegistry(t *testing.T, srvURL string, mod func(*registry.Provider)) *registry.Registry {
	t.Helper()
	prov := registry.Provider{
		Base:      "google",
		APIKey:    "test-key",
		Caps:      registry.Caps{WebSearch: new(true)},
		Transport: registry.Transport{BaseURL: srvURL},
	}
	if mod != nil {
		mod(&prov)
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{"google": prov}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return r
}

func TestGenerateGooglePreflightRejectsAnchoredAndCombinedBeforeTurn1(t *testing.T) {
	t.Parallel()
	srv, hits := newGooglePreflightTestServer(t)
	r := loadGooglePreflightRegistry(t, srv.URL, nil)

	activeToolExecRan := false
	activeTool := llm.Tool{
		Definition: llm.ToolDefinition{Name: "ping", Parameters: map[string]any{"type": "object"}},
		Execute: func(context.Context, any) (any, error) {
			activeToolExecRan = true
			return "pong", nil
		},
	}
	passiveTool := llm.Tool{
		Definition: llm.ToolDefinition{Name: "ping", Parameters: map[string]any{"type": "object"}},
	}

	t.Run("anchored PreviousResponseID rejected before HTTP", func(t *testing.T) {
		before := hits.Load()
		c := llm.NewClient(llm.WithRegistry(r))
		_, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client:             c,
			Provider:           "google",
			Model:              "gemini-3.8-flash",
			Prompt:             new("hi"),
			PreviousResponseID: "int_prev",
			Tools:              []llm.Tool{activeTool},
		})
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before || activeToolExecRan {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v hits=%d exec=%v", err, hits.Load()-before, activeToolExecRan)
		}
	})

	t.Run("anchored provider_options rejected on StreamGenerate before HTTP", func(t *testing.T) {
		before := hits.Load()
		c := llm.NewClient(llm.WithRegistry(r))
		_, err := llm.StreamGenerate(context.Background(), llm.GenerateOptions{
			Client:   c,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hi"),
			Tools:    []llm.Tool{activeTool},
			ProviderOptions: map[string]any{
				"google": map[string]any{"previous_interaction_id": "int_prev"},
			},
		})
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v hits=%d", err, hits.Load()-before)
		}
	})

	t.Run("non-string previous_interaction_id rejected before HTTP", func(t *testing.T) {
		before := hits.Load()
		c := llm.NewClient(llm.WithRegistry(r))
		_, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client:   c,
			Provider: "google",
			Model:    "gemini-3.8-flash",
			Prompt:   new("hi"),
			Tools:    []llm.Tool{activeTool},
			ProviderOptions: map[string]any{
				"google": map[string]any{"previous_interaction_id": 123},
			},
		})
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v hits=%d", err, hits.Load()-before)
		}
	})

	t.Run("combined web_search and active tools rejected before HTTP", func(t *testing.T) {
		before := hits.Load()
		c := llm.NewClient(llm.WithRegistry(r))
		_, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client:    c,
			Provider:  "google",
			Model:     "gemini-3.8-flash",
			Prompt:    new("search and call tool"),
			Tools:     []llm.Tool{activeTool},
			WebSearch: true,
		})
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before {
			t.Fatalf("expected ConfigurationError before Turn 1; err=%v hits=%d", err, hits.Load()-before)
		}
	})

	t.Run("combined replacement in ProviderOptions or Transport.Body rejected before Turn 1", func(t *testing.T) {
		combinedSlice := []map[string]any{
			{"type": "google_search"},
			{"type": "function", "name": "ping", "description": "d", "parameters": map[string]any{"type": "object"}},
		}
		combinedRaw := json.RawMessage(`[{"type":"google_search"},{"type":"function","name":"ping","description":"d","parameters":{"type":"object"}}]`)
		for _, tc := range []struct {
			name     string
			provOpts map[string]any
			stream   bool
		}{
			{"Generate slice", map[string]any{"google": map[string]any{"tools": combinedSlice}}, false},
			{"StreamGenerate json.RawMessage", map[string]any{"google": map[string]any{"tools": combinedRaw}}, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := hits.Load()
				c := llm.NewClient(llm.WithRegistry(r))
				opts := llm.GenerateOptions{
					Client: c, Provider: "google", Model: "gemini-3.8-flash",
					Prompt: new("search"), Tools: []llm.Tool{activeTool}, ProviderOptions: tc.provOpts,
				}
				var err error
				if tc.stream {
					_, err = llm.StreamGenerate(context.Background(), opts)
				} else {
					_, err = llm.Generate(context.Background(), opts)
				}
				if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before || activeToolExecRan {
					t.Fatalf("expected ConfigurationError before Turn 1; err=%v hits=%d exec=%v", err, hits.Load()-before, activeToolExecRan)
				}
			})
		}

		rBody := loadGooglePreflightRegistry(t, srv.URL, func(p *registry.Provider) {
			p.Transport.Body = map[string]any{"tools": combinedSlice}
		})
		before := hits.Load()
		cBody := llm.NewClient(llm.WithRegistry(rBody))
		_, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client: cBody, Provider: "google", Model: "gemini-3.8-flash",
			Prompt: new("search"), Tools: []llm.Tool{activeTool},
		})
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before {
			t.Fatalf("expected Transport.Body combined tools preflight ConfigurationError; err=%v hits=%d", err, hits.Load()-before)
		}
	})

	t.Run("combined replacement with json.RawMessage type rejected before Turn 1", func(t *testing.T) {
		rStateful := loadGooglePreflightRegistry(t, srv.URL, func(p *registry.Provider) {
			p.Caps.Fields = map[string]bool{"previous_interaction_id": true}
		})
		rawSearchSlice := []map[string]any{
			{"type": json.RawMessage(`"google_search"`)},
			{"type": "function", "name": "ping", "description": "d", "parameters": map[string]any{"type": "object"}},
		}
		rawSearchAny := []any{
			map[string]any{"type": json.RawMessage(`"google_search"`)},
			map[string]any{"type": "function", "name": "ping", "description": "d", "parameters": map[string]any{"type": "object"}},
		}
		for _, tc := range []struct {
			name  string
			tools any
		}{
			{"[]map[string]any with json.RawMessage type", rawSearchSlice},
			{"[]any with json.RawMessage type", rawSearchAny},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := hits.Load()
				c := llm.NewClient(llm.WithRegistry(rStateful))
				_, err := llm.Generate(context.Background(), llm.GenerateOptions{
					Client: c, Provider: "google", Model: "gemini-3.8-flash",
					Prompt: new("search"), Tools: []llm.Tool{activeTool}, Store: new(true),
					ProviderOptions: map[string]any{"google": map[string]any{"tools": tc.tools}},
				})
				if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != before || activeToolExecRan {
					t.Fatalf("expected ConfigurationError before Turn 1; err=%v hits=%d exec=%v", err, hits.Load()-before, activeToolExecRan)
				}
			})
		}

		// MaxToolRounds=0 (caller-managed) with the same json.RawMessage combined list succeeds.
		zero := 0
		before := hits.Load()
		c := llm.NewClient(llm.WithRegistry(rStateful))
		if _, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client: c, Provider: "google", Model: "gemini-3.8-flash",
			Prompt: new("search"), Tools: []llm.Tool{activeTool}, Store: new(true), MaxToolRounds: &zero,
			ProviderOptions: map[string]any{"google": map[string]any{"tools": rawSearchSlice}},
		}); err != nil || hits.Load() != before+1 {
			t.Fatalf("MaxToolRounds=0 with json.RawMessage combined list failed: err=%v hits=%d", err, hits.Load()-before)
		}
	})

	t.Run("function-only replacement overrides WebSearch=true in Generate preflight", func(t *testing.T) {
		before := hits.Load()
		c := llm.NewClient(llm.WithRegistry(r))
		funcOnly := []map[string]any{{"type": "function", "name": "ping", "description": "d", "parameters": map[string]any{"type": "object"}}}
		if _, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client: c, Provider: "google", Model: "gemini-3.8-flash",
			Prompt: new("hi"), Tools: []llm.Tool{activeTool}, WebSearch: true,
			ProviderOptions: map[string]any{"google": map[string]any{"tools": funcOnly}},
		}); err != nil || hits.Load() != before+1 {
			t.Fatalf("function-only replacement must pass preflight and reach HTTP: err=%v hits=%d", err, hits.Load()-before)
		}

		before = hits.Load()
		rawFuncOnly := []map[string]any{{"type": json.RawMessage(`"function"`), "name": "ping", "description": "d", "parameters": map[string]any{"type": "object"}}}
		if _, err := llm.Generate(context.Background(), llm.GenerateOptions{
			Client: c, Provider: "google", Model: "gemini-3.8-flash",
			Prompt: new("hi"), Tools: []llm.Tool{activeTool}, WebSearch: true,
			ProviderOptions: map[string]any{"google": map[string]any{"tools": rawFuncOnly}},
		}); err != nil || hits.Load() != before+1 {
			t.Fatalf("json.RawMessage function-only replacement must pass preflight: err=%v hits=%d", err, hits.Load()-before)
		}
	})

	t.Run("MaxToolRounds=0 and passive tools and unanchored function loop remain allowed", func(t *testing.T) {
		rAnchor := loadGooglePreflightRegistry(t, srv.URL, func(p *registry.Provider) {
			p.Caps.Fields = map[string]bool{"previous_interaction_id": true}
		})
		zero := 0
		for _, tc := range []struct {
			name string
			opts llm.GenerateOptions
		}{
			{
				name: "MaxToolRounds=0",
				opts: llm.GenerateOptions{
					Provider: "google", Model: "gemini-3.8-flash", Prompt: new("hi"),
					PreviousResponseID: "int_prev", Store: new(true), Tools: []llm.Tool{activeTool}, MaxToolRounds: &zero,
				},
			},
			{
				name: "passive tools",
				opts: llm.GenerateOptions{
					Provider: "google", Model: "gemini-3.8-flash", Prompt: new("hi"),
					PreviousResponseID: "int_prev", Store: new(true), Tools: []llm.Tool{passiveTool},
				},
			},
			{
				name: "unanchored function loop",
				opts: llm.GenerateOptions{
					Provider: "google", Model: "gemini-3.8-flash", Prompt: new("hi"),
					Tools: []llm.Tool{activeTool},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := hits.Load()
				c := llm.NewClient(llm.WithRegistry(rAnchor))
				tc.opts.Client = c
				if _, err := llm.Generate(context.Background(), tc.opts); err != nil {
					t.Fatalf("%s unexpectedly failed: %v", tc.name, err)
				}
				if hits.Load() != before+1 {
					t.Fatalf("%s hits = %d, want 1", tc.name, hits.Load()-before)
				}
			})
		}
	})

	t.Run("registered adapter override bypasses Interactions preflight", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			provider string
			stream   bool
		}{
			{"Generate explicit google override", "google", false},
			{"StreamGenerate explicit google override", "google", true},
			{"Generate default-provider google override", "", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				spy := &preflightSpyAdapter{name: "google"}
				c := llm.NewClient(llm.WithRegistry(r))
				c.SetDefaultProvider("google")
				c.Register(spy)
				before := hits.Load()
				opts := llm.GenerateOptions{
					Client:             c,
					Provider:           tc.provider,
					Model:              "gemini-3.8-flash",
					Prompt:             new("hi"),
					PreviousResponseID: "int_prev",
					Tools:              []llm.Tool{activeTool},
					WebSearch:          true,
				}
				if tc.stream {
					sr, err := llm.StreamGenerate(context.Background(), opts)
					if err != nil {
						t.Fatalf("StreamGenerate with registered override failed: %v", err)
					}
					for range sr.Events() {
					}
				} else {
					if _, err := llm.Generate(context.Background(), opts); err != nil {
						t.Fatalf("Generate with registered override failed: %v", err)
					}
				}
				if !spy.called || hits.Load() != before {
					t.Fatalf("override called=%v httpHits=%d, want called=true httpHits=0", spy.called, hits.Load()-before)
				}
			})
		}
	})
}

func TestGenerateGooglePreflightSkipsCommandCredentialAndIgnoresNilAnchor(t *testing.T) {
	t.Parallel()
	srv, hits := newGooglePreflightTestServer(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "cred-ran")
	cfgPath := filepath.Join(dir, "providers.toml")
	cfg := fmt.Sprintf(
		"[providers.google-cmd]\nbase = \"google\"\nbase_url = %q\napi_key = '''$(sh -c 'printf x >> %q; printf test-key')'''\nweb_search = true\n",
		srv.URL, marker,
	)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	r, err := registry.Load(
		registry.WithOffline(true),
		registry.WithoutCache(),
		registry.WithConfigPath(cfgPath),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}

	activeTool := llm.Tool{
		Definition: llm.ToolDefinition{Name: "ping", Parameters: map[string]any{"type": "object"}},
		Execute:    func(context.Context, any) (any, error) { return "pong", nil },
	}
	credRuns := func() int {
		b, err := os.ReadFile(marker)
		if err != nil {
			return 0
		}
		return len(b)
	}

	// 1. Preflight rejection must run the credential command 0 times.
	c := llm.NewClient(llm.WithRegistry(r))
	_, err = llm.Generate(context.Background(), llm.GenerateOptions{
		Client:             c,
		Provider:           "google-cmd",
		Model:              "gemini-3.8-flash",
		Prompt:             new("hi"),
		PreviousResponseID: "int_prev",
		Tools:              []llm.Tool{activeTool},
	})
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != 0 {
		t.Fatalf("expected preflight ConfigurationError before dispatch; err=%v hits=%d", err, hits.Load())
	}
	if runs := credRuns(); runs != 0 {
		t.Fatalf("preflight rejection executed credential command %d times, want 0", runs)
	}
	_, err = llm.Generate(context.Background(), llm.GenerateOptions{
		Client:   c,
		Provider: "google-cmd",
		Model:    "gemini-3.8-flash",
		Prompt:   new("search"),
		Tools:    []llm.Tool{activeTool},
		ProviderOptions: map[string]any{
			"google": map[string]any{"tools": []map[string]any{{"type": "google_search"}, {"type": "function", "name": "ping", "parameters": map[string]any{"type": "object"}}}},
		},
	})
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || hits.Load() != 0 {
		t.Fatalf("expected combined replacement preflight ConfigurationError; err=%v hits=%d", err, hits.Load())
	}
	if runs := credRuns(); runs != 0 {
		t.Fatalf("preflight rejection executed credential command %d times, want 0", runs)
	}

	// 2. Allowed dispatch with nil previous_interaction_id succeeds and runs credential command once.
	_, err = llm.Generate(context.Background(), llm.GenerateOptions{
		Client:   c,
		Provider: "google-cmd",
		Model:    "gemini-3.8-flash",
		Prompt:   new("hi"),
		Tools:    []llm.Tool{activeTool},
		ProviderOptions: map[string]any{
			"google": map[string]any{"previous_interaction_id": nil},
		},
	})
	if err != nil {
		t.Fatalf("nil previous_interaction_id unexpectedly failed: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("allowed dispatch HTTP hits = %d, want 1", hits.Load())
	}
	if runs := credRuns(); runs != 1 {
		t.Fatalf("allowed dispatch executed credential command %d times, want 1", runs)
	}
}
