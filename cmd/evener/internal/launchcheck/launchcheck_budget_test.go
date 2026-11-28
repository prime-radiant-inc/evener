package launchcheck

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// listingBudgetAdapter records the deadline a listing receives and answers
// with its rows, so a test can pin the launch check's budget without a network.
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

func launchCheckBudgetRegistry(t *testing.T, instances map[string]registry.Provider) *registry.Registry {
	t.Helper()
	reg, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

func muteOtherInstances(client *llm.Client, keep ...string) {
	for _, inst := range client.Registry().Instances() {
		if !slices.Contains(keep, inst.Name) {
			client.Register(&listingBudgetAdapter{name: inst.Name})
		}
	}
}

// TestLaunchCheckModelsUsesSharedListingBudget proves the models contract's
// per-instance listing uses the shared budget regardless of the instance's
// transport, so one stalled endpoint keeps its 8s slice of the child budget.
func TestLaunchCheckModelsUsesSharedListingBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := launchCheckBudgetRegistry(t, map[string]registry.Provider{
			"gw": {
				Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
				APIKey: "k",
				Transport: registry.Transport{
					Auth: registry.AuthBearer, BaseURL: "https://gw.example.test",
					Endpoint: "/chat/completions", ModelsEndpoint: "/models",
					CountTokensEndpoint: registry.EndpointUnsupported,
				},
				Models: map[string]registry.Model{"m": {ID: "m"}},
			},
		})
		client := llm.NewClient(llm.WithRegistry(reg))
		recorder := &listingBudgetAdapter{name: "gw", rows: []registry.Model{{ID: "m"}}}
		client.Register(recorder)
		muteOtherInstances(client, "gw")
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

		if _, _, err := launchCheckModels(); err != nil {
			t.Fatalf("launchCheckModels: %v", err)
		}
		if !recorder.hadDeadline {
			t.Fatal("the launch-check listing ran without the shared deadline")
		}
		if recorder.remaining != 8*time.Second {
			t.Fatalf("launch-check listing budget = %s, want the shared 8s listing budget", recorder.remaining)
		}
	})
}

// TestLaunchCheckModelValidationUsesSharedListingBudget proves the model
// validation's roster retry uses the same shared budget.
func TestLaunchCheckModelValidationUsesSharedListingBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := launchCheckBudgetRegistry(t, map[string]registry.Provider{
			"codex-plan": {Base: "openai-codex"},
		})
		client := llm.NewClient(llm.WithRegistry(reg))
		recorder := &listingBudgetAdapter{name: "codex-plan", rows: []registry.Model{{ID: "gpt-reserve"}}}
		client.Register(recorder)
		muteOtherInstances(client, "codex-plan")
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

		if err := validateLaunchCheckModel(cmdutil.ModelRef{Provider: "codex-plan", Model: "gpt-reserve"}); err != nil {
			t.Fatalf("validateLaunchCheckModel: %v", err)
		}
		if !recorder.hadDeadline {
			t.Fatal("the validation roster fetch ran without the shared deadline")
		}
		if recorder.remaining != 8*time.Second {
			t.Fatalf("validation listing budget = %s, want the shared 8s listing budget", recorder.remaining)
		}
	})
}

func TestLaunchCheckModelsListsInstancesConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gwTransport := registry.Transport{
			Auth: registry.AuthBearer, BaseURL: "https://gw.example.test",
			Endpoint: "/chat/completions", ModelsEndpoint: "/models",
			CountTokensEndpoint: registry.EndpointUnsupported,
		}
		reg := launchCheckBudgetRegistry(t, map[string]registry.Provider{
			"alpha": {
				Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
				APIKey: "k", Transport: gwTransport, Models: map[string]registry.Model{"m-a": {ID: "m-a"}},
			},
			"beta": {
				Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
				APIKey: "k", Transport: gwTransport, Models: map[string]registry.Model{"m-b": {ID: "m-b"}},
			},
			"gamma": {
				Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
				APIKey: "k", Transport: gwTransport, Models: map[string]registry.Model{"m-g": {ID: "m-g"}},
			},
		})
		barrier := func(name string, rows []registry.Model, entered chan struct{}, peer <-chan struct{}) listingOnlyAdapter {
			markEntered := sync.OnceFunc(func() { close(entered) })
			return listingOnlyAdapter{
				name: name,
				list: func(ctx context.Context) ([]registry.Model, error) {
					markEntered()
					select {
					case <-peer:
						return append([]registry.Model(nil), rows...), nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			}
		}
		alphaEntered := make(chan struct{})
		betaEntered := make(chan struct{})
		client := llm.NewClient(llm.WithRegistry(reg))
		client.Register(barrier("alpha", []registry.Model{{ID: "m-a"}}, alphaEntered, betaEntered))
		client.Register(barrier("beta", []registry.Model{{ID: "m-b"}}, betaEntered, alphaEntered))
		client.Register(&launchCheckFakeAdapter{name: "gamma", err: errors.New("boom")})
		muteOtherInstances(client, "alpha", "beta", "gamma")
		withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

		models, diagnostics, err := launchCheckModels()
		if err != nil {
			t.Fatalf("launchCheckModels: %v", err)
		}
		alphaIdx, betaIdx := -1, -1
		for i, m := range models {
			switch {
			case m.Provider == "alpha" && m.Model == "m-a":
				alphaIdx = i
			case m.Provider == "beta" && m.Model == "m-b":
				betaIdx = i
			}
		}
		if alphaIdx < 0 || betaIdx < 0 {
			t.Fatalf("missing concurrent rows: models=%+v diagnostics=%+v", models, diagnostics)
		}
		if alphaIdx > betaIdx {
			t.Fatalf("non-deterministic order: alpha at %d, beta at %d", alphaIdx, betaIdx)
		}
		sawGammaDiag := false
		for _, d := range diagnostics {
			if d.Provider == "gamma" {
				sawGammaDiag = true
			}
			if d.Provider == "alpha" || d.Provider == "beta" {
				t.Fatalf("concurrent peer failed with diagnostic: %+v", d)
			}
		}
		if !sawGammaDiag {
			t.Fatalf("expected diagnostic for gamma, got %+v", diagnostics)
		}
	})
}

func TestRunLaunchCheckLoadsClientOnce(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantLoads int32
	}{
		{name: "protocol only", args: []string{"--json"}, wantLoads: 0},
		{name: "model validation", args: []string{"--json", "--model", "fake/m"}, wantLoads: 1},
		{name: "models and model validation", args: []string{"--json", "--models", "--model", "fake/m"}, wantLoads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := launchCheckClient(t, gatewayInstances(),
				&launchCheckFakeAdapter{name: "fake", models: []registry.Model{{ID: "m"}}})
			muteOtherInstances(client, "fake")
			var loads atomic.Int32
			withLaunchCheckLoadClient(t, func(string) (*llm.Client, error) {
				loads.Add(1)
				return client, nil
			})
			if err := RunLaunchCheck(tc.args, io.Discard, io.Discard); err != nil {
				t.Fatalf("RunLaunchCheck(%v): %v", tc.args, err)
			}
			if got := loads.Load(); got != tc.wantLoads {
				t.Fatalf("launchCheckLoadClient called %d times, want %d", got, tc.wantLoads)
			}
		})
	}
}
