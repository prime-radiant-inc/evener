package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// The system prompt's context-length guidance names compact_context only for a
// session that can call it; a delegate whose tool ceiling excludes it is told
// only that compaction is automatic. The paragraph is pinned whole per case.
// Regenerate after a wording change with
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
			var system strings.Builder
			s := newSession(t, withSteps(func(req llm.Request) llm.Response {
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleSystem {
						system.WriteString(msg.Text())
					}
				}
				return agenttest.FinalResponse("done")
			}))
			if tc.revoke != "" {
				s.reg.Remove(tc.revoke)
				refreshModelFacingCaches(s)
			}
			if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
				t.Fatal(err)
			}
			var paragraph string
			for p := range strings.SplitSeq(system.String(), "\n\n") {
				if strings.Contains(p, "because of context length") {
					paragraph = p
				}
			}
			if paragraph == "" {
				t.Fatal("system prompt has no context-length guidance")
			}
			agenttest.CheckGolden(t, filepath.Join("testdata", "contextguidance", tc.name+".md"), []byte(paragraph+"\n"), *updatePromptGoldens,
				"Regenerate with `go test ./agent -run 'TestContextLengthGuidanceFollowsTheCompactTool$' -count=1 -update-prompt` and read the diff.")
		})
	}
}
