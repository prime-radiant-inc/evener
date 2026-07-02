package openaicompat

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
)

// toolReq returns a request carrying one tool definition.
func toolReq(model string) llm.Request {
	req := plainReq(model)
	req.Tools = []llm.ToolDefinition{{
		Name:        "get_weather",
		Description: "look up weather",
		Parameters:  map[string]any{"type": "object"},
	}}
	return req
}

func TestSupportsStrictMode_AddsStrictFalseWhenExplicitlyTrue(t *testing.T) {
	body := requestBody(t, toolReq("m"), false, ModelCompat{Quirks: ProviderQuirks{SupportsStrictMode: boolp(true)}})
	tools := body["tools"].([]map[string]any)
	fn := tools[0]["function"].(map[string]any)
	got, present := fn["strict"]
	if !present {
		t.Fatalf("function object missing strict field: %#v", fn)
	}
	if got != false {
		t.Errorf("strict = %#v, want false", got)
	}
}

func TestSupportsStrictMode_DefaultOmitsStrict(t *testing.T) {
	for _, q := range []ProviderQuirks{
		{},                                 // nil → today's behavior
		{SupportsStrictMode: boolp(false)}, // explicit false → still omit
	} {
		body := requestBody(t, toolReq("m"), false, ModelCompat{Quirks: q})
		tools := body["tools"].([]map[string]any)
		fn := tools[0]["function"].(map[string]any)
		if _, present := fn["strict"]; present {
			t.Errorf("quirks %+v: strict must be absent by default, got %#v", q, fn["strict"])
		}
	}
}

func TestThinkingFormat_QwenChatTemplate(t *testing.T) {
	effortReq := plainReq("m")
	effortReq.ReasoningEffort = strp("high")

	// With effort: chat_template_kwargs with enable_thinking + preserve_thinking.
	body := requestBody(t, effortReq, false, ModelCompat{Quirks: ProviderQuirks{ThinkingFormat: "qwen-chat-template"}})
	want := map[string]any{"enable_thinking": true, "preserve_thinking": true}
	if got := body["chat_template_kwargs"]; !reflect.DeepEqual(got, want) {
		t.Errorf("chat_template_kwargs = %#v, want %#v", got, want)
	}

	// No effort: nothing emitted (serf's none-clears convention).
	body = requestBody(t, plainReq("m"), false, ModelCompat{Quirks: ProviderQuirks{ThinkingFormat: "qwen-chat-template"}})
	if _, present := body["chat_template_kwargs"]; present {
		t.Errorf("chat_template_kwargs must be absent with no effort, got %#v", body["chat_template_kwargs"])
	}
}

func TestThinkingFormat_ChatTemplate_VerbatimKwargs(t *testing.T) {
	effortReq := plainReq("m")
	effortReq.ReasoningEffort = strp("high")

	kwargs := map[string]any{"enable_thinking": true, "thinking_budget": int64(2048)}
	body := requestBody(t, effortReq, false, ModelCompat{Quirks: ProviderQuirks{
		ThinkingFormat:     "chat-template",
		ChatTemplateKwargs: kwargs,
	}})
	if got := body["chat_template_kwargs"]; !reflect.DeepEqual(got, kwargs) {
		t.Errorf("chat_template_kwargs = %#v, want %#v", got, kwargs)
	}

	// No effort → omitted.
	body = requestBody(t, plainReq("m"), false, ModelCompat{Quirks: ProviderQuirks{
		ThinkingFormat:     "chat-template",
		ChatTemplateKwargs: kwargs,
	}})
	if _, present := body["chat_template_kwargs"]; present {
		t.Errorf("chat_template_kwargs must be absent with no effort")
	}

	// Effort but empty kwargs → omitted (resolved-empty rule, not a load error).
	body = requestBody(t, effortReq, false, ModelCompat{Quirks: ProviderQuirks{ThinkingFormat: "chat-template"}})
	if _, present := body["chat_template_kwargs"]; present {
		t.Errorf("chat_template_kwargs must be absent when ChatTemplateKwargs is empty")
	}
}
