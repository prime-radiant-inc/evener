package registry

import (
	"slices"
	"testing"
)

func TestCodexAstraAvailableOnNamedInstance(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.subscription]\nbase = \"openai-codex\"\n")
	res := mustResolve(t, r, "subscription/gpt-6-astra")
	if res.WireID != "gpt-6-astra" || res.Protocol != ProtocolOpenAIResponses || res.Transport.Auth != AuthOAuthOpenAICodex {
		t.Fatalf("wrong Astra route: %+v", res)
	}
	if res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 272000 || !BoolValue(res.Caps.Tools) || !BoolValue(res.Caps.Reasoning) {
		t.Fatalf("missing Codex model facts: %+v", res.Caps)
	}
	if res.Caps.EffortOffCapable() {
		t.Fatal("Astra must not advertise reasoning off")
	}
	r.ApplyLive("subscription", []Model{{ID: "gpt-6-astra", Caps: Caps{ContextWindow: new(872000), DefaultEffort: new("medium")}}})
	res = mustResolve(t, r, "subscription/gpt-6-astra")
	if *res.Caps.ContextWindow != 872000 || StringValue(res.Caps.DefaultEffort) != "medium" || !BoolValue(res.Caps.ResponsesLite) {
		t.Fatalf("live facts must update without losing Lite framing: %+v", res.Caps)
	}
}

func TestCodexCurrentModelsAvailableOnNamedInstance(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.subscription]\nbase = \"openai-codex\"\n")
	wantEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	for _, tc := range []struct {
		id            string
		defaultEffort string
	}{
		{id: "gpt-6-luna", defaultEffort: "medium"},
		{id: "gpt-6.1-sol", defaultEffort: "low"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			res := mustResolve(t, r, "subscription/"+tc.id)
			if res.WireID != tc.id || res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 272000 {
				t.Fatalf("wrong Codex route or context: %+v", res)
			}
			if !BoolValue(res.Caps.Tools) || !BoolValue(res.Caps.StructuredOutput) || !BoolValue(res.Caps.Reasoning) || BoolValue(res.Caps.Sampling) {
				t.Fatalf("missing Codex capabilities: %+v", res.Caps)
			}
			if !slices.Equal(res.Caps.EffortValues, wantEfforts) || StringValue(res.Caps.DefaultEffort) != tc.defaultEffort {
				t.Fatalf("wrong reasoning efforts: %+v", res.Caps)
			}
			if !slices.Equal(res.Caps.InputModalities, []string{"text", "image"}) || !BoolValue(res.Caps.ResponsesLite) || StringValue(res.Caps.ImageDetail) != "original" {
				t.Fatalf("wrong Codex input or framing facts: %+v", res.Caps)
			}
			if !BoolValue(res.Caps.ThinkingAlwaysOn) || StringValue(res.Caps.ReasoningSummary) != "detailed" {
				t.Fatalf("wrong Codex reasoning presentation: %+v", res.Caps)
			}
			if res.Transport.Body["reasoning.context"] != "all_turns" || res.Transport.Body["text.verbosity"] != "low" || res.Transport.Body["parallel_tool_calls"] != false {
				t.Fatalf("wrong Codex request body: %+v", res.Transport.Body)
			}
		})
	}
}

func TestCodexSolOneMillionAliasUsesBaseWireID(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.subscription]\nbase = \"openai-codex\"\n")
	res := mustResolve(t, r, "subscription/gpt-6.1-sol-1m")
	if res.WireID != "gpt-6.1-sol" || res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 872000 {
		t.Fatalf("wrong 1M alias: %+v", res)
	}
}

// A window the alias row declares is the alias's own fact: the base model's
// live row (taken from the shared wire id) must not replace it, while a live
// row listed under the alias's own id still reports the truth.
func TestAliasDeclaredWindowBeatsLiveRowOfWireID(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.subscription]\nbase = \"openai-codex\"\n")
	r.ApplyLive("subscription", []Model{{ID: "gpt-6.1-sol", Caps: Caps{ContextWindow: new(524288)}}})
	res := mustResolve(t, r, "subscription/gpt-6.1-sol-1m")
	if res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 872000 {
		t.Fatalf("the base model's live row replaced the alias's declared window: %v", res.Caps.ContextWindow)
	}
	base := mustResolve(t, r, "subscription/gpt-6.1-sol")
	if *base.Caps.ContextWindow != 524288 || base.Provenance["ContextWindow"] != "live" {
		t.Fatalf("the base model keeps its own live window: %v %v", base.Caps.ContextWindow, base.Provenance["ContextWindow"])
	}
	r.ApplyLive("subscription", []Model{
		{ID: "gpt-6.1-sol", Caps: Caps{ContextWindow: new(524288)}},
		{ID: "gpt-6.1-sol-1m", Caps: Caps{ContextWindow: new(900000)}},
	})
	res = mustResolve(t, r, "subscription/gpt-6.1-sol-1m")
	if *res.Caps.ContextWindow != 900000 || res.Provenance["ContextWindow"] != "live" {
		t.Fatalf("a live row under the alias's own id still wins: %v %v", res.Caps.ContextWindow, res.Provenance["ContextWindow"])
	}
}

// A narrowed instance keeps the shaping glob that applies to the rows it
// kept: Astra still resolves with the Lite framing and its window.
func TestCodexInheritModelsMatchingKeepsShapingGlobForKeptRows(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.subscription]\nbase = \"openai-codex\"\ninherit_models_matching = [\"gpt-6-astra\"]\n")
	res := mustResolve(t, r, "subscription/gpt-6-astra")
	if res.Synthesized || !BoolValue(res.Caps.ResponsesLite) || res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 272000 || res.Transport.Body["text.verbosity"] != "low" {
		t.Fatalf("narrowed Astra lost its shaping glob: %+v", res)
	}
	if narrowed, err := r.Resolve("subscription/gpt-6-luna"); err == nil {
		t.Fatalf("gpt-6-luna must stay dropped by the narrowing: %+v", narrowed)
	}
}

func TestConfigAliasDeclaredWindowBeatsLiveRowOfWireID(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.subscription]\nbase = \"openai-codex\"\n[providers.subscription.models.\"sol-wide\"]\nalias_of = \"gpt-6.1-sol\"\nwire_id = \"gpt-6.1-sol\"\ncontext_window = 600000\n")
	r.ApplyLive("subscription", []Model{{ID: "gpt-6.1-sol", Caps: Caps{ContextWindow: new(524288)}}})
	res := mustResolve(t, r, "subscription/sol-wide")
	if res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 600000 || res.Provenance["ContextWindow"] != "config/row" {
		t.Fatalf("config alias window lost to the base live row: %v %v", res.Caps.ContextWindow, res.Provenance["ContextWindow"])
	}
}

// A window the alias merely inherits from a cross-provider target is the
// target's figure: the alias's own provider listing, found by the shared wire
// id, outranks it. Only a window declared on the alias row itself is protected.
func TestInheritedAliasWindowYieldsToLiveRowOfWireID(t *testing.T) {
	r := fixtureLoad(t, nil, "[providers.mine]\nbase = \"openai\"\n[providers.mine.models.\"sol-alias\"]\nalias_of = \"anthropic/claude-mythos-preview\"\nwire_id = \"claude-mythos-preview\"\n")
	res := mustResolve(t, r, "mine/sol-alias")
	if res.Caps.ContextWindow == nil || *res.Caps.ContextWindow != 1000000 || res.Provenance["ContextWindow"] != "overlay/row" {
		t.Fatalf("fixture must inherit the target's overlay window: %v %v", res.Caps.ContextWindow, res.Provenance["ContextWindow"])
	}
	r.ApplyLive("mine", []Model{{ID: "claude-mythos-preview", Caps: Caps{ContextWindow: new(524288)}}})
	res = mustResolve(t, r, "mine/sol-alias")
	if *res.Caps.ContextWindow != 524288 || res.Provenance["ContextWindow"] != "live" {
		t.Fatalf("a live row for the wire id must beat an inherited window: %v %v", *res.Caps.ContextWindow, res.Provenance["ContextWindow"])
	}
}
