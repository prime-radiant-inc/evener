package llm

import (
	"context"
	"errors"
	"testing"
)

// fuzzCapAdapter is a ProviderAdapter that also implements the optional
// capability interfaces (ToolChoiceSupporter, ModelLister, Initializer, Closer,
// ResponsesContinuationPlanner) with fuzzer-controlled outcomes. Its Stream
// honors the adapter contract by returning a non-nil already-closed stream.
type fuzzCapAdapter struct {
	name      string
	supports  bool
	models    []ModelInfo
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
func (a *fuzzCapAdapter) SupportsToolChoice(_ string) bool { return a.supports }
func (a *fuzzCapAdapter) ListModels(_ context.Context) ([]ModelInfo, error) {
	return a.models, nil
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

// FuzzClientCapabilities drives the Client optional-interface dispatch methods —
// SupportsToolChoice, ListModels, BehaviorTagOf, PlanResponsesContinuation,
// Initialize, Close, ProviderNames, Use — over a fuzzed provider name, behavior
// tag mapping, and adapter outcomes. These thread provider resolution and the
// nameToTag mapping; only fixed unit cases touched them (0% fuzz).
//
// Oracles:
//   - BehaviorTagOf honors its documented identity fallback: mapped tag when the
//     name is in the mapping, the name itself otherwise.
//   - SupportsToolChoice returns the adapter's verdict for a registered provider
//     and false for an unknown one.
//   - ListModels round-trips the adapter's model slice for a registered provider.
//   - Initialize/Close propagate the adapter's error.
//   - ProviderNames length equals the number of registered providers.
func FuzzClientCapabilities(f *testing.F) {
	f.Add("openai", "work", true, false, false, false)
	f.Add("  Anthropic ", "", false, true, true, true)
	f.Add("", "tag", true, false, true, false)

	f.Fuzz(func(t *testing.T, rawName, tag string, supports, withInitErr, withCloseErr, withMapping bool) {
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
			supports:  supports,
			models:    []ModelInfo{{ID: "m1", Provider: name}, {ID: "m2", Provider: name}},
			initErr:   initErr,
			closeErr:  closeErr,
			closeSeen: &closeSeen,
		}

		c := NewClient()
		if withMapping {
			c.SetNameToTag(map[string]string{name: tag})
		}
		c.Register(adapter)
		c.Use() // no-op middleware append must not panic

		// BehaviorTagOf identity-fallback contract.
		gotTag := c.BehaviorTagOf(name)
		if withMapping {
			if gotTag != tag {
				t.Fatalf("BehaviorTagOf(%q)=%q, want mapped %q", name, gotTag, tag)
			}
		} else if gotTag != name {
			t.Fatalf("BehaviorTagOf(%q)=%q, want identity", name, gotTag)
		}
		if other := c.BehaviorTagOf("definitely-unmapped-xyz"); other != "definitely-unmapped-xyz" {
			t.Fatalf("BehaviorTagOf unmapped name lost identity: %q", other)
		}

		// SupportsToolChoice: adapter verdict for the registered provider, false for unknown.
		if c.SupportsToolChoice(name, "auto") != supports {
			t.Fatalf("SupportsToolChoice(%q) != adapter verdict %v", name, supports)
		}
		if c.SupportsToolChoice("no-such-provider", "auto") {
			t.Fatalf("SupportsToolChoice on unknown provider returned true")
		}

		// ListModels round-trips the adapter's slice.
		models, err := c.ListModels(context.Background(), name)
		if err != nil {
			t.Fatalf("ListModels(%q) errored: %v", name, err)
		}
		if len(models) != 2 {
			t.Fatalf("ListModels returned %d models, want 2", len(models))
		}
		if _, err := c.ListModels(context.Background(), "no-such-provider"); err == nil {
			t.Fatalf("ListModels on unknown provider returned no error")
		}

		// PlanResponsesContinuation: adapter is a planner; the planErr is nil so a
		// plan is returned without a configuration error.
		if _, err := c.PlanResponsesContinuation(context.Background(), Request{Provider: name}); err != nil {
			t.Fatalf("PlanResponsesContinuation errored unexpectedly: %v", err)
		}

		// ProviderNames length.
		if got := len(c.ProviderNames()); got != 1 {
			t.Fatalf("ProviderNames len=%d, want 1", got)
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
