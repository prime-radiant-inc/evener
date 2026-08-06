package anthropic

import (
	"testing"

	"primeradiant.com/serf/llm"
)

func buildBodyForModel(t *testing.T, model string, maxTokens *int) map[string]any {
	t.Helper()
	a := &Adapter{}
	req := llm.Request{
		Model:     model,
		Messages:  []llm.Message{llm.User("hi")},
		MaxTokens: maxTokens,
	}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	return body
}

// Catalog-known model: the default cap is the catalog's, not 4096.
func TestBuildRequest_MaxTokensDefaultsFromCatalog(t *testing.T) {
	const model = "claude-sonnet-4-5"
	want := llm.EmbeddedModelCatalog().MaxOutputTokensFor(model)
	if want <= 0 {
		t.Fatalf("embedded catalog has no output cap for %s; pick a model it covers", model)
	}
	body := buildBodyForModel(t, model, nil)
	if got := body["max_tokens"].(int); got != want {
		t.Errorf("max_tokens = %d, want catalog cap %d", got, want)
	}
}

// Catalog-unknown model: liberal 32000 fallback, not 4096.
func TestBuildRequest_MaxTokensFallback32000(t *testing.T) {
	body := buildBodyForModel(t, "no-such-model-xyz", nil)
	if got := body["max_tokens"].(int); got != 32000 {
		t.Errorf("max_tokens = %d, want 32000", got)
	}
}

// Explicit request cap always wins.
func TestBuildRequest_ExplicitMaxTokensWins(t *testing.T) {
	mt := 512
	body := buildBodyForModel(t, "claude-sonnet-4-5", &mt)
	if got := body["max_tokens"].(int); got != 512 {
		t.Errorf("max_tokens = %d, want 512", got)
	}
}

// With max_tokens now defaulting to the model's real maximum, budget + max
// can exceed the model ceiling. The reconciliation must clamp to the
// catalog cap rather than requesting an out-of-range max_tokens.
//
// claude-3-7-sonnet-20250219 takes the legacy manual-thinking path (not
// adaptive thinking, not Claude 5+), so ReasoningEffort maps to an explicit
// thinking.budget_tokens that gets added to max_tokens.
//
// To actually enter the clamp branch (request.go's reconciliation only
// raises/clamps when out <= thinkingBudget) an explicit, smaller MaxTokens
// is required: this model's catalog cap (64000) already exceeds every
// budget level, so the untouched default alone never satisfies
// out <= thinkingBudget. The explicit MaxTokens (32000) is chosen so that
// out <= thinkingBudget (32000 <= 32768, effort "high") while
// thinkingBudget + out (32768 + 32000 = 64768) exceeds the catalog cap
// (64000), and the catalog cap still strictly exceeds the budget
// (64000 > 32768) — the exact condition the clamp is guarded on.
func TestBuildRequest_ThinkingBudgetClampsToCatalogMax(t *testing.T) {
	const model = "claude-3-7-sonnet-20250219"
	catalogMax := llm.EmbeddedModelCatalog().MaxOutputTokensFor(model)
	if catalogMax <= 0 {
		t.Fatalf("embedded catalog has no output cap for %s; pick a model it covers", model)
	}
	effort := "high"
	budget := llm.ReasoningBudget(effort)
	explicitMaxTokens := 32000
	if budget <= 0 || explicitMaxTokens > budget {
		t.Fatalf("test setup: need explicit maxTokens (%d) <= budget (%d)", explicitMaxTokens, budget)
	}
	unclampedSum := budget + explicitMaxTokens
	if catalogMax <= budget || unclampedSum <= catalogMax {
		t.Fatalf("test setup: need catalog cap (%d) > budget (%d) and unclamped sum (%d) > catalog cap", catalogMax, budget, unclampedSum)
	}

	a := &Adapter{}
	body, err := a.buildRequestBody(llm.Request{
		Model:           model,
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
		MaxTokens:       &explicitMaxTokens,
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	got, _ := body["max_tokens"].(int)
	if got != catalogMax {
		t.Errorf("max_tokens = %d, want clamped to catalog cap %d (unclamped sum would have been %d)", got, catalogMax, unclampedSum)
	}
	if got <= budget {
		t.Fatalf("max_tokens = %d must still exceed thinking budget %d", got, budget)
	}
}
