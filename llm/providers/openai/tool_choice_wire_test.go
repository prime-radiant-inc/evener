package openai

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// The OpenAI Responses API and Chat Completions API use DIFFERENT wire shapes for
// forcing a specific tool. The Responses API puts the function name at the top
// level ({"type":"function","name":"X"}); Chat Completions nests it under
// "function" ({"type":"function","function":{"name":"X"}}). These tests pin both
// contracts so the two converters cannot silently converge again (PRI-2007).
// Refs: OpenAI SDK ToolChoiceFunctionParam vs ChatCompletionNamedToolChoiceParam.

func TestToResponsesToolChoice_NamedIsTopLevel(t *testing.T) {
	got, err := toResponsesToolChoice(llm.ToolChoice{Mode: "named", Name: "shell"})
	if err != nil {
		t.Fatalf("toResponsesToolChoice: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %#v", got)
	}
	if m["type"] != "function" {
		t.Fatalf("type: got %#v want \"function\"", m["type"])
	}
	if m["name"] != "shell" {
		t.Fatalf("name: got %#v want \"shell\" at top level", m["name"])
	}
	if _, nested := m["function"]; nested {
		t.Fatalf("Responses API must NOT nest the name under \"function\": %#v", m)
	}
}

func TestToChatCompletionsToolChoice_NamedIsNested(t *testing.T) {
	got, err := toChatCompletionsToolChoice(llm.ToolChoice{Mode: "named", Name: "shell"})
	if err != nil {
		t.Fatalf("toChatCompletionsToolChoice: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %#v", got)
	}
	if m["type"] != "function" {
		t.Fatalf("type: got %#v want \"function\"", m["type"])
	}
	fn, ok := m["function"].(map[string]any)
	if !ok {
		t.Fatalf("Chat Completions must nest the name under \"function\": %#v", m)
	}
	if fn["name"] != "shell" {
		t.Fatalf("function.name: got %#v want \"shell\"", fn["name"])
	}
}

// The two converters agree on the non-named modes (the divergence is only in how
// a forced function is encoded).
func TestToolChoiceConverters_AgreeOnSimpleModes(t *testing.T) {
	for _, mode := range []string{"", "auto", "none", "required"} {
		resp, err := toResponsesToolChoice(llm.ToolChoice{Mode: mode})
		if err != nil {
			t.Fatalf("toResponsesToolChoice(%q): %v", mode, err)
		}
		chat, err := toChatCompletionsToolChoice(llm.ToolChoice{Mode: mode})
		if err != nil {
			t.Fatalf("toChatCompletionsToolChoice(%q): %v", mode, err)
		}
		if resp != chat {
			t.Fatalf("mode %q: responses=%#v chat=%#v should match", mode, resp, chat)
		}
	}
}
