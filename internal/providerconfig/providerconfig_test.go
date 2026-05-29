package providerconfig

import "testing"

func TestBehaviorTag(t *testing.T) {
	cases := []struct{ typ, style, want string }{
		{"openai", "responses", "openai"},
		{"openai", "chat-completions", "openai-compatible"},
		{"openai", "", "openai"}, // default style = responses
		{"anthropic", "", "anthropic"},
		{"google", "", "google"},
		{"openrouter", "", "openrouter"},
		{"openrouter-anthropic", "", "openrouter-anthropic"},
		{"kimi", "", "kimi"},
		{"glm", "", "glm"},
		{"minimax", "", "minimax"},
		{"ollama", "", "ollama"},
	}
	for _, c := range cases {
		if got := BehaviorTag(c.typ, c.style); got != c.want {
			t.Errorf("BehaviorTag(%q,%q)=%q want %q", c.typ, c.style, got, c.want)
		}
	}
}

func TestNameToTagIdentityForTypeNames(t *testing.T) {
	cfg := Config{Instances: []InstanceConfig{
		{Name: "openai", Type: "openai"},
		{Name: "work", Type: "openai", APIStyle: "chat-completions"},
	}}
	m := NameToTag(cfg)
	if m["openai"] != "openai" || m["work"] != "openai-compatible" {
		t.Errorf("NameToTag = %v", m)
	}
}
