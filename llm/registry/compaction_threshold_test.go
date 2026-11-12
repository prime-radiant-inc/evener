package registry

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompactionThresholdConfigRoundTrip(t *testing.T) {
	input := `
[models."*deepseek*"]
compaction_threshold = 200000

[providers.work]
base = "openai-compatible"
base_url = "https://example.com/v1"
compaction_threshold = 300000

[providers.work.models."deepseek/*"]
compaction_threshold = 275000

[providers.work.models."deepseek/deepseek-4.1-flash"]
compaction_threshold = 250000

[providers.work.models."plain-model"]
context_window = 128000
`
	layer, err := ParseConfig([]byte(input))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if got := layer.TopGlobs["*deepseek*"].Caps.CompactionThreshold; got == nil || *got != 200000 {
		t.Fatalf("top glob CompactionThreshold = %v, want 200000", got)
	}
	work := layer.Providers["work"]
	if got := work.Caps.CompactionThreshold; got == nil || *got != 300000 {
		t.Fatalf("provider CompactionThreshold = %v, want 300000", got)
	}
	if got := work.Models["deepseek/*"].Caps.CompactionThreshold; got == nil || *got != 275000 {
		t.Fatalf("provider glob CompactionThreshold = %v, want 275000", got)
	}
	if got := work.Models["deepseek/deepseek-4.1-flash"].Caps.CompactionThreshold; got == nil || *got != 250000 {
		t.Fatalf("exact row CompactionThreshold = %v, want 250000", got)
	}
	if got := work.Models["plain-model"].Caps.CompactionThreshold; got != nil {
		t.Fatalf("plain-model CompactionThreshold = %v, want nil", got)
	}

	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := WriteConfigFile(path, layer); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	reloaded, exists, err := ReadConfigFile(path)
	if err != nil || !exists {
		t.Fatalf("ReadConfigFile: exists=%v err=%v", exists, err)
	}
	if !reflect.DeepEqual(reloaded, layer) {
		t.Fatalf("round-trip layer mismatch:\n got %#v\nwant %#v", reloaded, layer)
	}

	// Verify nil CompactionThreshold omits the key in MarshalConfig.
	onlyPlain := &Layer{
		Tag: LayerConfig,
		Providers: map[string]Provider{
			"work": {
				ID:   "work",
				Base: "openai",
				Models: map[string]Model{
					"plain-model": {ID: "plain-model", Caps: Caps{ContextWindow: new(128000)}},
				},
			},
		},
	}
	raw, err := MarshalConfig(onlyPlain)
	if err != nil {
		t.Fatalf("MarshalConfig: %v", err)
	}
	if strings.Contains(string(raw), "compaction_threshold") {
		t.Fatalf("nil CompactionThreshold must be omitted from marshaled TOML:\n%s", raw)
	}
}

func TestCompactionThresholdAndTokenLimitValidation(t *testing.T) {
	for _, invalid := range []int64{0, -1, 250, MinCompactionThreshold - 1, int64(MaxTokenLimit) + 1} {
		tomlData := fmt.Sprintf("[providers.openai.models.\"gpt-5.6\"]\ncompaction_threshold = %d\n", invalid)
		if _, err := ParseConfig([]byte(tomlData)); err == nil {
			t.Fatalf("ParseConfig accepted invalid compaction_threshold = %d", invalid)
		}
	}

	for _, valid := range []int{MinCompactionThreshold, 250000, MaxTokenLimit} {
		tomlData := fmt.Sprintf("[providers.openai.models.\"gpt-5.6\"]\ncompaction_threshold = %d\n", valid)
		l, err := ParseConfig([]byte(tomlData))
		if err != nil {
			t.Fatalf("ParseConfig rejected valid compaction_threshold = %d: %v", valid, err)
		}
		if got := l.Providers["openai"].Models["gpt-5.6"].Caps.CompactionThreshold; got == nil || *got != valid {
			t.Fatalf("parsed CompactionThreshold = %v, want %d", got, valid)
		}
	}

	for _, field := range []string{"context_window", "max_input_tokens", "max_output_tokens"} {
		tooLarge := fmt.Sprintf("[providers.openai.models.\"gpt-5.6\"]\n%s = %d\n", field, int64(MaxTokenLimit)+1)
		if _, err := ParseConfig([]byte(tooLarge)); err == nil {
			t.Fatalf("ParseConfig accepted %s > MaxTokenLimit", field)
		}
		atMax := fmt.Sprintf("[providers.openai.models.\"gpt-5.6\"]\n%s = %d\n", field, MaxTokenLimit)
		if _, err := ParseConfig([]byte(atMax)); err != nil {
			t.Fatalf("ParseConfig rejected %s = MaxTokenLimit: %v", field, err)
		}
	}
}

func TestInputCeiling(t *testing.T) {
	cases := []struct {
		name string
		caps Caps
		want int
	}{
		{name: "both unset", caps: Caps{}, want: 0},
		{name: "non-positive ignored", caps: Caps{ContextWindow: new(0), MaxInputTokens: new(-5)}, want: 0},
		{name: "context window only", caps: Caps{ContextWindow: new(1_000_000)}, want: 1_000_000},
		{name: "max input only", caps: Caps{MaxInputTokens: new(400_000)}, want: 400_000},
		{name: "both set context smaller", caps: Caps{ContextWindow: new(200_000), MaxInputTokens: new(400_000)}, want: 200_000},
		{name: "both set max input smaller", caps: Caps{ContextWindow: new(1_000_000), MaxInputTokens: new(272_000)}, want: 272_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.caps.InputCeiling(); got != tc.want {
				t.Fatalf("InputCeiling() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCompactionThresholdResolutionAndClamping(t *testing.T) {
	overlayExtra := `
[providers.anthropic.models."claude-opus-4-6"]
compaction_threshold = 150000
`
	cfg := `
[models."claude-*"]
compaction_threshold = 160000

[providers.anthropic]
compaction_threshold = 170000

[providers.anthropic.models."claude-opus-*"]
compaction_threshold = 180000

[providers.anthropic.models."claude-opus-4-6"]
compaction_threshold = 190000

[providers.anthropic.models."alias-opus"]
alias_of = "claude-opus-4-6"

[providers.anthropic.models."clamped-model"]
context_window = 128000
compaction_threshold = 250000

[providers.anthropic.models."clamped-equal-input"]
context_window = 1000000
max_input_tokens = 250000
compaction_threshold = 250000
`
	r := fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "sk"}, cfg, WithOverlay(overlayWith(overlayExtra)))
	// Live listings do not carry compaction_threshold and must not clobber user/curated thresholds.
	r.ApplyLive("anthropic", []Model{
		{ID: "claude-opus-4-6", Caps: Caps{ContextWindow: new(200000), CompactionThreshold: new(90000)}},
		{ID: "claude-sonnet-4-5", Caps: Caps{ContextWindow: new(200000)}},
	})

	// Exact config/row wins over provider-glob, top-glob, provider, live, and overlay.
	opus, err := r.Resolve("anthropic/claude-opus-4-6")
	if err != nil {
		t.Fatalf("Resolve claude-opus-4-6: %v", err)
	}
	if opus.Caps.CompactionThreshold == nil || *opus.Caps.CompactionThreshold != 190000 {
		t.Fatalf("claude-opus-4-6 CompactionThreshold = %v, want 190000", opus.Caps.CompactionThreshold)
	}
	if opus.Provenance["CompactionThreshold"] != "config/row" {
		t.Fatalf("claude-opus-4-6 provenance = %q, want config/row", opus.Provenance["CompactionThreshold"])
	}

	// Provider-level and top-glob precedence on claude-sonnet-4-5 (matches top-glob "claude-*", which replays after provider caps).
	sonnet, err := r.Resolve("anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("Resolve claude-sonnet-4-5: %v", err)
	}
	if sonnet.Caps.CompactionThreshold == nil || *sonnet.Caps.CompactionThreshold != 160000 {
		t.Fatalf("claude-sonnet-4-5 CompactionThreshold = %v, want 160000", sonnet.Caps.CompactionThreshold)
	}
	if sonnet.Provenance["CompactionThreshold"] != "config/glob:claude-*" {
		t.Fatalf("claude-sonnet-4-5 provenance = %q, want config/glob:claude-*", sonnet.Provenance["CompactionThreshold"])
	}

	// Alias inherits target's CompactionThreshold and preserves target's provenance when no glob overrides it.
	rAlias := fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "sk"}, `
[providers.anthropic.models."claude-opus-4-6"]
compaction_threshold = 150000

[providers.anthropic.models."my-alias"]
alias_of = "claude-opus-4-6"
`)
	aliased, err := rAlias.Resolve("anthropic/my-alias")
	if err != nil {
		t.Fatalf("Resolve my-alias: %v", err)
	}
	if aliased.Caps.CompactionThreshold == nil || *aliased.Caps.CompactionThreshold != 150000 {
		t.Fatalf("my-alias CompactionThreshold = %v, want 150000", aliased.Caps.CompactionThreshold)
	}
	if aliased.Provenance["CompactionThreshold"] != "config/row" {
		t.Fatalf("my-alias CompactionThreshold provenance = %q, want target's config/row", aliased.Provenance["CompactionThreshold"])
	}

	// ResolveInstanceModelFacts on a same-provider alias whose target is disabled still returns the alias's resolved facts and clamping warnings while Resolve rejects it with ErrModelDisabled.
	rDisabledAlias := fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "sk"}, `
[providers.anthropic.models."claude-haiku-4-5"]
disabled = true

[providers.anthropic.models."disabled-haiku-alias"]
alias_of = "claude-haiku-4-5"
compaction_threshold = 250000
`)
	if _, err := rDisabledAlias.Resolve("anthropic/disabled-haiku-alias"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(disabled-haiku-alias) = %v, want ErrModelDisabled", err)
	}
	disabledAliasFacts, err := rDisabledAlias.ResolveInstanceModelFacts("anthropic", "disabled-haiku-alias")
	if err != nil {
		t.Fatalf("ResolveInstanceModelFacts(disabled-haiku-alias): %v", err)
	}
	if !BoolValue(disabledAliasFacts.Model.Disabled) || disabledAliasFacts.Caps.ContextWindow == nil || *disabledAliasFacts.Caps.ContextWindow != 200000 || disabledAliasFacts.Caps.CompactionThreshold != nil || disabledAliasFacts.Provenance["CompactionThreshold"] != "derived" {
		t.Fatalf("disabled-haiku-alias facts = %+v", disabledAliasFacts)
	}

	// Clamping when *CompactionThreshold >= InputCeiling() clears to nil, marks provenance "derived", and emits a warning without failing Load.
	for _, modelID := range []string{"clamped-model", "clamped-equal-input"} {
		res, err := r.Resolve("anthropic/" + modelID)
		if err != nil {
			t.Fatalf("Resolve %s: %v", modelID, err)
		}
		if res.Caps.CompactionThreshold != nil {
			t.Fatalf("%s CompactionThreshold = %v, want nil after clamping", modelID, res.Caps.CompactionThreshold)
		}
		if res.Provenance["CompactionThreshold"] != "derived" {
			t.Fatalf("%s provenance = %q, want derived", modelID, res.Provenance["CompactionThreshold"])
		}
		if len(res.Warnings) == 0 {
			t.Fatalf("%s Warnings = empty, want clamping warning", modelID)
		}
	}

	// Direct unit check on derive returning a non-empty warning slice when clamping fires.
	c := Caps{ContextWindow: new(128000), CompactionThreshold: new(250000)}
	m := Model{ID: "m"}
	prov := map[string]string{"CompactionThreshold": "config/row"}
	warns := derive(&c, &m, deriveInput{Protocol: ProtocolOpenAIChat}, prov)
	if c.CompactionThreshold != nil || prov["CompactionThreshold"] != "derived" || len(warns) == 0 {
		t.Fatalf("derive clamp: threshold=%v prov=%v warns=%v", c.CompactionThreshold, prov, warns)
	}
}

func TestCompactionThresholdRowScalarProvenance(t *testing.T) {
	cfg := `
[providers.anthropic.models."custom-row"]
protocol = "openai-chat"
wire_id = "custom-wire-v1"
alias_of = "claude-opus-4-6"
`
	r := fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "sk"}, cfg)

	base, err := r.Resolve("anthropic/claude-opus-4-6")
	if err != nil {
		t.Fatalf("Resolve base: %v", err)
	}
	if base.Provenance["Protocol"] != "snapshot/provider" && base.Provenance["Protocol"] != "overlay/provider" {
		t.Fatalf("base Protocol provenance = %q, want provider layer", base.Provenance["Protocol"])
	}
	if base.Provenance["WireID"] != "snapshot/row" && base.Provenance["WireID"] != "overlay/row" {
		t.Fatalf("base WireID provenance = %q, want catalog/overlay row", base.Provenance["WireID"])
	}
	if _, ok := base.Provenance["AliasOf"]; ok {
		t.Fatalf("non-alias row must not record AliasOf provenance: %v", base.Provenance)
	}

	custom, err := r.Resolve("anthropic/custom-row")
	if err != nil {
		t.Fatalf("Resolve custom-row: %v", err)
	}
	if custom.Provenance["Protocol"] != "config/row" {
		t.Fatalf("custom Protocol provenance = %q, want config/row", custom.Provenance["Protocol"])
	}
	if custom.Provenance["WireID"] != "config/row" {
		t.Fatalf("custom WireID provenance = %q, want config/row", custom.Provenance["WireID"])
	}
	if custom.Provenance["AliasOf"] != "config/row" {
		t.Fatalf("custom AliasOf provenance = %q, want config/row", custom.Provenance["AliasOf"])
	}
}

func TestThinkingShapes(t *testing.T) {
	got := ThinkingShapes()
	want := []string{"adaptive", "budget", "budget+effort", "level"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ThinkingShapes() = %v, want %v", got, want)
	}
}

func TestAuthoredProviderAndModel(t *testing.T) {
	emptyReg := fixtureLoad(t, nil, "")
	if _, ok := emptyReg.AuthoredProvider("work"); ok {
		t.Fatal("AuthoredProvider on no-user-layer registry must return false")
	}
	if _, ok := emptyReg.AuthoredModel("work", "m1"); ok {
		t.Fatal("AuthoredModel on no-user-layer registry must return false")
	}

	cfg := `
[providers.work]
base = "openai-compatible"
base_url = "https://example.com/v1"
api_key_env = ["WORK_KEY"]
headers = { "X-Work" = "1" }
compaction_threshold = 250000

[providers.work.models."deepseek/*"]
compaction_threshold = 200000

[providers.work.models."deepseek/deepseek-4.1-flash"]
wire_id = "deepseek-v4.1"
compaction_threshold = 250000
context_window = 1000000
effort_values = ["low", "high"]
input_modalities = ["text", "image"]
headers = { "X-Model" = "true" }
disabled = false
`
	r := fixtureLoad(t, nil, cfg)
	if _, ok := r.AuthoredProvider("missing"); ok {
		t.Fatal("AuthoredProvider for unauthored provider must return false")
	}
	if _, ok := r.AuthoredModel("work", "deepseek/*"); ok {
		t.Fatal("AuthoredModel for glob pattern must return false")
	}
	if _, ok := r.AuthoredModel("work", "missing"); ok {
		t.Fatal("AuthoredModel for unauthored model must return false")
	}

	p, ok := r.AuthoredProvider("work")
	if !ok {
		t.Fatal("AuthoredProvider(work) = false, want true")
	}
	if p.Caps.CompactionThreshold == nil || *p.Caps.CompactionThreshold != 250000 {
		t.Fatalf("AuthoredProvider CompactionThreshold = %v, want 250000", p.Caps.CompactionThreshold)
	}
	// Mutate returned Provider deeply and verify Registry copy is untouched.
	*p.Caps.CompactionThreshold = 999999
	p.APIKeyEnv[0] = "MUTATED"
	p.Headers["X-Work"] = "mutated"
	delete(p.Models, "deepseek/deepseek-4.1-flash")

	p2, ok := r.AuthoredProvider("work")
	if !ok || *p2.Caps.CompactionThreshold != 250000 || p2.APIKeyEnv[0] != "WORK_KEY" || p2.Headers["X-Work"] != "1" {
		t.Fatalf("AuthoredProvider mutation leaked into Registry: %+v", p2)
	}

	m, ok := r.AuthoredModel("work", "deepseek/deepseek-4.1-flash")
	if !ok {
		t.Fatal("AuthoredModel(work, deepseek/deepseek-4.1-flash) = false, want true")
	}
	if m.WireID != "deepseek-v4.1" || m.Caps.CompactionThreshold == nil || *m.Caps.CompactionThreshold != 250000 {
		t.Fatalf("AuthoredModel = %+v", m)
	}
	// Mutate returned Model deeply and verify Registry copy is untouched.
	*m.Caps.CompactionThreshold = 111111
	*m.Caps.ContextWindow = 222222
	*m.Disabled = true
	m.Caps.EffortValues[0] = "max"
	m.Caps.InputModalities[0] = "pdf"
	m.Headers["X-Model"] = "mutated"

	m2, ok := r.AuthoredModel("work", "deepseek/deepseek-4.1-flash")
	if !ok || *m2.Caps.CompactionThreshold != 250000 || *m2.Caps.ContextWindow != 1000000 || *m2.Disabled ||
		m2.Caps.EffortValues[0] != "low" || m2.Caps.InputModalities[0] != "text" || m2.Headers["X-Model"] != "true" {
		t.Fatalf("AuthoredModel mutation leaked into Registry: %+v", m2)
	}
}

func TestCheckAliasTarget(t *testing.T) {
	cfg := `
[providers.work]
base = "anthropic"
api_key = "sk"

[providers.work.models."deepseek/deepseek-4.1-flash"]
context_window = 1000000

[providers.work.models."existing-alias"]
alias_of = "claude-opus-4-6"
`
	r := fixtureLoad(t, nil, cfg)

	// Valid targets: same-instance catalog row, same-instance slashed row, and <curated>/<id> non-alias row.
	for _, validTarget := range []string{
		"claude-opus-4-6",
		"deepseek/deepseek-4.1-flash",
		"openai/gpt-5.6",
	} {
		if err := r.CheckAliasTarget("work", "new-alias", validTarget); err != nil {
			t.Fatalf("CheckAliasTarget(%q) unexpected error: %v", validTarget, err)
		}
	}

	// Invalid targets: self-alias, glob, unknown target, chained two-hop alias (same-instance and curated), unknown instance.
	invalidCases := []struct {
		instance string
		model    string
		aliasOf  string
	}{
		{instance: "unknown-inst", model: "m", aliasOf: "claude-opus-4-6"},
		{instance: "work", model: "claude-opus-4-6", aliasOf: "claude-opus-4-6"},
		{instance: "work", model: "claude-opus-4-6", aliasOf: "work/claude-opus-4-6"},
		{instance: "work", model: "new-alias", aliasOf: "new-alias"},
		{instance: "work", model: "new-alias", aliasOf: ""},
		{instance: "work", model: "new-alias", aliasOf: "claude-*"},
		{instance: "work", model: "new-alias", aliasOf: "openai/gpt-*"},
		{instance: "work", model: "new-alias", aliasOf: "*/gpt-5.6"},
		{instance: "work", model: "new-alias", aliasOf: "nonexistent-model"},
		{instance: "work", model: "new-alias", aliasOf: "openai/nonexistent-model"},
		{instance: "work", model: "new-alias", aliasOf: "existing-alias"},
		{instance: "work", model: "new-alias", aliasOf: "anthropic/claude-sonnet-4-5[1m]"}, // curated alias row in overlay
	}
	for _, tc := range invalidCases {
		if err := r.CheckAliasTarget(tc.instance, tc.model, tc.aliasOf); err == nil {
			t.Fatalf("CheckAliasTarget(%q, %q, %q) = nil, want error", tc.instance, tc.model, tc.aliasOf)
		}
	}
}

func TestInstanceModelsSummary(t *testing.T) {
	cfg := `
[providers.work]
base = "anthropic"
api_key = "sk"

[providers.work.models."claude-opus-4-6"]
disabled = true

[providers.work.models."claude-opus-4-5[1m]"]
compaction_threshold = 250000

[providers.work.models."deepseek/deepseek-4.1-flash"]
context_window = 1000000
compaction_threshold = 250000

[providers.work.models."custom-bare"]

[providers.work.models."clamped-custom"]
context_window = 128000
compaction_threshold = 250000
`
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "sk"}, cfg)

	// Implicit instance ("openai") has no user layer overrides -> AuthoredOnly=false, Customized=false.
	openaiModels, err := r.InstanceModels("openai")
	if err != nil {
		t.Fatalf("InstanceModels(openai): %v", err)
	}
	openaiByID := map[string]InstanceModel{}
	for _, m := range openaiModels {
		openaiByID[m.ID] = m
	}
	gpt56, ok := openaiByID["gpt-5.6"]
	if !ok {
		t.Fatalf("gpt-5.6 missing from openai InstanceModels")
	}
	if gpt56.Disabled || gpt56.AuthoredOnly || gpt56.Customized || gpt56.ContextWindow == nil || *gpt56.ContextWindow <= 0 || gpt56.CompactionThreshold != nil {
		t.Fatalf("unexpected openai/gpt-5.6 summary: %+v", gpt56)
	}

	// Explicit instance ("work") before live listing.
	workModels, err := r.InstanceModels("work")
	if err != nil {
		t.Fatalf("InstanceModels(work): %v", err)
	}
	byID := map[string]InstanceModel{}
	for _, m := range workModels {
		byID[m.ID] = m
	}

	// Catalog row with no config row: AuthoredOnly=false, Customized=false.
	sonnet5 := byID["claude-sonnet-5"]
	if sonnet5.Disabled || sonnet5.AuthoredOnly || sonnet5.Customized || sonnet5.ContextWindow == nil {
		t.Fatalf("unexpected claude-sonnet-5 summary: %+v", sonnet5)
	}
	if r.IsAuthoredOnlyModel("work", "claude-sonnet-5") {
		t.Fatal("IsAuthoredOnlyModel(claude-sonnet-5) = true, want false")
	}

	// Catalog row with only disabled = true: Disabled=true, AuthoredOnly=false, Customized=false.
	opus46 := byID["claude-opus-4-6"]
	if !opus46.Disabled || opus46.AuthoredOnly || opus46.Customized {
		t.Fatalf("unexpected claude-opus-4-6 summary (only disabled=true must not mark Customized): %+v", opus46)
	}
	if r.IsAuthoredOnlyModel("work", "claude-opus-4-6") {
		t.Fatal("IsAuthoredOnlyModel(claude-opus-4-6) = true, want false")
	}

	// Bracketed curated overlay alias row with compaction_threshold: AuthoredOnly=false, Customized=true.
	opus1m := byID["claude-opus-4-5[1m]"]
	if opus1m.Disabled || opus1m.AuthoredOnly || !opus1m.Customized ||
		opus1m.ContextWindow == nil || *opus1m.ContextWindow != 1000000 ||
		opus1m.CompactionThreshold == nil || *opus1m.CompactionThreshold != 250000 {
		t.Fatalf("unexpected claude-opus-4-5[1m] summary: %+v", opus1m)
	}
	if r.IsAuthoredOnlyModel("work", "claude-opus-4-5[1m]") {
		t.Fatal("IsAuthoredOnlyModel(claude-opus-4-5[1m]) = true, want false")
	}

	// Slashed custom model: AuthoredOnly=true, Customized=true, ContextWindow=1000000, CompactionThreshold=250000.
	ds := byID["deepseek/deepseek-4.1-flash"]
	if ds.Disabled || !ds.AuthoredOnly || !ds.Customized ||
		ds.ContextWindow == nil || *ds.ContextWindow != 1000000 ||
		ds.CompactionThreshold == nil || *ds.CompactionThreshold != 250000 {
		t.Fatalf("unexpected deepseek/deepseek-4.1-flash summary: %+v", ds)
	}
	if !r.IsAuthoredOnlyModel("work", "deepseek/deepseek-4.1-flash") {
		t.Fatal("IsAuthoredOnlyModel(deepseek/deepseek-4.1-flash) = false, want true")
	}

	// Bare custom model (no fields set other than table header): AuthoredOnly=true, Customized=true.
	bare := byID["custom-bare"]
	if bare.Disabled || !bare.AuthoredOnly || !bare.Customized {
		t.Fatalf("unexpected custom-bare summary: %+v", bare)
	}
	if !r.IsAuthoredOnlyModel("work", "custom-bare") {
		t.Fatal("IsAuthoredOnlyModel(custom-bare) = false, want true")
	}

	// Clamped custom model: CompactionThreshold >= ContextWindow is clamped to nil in summary.
	clamped := byID["clamped-custom"]
	if !clamped.AuthoredOnly || !clamped.Customized || clamped.ContextWindow == nil || *clamped.ContextWindow != 128000 || clamped.CompactionThreshold != nil {
		t.Fatalf("unexpected clamped-custom summary: %+v", clamped)
	}

	// Live-overlaid: snapshot-only row ("claude-opus-4-6" is in snapshot, not in overlay) hidden by live listing,
	// but present in config -> becomes AuthoredOnly=true and Customized=true!
	r.ApplyLive("work", []Model{{ID: "claude-sonnet-5", Caps: Caps{ContextWindow: new(400000)}}})
	if !r.IsAuthoredOnlyModel("work", "claude-opus-4-6") {
		t.Fatal("snapshot row hidden by live listing and re-added in config must be AuthoredOnly=true")
	}
	// Overlay row ("claude-opus-4-5[1m]") remains in non-config visible IDs even with live listing -> AuthoredOnly=false.
	if r.IsAuthoredOnlyModel("work", "claude-opus-4-5[1m]") {
		t.Fatal("overlay row must remain AuthoredOnly=false when live listing is active")
	}
	workLiveModels, err := r.InstanceModels("work")
	if err != nil {
		t.Fatalf("InstanceModels(work) with live: %v", err)
	}
	for _, m := range workLiveModels {
		facts, err := r.ResolveInstanceModelFacts("work", m.ID)
		if err != nil {
			t.Fatalf("ResolveInstanceModelFacts(work, %s): %v", m.ID, err)
		}
		if m.Disabled != BoolValue(facts.Model.Disabled) {
			t.Fatalf("%s: summary Disabled=%v != facts Disabled=%v", m.ID, m.Disabled, BoolValue(facts.Model.Disabled))
		}
		if !reflect.DeepEqual(m.ContextWindow, facts.Caps.ContextWindow) {
			t.Fatalf("%s: summary ContextWindow=%v != facts ContextWindow=%v", m.ID, m.ContextWindow, facts.Caps.ContextWindow)
		}
		if !reflect.DeepEqual(m.CompactionThreshold, facts.Caps.CompactionThreshold) {
			t.Fatalf("%s: summary CompactionThreshold=%v != facts CompactionThreshold=%v", m.ID, m.CompactionThreshold, facts.Caps.CompactionThreshold)
		}
	}
}
