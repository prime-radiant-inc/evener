package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The shadow is priced through a protocol's deriver only when a protocol serves
// the request.
//
// An override marshals the definition it was handed, so its received list is its
// wire list. Pricing it through a built-in deriver bills an emitted list the
// override never sends, and an input cap between that figure and the override's
// own estimate rejects a request the override could dispatch. The inflated
// reading also outlives the case that produced it, because it is recorded on a
// request that falls back to full history when the override has no continuation
// planner.
func TestContinuationShadowLeavesAnOverrideServedRouteUnpriced(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "shadow-override"})
	// The row is a Responses route that requires explicit types on every node —
	// the route whose builder states one — so pricing it through the deriver is
	// observable. It is built here rather than taken from the live registry so
	// the test does not depend on which instance happens to be configured.
	row := registry.Resolved{
		Instance: "shadow-override",
		ModelID:  "test",
		WireID:   "test",
	}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(false)
	profile := testOpenAICompatProfile("shadow-override", "test", 0).WithResolved(row)
	sess := newSession(t, withClient(client), withProfile(profile), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
	defer sess.Close()

	if !sess.servesAnOverride(profile) {
		t.Fatalf("the registered adapter does not read as serving this profile, so this test cannot see the " +
			"difference it exists to pin")
	}
	// The route has to be one the deriver would price, or the guard above is
	// invisible and the test passes for the wrong reason.
	if priced := llm.PriceToolSchemas(llm.Request{Tools: nil}, row); priced.ToolsJSONBytes != 0 {
		t.Fatalf("an empty catalog was priced, so the fixture is wrong about what the deriver does")
	}

	// A closed typeless value set: a built-in Responses builder states a type on
	// it, which is what makes the protocol price differ from the advertised one.
	tools := []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
	req := llm.Request{
		Provider:    profile.ID(),
		Model:       profile.Model(),
		Messages:    []llm.Message{llm.User("hello")},
		Tools:       tools,
		HistoryMode: llm.HistoryModeResponsesDelta,
	}
	advertised := llm.ToolsJSONBytes(tools)

	var observed []llm.Request
	sess.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(r llm.Request) (int, bool) {
		observed = append(observed, r)
		return llm.EstimateInputTokensForResolved(profile.Resolved(), r).Tokens, true
	}
	sess.applyResponsesContinuationShadowEstimate(profile, req)

	if len(observed) != 1 {
		t.Fatalf("shadow estimate ran %d times, want once", len(observed))
	}
	if got := observed[0].ToolsJSONBytes; got != req.ToolsJSONBytes {
		t.Fatalf("override-served shadow billed ToolsJSONBytes = %d, want the advertised %d: an override "+
			"marshals the definition it was handed, so pricing it through a built-in deriver bills a list "+
			"the wire never carries", got, req.ToolsJSONBytes)
	}
	if observed[0].ToolsJSONBytes == advertised+1 {
		t.Fatalf("fixture is degenerate: the advertised count is the only one in play")
	}
}

// The full-history shadow is taken before the delta is built, and the reading it
// produces is carried past that build: admission takes the larger of the local
// estimate and this one. A route whose builder rewrites tool schemas therefore
// has to be billed over the emitted list here, or the full-history figure
// describes a catalog the builder has already replaced and the smaller reading
// is the one that governs.
//
// The stub observes the request it is handed rather than the count, because the
// count is what the fix changes.
func TestContinuationShadowBillsTheSchemasTheRouteEmits(t *testing.T) {
	t.Parallel()
	//
	// The row is a Responses route that requires explicit types on every node,
	// which is the route whose builder states one. It is built here rather than
	// taken from the live registry so the test does not depend on which
	// instance happens to be configured.
	row := registry.Resolved{
		Instance: "shadow-emitted",
		ModelID:  "test",
		WireID:   "test",
	}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(false)
	profile := testOpenAICompatProfile("shadow-emitted", "test", 0).WithResolved(row)
	sess := newSession(t, withProfile(profile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	// A typeless closed value set: the builder states a type on it, which is
	// exactly the difference between the advertised count and the emitted one.
	tools := []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
	req := llm.Request{
		Provider:    profile.ID(),
		Model:       profile.Model(),
		Messages:    []llm.Message{llm.User("hello")},
		Tools:       tools,
		HistoryMode: llm.HistoryModeResponsesDelta,
	}
	advertised := llm.ToolsJSONBytes(tools)

	var observed []llm.Request
	sess.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(r llm.Request) (int, bool) {
		observed = append(observed, r)
		return llm.EstimateInputTokensForResolved(profile.Resolved(), r).Tokens, true
	}
	got := sess.applyResponsesContinuationShadowEstimate(profile, req)

	if len(observed) != 1 {
		t.Fatalf("shadow estimate ran %d times, want once", len(observed))
	}
	if observed[0].ToolsJSONBytes <= advertised {
		t.Fatalf("shadow billed ToolsJSONBytes = %d, want more than the advertised count %d: the route's builder "+
			"states a type on the closed value set, so the full-history reading must bill the emitted list",
			observed[0].ToolsJSONBytes, advertised)
	}
	// The reading actually reaches the field admission takes the maximum of.
	if got.FullHistoryInputTokensEstimate <= 0 {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want the priced reading", got.FullHistoryInputTokensEstimate)
	}
	priced := llm.EstimateInputTokensForResolved(profile.Resolved(), llm.PriceToolSchemas(req, profile.Resolved())).Tokens
	if got.FullHistoryInputTokensEstimate < priced {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want at least the emitted-list price %d",
			got.FullHistoryInputTokensEstimate, priced)
	}
}
