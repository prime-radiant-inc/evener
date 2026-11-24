package launchcheck

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
		for _, inst := range reg.Instances() {
			if inst.Name != "gw" {
				client.Register(&listingBudgetAdapter{name: inst.Name})
			}
		}
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
		for _, inst := range reg.Instances() {
			if inst.Name != "codex-plan" {
				client.Register(&listingBudgetAdapter{name: inst.Name})
			}
		}
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
