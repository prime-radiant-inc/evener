package registry

import "testing"

// The alias-walk fixtures: a curated overlay row may dangle (upstream
// dropped the target) or name glob parts, and both degrade to hidden rows
// that still resolve with a warning; a config provider's glob rows may name
// aliases and glob parts because the load-time alias validation checks
// exact rows only.
const aliasWalkOverlay = `
[providers.anthropic.models.dang]
alias_of = "missing-row"
[providers.anthropic.models.dglob]
alias_of = "x*/y"
[providers.anthropic.models.claude-testrow]
family = "claude"
[providers.anthropic.models.flagrow]
disabled = false
`

const aliasWalkConfig = `
[providers.p]
protocol = "openai-chat"
base_url = "https://x/v1"
[providers.p.models.t]
family = "tt"
[providers.p.models.t2]
family = "t2"
disabled = false
[providers.p.models.b]
alias_of = "t"
[providers.p.models.a2]
alias_of = "t2"
[providers.q]
protocol = "openai-chat"
base_url = "https://x/v1"
[providers.q.models."k*"]
alias_of = "x*/y"
[providers.q.models."h*"]
alias_of = "p/b"
`

func aliasWalkRegistry(t *testing.T) *Registry {
	t.Helper()
	return fixtureLoad(t,
		map[string]string{"ANTHROPIC_API_KEY": "sk-x"},
		aliasWalkConfig,
		WithOverlay(overlayWith(aliasWalkOverlay)))
}

// A dangling alias stays a warning: the row resolves from what it says
// itself, and the target lookup naming glob parts is the same refusal.
func TestResolveDanglingAliasWarnsAndResolves(t *testing.T) {
	t.Parallel()
	r := aliasWalkRegistry(t)
	for _, ref := range []string{"anthropic/dang", "anthropic/dglob"} {
		res, err := r.Resolve(ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if len(res.Warnings) == 0 {
			t.Fatalf("%s: a dangling alias must warn", ref)
		}
		if res.Model.ID == "" {
			t.Fatalf("%s: row did not resolve", ref)
		}
	}
}

// FindModel asks the disabled verdict for rows that may disable; an alias
// row with a missing target answers through its own replay, and a
// region-prefixed spelling of a row replays against the canonical row it
// resolves to.
func TestFindModelDisabledVerdicts(t *testing.T) {
	t.Parallel()
	r := aliasWalkRegistry(t)
	if got := r.FindModel("dang"); len(got) == 0 {
		t.Fatal("a dangling-alias row the config did not disable must be findable")
	}
	refs := r.FindModel("us.claude-testrow")
	if len(refs) == 0 {
		t.Fatal("a region-prefixed spelling must resolve to its canonical row")
	}
	found := false
	for _, ref := range refs {
		if ref.Instance == "anthropic" && ref.Model == "us.claude-testrow" {
			found = true
		}
	}
	if !found {
		t.Fatalf("FindModel(us.claude-testrow) = %+v, want the anthropic ref verbatim", refs)
	}
}

// The cross-provider disable walk skips alias rows whose reference carries
// glob parts, and rows whose target is itself an alias (one hop only), and
// still lists the instance's models — glob rows are not toggleable rows,
// so q lists none.
func TestInstanceModelsWalksAliasEdges(t *testing.T) {
	t.Parallel()
	r := aliasWalkRegistry(t)
	rows, err := r.InstanceModels("q")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("q rows = %+v, want none (globs are not toggleable rows)", rows)
	}
}

// A same-provider alias follows its target's Disabled verdict in lockstep:
// an explicitly-enabled target records the inherited verdict as alias
// provenance.
func TestSameProviderAliasLockstepProvenance(t *testing.T) {
	t.Parallel()
	r := aliasWalkRegistry(t)
	res, err := r.Resolve("p/a2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model.Disabled == nil || *res.Model.Disabled {
		t.Fatalf("a2 must inherit t2's explicit enabled verdict: %+v", res.Model)
	}
	if got := res.Provenance["Disabled"]; got != provAlias {
		t.Fatalf("Disabled provenance = %q, want %q", got, provAlias)
	}
}
