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
