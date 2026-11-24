package agent

import (
	"strings"
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
			checkPromptInput(t, "canInstructTool compact_context", s.canInstructTool("compact_context"), tc.revoke == "")
			checkPromptInput(t, "HasTool compact_context (deferred on Turn 1)", data.HasTool("compact_context"), false)
			prompt, warning, _ := s.renderSystemPrompt(s.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			how := systemPromptSection(t, prompt, "## How you work")
			if strings.Contains(how, "compact_context") {
				t.Fatalf("## How you work unexpectedly named deferred compact_context:\n%s", how)
			}
			nudge := selfCompactNudge(s.canInstructTool("compact_context"), nil)
			checkPromptInput(t, "selfCompactNudge names compact_context", strings.Contains(nudge, "compact_context"), tc.revoke == "")
		})
	}
}
