package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
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
