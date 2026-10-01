package llm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// listingProtocol returns the rows and error configured by each test.
type listingProtocol struct {
	stubProtocol
	rows []registry.Model
	err  error
}

func (p listingProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return p.rows, p.err
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
				Models: map[string]registry.Model{
					"catalog-model":     {Caps: registry.Caps{Tools: new(true)}},
					"disabled-live-row": {Disabled: new(true)},
				},
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
	RegisterProtocol(listingProtocol{id: "test-proto-applylive", rows: []registry.Model{{ID: "live-only-model"}}})

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
		if listing.Usable {
			t.Fatal("borrowed registry listing marked usable without recording its live rows")
		}
		if got := modelIDs(listing.Models); slices.Contains(got, "live-only-model") || !slices.Contains(got, "catalog-model") {
			t.Fatalf("models = %v, want static fallback without the unrecorded live row", got)
		}
		if got := r.LiveModels("listing"); len(got) != 0 {
			t.Fatalf("live rows = %+v, want none: a client that does not own the registry must not write to it", got)
		}
	})
}

func TestModelsFallsBackToRegistryWhenLiveListingFails(t *testing.T) {
	liveErr := errors.New("listing unavailable")
	RegisterProtocol(listingProtocol{id: "test-proto-fallback-error", err: liveErr})
	r := applyLiveRegistry(t, "test-proto-fallback-error")
	r.ApplyLive("listing", []registry.Model{{ID: "stale-live-model"}})

	listing, err := NewClient(WithRegistry(r)).Models(context.Background(), "listing")
	if err == nil || !strings.Contains(err.Error(), liveErr.Error()) {
		t.Fatalf("Models error = %v, want the live listing failure", err)
	}
	if listing.Live {
		t.Fatal("failed live listing marked live")
	}
	if listing.Usable {
		t.Fatal("failed live listing marked usable")
	}
	if got := modelIDs(listing.Models); !slices.Contains(got, "catalog-model") || !slices.Contains(got, "gpt-4o") || slices.Contains(got, "stale-live-model") {
		t.Fatalf("fallback model ids = %v, want base and config rows without stale live rows", got)
	}
}

func TestModelsFallsBackToRegistryWhenLiveListingIsUnsupported(t *testing.T) {
	RegisterProtocol(stubProtocol{id: "test-proto-fallback-unsupported"})
	r := applyLiveRegistry(t, "test-proto-fallback-unsupported")
	r.ApplyLive("listing", []registry.Model{{ID: "stale-live-model"}})

	listing, err := NewClient(WithRegistry(r)).Models(context.Background(), "listing")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if listing.Live {
		t.Fatal("unsupported live listing marked live")
	}
	if listing.Usable {
		t.Fatal("unsupported live listing marked usable")
	}
	if got := modelIDs(listing.Models); !slices.Contains(got, "catalog-model") || !slices.Contains(got, "gpt-4o") || slices.Contains(got, "stale-live-model") {
		t.Fatalf("fallback model ids = %v, want base and config rows without stale live rows", got)
	}
}

func TestModelsFallbackIgnoresStaleLiveFacts(t *testing.T) {
	liveErr := errors.New("listing unavailable")
	RegisterProtocol(listingProtocol{id: "test-proto-fallback-stale-facts", err: liveErr})
	r := applyLiveRegistry(t, "test-proto-fallback-stale-facts")
	r.ApplyLive("listing", []registry.Model{{ID: "gpt-4o", Caps: registry.Caps{Tools: new(false)}}})

	listing, err := NewClient(WithRegistry(r)).Models(context.Background(), "listing")
	if err == nil || !strings.Contains(err.Error(), liveErr.Error()) {
		t.Fatalf("Models error = %v, want the live listing failure", err)
	}
	if got := modelIDs(listing.Models); !slices.Contains(got, "gpt-4o") {
		t.Fatalf("fallback model ids = %v, want gpt-4o from the static catalog", got)
	}
	if got := r.LiveModels("listing"); len(got) != 1 || got[0].ID != "gpt-4o" {
		t.Fatalf("cached live rows = %+v, want the stale row preserved outside the fallback view", got)
	}
}

func TestModelsFallsBackToRegistryWhenLiveListingIsUnusable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows       []registry.Model
		filteredID string
	}{
		{name: "empty"},
		{name: "non-chat", rows: []registry.Model{{ID: "whisper-large"}}, filteredID: "whisper-large"},
		{name: "hidden", rows: []registry.Model{{ID: "hidden-live", Hidden: true}}, filteredID: "hidden-live"},
		{name: "no tools", rows: []registry.Model{{ID: "no-tools-live", Caps: registry.Caps{Tools: new(false)}}}, filteredID: "no-tools-live"},
		{name: "disabled by config", rows: []registry.Model{{ID: "disabled-live-row"}}, filteredID: "disabled-live-row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protocolID := "test-proto-fallback-" + strings.ReplaceAll(tc.name, " ", "-")
			RegisterProtocol(listingProtocol{id: protocolID, rows: tc.rows})
			listing, err := NewClient(WithRegistry(applyLiveRegistry(t, protocolID))).Models(context.Background(), "listing")
			if err != nil {
				t.Fatalf("Models: %v", err)
			}
			if !listing.Live {
				t.Fatal("unusable live listing not marked live: the transport answered")
			}
			if listing.Usable {
				t.Fatal("unusable live listing marked usable")
			}
			if got := modelIDs(listing.Models); !slices.Contains(got, "catalog-model") || !slices.Contains(got, "gpt-4o") || slices.Contains(got, tc.filteredID) {
				t.Fatalf("fallback model ids = %v, want base and config rows without filtered live rows", got)
			}
		})
	}
}

func TestModelsDropsHiddenRowsFromUsableLiveListing(t *testing.T) {
	RegisterProtocol(listingProtocol{id: "test-proto-mixed-hidden", rows: []registry.Model{
		{ID: "hidden-live", Hidden: true},
		{ID: "visible-live"},
	}})

	listing, err := NewClient(WithRegistry(applyLiveRegistry(t, "test-proto-mixed-hidden"))).Models(context.Background(), "listing")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if !listing.Live {
		t.Fatal("listing with a visible row not marked live")
	}
	if !listing.Usable {
		t.Fatal("listing with a visible row not marked usable")
	}
	if got := modelIDs(listing.Models); !slices.Contains(got, "visible-live") || slices.Contains(got, "hidden-live") {
		t.Fatalf("live model ids = %v, want visible-live without hidden-live", got)
	}
}

func TestLiveListingUsableHonorsDisabledConfigGlobForCodexLiveOnlyRow(t *testing.T) {
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"subscription": {
				Base: "openai-codex",
				Models: map[string]registry.Model{
					"live-*": {Disabled: new(true)},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	if NewClient(WithRegistry(r)).LiveListingUsable("subscription", []registry.Model{{ID: "live-new", Caps: registry.Caps{Tools: new(true)}}}) {
		t.Fatal("Codex row disabled by a user config glob marked usable")
	}
}

func modelIDs(rows []registry.Resolved) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ModelID)
	}
	return ids
}
