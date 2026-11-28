package agent

import (
	"testing"
)

func TestContextLengthGuidanceFollowsTheCompactTool(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, revoke string }{
		{"with-compact-tool", ""},
		{"without-compact-tool", "compact_context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t)
			if tc.revoke != "" {
				s.reg.Remove(tc.revoke)
				refreshModelFacingCaches(s)
			}
			data, _ := s.buildPromptData(s.env)
			checkPromptInput(t, "HasTool compact_context", data.HasTool("compact_context"), tc.revoke == "")
			prompt, warning, _ := s.renderSystemPrompt(s.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			systemPromptSection(t, prompt, "## How you work")
		})
	}
}
