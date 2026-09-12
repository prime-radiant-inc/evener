package registry

import "testing"

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
