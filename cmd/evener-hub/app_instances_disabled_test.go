package hub

import (
	"errors"
	"os"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
)

func TestInstances_SetModelDisabledShadowsImplicitInstance(t *testing.T) {
	// An instance that exists only via an environment credential has
	// no authored entry: the toggle authors a shadow entry carrying
	// just the flag, and base/credential resolution still works
	// through it while the toggle takes effect.
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk"})
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	before, ok := f.ctl.reg.Get().Instance("anthropic")
	if !ok || !before.Implicit {
		t.Fatalf("want an implicit anthropic instance, got ok=%v implicit=%v", ok, before.Implicit)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "anthropic", Model: "claude-opus-4-6", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled: %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "anthropic")
	row, ok := p.Models["claude-opus-4-6"]
	if !ok || !registry.BoolValue(row.Disabled) {
		t.Fatalf("authored shadow row = %+v, want disabled=true", row)
	}
	after, ok := f.ctl.reg.Get().Instance("anthropic")
	if !ok {
		t.Fatal("anthropic instance vanished after shadowing toggle")
	}
	if after.ProviderID != before.ProviderID || after.Protocol != before.Protocol {
		t.Fatalf("shadowed instance changed identity: %+v -> %+v", before, after)
	}
	if _, err := f.ctl.reg.Get().Resolve("anthropic/claude-opus-4-6"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve after shadow disable = %v, want ErrModelDisabled", err)
	}
}

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

func TestInstances_SetModelDisabledLiveOnlyID(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk"})
	writeMinimalProvidersToml(t, f.tomlPath)
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	f.ctl.reg.Get().ApplyLive("base", []registry.Model{{ID: "claude-live-new"}, {ID: "claude-live-sibling"}})
	got := entry(t, f.ctl.List(), "base")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "claude-live-new" && !m.Disabled }) {
		t.Fatalf("entry models = %+v, want live-only id listed enabled", got.Models)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "claude-live-new", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled(live): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "base")
	if row, ok := p.Models["claude-live-new"]; !ok || !registry.BoolValue(row.Disabled) {
		t.Fatalf("authored row = %+v, want live id disabled=true", row)
	}
	// The authored exact row precedes live lookup, so the toggle takes effect.
	if _, err := f.ctl.reg.Get().Resolve("base/claude-live-new"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve after live disable = %v, want ErrModelDisabled", err)
	}
	// The toggle's Reload swaps in a fresh registry: the sibling live-only
	// id fetched earlier must survive it in the returned inventory.
	got = entry(t, f.ctl.List(), "base")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "claude-live-sibling" }) {
		t.Fatalf("entry models = %+v, want sibling live id to survive the toggle reload", got.Models)
	}
}

func TestInstances_SetModelDisabledAliasWritesTarget(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk"})
	cfg := "[providers.base]\nbase = \"anthropic\"\napi_key = \"sk-inline\"\n[providers.base.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n"
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "house-model", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled(alias): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "base")
	target, ok := p.Models["claude-opus-4-6"]
	if !ok || !registry.BoolValue(target.Disabled) {
		t.Fatalf("authored target row = %+v, want disabled=true", target)
	}
	if row, ok := p.Models["house-model"]; !ok || row.Disabled != nil {
		t.Fatalf("alias row must keep no disabled flag of its own: %+v", p.Models)
	}
	if _, err := f.ctl.reg.Get().Resolve("base/house-model"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve(alias) = %v, want ErrModelDisabled", err)
	}
	if _, err := f.ctl.reg.Get().Resolve("base/claude-opus-4-6"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve(target) = %v, want ErrModelDisabled", err)
	}
}

func TestInstances_SetModelDisabledVariantOfAliasWritesTarget(t *testing.T) {
	// A dated-suffix or region-prefixed spelling of an alias id strips down
	// to the alias row, so the toggle has to follow the alias to its target
	// there too. Writing the alias row instead reports success and changes
	// nothing: an alias's own flag is replayed for nothing.
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": "sk"})
	cfg := "[providers.base]\nbase = \"anthropic\"\napi_key = \"sk-inline\"\n[providers.base.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n"
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "base", Model: "house-model-20250929", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled(variant of alias): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "base")
	target, ok := p.Models["claude-opus-4-6"]
	if !ok || !registry.BoolValue(target.Disabled) {
		t.Fatalf("authored target row = %+v, want disabled=true", target)
	}
	if row, ok := p.Models["house-model"]; !ok || row.Disabled != nil {
		t.Fatalf("alias row must keep no disabled flag of its own: %+v", p.Models)
	}
	if _, err := f.ctl.reg.Get().Resolve("base/house-model-20250929"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve(variant of alias) = %v, want ErrModelDisabled", err)
	}
}

func TestInstances_SetModelDisabledCrossProviderAliasWritesOwnRow(t *testing.T) {
	// A cross-provider alias is toggled per connection: the flag lands on
	// this instance's own alias row, so disabling the model here never
	// disables it on the connection the target belongs to.
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "sk"})
	cfg := "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk-inline\"\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n"
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "mine", Model: "house-model", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled(cross-provider alias): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "mine")
	row, ok := p.Models["house-model"]
	if !ok || !registry.BoolValue(row.Disabled) {
		t.Fatalf("authored alias row = %+v, want disabled=true", row)
	}
	if row.AliasOf != "openai/gpt-5.6" {
		t.Fatalf("authored alias row lost its alias_of: %+v", row)
	}
	l, exists, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile: exists=%v err=%v", exists, err)
	}
	if op, ok := l.Providers["openai"]; ok {
		if _, ok := op.Models["gpt-5.6"]; ok {
			t.Fatalf("the target's record must stay untouched: %+v", l.Providers)
		}
	}
	if _, err := f.ctl.reg.Get().Resolve("mine/house-model"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve(alias here) = %v, want ErrModelDisabled", err)
	}
	if _, err := f.ctl.reg.Get().Resolve("openai/gpt-5.6"); err != nil {
		t.Fatalf("Resolve(target there) = %v, want nil", err)
	}
	got := entry(t, f.ctl.List(), "mine")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "house-model" && m.Disabled }) {
		t.Fatalf("entry models = %+v, want house-model listed disabled", got.Models)
	}
}

func TestInstances_SetModelDisabledVariantOfCrossProviderAliasWritesOwnRow(t *testing.T) {
	// A dated-suffix spelling of a cross-provider alias strips down to the
	// alias row itself, so the toggle authors that row rather than a new
	// exact row for the variant.
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "sk"})
	cfg := "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk-inline\"\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n"
	if err := os.WriteFile(f.tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "mine", Model: "house-model-20250929", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled(variant of cross-provider alias): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "mine")
	row, ok := p.Models["house-model"]
	if !ok || !registry.BoolValue(row.Disabled) {
		t.Fatalf("authored alias row = %+v, want disabled=true", row)
	}
	if _, ok := p.Models["house-model-20250929"]; ok {
		t.Fatalf("the variant must not author a row of its own: %+v", p.Models)
	}
	if _, err := f.ctl.reg.Get().Resolve("mine/house-model-20250929"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve(variant of alias) = %v, want ErrModelDisabled", err)
	}
}

func TestInstances_SetModelDisabledImplicitCuratedAliasShadows(t *testing.T) {
	// The story the sheet tells: openai-codex exists implicitly (OAuth
	// credential, no authored entry) and its curated gpt-5.6-sol row is an
	// alias onto openai's. Toggling it authors a shadow entry carrying the
	// row and nothing else; the curated alias survives the shadow, the
	// target's record is untouched, and the inventory reports the choice.
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "sk"})
	if err := f.ctl.auth.saveAuth(f.stateDir, "openai-codex", makeOAuthRecord("openai-codex", "bot@example.com")); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	before, ok := f.ctl.reg.Get().Instance("openai-codex")
	if !ok || !before.Implicit {
		t.Fatalf("want an implicit openai-codex instance, got ok=%v implicit=%v", ok, before.Implicit)
	}
	if err := f.ctl.SetModelDisabled(appwire.InstanceSetModelDisabledParams{Name: "openai-codex", Model: "gpt-5.6-sol", Disabled: true}); err != nil {
		t.Fatalf("SetModelDisabled(curated cross-provider alias): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "openai-codex")
	row, ok := p.Models["gpt-5.6-sol"]
	if !ok || !registry.BoolValue(row.Disabled) {
		t.Fatalf("authored shadow row = %+v, want disabled=true", row)
	}
	after, ok := f.ctl.reg.Get().Instance("openai-codex")
	if !ok || after.ProviderID != before.ProviderID || after.Protocol != before.Protocol {
		t.Fatalf("shadowed instance changed identity: %+v -> %+v", before, after)
	}
	if _, err := f.ctl.reg.Get().Resolve("openai-codex/gpt-5.6-sol"); !errors.Is(err, registry.ErrModelDisabled) {
		t.Fatalf("Resolve(codex alias) = %v, want ErrModelDisabled", err)
	}
	if _, err := f.ctl.reg.Get().Resolve("openai/gpt-5.6-sol"); err != nil {
		t.Fatalf("Resolve(target) = %v, want nil: the target's record is untouched", err)
	}
	got := entry(t, f.ctl.List(), "openai-codex")
	if !slices.ContainsFunc(got.Models, func(m appwire.InstanceModelEntry) bool { return m.ID == "gpt-5.6-sol" && m.Disabled }) {
		t.Fatalf("entry models = %+v, want gpt-5.6-sol listed disabled", got.Models)
	}
	l, exists, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile: exists=%v err=%v", exists, err)
	}
	if op, ok := l.Providers["openai"]; ok {
		if _, ok := op.Models["gpt-5.6-sol"]; ok {
			t.Fatalf("the target's record must stay untouched: %+v", l.Providers)
		}
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
