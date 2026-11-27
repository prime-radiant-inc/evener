package agent

import (
	"strings"
	"testing"
)

func TestFinishingPromptGating(t *testing.T) {
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
			checkPromptInput(t, "WorktreeDisposeOnly", data.WorktreeDisposeOnly, tc.disposeOnly)
			prompt, warning, _ := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			body := operationalPromptBody(t, prompt)
			if got := strings.Contains(body, "manage_worktree"); got != tc.worktree {
				t.Fatalf("operational prompt mentions manage_worktree = %v, want %v:\n%s", got, tc.worktree, body)
			}
		})
	}
}
