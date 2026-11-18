package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

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
