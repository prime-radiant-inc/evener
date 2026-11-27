package agent

import (
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

func TestFinishingPromptGating(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		delegate     bool
		allowance    int
		worktree     bool
		disposeOnly  bool
		wantHasTool  bool
		denyWorktree bool
		ceilingNoWT  bool
	}{
		{name: "root", worktree: true},
		{name: "root-without-worktree-tool"},
		{name: "isolated-coordinator", delegate: true, allowance: 1, worktree: true, disposeOnly: true, wantHasTool: true},
		{name: "isolated-coordinator-without-worktree-tool", delegate: true, allowance: 1, denyWorktree: true},
		{name: "isolated-coordinator-ceiling-omits-worktree", delegate: true, allowance: 1, disposeOnly: true, ceilingNoWT: true},
		{name: "isolated-leaf", delegate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sess *Session
			if tc.delegate {
				sp := spawnConfig{
					parentSessionID:     "finishing-prompt-parent",
					depth:               1,
					delegationAllowance: tc.allowance,
					isolation:           "worktree",
				}
				if tc.denyWorktree {
					sp.deniedToolNames = []string{"manage_worktree"}
				}
				if tc.ceilingNoWT {
					sp.toolNameCeiling = []string{"read_file", "communicate", "delegate", "delegate_send", "read_transcript"}
				}
				sess = newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
					MaxSubagentDepth: 3,
					spawn:            sp,
					testOnly:         testConfig{skipGitSnapshot: true},
				}))
			} else {
				sess = buildRootInteractiveAnthropicSession(t)
				if !tc.worktree {
					sess.reg.Remove("manage_worktree")
					refreshModelFacingCaches(sess)
				}
			}
			data, _ := sess.buildPromptData(sess.env)
			checkPromptInput(t, "IsSubagent", data.IsSubagent, tc.delegate)
			checkPromptInput(t, "registered manage_worktree", sess.reg.Get("manage_worktree") != nil, tc.worktree)
			checkPromptInput(t, "HasTool manage_worktree", data.HasTool("manage_worktree"), tc.wantHasTool)
			checkPromptInput(t, "worktreeDisposeOnly", sess.worktreeDisposeOnly, tc.disposeOnly)
			checkPromptInput(t, "WorktreeDisposeOnly", data.WorktreeDisposeOnly, tc.disposeOnly)
			var mwDef llm.ToolDefinition
			var hasMWDef bool
			for _, td := range sess.ToolDefinitions() {
				if td.Name == "manage_worktree" {
					mwDef = td
					hasMWDef = true
					break
				}
			}
			checkPromptInput(t, "advertised manage_worktree", hasMWDef, tc.worktree)
			if tc.worktree {
				if !sess.isDeferredTool("manage_worktree") {
					t.Fatal("manage_worktree schema must stay deferred")
				}
				assertDeferredStubDef(t, mwDef, "")
			}
			prompt, warning, _ := sess.renderSystemPrompt(sess.env)
			if warning != "" {
				t.Fatalf("render system prompt: %s", warning)
			}
			finishing := systemPromptSection(t, prompt, "## Finishing")
			if tc.wantHasTool {
				if slices.ContainsFunc(data.Skills, func(e skillEntry) bool { return e.CatalogNameOrName() == skill.UsingWorktreesSkillName }) {
					t.Fatalf("dispose-only coordinator still advertises %q in <skill-catalog>", skill.UsingWorktreesSkillName)
				}
				if !strings.Contains(finishing, "`manage_worktree`") || !strings.Contains(finishing, `operation="dispose"`) {
					t.Fatalf("dispose-only coordinator ## Finishing missing manage_worktree dispose guidance:\n%s", finishing)
				}
			} else if strings.Contains(finishing, "manage_worktree") {
				t.Fatalf("unexpected manage_worktree guidance in ## Finishing:\n%s", finishing)
			}
		})
	}
}
