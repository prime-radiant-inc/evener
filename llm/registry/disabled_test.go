package registry

import (
	"errors"
	"slices"
	"strings"
	"testing"
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
