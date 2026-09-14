package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
)

func TestPrefetchLiveModelsPopulatesHeldRegistry(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	prefetchAllLiveModels(context.Background(), ctl.reg, func() {})
	got := entry(t, ctl.List(), "gw")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "gpt-live" && !m.Disabled }) {
		t.Fatalf("entry models = %+v, want live gpt-live", got.Models)
	}
	if slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "text-embedding-3-small" }) {
		t.Fatalf("entry models = %+v, want the embedding id filtered out", got.Models)
	}
}

func TestPrefetchLiveModelsBroadcastsOnCapabilityChange(t *testing.T) {
	// Same id set, different advertised facts: the pass must still
	// broadcast, or the picker's cached descriptors go stale.
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	announced := 0
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 1 {
		t.Fatalf("announced = %d, want 1 after initial pass", announced)
	}
	// Simulate a capability change behind the same id: re-apply live
	// rows with a context window the cache did not have, then run a
	// pass whose endpoint reports the same id. The broadcast must
	// fire on the facts change even though the id set is identical.
	reg := ctl.reg.Get()
	reg.ApplyLive("gw", []registry.Model{{ID: "gpt-live", Caps: registry.Caps{ContextWindow: new(100)}}})
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 2 {
		t.Fatalf("announced = %d, want 2 after a capability-only change with identical ids", announced)
	}
}

func TestPrefetchLiveModelsBroadcastsOnlyOnChange(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"},{"id":"text-embedding-3-small"}]}`)
	dir := filepath.Dir(tomlPath)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	announced := 0
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 1 {
		t.Fatalf("announced = %d, want 1 after a pass that adds live ids", announced)
	}
	prefetchAllLiveModels(context.Background(), ctl.reg, func() { announced++ })
	if announced != 1 {
		t.Fatalf("announced = %d, want still 1 after a pass that changes nothing", announced)
	}
}

func TestPrefetchLiveModelsSurvivesUnreachable(t *testing.T) {
	tomlPath := refreshGateway(t, `{"data":[{"id":"gpt-live"}]}`)
	dir := filepath.Dir(tomlPath)
	// A closed-port sibling must not fail or stall the pass.
	cfg, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("[providers.dead]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"test-key\"\n")...)
	if err := os.WriteFile(tomlPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
	if err := ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	prefetchAllLiveModels(context.Background(), ctl.reg, func() {})
	got := entry(t, ctl.List(), "gw")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "gpt-live" }) {
		t.Fatalf("entry models = %+v, want live gpt-live despite dead sibling", got.Models)
	}
}
