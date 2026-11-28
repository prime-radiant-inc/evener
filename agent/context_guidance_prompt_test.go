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
			registered := tc.revoke == ""
			checkPromptInput(t, "canInstructTool compact_context", s.canInstructTool("compact_context"), registered)
			advertised := hasToolDef(s.ToolDefinitions(), "compact_context")
			checkPromptInput(t, "advertised compact_context", advertised, registered)
			data, _ := s.buildPromptData(s.env)
			checkPromptInput(t, "HasTool compact_context", data.HasTool("compact_context"), advertised)
			prompt, warning, _ := s.renderSystemPrompt(s.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			body := operationalPromptBody(t, prompt)
			if got := strings.Contains(body, "compact_context"); got != advertised {
				t.Fatalf("operational prompt mentions compact_context = %v, want %v:\n%s", got, advertised, body)
			}
			nudge := selfCompactNudge(s.canInstructTool("compact_context"), nil)
			checkPromptInput(t, "selfCompactNudge names compact_context", strings.Contains(nudge, "compact_context"), registered)
		})
	}
}
