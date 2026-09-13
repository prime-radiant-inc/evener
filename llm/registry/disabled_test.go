package registry

import (
	"errors"
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

func TestAliasTarget_RefusesCrossProviderTarget(t *testing.T) {
	// The config layer cannot author another provider's rows: toggling a
	// cross-provider alias is refused, and nothing is writable through it.
	r := fixtureLoad(t, nil, "[providers.mine]\nbase = \"openai-codex\"\napi_key = \"sk\"\n[providers.mine.models.\"house-model\"]\nalias_of = \"openai/gpt-5.6\"\n")
	if _, err := r.AliasTarget("mine", "house-model"); err == nil {
		t.Fatal("AliasTarget(cross-provider alias) must error")
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
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"claude-*\"]\ndisabled = true\n[providers.anthropic.models.\"claude-opus-5\"]\ndisabled = false\n")
	models, err := r.InstanceModels("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	kept := slices.ContainsFunc(r.FindModel("claude-opus-5"), func(ref Ref) bool { return ref.Instance == "anthropic" })
	if !kept {
		t.Fatal("FindModel dropped a re-enabled row")
	}
	if _, err := r.Resolve("anthropic/claude-opus-5"); err != nil {
		t.Fatalf("Resolve(re-enabled) = %v, want nil", err)
	}
	for _, m := range models {
		_, err := r.Resolve("anthropic/" + m.ID)
		if m.Disabled && !errors.Is(err, ErrModelDisabled) {
			t.Fatalf("Resolve(%s) = %v, want ErrModelDisabled", m.ID, err)
		}
		if !m.Disabled && errors.Is(err, ErrModelDisabled) {
			t.Fatalf("Resolve(%s) disabled, but the inventory says enabled", m.ID)
		}
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

func TestInstanceModels_SkipsAliasRows(t *testing.T) {
	// The flag lives on the target, so the inventory offers no toggle on
	// the alias itself: every row it lists is directly writable.
	r := fixtureLoad(t, nil, "[providers.anthropic.models.\"house-model\"]\nalias_of = \"claude-opus-4-6\"\n")
	models, err := r.InstanceModels("anthropic")
	if err != nil {
		t.Fatalf("InstanceModels: %v", err)
	}
	for _, m := range models {
		if m.ID == "house-model" {
			t.Fatalf("alias row must not list: %+v", models)
		}
	}
	if len(models) == 0 {
		t.Fatal("inventory must still list the target rows")
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
