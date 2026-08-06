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
// claude-opus-4-1 takes the legacy manual-thinking path (not adaptive
// thinking, not Claude 5+), so ReasoningEffort maps to an explicit
// thinking.budget_tokens that gets added to max_tokens.
func TestBuildRequest_ThinkingBudgetClampsToCatalogMax(t *testing.T) {
	const model = "claude-opus-4-1"
	catalogMax := llm.EmbeddedModelCatalog().MaxOutputTokensFor(model)
	if catalogMax <= 0 {
		t.Fatalf("embedded catalog has no output cap for %s; pick a model it covers", model)
	}
	effort := "medium"
	budget := llm.ReasoningBudget(effort)
	if budget <= 0 || budget >= catalogMax {
		t.Fatalf("test setup: need 0 < budget < catalog cap %d, got budget=%d for effort=%s", catalogMax, budget, effort)
	}

	a := &Adapter{}
	body, err := a.buildRequestBody(llm.Request{
		Model:           model,
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	got, _ := body["max_tokens"].(int)
	if got != catalogMax {
		t.Errorf("max_tokens = %d, want clamped to catalog cap %d", got, catalogMax)
	}
	if got <= budget {
		t.Fatalf("max_tokens = %d must still exceed thinking budget %d", got, budget)
	}
}
