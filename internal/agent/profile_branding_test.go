package agent

import "testing"

// TestProfileSystemPromptsSaySerf verifies that no profile still references "Kilroy".
func TestProfileSystemPromptsSaySerf(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("test-model"),
		NewAnthropicProfile("test-model"),
		NewGeminiProfile("test-model"),
	}

	env := EnvironmentInfo{
		WorkingDir: "/tmp/test",
		Platform:   "linux",
		Today:      "2026-02-08",
	}

	for _, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil)
		if containsIgnoreCase(prompt, "kilroy") {
			t.Errorf("profile %q system prompt still references Kilroy:\n%s", p.ID(), prompt)
		}
		if !containsIgnoreCase(prompt, "serf") {
			t.Errorf("profile %q system prompt does not reference serf:\n%s", p.ID(), prompt)
		}
	}
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) &&
		len(substr) > 0 &&
		indexIgnoreCase(s, substr) >= 0
}

func indexIgnoreCase(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
