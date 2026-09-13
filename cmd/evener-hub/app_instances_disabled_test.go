package hub

import (
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
)

func TestInstances_SetModelDisabledWritesRowAndLists(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk"})
	writeMinimalProvidersToml(t, f.tomlPath)
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "claude-opus-4-6", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled: %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "base")
	row, ok := p.Models["claude-opus-4-6"]
	if !ok || !registry.BoolValue(row.Disabled) {
		t.Fatalf("authored row = %+v, want disabled=true", row)
	}
	got := entry(t, f.ctl.List(), "base")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "claude-opus-4-6" && m.Disabled }) {
		t.Fatalf("entry models = %+v, want claude-opus-4-6 flagged disabled", got.Models)
	}
	if _, err := f.ctl.reg.Get().Resolve("base/claude-opus-4-6"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve after disable = %v, want ErrModelDisabled", err)
	}

	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "claude-opus-4-6", Disabled: false}); err != nil {
		t.Fatalf("SetModelDisabled(false): %v", err)
	}
	p = authoredEntry(t, f.tomlPath, "base")
	if row, ok := p.Models["claude-opus-4-6"]; !ok || registry.BoolValue(row.Disabled) {
		t.Fatalf("authored row after re-enable = %+v, want disabled=false", row)
	}
}

func TestInstances_SetModelDisabledRefusals(t *testing.T) {
	f := newInstancesFixture(t, nil)
	writeMinimalProvidersToml(t, f.tomlPath)
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "nope", Model: "m", Disabled: true}); err == nil {
		t.Fatal("unknown instance must be refused")
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "nope-*", Disabled: true}); err == nil {
		t.Fatal("a glob id must be refused")
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "not-a-row", Disabled: true}); err == nil {
		t.Fatal("an unknown row must be refused")
	}
}
