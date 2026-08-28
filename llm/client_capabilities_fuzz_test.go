package llm

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// fuzzCapAdapter is an override that also implements the optional seams a
// registered adapter may bring (LiveModelLister, InputTokenCounter,
// ResponsesContinuationPlanner, Initializer, Closer) with fuzzer-controlled
// outcomes. Its Stream honors the adapter contract by returning a non-nil
// already-closed stream.
type fuzzCapAdapter struct {
	name      string
	models    []registry.Model
	tokens    int
	initErr   error
	closeErr  error
	planErr   error
	closeSeen *bool
}

func (a *fuzzCapAdapter) Name() string { return a.name }
func (a *fuzzCapAdapter) Complete(_ context.Context, _ Request) (Response, error) {
	return Response{}, nil
}
func (a *fuzzCapAdapter) Stream(_ context.Context, _ Request) (Stream, error) {
	return doneStream{}, nil
}
func (a *fuzzCapAdapter) LiveModels(_ context.Context) ([]registry.Model, error) {
	return a.models, nil
}
func (a *fuzzCapAdapter) CountInputTokens(_ context.Context, req Request) (InputTokenCount, error) {
	return InputTokenCount{Tokens: a.tokens, Exact: true, Source: TokenCountSourceProvider, Provider: req.Provider, Model: req.Model}, nil
}
func (a *fuzzCapAdapter) Initialize(_ context.Context) error { return a.initErr }
func (a *fuzzCapAdapter) Close() error {
	if a.closeSeen != nil {
		*a.closeSeen = true
	}
	return a.closeErr
}
func (a *fuzzCapAdapter) PlanResponsesContinuation(_ Request) (ResponsesContinuationPlan, error) {
	return ResponsesContinuationPlan{}, a.planErr
}

// FuzzClientCapabilities drives the client's override seams — Models,
// CountInputTokens, PlanResponsesContinuation, CanServe, Initialize, Close,
// ProviderNames, Use — over a fuzzed instance name and adapter outcomes. An
// override owns its name outright, so these paths never touch the registry:
// that is what the oracles below pin.
//
// Oracles:
//   - CanServe is true for the registered name whatever model is asked for,
//     because an override answers for every model under its name.
//   - Models returns the override's own rows, marked live, with the §5
//     visibility rule applied (a hidden row is dropped).
//   - CountInputTokens returns the override's exact count.
//   - PlanResponsesContinuation reaches the override's planner.
//   - Initialize/Close propagate the adapter's error.
//   - ProviderNames is exactly the registered name: a client with no
//     registry of its own lists no registry instances.
func FuzzClientCapabilities(f *testing.F) {
	f.Add("openai", 7, false, false, false)
	f.Add("  Anthropic ", 0, true, true, true)
	f.Add("", 1024, false, true, false)

	f.Fuzz(func(t *testing.T, rawName string, tokens int, withInitErr, withCloseErr, withHiddenRow bool) {
		name := normalizeProviderName(rawName)
		if name == "" {
			name = "stub"
		}

		var initErr, closeErr error
		if withInitErr {
			initErr = errors.New("init failed")
		}
		if withCloseErr {
			closeErr = errors.New("close failed")
		}
		var closeSeen bool
		adapter := &fuzzCapAdapter{
			name:      name,
			models:    []registry.Model{{ID: "m1"}, {ID: "m2"}},
			tokens:    tokens,
			initErr:   initErr,
			closeErr:  closeErr,
			closeSeen: &closeSeen,
		}
		if withHiddenRow {
			adapter.models = append(adapter.models, registry.Model{ID: "m3", Hidden: true})
		}

		c := NewClient()
		c.Register(adapter)
		c.Use() // no-op middleware append must not panic

		// CanServe: the override answers for every model under its name.
		if !c.CanServe(name, "any-model") {
			t.Fatalf("CanServe(%q) = false for a registered override", name)
		}

		// Models returns the override's rows, live, minus the hidden one.
		listing, err := c.Models(context.Background(), name)
		if err != nil {
			t.Fatalf("Models(%q) errored: %v", name, err)
		}
		if !listing.Live {
			t.Fatalf("Models(%q) not marked live", name)
		}
		if len(listing.Models) != 2 {
			t.Fatalf("Models returned %d rows, want 2 (hidden rows dropped): %+v", len(listing.Models), listing.Models)
		}
		if _, err := c.Models(context.Background(), "no-such-provider"); err == nil {
			t.Fatalf("Models on an unknown provider returned no error")
		}

		// CountInputTokens takes the override's exact count.
		count, err := c.CountInputTokens(context.Background(), Request{Provider: name, Model: "m1", Messages: []Message{User("hi")}})
		if err != nil {
			t.Fatalf("CountInputTokens errored: %v", err)
		}
		if count.Tokens != tokens || !count.Exact {
			t.Fatalf("CountInputTokens = %+v, want the override's exact %d", count, tokens)
		}

		// PlanResponsesContinuation reaches the override's planner.
		if _, err := c.PlanResponsesContinuation(context.Background(), Request{Provider: name, Model: "m1", Messages: []Message{User("hi")}}); err != nil {
			t.Fatalf("PlanResponsesContinuation errored unexpectedly: %v", err)
		}

		// ProviderNames: the override alone; no registry was supplied.
		names := c.ProviderNames()
		if len(names) != 1 || names[0] != name {
			t.Fatalf("ProviderNames = %v, want exactly [%q]", names, name)
		}

		// Initialize/Close propagate the adapter error.
		if err := c.Initialize(context.Background()); (err != nil) != withInitErr {
			t.Fatalf("Initialize error mismatch: %v (withInitErr=%v)", err, withInitErr)
		}
		if err := c.Close(); (err != nil) != withCloseErr {
			t.Fatalf("Close error mismatch: %v (withCloseErr=%v)", err, withCloseErr)
		}
		if !closeSeen {
			t.Fatalf("Close did not reach the adapter")
		}
	})
}
