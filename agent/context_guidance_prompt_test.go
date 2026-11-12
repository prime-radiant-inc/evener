package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// The system prompt's context-length guidance names compact_context only for a
// session that can call it; a delegate whose tool ceiling excludes it is told
// only that compaction is automatic. The "How you work" section, which holds
// the guidance, is pinned whole per case. Regenerate after a wording change with
//
//	go test ./agent -run 'TestContextLengthGuidanceFollowsTheCompactTool$' -count=1 -update-prompt
//
// and read the diff.
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
			// Drop the helper's appended framing newline, not rendered content.
			section := strings.TrimSuffix(systemPromptSection(t, prompt, "## How you work"), "\n")
			agenttest.CheckGolden(t, filepath.Join("testdata", "contextguidance", tc.name+".md"), []byte(section), *updatePromptGoldens,
				"Regenerate with `go test ./agent -run 'TestContextLengthGuidanceFollowsTheCompactTool$' -count=1 -update-prompt` and read the diff.")
		})
	}
}
