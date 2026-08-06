package openai

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// The output cap must go on the wire as max_completion_tokens: OpenAI accepts
// it for all current models, while the legacy max_tokens field 400s on
// reasoning models — which do reach this fallback (the same body carries
// reasoning_effort).
func TestBuildChatCompletionsBody_UsesMaxCompletionTokens(t *testing.T) {
	mt := 4096
	body, err := buildChatCompletionsBody(llm.Request{
		Model:     "gpt-4o",
		Messages:  []llm.Message{llm.User("hi")},
		MaxTokens: &mt,
	}, false)
	if err != nil {
		t.Fatalf("buildChatCompletionsBody: %v", err)
	}
	if got, ok := body["max_completion_tokens"]; !ok || got != 4096 {
		t.Errorf("max_completion_tokens = %v (present=%v), want 4096", got, ok)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("legacy max_tokens field present; reasoning models reject it")
	}
}
