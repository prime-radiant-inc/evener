package hub

// The model picker needs each model's supported reasoning-effort levels so the
// effort chip can offer per-model choices instead of a static list. Those
// levels are a registry capability now (spec §7.4, §11.3): the hub carries
// what the resolved record says and adds nothing of its own.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestFetchLiveModels_CarriesRegistryEffortLevels walks the whole hub path a
// browser's model picker takes: a provider lists a model over its own
// transport, the registry resolves that row against the instance's authored
// caps, and the descriptor on the wire carries them.
func TestFetchLiveModels_CarriesRegistryEffortLevels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.2-nvfp4"},{"id":"plain"}]}`))
	}))
	t.Cleanup(server.Close)

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"lunaroute": {
				Base:      "openai-compatible",
				APIKey:    "k",
				Transport: registry.Transport{BaseURL: server.URL},
				Models: map[string]registry.Model{
					// Deliberately out of rank order: the resolved record is
					// what reaches the wire, ranked the way the registry ranks.
					"glm-5.2-nvfp4": {Caps: registry.Caps{EffortValues: []string{"high", "low", "max"}}},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	// Every other instance the registry knows gets a mute lister so no test
	// client can reach a real transport.
	for _, inst := range r.Instances() {
		if inst.Name != "lunaroute" {
			client.Register(&modelMetadataAdapter{name: inst.Name})
		}
	}

	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	models := NewWebServer(hubcore.WebConfig{}).fetchLiveModels(context.Background())
	levels := map[string][]string{}
	for _, m := range models {
		if m.Provider == "lunaroute" {
			levels[m.Model] = m.ReasoningEffortLevels
		}
	}
	want, err := r.Resolve("lunaroute/glm-5.2-nvfp4")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(want.Caps.EffortValues) == 0 {
		t.Fatal("the fixture resolves no effort values, so this test proves nothing")
	}
	if !reflect.DeepEqual(levels["glm-5.2-nvfp4"], want.Caps.EffortValues) {
		t.Errorf("reasoning_effort_levels = %v, want the registry's %v", levels["glm-5.2-nvfp4"], want.Caps.EffortValues)
	}
	if got, ok := levels["plain"]; !ok {
		t.Errorf("the listing's other model is missing from %+v", models)
	} else if len(got) != 0 {
		t.Errorf("a model the registry gives no effort values must carry none; got %v", got)
	}
}
