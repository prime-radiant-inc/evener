package provider_test

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// Amazon Bedrock serves the GPT-5.6 family over an OpenAI-compatible Responses
// endpoint that does not offer OpenAI's hosted tools, and rejects the entire
// request — not just the tool — when web_search is present. Its inference-profile
// IDs also resolved no catalog entry at all, so the effort ladder fell back to
// the provider default and ClampReasoningEffort silently turned a max run into
// an xhigh one.

func TestOpenAIProfile_BedrockModelDisablesWebSearch(t *testing.T) {
	for _, model := range []string{
		"us.openai.gpt-5.6-luna",
		"global.openai.gpt-5.6-luna",
		"us.openai.gpt-5.6-sol",
		"us.openai.gpt-5.6-terra",
	} {
		if provider.NewOpenAIProfile(model).SupportsWebSearch() {
			t.Errorf("%s SupportsWebSearch() = true; Bedrock rejects the whole request when the hosted web_search tool is present", model)
		}
	}
}

// Without "max" in the ladder, ClampReasoningEffort returns the highest level it
// does know, so a run asked for max silently becomes an xhigh run.
func TestOpenAIProfile_BedrockModelKeepsMaxEffort(t *testing.T) {
	levels := provider.NewOpenAIProfile("us.openai.gpt-5.6-luna").ReasoningEffortLevels()

	if !slices.Contains(levels, "max") {
		t.Fatalf("ReasoningEffortLevels() = %v, want it to contain %q", levels, "max")
	}
	if got := llm.ClampReasoningEffort("max", levels); got != "max" {
		t.Errorf("ClampReasoningEffort(%q, %v) = %q, want %q", "max", levels, got, "max")
	}
}

// An uncatalogued model must keep the provider default rather than inheriting
// another model's answer.
func TestOpenAIProfile_UncataloguedModelKeepsConstructorDefault(t *testing.T) {
	if !provider.NewOpenAIProfile("not-a-catalogued-model").SupportsWebSearch() {
		t.Error("SupportsWebSearch() = false for an uncatalogued model; want the constructor default true")
	}
}

// Switching models on the same profile must re-resolve the capability. A stale
// value is the dangerous direction in both senses: carrying false onto a model
// that supports web search loses a capability, and carrying true onto a Bedrock
// model restores the tool that makes the endpoint reject every request.
func TestOpenAIProfile_WithModelReresolvesWebSearch(t *testing.T) {
	toBedrock := provider.NewOpenAIProfile("gpt-5.6-luna").WithModel("us.openai.gpt-5.6-luna")
	if toBedrock.SupportsWebSearch() {
		t.Error("after WithModel to a Bedrock model, SupportsWebSearch() = true, want false")
	}
	if levels := toBedrock.ReasoningEffortLevels(); !slices.Contains(levels, "max") {
		t.Errorf("after WithModel to a Bedrock model, ReasoningEffortLevels() = %v, want it to contain %q", levels, "max")
	}

	fromBedrock := provider.NewOpenAIProfile("us.openai.gpt-5.6-luna").WithModel("gpt-5.6-luna")
	if !fromBedrock.SupportsWebSearch() {
		t.Error("after WithModel away from a Bedrock model, SupportsWebSearch() = false, want the catalog's true")
	}
}
