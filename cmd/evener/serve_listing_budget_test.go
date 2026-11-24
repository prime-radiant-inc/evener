package main

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// listingBudgetAdapter records the deadline a listing receives and answers
// with its rows, so a test can pin the caller's budget without a network.
type listingBudgetAdapter struct {
	name        string
	rows        []registry.Model
	remaining   time.Duration
	hadDeadline bool
}

func (a *listingBudgetAdapter) Name() string { return a.name }

func (*listingBudgetAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (*listingBudgetAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *listingBudgetAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	deadline, ok := ctx.Deadline()
	a.hadDeadline = ok
	if ok {
		a.remaining = time.Until(deadline)
	}
	return append([]registry.Model(nil), a.rows...), nil
}

// TestResolveInitialProfileUsesSharedListingBudget proves a launch's roster
// fetch receives the one shared listing budget, not a route-specific deadline
// chosen from the instance's transport.
func TestResolveInitialProfileUsesSharedListingBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg, err := registry.Load(
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"work": {
					Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
					APIKey: "k",
					Transport: registry.Transport{
						Auth: registry.AuthOAuthGitHubCopilot, BaseURL: "https://copilot.example.test",
						Endpoint: "/chat/completions", ModelsEndpoint: "/models",
						CountTokensEndpoint: registry.EndpointUnsupported,
					},
					Models: map[string]registry.Model{"m": {ID: "m"}},
				},
			}),
		)
		if err != nil {
			t.Fatalf("registry: %v", err)
		}
		client := llm.NewClient(llm.WithRegistry(reg))
		recorder := &listingBudgetAdapter{name: "work", rows: []registry.Model{{ID: "m"}}}
		client.Register(recorder)

		if _, _, err := resolveInitialProfile(context.Background(), client,
			cmdutil.ModelRef{Provider: "work", Model: "m"}, profileLaunch{}); err != nil {
			t.Fatalf("resolveInitialProfile: %v", err)
		}
		if !recorder.hadDeadline {
			t.Fatal("the startup roster fetch ran without the shared listing deadline")
		}
		if recorder.remaining != 8*time.Second {
			t.Fatalf("startup roster budget = %s, want the shared 8s listing budget", recorder.remaining)
		}
	})
}
