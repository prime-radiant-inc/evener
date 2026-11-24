package agent

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// listingBudgetAdapter records the deadline a live model listing ran under and
// answers with the roster the test wants the switch to prime.
type listingBudgetAdapter struct {
	name   string
	models []registry.Model
	budget time.Duration
	listed bool
}

func (a *listingBudgetAdapter) Name() string { return a.name }

func (a *listingBudgetAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (a *listingBudgetAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func (a *listingBudgetAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	a.listed = true
	if deadline, ok := ctx.Deadline(); ok {
		a.budget = time.Until(deadline)
	}
	return append([]registry.Model(nil), a.models...), nil
}

// TestPrimeCodexRosterUsesSharedListingBudget pins the shared model switch's
// roster preflight to the one 8-second listing budget.
func TestPrimeCodexRosterUsesSharedListingBudget(t *testing.T) {
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"codex-home":   {Base: "openai-codex"},
			"codex-target": {Base: "openai-codex"},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	target := &listingBudgetAdapter{name: "codex-target", models: []registry.Model{{ID: "gpt-reserve"}}}
	client.Register(target)
	for _, inst := range r.Instances() {
		if inst.Name != "codex-target" {
			client.Register(&fakeAdapter{name: inst.Name})
		}
	}
	profile, err := provider.Resolve(r, "codex-home/gpt-5.6")
	if err != nil {
		t.Fatalf("home profile: %v", err)
	}

	sess := &Session{client: client, profile: profile, id: "listing-budget"}
	synctest.Test(t, func(t *testing.T) {
		instance, listing := sess.primeCodexRoster("codex-target/gpt-reserve")
		if instance != "codex-target" || listing == nil {
			t.Fatalf("primeCodexRoster = %q, %v; want the target instance and its listing", instance, listing)
		}
		if !target.listed {
			t.Fatal("the target's live roster was never listed")
		}
		if target.budget != 8*time.Second {
			t.Fatalf("roster listing budget = %s, want the shared 8s listing budget", target.budget)
		}
	})
}
