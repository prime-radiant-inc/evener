package registry

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseConfig_DisabledRow(t *testing.T) {
	l, err := ParseConfig([]byte("[providers.work.models.\"glm-5.2-nvfp4\"]\ndisabled = true\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	row := l.Providers["work"].Models["glm-5.2-nvfp4"]
	if !BoolValue(row.Disabled) {
		t.Fatalf("row.Disabled = %v, want true: %+v", row.Disabled, row)
	}
}

func TestParseConfig_DisabledGlobRow(t *testing.T) {
	l, err := ParseConfig([]byte("[providers.work.models.\"glm-*\"]\ndisabled = true\n[models.\"*gemini-3*\"]\ndisabled = true\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !BoolValue(l.Providers["work"].Models["glm-*"].Disabled) {
		t.Fatalf("provider glob row not disabled: %+v", l.Providers["work"].Models["glm-*"])
	}
	if !BoolValue(l.TopGlobs["*gemini-3*"].Disabled) {
		t.Fatalf("top-level glob row not disabled: %+v", l.TopGlobs["*gemini-3*"])
	}
}

func TestLoad_DisabledRowMerges(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n")
	row := r.explicit["anthropic"].head.Models["claude-opus-4-6"]
	if !BoolValue(row.Disabled) {
		t.Fatalf("merged row.Disabled = %v, want true: %+v", row.Disabled, row)
	}
}

func TestResolve_DisabledRowIsBlocked(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n")
	_, err := r.Resolve("anthropic/claude-opus-4-6")
	if !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve disabled = %v, want ErrModelDisabled", err)
	}
}

func TestResolve_DisabledGlobBlocksMatch(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n")
	_, err := r.Resolve("anthropic/claude-opus-4-6")
	if !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve glob-disabled = %v, want ErrModelDisabled", err)
	}
}

func TestResolve_DisabledGlobExactReEnable(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = false\n")
	if _, err := r.Resolve("anthropic/claude-opus-4-6"); err != nil {
		t.Fatalf("exact disabled=false must re-enable the row: %v", err)
	}
}

func TestResolve_AliasOfDisabledTargetIsBlocked(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n")
	_, err := r.Resolve("anthropic/house-model")
	if !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve alias of disabled = %v, want ErrModelDisabled", err)
	}
}

func TestResolve_UserTopGlobBeatsLiveForImplicitInstance(t *testing.T) {
	// Spec order is layers, then live, then user config: a user
	// top-level glob scalar must win over the live listing's value on
	// an implicit instance too — not just on explicit ones.
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "sk"}, "[models.\"gpt-*\"]\ncontext_window = 100\n")
	r.ApplyLive("openai", []Model{{ID: "gpt-5.6", Caps: Caps{ContextWindow: new(200)}}})
	res, err := r.Resolve("openai/gpt-5.6")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 100 {
		t.Fatalf("ContextWindow = %v, want 100 from user config over live 200", res.Caps.ContextWindow)
	}
}

func TestResolve_UserConfigBeatsOverlayTopGlobScalar(t *testing.T) {
	// The curated overlay's *claude-opus-4-5* top glob sets a scalar;
	// a user exact row setting its own value must win. Disabled-only
	// tests cannot catch an ordering inversion because no overlay
	// glob sets Disabled today.
	r := fixtureLoad(t, nil, "[providers.custom]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n[providers.custom.models.\"claude-opus-4-5-x\"]\nthinking_shape = \"budget\"\n")
	res, err := r.Resolve("custom/claude-opus-4-5-x")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Caps.ThinkingShape == nil || *res.Caps.ThinkingShape != "budget" {
		t.Fatalf("ThinkingShape = %+v, want user budget over overlay budget+effort", res.Caps.ThinkingShape)
	}
}

func TestResolve_UserDisabledBeatsCuratedOverlayGlob(t *testing.T) {
	// Standalone instance with a user exact disabled=true: layer rows
	// replay per layer in order, so the user's verdict must stand no
	// matter what any curated overlay glob carries. (Today no overlay
	// glob sets Disabled; this pins the ordering, not a current value.)
	r := fixtureLoad(t, nil, "[providers.custom]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"sk\"\n[providers.custom.models.\"claude-opus-4-5-x\"]\ndisabled = true\n")
	if _, err := r.Resolve("custom/claude-opus-4-5-x"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve user-disabled row = %v, want ErrModelDisabled", err)
	}
}

func TestResolve_UserTopGlobDisablesImplicitInstance(t *testing.T) {
	// A user top-level [models."<glob>"] disabled=true applies to every
	// provider — including an implicit instance whose record has no
	// LayerConfig layer of its own.
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "sk"}, "[models.\"gpt-*\"]\ndisabled = true\n")
	if _, err := r.Resolve("openai/gpt-5.6"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve implicit under user top glob = %v, want ErrModelDisabled", err)
	}
	if got := r.FindModel("gpt-5.6"); len(got) != 0 {
		t.Fatalf("FindModel(gpt-5.6) = %v, want no serving instance", got)
	}
}

func TestResolve_CrossProviderAliasOfDisabledTargetIsBlocked(t *testing.T) {
	// A cross-provider alias ("provider/id") must inherit its target's
	// Disabled verdict from the target's instance record, not the
	// curated-only record. The verdict's own repro:
	// [providers.mine] base=openai-codex + openai/gpt-5.6 disabled.
	r := fixtureLoad(t, nil, "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n[providers.openai.models.\"gpt-5.6\"]\ndisabled = true\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n")
	if _, err := r.Resolve("openai/gpt-5.6"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve target = %v, want ErrModelDisabled", err)
	}
	if _, err := r.Resolve("mine/house-model"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve cross-provider alias of disabled = %v, want ErrModelDisabled", err)
	}
	if got := r.FindModel("house-model"); len(got) != 0 {
		t.Fatalf("FindModel(house-model) = %v, want no serving instance", got)
	}
}

func TestResolve_CrossProviderAliasOwnFlagOverridesTarget(t *testing.T) {
	// The target's verdict is the cross-provider alias's default; a flag on
	// the alias's own row — what this instance's toggle writes — overrides
	// it both ways, so one connection can disable or re-enable the model
	// without touching the target's record.
	const mine = "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n"

	// Disabled here, served there.
	r := fixtureLoad(t, nil, mine+"[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\ndisabled = true\n")
	if _, err := r.Resolve("mine/house-model"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(own-disabled alias) = %v, want ErrModelDisabled", err)
	}
	if _, err := r.Resolve("openai/gpt-5.6"); err != nil {
		t.Fatalf("Resolve(target) = %v, want nil: the target's connection is untouched", err)
	}
	if got := r.FindModel("house-model"); len(got) != 0 {
		t.Fatalf("FindModel(house-model) = %v, want no serving instance", got)
	}

	// Served here, disabled there: the explicit false wins over the
	// inherited verdict, and the facts still seed from the target's row.
	control := fixtureLoad(t, nil, mine+"[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n")
	r2 := fixtureLoad(t, nil, mine+"[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\ndisabled = false\n[providers.openai.models.\"gpt-5.6\"]\ndisabled = true\n")
	if _, err := r2.Resolve("openai/gpt-5.6"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(target) = %v, want ErrModelDisabled", err)
	}
	res, err := r2.Resolve("mine/house-model")
	if err != nil {
		t.Fatalf("Resolve(own-enabled alias over disabled target) = %v, want nil", err)
	}
	// The control is the same alias with the target still served: the
	// override path must resolve to the same facts, which proves the facts
	// still seed from the target the flag disables (only the verdict differs).
	want, err := control.Resolve("mine/house-model")
	if err != nil {
		t.Fatalf("control Resolve(alias) = %v", err)
	}
	if !reflect.DeepEqual(res.Caps, want.Caps) {
		t.Fatalf("seeded caps = %+v, want the enabled target's %+v", res.Caps, want.Caps)
	}
	if got := r2.FindModel("house-model"); len(got) != 1 || got[0].Instance != "mine" {
		t.Fatalf("FindModel(house-model) = %v, want the alias's own instance", got)
	}
}

func TestResolve_CrossProviderAliasTopGlobOverridesTarget(t *testing.T) {
	// A user top-level glob matching the alias id is an own flag too: it
	// overrides the inherited verdict the way an exact row does, and the
	// target's own connection stays served.
	r := fixtureLoad(t, nil, "[models.\"house-*\"]\ndisabled = true\n[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n")
	if _, err := r.Resolve("mine/house-model"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(alias under top glob) = %v, want ErrModelDisabled", err)
	}
	if _, err := r.Resolve("openai/gpt-5.6"); err != nil {
		t.Fatalf("Resolve(target) = %v, want nil", err)
	}
	if got := r.FindModel("house-model"); len(got) != 0 {
		t.Fatalf("FindModel(house-model) = %v, want no serving instance", got)
	}
}

func TestResolve_CuratedCrossProviderAliasOwnFlagOverrides(t *testing.T) {
	// The shape the sheet shows: openai-codex's curated gpt-5.6-sol aliases
	// onto openai's row, and a toggle authors a providers.toml row for it
	// carrying only the flag (the hub's shadow shape: no provider header,
	// which keeps the curated overlay in play). The alias stays an alias,
	// and the flag overrides the verdict inherited from the disabled target.
	r := fixtureLoad(t, nil, "[providers.openai.models.\"gpt-5.6-sol\"]\ndisabled = true\n[providers.openai-codex.models.\"gpt-5.6-sol\"]\ndisabled = false\n")
	res, err := r.Resolve("openai-codex/gpt-5.6-sol")
	if err != nil {
		t.Fatalf("Resolve(curated alias, own enabled over disabled target) = %v, want nil", err)
	}
	if res.Model.AliasOf != "openai/gpt-5.6-sol" {
		t.Fatalf("authored flag row de-aliased the curated row: %+v", res.Model)
	}
	if _, err := r.Resolve("openai/gpt-5.6-sol"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(target) = %v, want ErrModelDisabled", err)
	}
	got := r.FindModel("gpt-5.6-sol")
	if !slices.Contains(got, Ref{Instance: "openai-codex", Model: "gpt-5.6-sol"}) {
		t.Fatalf("FindModel(gpt-5.6-sol) = %v, want the codex alias served", got)
	}
	if slices.ContainsFunc(got, func(ref Ref) bool { return ref.Instance == "openai" }) {
		t.Fatalf("FindModel(gpt-5.6-sol) = %v, want the disabled target's instance dropped", got)
	}
}

func TestRecordMayDisable_AliasCycleDoesNotOverflow(t *testing.T) {
	// A mutual cross-provider alias cycle loads (each target exists) but
	// must not recurse forever when browse paths ask whether anything
	// may be disabled: the cycle carries no Disabled flag anywhere.
	r := fixtureLoad(t, nil, "[providers.anthropic]\nbase = \"anthropic\"\napi_key = \"sk\"\n[providers.anthropic.models.\"gpt-5.5\"]\nalias_of = \"openai/gpt-5.5\"\n[providers.openai]\nbase = \"openai\"\napi_key = \"sk\"\n[providers.openai.models.\"claude-opus-4-6\"]\nalias_of = \"anthropic/claude-opus-4-6\"\n")
	done := make(chan bool, 1)
	go func() {
		defer func() { done <- recover() == nil }()
		_ = r.FindModel("gpt-5.5")
		if _, err := r.InstanceModels("anthropic"); err != nil {
			t.Errorf("InstanceModels: %v", err)
		}
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("alias cycle panicked")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("alias cycle did not terminate")
	}
}

func TestResolve_AliasIgnoresOwnDisabledFlag(t *testing.T) {
	// Lockstep: the alias follows its target. Its own Disabled never
	// applies, so an alias-own disable with an enabled target resolves.
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\ndisabled = true\n")
	if _, err := r.Resolve("anthropic/house-model"); err != nil {
		t.Fatalf("Resolve(alias, own-disabled, target-enabled) = %v, want nil", err)
	}
}

func TestAliasTarget_ResolvesSameProviderPassthroughAndDangling(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n")
	got, err := r.AliasTarget("anthropic", "house-model")
	if err != nil || got != (Ref{Instance: "anthropic", Model: "claude-opus-4-6"}) {
		t.Fatalf("AliasTarget(alias) = %+v, %v; want anthropic/claude-opus-4-6", got, err)
	}
	got, err = r.AliasTarget("anthropic", "claude-opus-4-6")
	if err != nil || got != (Ref{Instance: "anthropic", Model: "claude-opus-4-6"}) {
		t.Fatalf("AliasTarget(exact) = %+v, %v; want passthrough", got, err)
	}
	// A dangling config alias never loads (validateRecord refuses it), so
	// the reachable refusals are glob ids and unknown instances.
	if _, err := r.AliasTarget("anthropic", "claude-*"); err == nil {
		t.Fatal("AliasTarget(glob) must error")
	}
	if _, err := r.AliasTarget("anthropic", "not-a-model"); err == nil {
		t.Fatal("AliasTarget(unknown) must error")
	}
	if _, err := r.AliasTarget("nope", "m"); err == nil {
		t.Fatal("AliasTarget(unknown instance) must error")
	}
}

func TestAliasTarget_CanonicalizesDatedVariant(t *testing.T) {
	// Toggling a dated-suffix variant must write the canonical row the
	// variant resolves to, not author a new exact row for the variant:
	// one logical model, one toggleable row. The custom provider below
	// carries ONLY the canonical row, so the dated reference reaches
	// lookupRow's dated step (in the sample catalog the dated id is
	// itself an exact row, which correctly wins).
	cfg := "[providers.acme]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"k\"\n[providers.acme.models.\"model-x\"]\n"
	r := fixtureLoad(t, nil, cfg)
	got, err := r.AliasTarget("acme", "model-x-20250929")
	if err != nil {
		t.Fatalf("AliasTarget(dated) = %v", err)
	}
	if got != (Ref{Instance: "acme", Model: "model-x"}) {
		t.Fatalf("AliasTarget(dated) = %+v, want acme/model-x", got)
	}
	// And the exact dated row still wins when it exists: authoring a
	// distinct row is then intentional, not a split.
	r2 := fixtureLoad(t, nil, cfg+"[providers.acme.models.\"model-x-20250929\"]\n")
	got2, err := r2.AliasTarget("acme", "model-x-20250929")
	if err != nil {
		t.Fatalf("AliasTarget(exact dated) = %v", err)
	}
	if got2 != (Ref{Instance: "acme", Model: "model-x-20250929"}) {
		t.Fatalf("AliasTarget(exact dated) = %+v, want the exact row", got2)
	}
}

func TestAliasTarget_CrossProviderAliasWritesItsOwnRow(t *testing.T) {
	// A cross-provider alias is a row of this instance, so the toggle writes
	// the alias's own row here: the config layer cannot author the target's
	// record, and each connection carries its own flag.
	r := fixtureLoad(t, nil, "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n")
	got, err := r.AliasTarget("mine", "house-model")
	if err != nil {
		t.Fatalf("AliasTarget(cross-provider alias) = %v", err)
	}
	if got != (Ref{Instance: "mine", Model: "house-model"}) {
		t.Fatalf("AliasTarget(cross-provider alias) = %+v, want mine/house-model", got)
	}
}

func TestAliasTarget_CanonicalizesVariantOfAlias(t *testing.T) {
	// A dated-suffix or region-prefixed spelling of an alias id strips down
	// to the alias row itself. A same-provider alias's flag still lives on
	// its target (lockstep: the alias's own flag replays for nothing), so
	// the variant has to route through the alias branch. Writing the alias
	// row instead is a silent no-op: the toggle reports success and nothing
	// changes.
	cfg := "[providers.acme]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\napi_key = \"k\"\n[providers.acme.models.\"model-x\"]\n[providers.acme.models.\"house-model\"]\nalias_of = \"model-x\"\n"
	r := fixtureLoad(t, nil, cfg)
	for _, variant := range []string{"house-model-20250929", "us.house-model"} {
		got, err := r.AliasTarget("acme", variant)
		if err != nil {
			t.Fatalf("AliasTarget(%q) = %v", variant, err)
		}
		if got != (Ref{Instance: "acme", Model: "model-x"}) {
			t.Fatalf("AliasTarget(%q) = %+v, want the alias's target acme/model-x", variant, got)
		}
	}
}

func TestAliasTarget_ResolvesDisabledTargetForReEnable(t *testing.T) {
	// Write-through must work in both directions: re-enabling through the
	// alias resolves the target even though the target currently fails
	// Resolve with ErrModelDisabled.
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n")
	got, err := r.AliasTarget("anthropic", "house-model")
	if err != nil || got != (Ref{Instance: "anthropic", Model: "claude-opus-4-6"}) {
		t.Fatalf("AliasTarget(alias of disabled) = %+v, %v; want the target ref", got, err)
	}
}

func TestFindModel_SkipsDisabled(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n")
	for _, ref := range r.FindModel("claude-opus-4-6") {
		if ref.Instance == "anthropic" {
			t.Fatalf("FindModel returned disabled row: %v", r.FindModel("claude-opus-4-6"))
		}
	}
}

// TestFindModelAgreesWithResolve pins the lightweight modelDisabled replay
// to resolveOn's verdict across exact, glob, and re-enable shapes: a row
// FindModel keeps must resolve, and one it drops must fail disabled.
func TestFindModelAgreesWithResolve(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n[providers.anthropic.models.\"claude-opus-5\"]\ndisabled = false\n[providers.openai.models.\"gpt-5.6-sol\"]\ndisabled = true\n")
	// Every instance's inventory must agree with Resolve, alias rows
	// included: a cross-provider alias only agrees when the inventory's
	// replay (modelDisabled) applies the same inherited default and own-flag
	// override the full one does. The codex aliases above are the shapes
	// that would drift.
	for _, inst := range r.rankedInstances() {
		models, err := r.InstanceModels(inst.name)
		if err != nil {
			t.Fatalf("InstanceModels(%s): %v", inst.name, err)
		}
		for _, m := range models {
			ref := inst.name + "/" + m.ID
			_, err := r.Resolve(ref)
			if m.Disabled && !errors.Is(err, ErrModelDisabled) {
				t.Fatalf("Resolve(%s) = %v, want ErrModelDisabled", ref, err)
			}
			if !m.Disabled && errors.Is(err, ErrModelDisabled) {
				t.Fatalf("Resolve(%s) disabled, but the inventory says enabled", ref)
			}
		}
	}
	kept := slices.ContainsFunc(r.FindModel("claude-opus-5"), func(ref Ref) bool { return ref.Instance == "anthropic" })
	if !kept {
		t.Fatal("FindModel dropped a re-enabled row")
	}
	if _, err := r.Resolve("anthropic/claude-opus-5"); err != nil {
		t.Fatalf("Resolve(re-enabled) = %v, want nil", err)
	}
}

func TestInstanceModels_ReportsDisabledState(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n[providers.anthropic.models.\"claude-opus-5\"]\ndisabled = false\n")
	models, err := r.InstanceModels("anthropic")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	byID := map[string]InstanceModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if !byID["claude-opus-4-6"].Disabled {
		t.Fatalf("exact disabled row not flagged: %+v", byID["claude-opus-4-6"])
	}
	if !byID["claude-sonnet-5"].Disabled {
		t.Fatalf("glob-disabled row not flagged: %+v", byID["claude-sonnet-5"])
	}
	if byID["claude-opus-5"].Disabled {
		t.Fatalf("re-enabled row flagged disabled: %+v", byID["claude-opus-5"])
	}
	if len(models) == 0 || !slices.IsSortedFunc(models, func(a, b InstanceModel) int { return strings.Compare(a.ID, b.ID) }) {
		t.Fatalf("InstanceModels must be sorted by id: %+v", models)
	}
}

// The cross-provider spelling of the same rule - the Codex family aliases
// gpt-5.6-sol/terra/luna onto openai's rows, which is the case that made these
// names invisible in the sheet while the picker offered them.
func TestInstanceModels_ListsCrossProviderAliasRows(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.openai-codex]\nbase = \"openai\"\napi_key = \"sk\"\n[providers.openai.models.\"gpt-5.6-sol\"]\ndisabled = true\n[providers.openai-codex.models.\"gpt-5.6-sol\"]\nalias_of = \"openai/gpt-5.6-sol\"\n")
	models, err := r.InstanceModels("openai-codex")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	byID := map[string]InstanceModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	alias, ok := byID["gpt-5.6-sol"]
	if !ok {
		t.Fatalf("alias row missing from the inventory: %+v", models)
	}
	if !alias.Disabled {
		t.Fatalf("alias row must report its target's state: %+v", alias)
	}
}

func TestInstanceModels_EveryListedRowIsToggleable(t *testing.T) {
	// The sheet renders one switch per listed row, so every id the inventory
	// returns must name a row a toggle can write: AliasTarget answers for all
	// of them. A cross-provider alias answers with its own row on this
	// instance, which is what makes its switch work per connection.
	r := fixtureLoad(t, nil, "[providers.openai-codex]\nbase = \"openai\"\napi_key = \"sk\"\n[providers.openai.models.\"gpt-5.6-sol\"]\ndisabled = true\n[providers.openai-codex.models.\"gpt-5.6-sol\"]\nalias_of = \"openai/gpt-5.6-sol\"\n")
	for _, instance := range []string{"openai-codex", "openai"} {
		models, err := r.InstanceModels(instance)
		if err != nil {
			t.Fatalf("InstanceModels(%s): %v", instance, err)
		}
		if len(models) == 0 {
			t.Fatalf("InstanceModels(%s) returned no rows", instance)
		}
		for _, m := range models {
			if _, err := r.AliasTarget(instance, m.ID); err != nil {
				t.Errorf("AliasTarget(%s, %q) = %v; every listed row must be toggleable", instance, m.ID, err)
			}
		}
	}
}

func TestInstanceModels_SkipsDanglingAliasRows(t *testing.T) {
	// A curated dangling alias — its target is gone upstream, so load hides
	// the row with a warning — names nothing a toggle could write: AliasTarget
	// refuses it. Listing it would render a switch that can only fail, so the
	// inventory leaves it out and every row it does list stays toggleable.
	r := fixtureLoad(t, nil, "", WithOverlay(overlayWith("[providers.anthropic.models.\"gone\"]\nalias_of = \"claude-nope\"\n")))
	models, err := r.InstanceModels("anthropic")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	if slices.ContainsFunc(models, func(m InstanceModel) bool { return m.ID == "gone" }) {
		t.Fatalf("dangling alias listed in the inventory: %+v", models)
	}
	for _, m := range models {
		if _, err := r.AliasTarget("anthropic", m.ID); err != nil {
			t.Errorf("AliasTarget(%q) = %v; every listed row must be toggleable", m.ID, err)
		}
	}
	if _, err := r.AliasTarget("anthropic", "gone"); err == nil {
		t.Fatal("AliasTarget(dangling alias) must still refuse the write")
	}
}

func TestInstanceModels_CrossProviderAliasMatchesTargetIDGlob(t *testing.T) {
	// Globs replay against the reference and the target row id (spec §4.1
	// order), which is what gives a provider's shaping glob to its aliases.
	// The light browse replay has to match the same set, or the sheet reports
	// the opposite of what Resolve does.
	r := fixtureLoad(t, nil, "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n[providers.mine.models.\"gpt-5.6*\"]\ndisabled = true\n")
	if _, err := r.Resolve("mine/house-model"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(alias under target-id glob) = %v, want ErrModelDisabled", err)
	}
	if got := r.FindModel("house-model"); len(got) != 0 {
		t.Fatalf("FindModel(house-model) = %v, want no serving instance", got)
	}
	models, err := r.InstanceModels("mine")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	byID := map[string]InstanceModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if !byID["house-model"].Disabled {
		t.Fatalf("inventory says enabled while Resolve fails: %+v", byID["house-model"])
	}
}

func TestInstanceModels_IncludesLiveOnlyIDs(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n")
	r.ApplyLive("anthropic", []Model{{ID: "claude-live-new"}, {ID: "embedding-thing"}})
	models, err := r.InstanceModels("anthropic")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	byID := map[string]InstanceModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	live, ok := byID["claude-live-new"]
	if !ok {
		t.Fatalf("live-only id missing from inventory: %+v", models)
	}
	if !live.Disabled {
		t.Fatalf("live id caught by the user glob must flag disabled: %+v", live)
	}
	if _, ok := byID["embedding-thing"]; ok {
		t.Fatalf("non-chat live id must not list: %+v", models)
	}
	if _, err := r.Resolve("anthropic/claude-live-new"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(live, glob-disabled) = %v, want ErrModelDisabled", err)
	}
}

func TestInstanceModels_ListsAliasRowsWithTheirTargetsState(t *testing.T) {
	// An alias row names a model the provider serves under another id, and the
	// picker offers that name, so the inventory lists it too: the toggle path
	// names the row to write (AliasTarget), which is what makes every listed
	// row - alias or not - directly writable. A same-provider alias writes its
	// target, so the state it reports is the target's: the flag lives there.
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n")
	models, err := r.InstanceModels("anthropic")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	byID := map[string]InstanceModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	alias, ok := byID["house-model"]
	if !ok {
		t.Fatalf("alias row missing from the inventory: %+v", models)
	}
	if !alias.Disabled {
		t.Fatalf("alias row must report its target's disabled state: %+v", alias)
	}
	if !byID["claude-opus-4-6"].Disabled {
		t.Fatalf("target row must stay listed and disabled: %+v", byID["claude-opus-4-6"])
	}
	if _, err := r.Resolve("anthropic/house-model"); !errors.Is(err, ErrModelDisabled) {
		t.Fatalf("Resolve(alias) = %v, want ErrModelDisabled (the state the row reported)", err)
	}
}

func TestSnapshotLive_RoundTripsListings(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	r.ApplyLive("anthropic", []Model{{ID: "claude-live-new"}, {ID: "embedding-thing"}})
	snap := r.SnapshotLive()
	rows, ok := snap["anthropic"]
	if !ok || len(rows) != 1 || rows[0].ID != "claude-live-new" {
		t.Fatalf("SnapshotLive = %+v, want the single chat id", snap)
	}
	r2 := fixtureLoad(t, nil, "")
	for inst, models := range snap {
		r2.ApplyLive(inst, models)
	}
	if got := r2.LiveModels("anthropic"); len(got) != 1 || got[0].ID != "claude-live-new" {
		t.Fatalf("restored live = %+v, want claude-live-new", got)
	}
}

func TestMarshalConfig_RoundTripsDisabled(t *testing.T) {
	l := &Layer{Tag: LayerConfig, Providers: map[string]Provider{
		"work": {ID: "work", Base: "openai", Models: map[string]Model{
			"m1": {ID: "m1", Disabled: new(true)},
		}},
	}}
	data, err := MarshalConfig(l)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("ParseConfig of marshaled output: %v\n%s", err, data)
	}
	if !BoolValue(back.Providers["work"].Models["m1"].Disabled) {
		t.Fatalf("disabled lost in round trip:\n%s", data)
	}
}
