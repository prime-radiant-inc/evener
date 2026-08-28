package llm

import (
	"context"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// listingProtocol is a registered protocol whose listing returns one row, so
// a Models call has live data to apply.
type listingProtocol struct {
	stubProtocol
	rows []registry.Model
}

func (p listingProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return p.rows, nil
}

// applyLiveRegistry is a registry holding one instance on the listing
// protocol, standing in for the snapshot a bare client resolves against.
func applyLiveRegistry(t *testing.T, protocolID string) *registry.Registry {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"listing": {
				Base: "openai", Protocol: protocolID, APIKey: "k",
				Transport: registry.Transport{BaseURL: "https://listing.test"},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return r
}

// TestModelsAppliesLiveListingOnlyToARegistryTheClientOwns pins the rule that
// keeps one process's clients out of each other's data: a client given a
// registry with WithRegistry writes its live listing into it, and a client
// that was given none does not — the registry it resolves against is then
// EmbeddedRegistry, a process-wide snapshot shared with every other bare
// client, and only an instance's own owner may record what its transport
// said (spec §5.1, §8.1).
//
// The no-registry case is driven with the same fixture registry attached
// through the unexported field rather than WithRegistry: that is exactly the
// state a bare client is in — a registry it resolves against and does not
// own — and it keeps the assertion off the shared EmbeddedRegistry, which a
// test must not mutate.
func TestModelsAppliesLiveListingOnlyToARegistryTheClientOwns(t *testing.T) {
	RegisterProtocol(listingProtocol{stubProtocol{id: "test-proto-applylive"}, []registry.Model{{ID: "live-only-model"}}})

	t.Run("owned registry takes the listing", func(t *testing.T) {
		r := applyLiveRegistry(t, "test-proto-applylive")
		c := NewClient(WithRegistry(r))
		listing, err := c.Models(context.Background(), "listing")
		if err != nil {
			t.Fatalf("Models: %v", err)
		}
		if !listing.Live {
			t.Fatal("listing not marked live")
		}
		if got := r.LiveModels("listing"); len(got) != 1 || got[0].ID != "live-only-model" {
			t.Fatalf("live rows = %+v, want the listed row", got)
		}
	})

	t.Run("borrowed registry keeps its own rows", func(t *testing.T) {
		r := applyLiveRegistry(t, "test-proto-applylive")
		c := NewClient()
		c.registry = r // resolve against it without owning it, as a bare client does
		listing, err := c.Models(context.Background(), "listing")
		if err != nil {
			t.Fatalf("Models: %v", err)
		}
		if !listing.Live {
			t.Fatal("listing not marked live: the transport did answer")
		}
		if got := r.LiveModels("listing"); len(got) != 0 {
			t.Fatalf("live rows = %+v, want none: a client that does not own the registry must not write to it", got)
		}
	})
}
