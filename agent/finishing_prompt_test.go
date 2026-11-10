package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// The complete section keeps cleanup instructions aligned with the session's
// worktree capabilities without treating individual phrases as test oracles.
func TestFinishingPromptGoldens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		delegate    bool
		allowance   int
		worktree    bool
		disposeOnly bool
	}{
		{name: "root", worktree: true},
		{name: "root-without-worktree-tool"},
		{name: "isolated-coordinator", delegate: true, allowance: 1, worktree: true, disposeOnly: true},
		{name: "isolated-leaf", delegate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sess *Session
			if tc.delegate {
				sess = newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
					MaxSubagentDepth: 3,
					spawn: spawnConfig{
						parentSessionID:     "finishing-prompt-parent",
						depth:               1,
						delegationAllowance: tc.allowance,
						isolation:           "worktree",
					},
					testOnly: testConfig{skipGitSnapshot: true},
				}))
			} else {
				sess = buildRootInteractiveAnthropicSession(t)
				if !tc.worktree {
					sess.reg.Remove("manage_worktree")
				}
			}
			data, _ := sess.buildPromptData(sess.env)
			checkPromptInput(t, "IsSubagent", data.IsSubagent, tc.delegate)
			checkPromptInput(t, "HasTool manage_worktree", data.HasTool("manage_worktree"), tc.worktree)
			checkPromptInput(t, "worktreeDisposeOnly", sess.worktreeDisposeOnly, tc.disposeOnly)
			prompt, warning, _ := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			// Drop the helper's appended framing newline, not rendered content.
			section := strings.TrimSuffix(systemPromptSection(t, prompt, "## Finishing"), "\n")
			agenttest.CheckGolden(t, filepath.Join("testdata", "finishingprompt", tc.name+".md"),
				[]byte(section), *updatePromptGoldens,
				"Regenerate with `go test ./agent -run '^TestFinishingPromptGoldens$' -count=1 -update-prompt` and read the diff.")
		})
	}
}
